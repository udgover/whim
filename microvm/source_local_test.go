package microvm

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unzipEntries reads a zip archive into a name->content map for assertions.
func unzipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := make(map[string]string, len(r.File))
	for _, f := range r.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestStageLocalDockerfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	const body = "FROM public.ecr.aws/amazonlinux/amazonlinux:2023\nCMD [\"sleep\", \"infinity\"]\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	zipped, err := stageLocalDockerfile(path, defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	require.Len(t, entries, 1)
	assert.Equal(t, body, entries["Dockerfile"], "Dockerfile must sit at the zip root")
}

func TestStageLocalDockerfileSelectedByOtherName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my.dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	zipped, err := stageLocalDockerfile(path, defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	require.Len(t, entries, 1)
	_, ok := entries["Dockerfile"]
	assert.True(t, ok, "a Dockerfile selected under another name is re-rooted to Dockerfile")
}

func TestStageLocalDockerfileMissing(t *testing.T) {
	_, err := stageLocalDockerfile(filepath.Join(t.TempDir(), "nope"), defaultStagingCaps())
	require.Error(t, err)
}

func TestStageLocalDockerfileDirectory(t *testing.T) {
	_, err := stageLocalDockerfile(t.TempDir(), defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "a directory is not a Dockerfile, got %v", err)
}

func TestStageLocalDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app", "main.go"), []byte("package main\n"), 0o644))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "app/main.go")
}

func TestStageLocalDirectoryViaDispatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))

	zipped, err := stageLocalSource(dir, "", defaultStagingCaps())
	require.NoError(t, err)
	assert.Contains(t, unzipEntries(t, zipped), "Dockerfile")
}

func TestStageLocalDirectoryMissingDockerfile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))

	_, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "missing Dockerfile should be rejected, got %v", err)
}

func TestStageLocalSymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.Symlink("../../../etc/passwd", filepath.Join(dir, "evil")))

	_, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "escaping symlink should be rejected, got %v", err)
}

func TestStageLocalSymlinkInRootAllowed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.Symlink("Dockerfile", filepath.Join(dir, "link")))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)
	assert.Contains(t, unzipEntries(t, zipped), "link")
}

func TestStageLocalContextSubdirSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM evil\n"), 0o644))

	// root/sub is a symlink to a directory outside the source root.
	root := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "sub")))

	_, err := stageLocalDirectory(root, "sub", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "context-subdir symlinked outside root must be rejected, got %v", err)
}

func TestStageLocalContextSubdirIntermediateSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "ctx"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "ctx", "Dockerfile"), []byte("FROM evil\n"), 0o644))

	// root/a is a symlink to outside; the subdir traverses through it (a/ctx).
	root := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "a")))

	_, err := stageLocalDirectory(root, "a/ctx", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "subdir traversing a symlink out of root must be rejected, got %v", err)
}

func TestStageLocalContextSubdirSymlinkInRootAllowed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "real", "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	// A symlink that stays within the source root is fine.
	require.NoError(t, os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")))

	zipped, err := stageLocalDirectory(root, "link", defaultStagingCaps())
	require.NoError(t, err)
	assert.Contains(t, unzipEntries(t, zipped), "Dockerfile")
}

func TestStageLocalCapsFileCount(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644))

	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1 << 30, maxFiles: 2}
	_, err := stageLocalDirectory(dir, "", caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
}

func TestStageLocalCapsFileCountWithSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.Symlink("Dockerfile", filepath.Join(dir, "l1")))
	require.NoError(t, os.Symlink("Dockerfile", filepath.Join(dir, "l2")))

	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1 << 30, maxFiles: 2}
	_, err := stageLocalDirectory(dir, "", caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "symlinks count toward the file cap, got %v", err)
}

func TestStageLocalCapsUncompressed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.bin"), bytes.Repeat([]byte("A"), 4096), 0o644))

	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1024, maxFiles: 10000}
	_, err := stageLocalDirectory(dir, "", caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
}

func TestStageLocalDockerfileUncompressedCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	// Highly compressible: stays tiny under the compressed cap but inflates well
	// past the uncompressed cap, so only an uncompressed-read bound rejects it.
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("A"), 4096), 0o644))

	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1024, maxFiles: 10000}
	_, err := stageLocalDockerfile(path, caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "want ErrSourceTooLarge, got %v", err)
}

func TestStageLocalDockerfileCompressedCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	// A 1-byte compressed cap is smaller than any real zip's header overhead.
	caps := stagingCaps{maxCompressedBytes: 1, maxUncompressedBytes: 1 << 30, maxFiles: 10000}
	_, err := stageLocalDockerfile(path, caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "want ErrSourceTooLarge, got %v", err)
}
