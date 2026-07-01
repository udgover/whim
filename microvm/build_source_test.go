package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

const testAccount = "123456789012"

// buildManager wires a Manager with a mock API, a fake artifact store, and a
// fast poll interval for orchestration tests.
func buildManager(mock *awsapi.Mock, fake *fakeArtifactStore) *Manager {
	return NewWithAPI(mock,
		WithRegion("us-east-1"),
		WithAccountID(testAccount),
		WithPollInterval(time.Millisecond),
		withArtifactStore(fake),
	)
}

// localDockerfileDir creates a temp dir holding a minimal Dockerfile.
func localDockerfileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	return dir
}

func sourceOpts() BuildFromSourceOptions {
	return BuildFromSourceOptions{
		Name:           "whim-x",
		ArtifactBucket: "whim-artifacts",
		BaseImageARN:   "arn:aws:lambda:us-east-1:aws:microvm-image:al2023",
		BuildRoleARN:   "arn:aws:iam::123456789012:role/whim-build",
		Egress:         EgressPublic,
	}
}

func notFound() error { return awsapi.ErrNotFound }

func TestBuildFromSourceRejectsInvalidOptions(t *testing.T) {
	mock := &awsapi.Mock{}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	opts := sourceOpts()
	opts.ArtifactBucket = ""
	_, err := m.BuildFromSource(context.Background(), localDockerfileDir(t), opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidOption), "got %v", err)
	assert.Empty(t, fake.puts, "must not stage when options are invalid")
}

func TestBuildFromSourceRejectsInvalidSource(t *testing.T) {
	m := buildManager(&awsapi.Mock{}, &fakeArtifactStore{})
	_, err := m.BuildFromSource(context.Background(), "http://example.com/app.zip", sourceOpts())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestBuildFromSourceReusesExistingWithoutStaging(t *testing.T) {
	mock := &awsapi.Mock{}
	arn := "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-x"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED", LatestActiveImageVersion: "3"}, nil
	}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	// A nonexistent source proves staging was skipped — stat would fail.
	got, err := m.BuildFromSource(context.Background(), filepath.Join(t.TempDir(), "nope"), sourceOpts())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Empty(t, fake.puts, "existing image must not stage source bytes")
	assert.Empty(t, mock.CreateMicrovmImageCalls, "existing image must not be rebuilt")
}

func TestBuildFromSourceInProgressPollsWithoutStaging(t *testing.T) {
	mock := &awsapi.Mock{}
	arn := "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-x"
	calls := 0
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		calls++
		state := "CREATING"
		if calls >= 2 {
			state = "CREATED"
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: state}, nil
	}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	got, err := m.BuildFromSource(context.Background(), filepath.Join(t.TempDir(), "nope"), sourceOpts())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Empty(t, fake.puts)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestBuildFromSourceFailedStateReturnsError(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: "arn:x", State: "CREATE_FAILED"}, nil
	}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	_, err := m.BuildFromSource(context.Background(), filepath.Join(t.TempDir(), "nope"), sourceOpts())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrImageBuildFailed), "got %v", err)
	assert.Empty(t, fake.puts, "a failed-state image must not stage source bytes")
}

