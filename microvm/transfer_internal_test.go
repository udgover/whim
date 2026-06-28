package microvm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/udgover/whim/internal/awsapi"
)

func TestTarballRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "file.txt"), []byte("hello\n"), 0o644)
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	mustWrite(t, filepath.Join(src, "sub", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	bin := []byte{0x00, 0x01, 0xff, 0xfe, 0x7f}
	mustWrite(t, filepath.Join(src, "blob.bin"), bin, 0o600)

	data, err := tarball(src)
	require(err)

	dest := t.TempDir()
	require(untar(data, dest))

	base := filepath.Base(src)
	// content preserved (incl. binary)
	if got, _ := os.ReadFile(filepath.Join(dest, base, "file.txt")); string(got) != "hello\n" {
		t.Errorf("file.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, base, "blob.bin")); !bytes.Equal(got, bin) {
		t.Errorf("blob.bin not byte-identical: %v", got)
	}
	// mode bits preserved
	fi, err := os.Stat(filepath.Join(dest, base, "sub", "run.sh"))
	require(err)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %o, want 0755", fi.Mode().Perm())
	}
}

func TestTarballSingleFile(t *testing.T) {
	src := t.TempDir()
	p := filepath.Join(src, "solo.txt")
	mustWrite(t, p, []byte("solo"), 0o644)

	data, err := tarball(p)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := untar(data, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "solo.txt")); string(got) != "solo" {
		t.Errorf("solo.txt = %q", got)
	}
}

func TestUntarRejectsTraversal(t *testing.T) {
	// Craft a malicious archive whose entry escapes the destination.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gz.Close()

	err := untar(buf.Bytes(), t.TempDir())
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("traversal must be rejected with ErrInvalidOption, got %v", err)
	}
}

// archiveWith builds a gzip+tar archive from the given headers (regular-file
// bodies are zero-filled to their Size), for crafting hostile archives.
func archiveWith(headers ...*tar.Header) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			_, _ = tw.Write(make([]byte, h.Size))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestUntar_RejectsAbsoluteSymlink(t *testing.T) {
	data := archiveWith(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777})
	if err := untar(data, t.TempDir()); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("absolute symlink target must be rejected, got %v", err)
	}
}

func TestUntar_RejectsEscapingSymlink(t *testing.T) {
	data := archiveWith(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape", Mode: 0o777})
	if err := untar(data, t.TempDir()); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("escaping symlink target must be rejected, got %v", err)
	}
}

func TestUntar_AllowsInDestSymlink(t *testing.T) {
	dest := t.TempDir()
	data := archiveWith(
		&tar.Header{Name: "sub/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "sub/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3},
		&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "sub/file", Mode: 0o777},
	)
	if err := untar(data, dest); err != nil {
		t.Fatalf("a safe in-dest symlink must still extract: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "sub/file" {
		t.Fatalf("symlink not created: target=%q err=%v", target, err)
	}
}

func TestPut_RejectsOversizeSource(t *testing.T) {
	old := maxPutBytes
	maxPutBytes = 4096
	t.Cleanup(func() { maxPutBytes = old })

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "big"), make([]byte, 16*1024), 0o644) // 16 KiB > 4 KiB cap

	sb := &Sandbox{mgr: NewWithAPI(&awsapi.Mock{}), id: "vm", endpoint: "x"}
	err := sb.Put(context.Background(), src, "/dest")
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("oversize Put must be rejected (before dialing), got %v", err)
	}
}

func TestUntar_StripsSetuid(t *testing.T) {
	dest := t.TempDir()
	data := archiveWith(&tar.Header{Name: "x", Typeflag: tar.TypeReg, Mode: 0o4755})
	if err := untar(data, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "x"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Errorf("setuid/setgid must not be applied, mode=%v", fi.Mode())
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("perm = %o, want 0755", fi.Mode().Perm())
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile honors umask; force exact bits
		t.Fatal(err)
	}
}
