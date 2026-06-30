package microvm

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ztEntry describes one entry to write into a test zip. A non-empty symlink
// makes it a symlink whose content is the link target; dir makes it a bare
// directory entry; otherwise it is a regular file holding content.
type ztEntry struct {
	name    string
	content string
	symlink string
	dir     bool
}

func makeZip(t *testing.T, entries ...ztEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		switch {
		case e.dir:
			_, err := zw.Create(e.name) // trailing slash marks a directory
			require.NoError(t, err)
		case e.symlink != "":
			hdr := &zip.FileHeader{Name: e.name}
			hdr.SetMode(fs.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(hdr)
			require.NoError(t, err)
			_, err = w.Write([]byte(e.symlink))
			require.NoError(t, err)
		default:
			hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			hdr.SetMode(0o644)
			w, err := zw.CreateHeader(hdr)
			require.NoError(t, err)
			_, err = w.Write([]byte(e.content))
			require.NoError(t, err)
		}
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestRerootSingleTopLevelDir(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "repo-abc123/", dir: true},
		ztEntry{name: "repo-abc123/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo-abc123/app/main.go", content: "package main\n"},
	)
	out, err := rerootZip(in, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, out)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "app/main.go")
	assert.NotContains(t, entries, "repo-abc123/Dockerfile")
}

func TestRerootNoStripWhenDockerfileAtRoot(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "main.go", content: "package main\n"},
	)
	out, err := rerootZip(in, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, out)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "main.go")
}

func TestRerootContextSubdir(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "repo-abc123/", dir: true},
		ztEntry{name: "repo-abc123/README.md", content: "ignore me\n"},
		ztEntry{name: "repo-abc123/services/api/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo-abc123/services/api/server.go", content: "package main\n"},
	)
	out, err := rerootZip(in, "services/api", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, out)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "server.go")
	assert.NotContains(t, entries, "README.md")
}

func TestContextSubdirEscapeRejected(t *testing.T) {
	in := makeZip(t, ztEntry{name: "Dockerfile", content: "FROM scratch\n"})
	_, err := rerootZip(in, "../etc", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestRerootMissingDockerfile(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "repo-abc123/", dir: true},
		ztEntry{name: "repo-abc123/main.go", content: "package main\n"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "missing Dockerfile should be rejected, got %v", err)
}

func TestZipRejectAbsolutePath(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "/etc/passwd", content: "x"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestZipAllowsColonInFilename(t *testing.T) {
	// A colon in a filename is legal on POSIX and must not be mistaken for a
	// Windows drive-letter absolute path.
	// The colon is at index 1, exactly where a drive-letter check would look.
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "a:b.txt", content: "ok"},
	)
	out, err := rerootZip(in, "", defaultStagingCaps())
	require.NoError(t, err)
	assert.Contains(t, unzipEntries(t, out), "a:b.txt")
}

func TestZipRejectsDriveLetterPath(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "C:/Windows/system32", content: "x"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestZipRejectDotDotTraversal(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "../evil.sh", content: "rm -rf /"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestZipRejectDuplicatePath(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "dup.txt", content: "one"},
		ztEntry{name: "dup.txt", content: "two"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestZipRejectSymlinkEscape(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "link", symlink: "../../etc/passwd"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "got %v", err)
}

func TestZipRejectSymlinkEscapeAfterReroot(t *testing.T) {
	// link is in-root for the archive (../secret resolves to repo/secret) but
	// escapes once the context is re-rooted into repo/sub via --context-subdir.
	in := makeZip(t,
		ztEntry{name: "repo/", dir: true},
		ztEntry{name: "repo/sub/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo/sub/link", symlink: "../secret"},
	)
	_, err := rerootZip(in, "sub", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "symlink escaping the re-rooted root must be rejected, got %v", err)
}

func TestZipRejectSymlinkEscapeAfterStrip(t *testing.T) {
	// link escapes the single wrapping top-level directory once it is stripped.
	in := makeZip(t,
		ztEntry{name: "repo/", dir: true},
		ztEntry{name: "repo/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo/link", symlink: "../outside"},
	)
	_, err := rerootZip(in, "", defaultStagingCaps())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSource), "symlink escaping the stripped root must be rejected, got %v", err)
}

func TestZipRejectFileCount(t *testing.T) {
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "a.txt", content: "a"},
		ztEntry{name: "b.txt", content: "b"},
	)
	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1 << 30, maxFiles: 2}
	_, err := rerootZip(in, "", caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
}

func TestZipRejectUncompressedCap(t *testing.T) {
	big := bytes.Repeat([]byte("A"), 4096) // compresses small, but inflates past the cap
	in := makeZip(t,
		ztEntry{name: "Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "big.bin", content: string(big)},
	)
	caps := stagingCaps{maxCompressedBytes: 1 << 30, maxUncompressedBytes: 1024, maxFiles: 10000}
	_, err := rerootZip(in, "", caps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "uncompressed cap must defeat a lying/expanding entry, got %v", err)
}