func TestBuildFromSourceAbsentStagesAndBuilds(t *testing.T) {
	mock := &awsapi.Mock{}
	arn := "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-x"
	created := false
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		if !created {
			return nil, notFound()
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		assert.Equal(t, "whim-x", in.Name)
		assert.Equal(t, "arn:aws:lambda:us-east-1:aws:microvm-image:al2023", in.BaseImageARN)
		assert.Equal(t, "s3://whim-artifacts/whim-x.zip", in.CodeArtifactURI)
		assert.Equal(t, "arn:aws:iam::123456789012:role/whim-build", in.BuildRoleARN)
		assert.NotEmpty(t, in.EgressConnectors, "EgressPublic must produce a connector")
		created = true
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	got, err := m.BuildFromSource(context.Background(), localDockerfileDir(t), sourceOpts())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	require.Len(t, fake.puts, 1, "absent image must stage the source")
	assert.Equal(t, "whim-artifacts", fake.puts[0].bucket)
	assert.Equal(t, "whim-x.zip", fake.puts[0].key)
	assert.Contains(t, unzipEntries(t, fake.puts[0].body), "Dockerfile")
}

func TestBuildFromSourceForceRebuilds(t *testing.T) {
	mock := &awsapi.Mock{}
	arn := "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-x"
	deleted, created := false, false
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		if created {
			return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
		}
		if deleted {
			return nil, notFound()
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
	}
	mock.DeleteMicrovmImageFn = func(_ context.Context, _ *awsapi.DeleteMicrovmImageInput) error {
		deleted = true
		return nil
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		require.True(t, deleted, "force must delete before rebuilding")
		created = true
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	fake := &fakeArtifactStore{}
	m := buildManager(mock, fake)

	opts := sourceOpts()
	opts.Force = true
	got, err := m.BuildFromSource(context.Background(), localDockerfileDir(t), opts)
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.NotEmpty(t, mock.DeleteMicrovmImageCalls, "force must delete the existing image")
	require.Len(t, mock.CreateMicrovmImageCalls, 1)
	require.Len(t, fake.puts, 1, "force must stage the source")
}

// validOpts returns a BuildFromSourceOptions with every required field set, so
// individual tests can blank exactly one field and assert the failure.
func validOpts() BuildFromSourceOptions {
	return BuildFromSourceOptions{
		Name:           "whim-example",
		ArtifactBucket: "whim-artifacts",
		BaseImageARN:   "arn:aws:lambda:us-east-1:123456789012:microvm-image:base",
		BuildRoleARN:   "arn:aws:iam::123456789012:role/whim-build",
		Egress:         EgressPublic,
	}
}

func TestBuildFromSourceValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*BuildFromSourceOptions)
		wantErr bool
	}{
		{name: "valid", mutate: func(*BuildFromSourceOptions) {}, wantErr: false},
		{name: "valid egress none", mutate: func(o *BuildFromSourceOptions) { o.Egress = EgressNone }, wantErr: false},
		{name: "missing name", mutate: func(o *BuildFromSourceOptions) { o.Name = "" }, wantErr: true},
		{name: "missing artifact bucket", mutate: func(o *BuildFromSourceOptions) { o.ArtifactBucket = "" }, wantErr: true},
		{name: "missing base image arn", mutate: func(o *BuildFromSourceOptions) { o.BaseImageARN = "" }, wantErr: true},
		{name: "missing build role arn", mutate: func(o *BuildFromSourceOptions) { o.BuildRoleARN = "" }, wantErr: true},
		{name: "egress vpc rejected", mutate: func(o *BuildFromSourceOptions) { o.Egress = EgressVPC }, wantErr: true},
		{name: "unknown egress rejected", mutate: func(o *BuildFromSourceOptions) { o.Egress = EgressMode(99) }, wantErr: true},
		{name: "capability all accepted", mutate: func(o *BuildFromSourceOptions) { o.Capabilities = []Capability{CapabilityAll} }, wantErr: false},
		{name: "unsupported capability rejected", mutate: func(o *BuildFromSourceOptions) { o.Capabilities = []Capability{"BOGUS"} }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validOpts()
			tt.mutate(&opts)
			err := opts.validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInvalidOption), "want ErrInvalidOption, got %v", err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildFromSourceOptionsDefaultCaps(t *testing.T) {
	caps := BuildFromSourceOptions{}.caps()
	assert.Equal(t, int64(256<<20), caps.maxCompressedBytes, "compressed cap should default to 256 MiB")
	assert.Equal(t, int64(1<<30), caps.maxUncompressedBytes, "uncompressed cap should default to 1 GiB")
	assert.Equal(t, 10000, caps.maxFiles, "file count cap should default to 10,000")
}

func TestBuildFromSourceOptionsExplicitCaps(t *testing.T) {
	opts := BuildFromSourceOptions{
		MaxCompressedBytes:   1234,
		MaxUncompressedBytes: 5678,
	}
	caps := opts.caps()
	assert.Equal(t, int64(1234), caps.maxCompressedBytes)
	assert.Equal(t, int64(5678), caps.maxUncompressedBytes)
	assert.Equal(t, 10000, caps.maxFiles, "file count cap is not caller-settable in the MVP")
}
