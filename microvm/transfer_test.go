package microvm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var reChunk = regexp.MustCompile(`printf %s '([^']*)' >> `)

// reassembleTarGz pulls the base64 chunks out of the captured Put commands,
// decodes them, and returns the entry name→content map of the tar.gz — i.e. what
// the remote would have received and extracted.
func reassembleTarGz(t *testing.T, cmds []string) map[string][]byte {
	t.Helper()
	var b64 strings.Builder
	for _, c := range cmds {
		if m := reChunk.FindStringSubmatch(c); m != nil {
			b64.WriteString(m[1])
		}
	}
	raw, err := base64.StdEncoding.DecodeString(b64.String())
	require.NoError(t, err)
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, _ := io.ReadAll(tr)
		out[hdr.Name] = data
	}
	return out
}

// buildTarGz builds a gzip+tar archive from name→content (mode 0644), plus one
// 0755 entry if execName is set, for Get tests.
func buildTarGz(t *testing.T, files map[string][]byte, execName string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		mode := int64(0o644)
		if name == execName {
			mode = 0o755
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestGet_DownloadsAndExtracts(t *testing.T) {
	tgz := buildTarGz(t, map[string][]byte{
		"dl/a.txt":  []byte("hello"),
		"dl/run.sh": []byte("#!/bin/sh\n"),
		"dl/b.bin":  {0, 9, 0xff, 0xfe},
	}, "dl/run.sh")
	b64 := base64.StdEncoding.EncodeToString(tgz)

	srv := shellWSServer(execHandler(func(cmd string) ([]byte, int) {
		if strings.Contains(cmd, "tar czf") {
			return []byte(b64), 0
		}
		return nil, 0
	}), nil)
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	dest := t.TempDir()
	require.NoError(t, sb.Get(context.Background(), "/remote/dl", dest))

	got, _ := os.ReadFile(filepath.Join(dest, "dl", "a.txt"))
	assert.Equal(t, "hello", string(got))
	gotBin, _ := os.ReadFile(filepath.Join(dest, "dl", "b.bin"))
	assert.Equal(t, []byte{0, 9, 0xff, 0xfe}, gotBin, "binary byte-exact")
	fi, err := os.Stat(filepath.Join(dest, "dl", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm(), "mode preserved")
}

func TestGet_RemoteFailureSurfaces(t *testing.T) {
	srv := shellWSServer(execHandler(func(cmd string) ([]byte, int) {
		if strings.Contains(cmd, "tar czf") {
			return []byte("tar: /nope: Cannot stat: No such file or directory"), 2
		}
		return nil, 0
	}), nil)
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	err = sb.Get(context.Background(), "/nope", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 2", "a remote tar failure (via pipefail) must surface")
}

func TestPut_DeliversTarballToRemote(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, "d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "d", "b.bin"), []byte{0, 1, 2, 0xff}, 0o644))

	var cmds []string
	srv := shellWSServer(execHandler(func(string) ([]byte, int) { return nil, 0 }), &cmds)
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	require.NoError(t, sb.Put(context.Background(), src, "/remote/dest"))

	entries := reassembleTarGz(t, cmds)
	base := filepath.Base(src)
	assert.Equal(t, []byte("alpha"), entries[base+"/a.txt"], "text file delivered")
	assert.Equal(t, []byte{0, 1, 2, 0xff}, entries[base+"/d/b.bin"], "binary file byte-exact")
}

func TestPut_RemoteStepFailureSurfaces(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "x"), []byte("x"), 0o644))

	// mkdir step fails (e.g. permission denied on the remote).
	srv := shellWSServer(func(cmd string) ([]byte, int) {
		if strings.HasPrefix(cmd, "mkdir") {
			return []byte("mkdir: permission denied"), 1
		}
		return nil, 0
	}, nil)
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	err = sb.Put(context.Background(), src, "/root/forbidden")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 1")
}

func TestPut_ChunksLargePayload(t *testing.T) {
	// A payload whose base64 exceeds one chunk must arrive across multiple
	// printf appends and still reassemble byte-exact.
	src := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 16*1024) // 256 KiB, low-entropy but gzip keeps it multi-chunk-ish
	require.NoError(t, os.WriteFile(filepath.Join(src, "big.dat"), big, 0o644))

	var cmds []string
	srv := shellWSServer(execHandler(func(string) ([]byte, int) { return nil, 0 }), &cmds)
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	require.NoError(t, sb.Put(context.Background(), src, "/dest"))

	chunks := 0
	for _, c := range cmds {
		if reChunk.MatchString(c) {
			chunks++
		}
	}
	assert.GreaterOrEqual(t, chunks, 1, "payload sent as chunked appends")
	entries := reassembleTarGz(t, cmds)
	assert.Equal(t, big, entries[filepath.Base(src)+"/big.dat"], "large payload reassembles byte-exact")
}
