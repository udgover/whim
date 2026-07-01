package microvm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

func s3Manager(t *testing.T) (*Manager, *fakeArtifactStore) {
	t.Helper()
	fake := &fakeArtifactStore{}
	m := NewWithAPI(&awsapi.Mock{}, WithRegion("us-east-1"), withArtifactStore(fake))
	return m, fake
}

func s3Opts() BuildFromSourceOptions {
	return BuildFromSourceOptions{
		Name:           "whim-s3",
		ArtifactBucket: "whim-artifacts",
		BaseImageARN:   "arn:base",
		BuildRoleARN:   "arn:role",
		Egress:         EgressPublic,
	}
}

func stageS3(t *testing.T, m *Manager, source string, opts BuildFromSourceOptions) (string, error) {
	t.Helper()
	src, err := classifySource(source)
	require.NoError(t, err)
	return m.stageS3Source(context.Background(), src, opts)
}

func TestStageS3ZipValidatedAndReRooted(t *testing.T) {
	// A forge-style single-top-level-dir zip must be downloaded, re-rooted, and
	// re-uploaded to the artifact bucket — not copied through unchecked.
	zipBody := makeZip(t,
		ztEntry{name: "repo-1/", dir: true},
		ztEntry{name: "repo-1/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo-1/app/main.go", content: "package main\n"},
	)
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "app.zip", zipBody)

	uri, err := stageS3(t, m, "s3://src-bucket/app.zip", s3Opts())
	require.NoError(t, err)
	assert.Equal(t, "s3://whim-artifacts/whim-s3.zip", uri)

	require.Len(t, fake.puts, 1, "the normalized zip must be uploaded to the artifact bucket")
	assert.Equal(t, "whim-artifacts", fake.puts[0].bucket)
	entries := unzipEntries(t, fake.puts[0].body)
	assert.Contains(t, entries, "Dockerfile", "single top-level dir must be stripped")
	assert.Contains(t, entries, "app/main.go")
}

func TestStageS3ZipRejectsMissingDockerfile(t *testing.T) {
	// Unsafe/invalid archives must fail before any upload (and thus before any
	// CreateMicrovmImage), per the build spec.
	zipBody := makeZip(t, ztEntry{name: "app/main.go", content: "package main\n"})
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "noroot.zip", zipBody)

	_, err := stageS3(t, m, "s3://src-bucket/noroot.zip", s3Opts())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
	assert.Empty(t, fake.puts, "an invalid archive must not be staged")
}

func TestStageS3ZipRejectsTraversal(t *testing.T) {
	zipBody := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "../escape.sh", content: "rm -rf /"},
	)
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "evil.zip", zipBody)

	_, err := stageS3(t, m, "s3://src-bucket/evil.zip", s3Opts())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
	assert.Empty(t, fake.puts)
}

func TestStageS3ZipContextSubdir(t *testing.T) {
	zipBody := makeZip(t,
		ztEntry{name: "repo-1/", dir: true},
		ztEntry{name: "repo-1/README.md", content: "ignore\n"},
		ztEntry{name: "repo-1/services/api/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo-1/services/api/server.go", content: "package main\n"},
	)
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "mono.zip", zipBody)

	opts := s3Opts()
	opts.ContextSubdir = "services/api"
	_, err := stageS3(t, m, "s3://src-bucket/mono.zip", opts)
	require.NoError(t, err)

	require.Len(t, fake.puts, 1)
	entries := unzipEntries(t, fake.puts[0].body)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "server.go")
	assert.NotContains(t, entries, "README.md")
}

func TestStageS3ZipCompressedCap(t *testing.T) {
	zipBody := makeZip(t, ztEntry{name: "Dockerfile", content: "FROM scratch\n"})
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "app.zip", zipBody)

	opts := s3Opts()
	opts.MaxCompressedBytes = 8 // smaller than any real zip's header overhead
	_, err := stageS3(t, m, "s3://src-bucket/app.zip", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
	assert.Empty(t, fake.puts)
}

func TestStageS3RawDockerfileRepackaged(t *testing.T) {
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "Dockerfile", []byte("FROM scratch\n"))

	uri, err := stageS3(t, m, "s3://src-bucket/Dockerfile", s3Opts())
	require.NoError(t, err)
	assert.Equal(t, "s3://whim-artifacts/whim-s3.zip", uri)

	require.Len(t, fake.puts, 1)
	entries := unzipEntries(t, fake.puts[0].body)
	assert.Equal(t, "FROM scratch\n", entries["Dockerfile"])
}

func TestStageS3RawDockerfileUncompressedCap(t *testing.T) {
	m, fake := s3Manager(t)
	fake.seed("src-bucket", "Dockerfile", bytes.Repeat([]byte("A"), 4096))

	opts := s3Opts()
	opts.MaxUncompressedBytes = 1024
	_, err := stageS3(t, m, "s3://src-bucket/Dockerfile", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
	assert.Empty(t, fake.puts)
}

func TestStageS3ArtifactURIInArtifactBucket(t *testing.T) {
	m, fake := s3Manager(t)
	fake.seed("other-bucket", "ctx.zip", makeZip(t, ztEntry{name: "Dockerfile", content: "FROM scratch\n"}))

	uri, err := stageS3(t, m, "s3://other-bucket/ctx.zip", s3Opts())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(uri, "s3://whim-artifacts/"), "artifact URI must point at the configured artifact bucket, got %q", uri)
}

func TestStageS3PermissionErrorDoesNotBlameBuildRole(t *testing.T) {
	m, fake := s3Manager(t)
	fake.getErr = errors.New("AccessDenied: not authorized to read")

	_, err := stageS3(t, m, "s3://src-bucket/app.zip", s3Opts())
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "build role", "source read failure must not suggest build-role changes")
	assert.NotContains(t, strings.ToLower(err.Error()), "build-role")
}
