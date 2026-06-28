package microvm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/udgover/whim/internal/shellio"
)

// transferChunkBytes is the base64 chars per Put command line — under the
// proven ~64 KiB pty line limit, leaving headroom for the printf wrapper +
// sentinel.
const transferChunkBytes = 48 * 1024

// maxPutBytes caps a Put's total source size. The whole payload is held in
// memory (tar.gz + base64), so very large transfers are refused rather than
// risking OOM (streaming is a v0.2 item). A var so tests can shrink it.
var maxPutBytes int64 = 256 << 20 // 256 MiB

// Put uploads a local file or directory tree into the MicroVM, extracting it
// INTO remoteDir with the basename preserved (like `scp -r local remote:dir` or
// `docker cp`). Transfer is binary-safe (tar.gz + base64) and chunked to stay
// within the pty line limit; remoteDir is created if absent. Canceling ctx
// aborts the transfer. The shell token is never logged.
//
// The whole payload is held in memory; sources larger than maxPutBytes are
// rejected with ErrInvalidOption.
func (s *Sandbox) Put(ctx context.Context, localPath, remoteDir string) error {
	if err := checkPutSize(localPath); err != nil {
		return err
	}
	data, err := tarball(localPath)
	if err != nil {
		return fmt.Errorf("pack %q: %w", localPath, err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := s.dialShell(sessCtx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	sess := shellio.New(conn)
	defer func() { _ = sess.Close() }()

	tmp := "/tmp/whim-" + randSuffix() + ".b64"
	// Best-effort cleanup of the remote temp, even on early return. Runs on the
	// still-open session (this defer precedes conn/sess close in LIFO order); a
	// fresh ctx so a canceled transfer ctx doesn't also cancel the cleanup.
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = sess.Run(cctx, "rm -f "+shellQuote1(tmp))
	}()

	steps := []string{"mkdir -p " + shellQuote1(remoteDir) + " && : > " + shellQuote1(tmp)}
	for i := 0; i < len(b64); i += transferChunkBytes {
		end := i + transferChunkBytes
		if end > len(b64) {
			end = len(b64)
		}
		steps = append(steps, "printf %s "+shellQuote1(b64[i:end])+" >> "+shellQuote1(tmp))
	}
	steps = append(steps, "base64 -d < "+shellQuote1(tmp)+" | tar xzf - -C "+shellQuote1(remoteDir))

	for _, step := range steps {
		if err := s.transferStep(sessCtx, sess, step, "put to "+s.id); err != nil {
			return err
		}
	}
	return nil
}

// checkPutSize sums the source's regular-file bytes and rejects an oversized
// Put before anything is read into memory.
func checkPutSize(localPath string) error {
	var total int64
	err := filepath.Walk(localPath, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if total > maxPutBytes {
		return fmt.Errorf("%w: source is %d bytes, over the %d-byte Put limit (held in memory)", ErrInvalidOption, total, maxPutBytes)
	}
	return nil
}

// defaultGetMaxOutput caps a Get's captured base64 (≈ the download size); very
// large downloads beyond this fail rather than OOM (chunked Get is a v0.2 item).
const defaultGetMaxOutput = 256 << 20

// Get downloads a remote file or directory tree out of the MicroVM into localDir,
// with the basename preserved (like `scp -r remote:path local` / `docker cp`).
// The remote `tar cz | base64` is captured (bounded by defaultGetMaxOutput),
// decoded, and extracted with a path-traversal guard. Canceling ctx aborts it.
func (s *Sandbox) Get(ctx context.Context, remotePath, localDir string) error {
	// Remote paths are POSIX — use path, not filepath (which is OS-specific).
	parent, base := path.Dir(remotePath), path.Base(remotePath)
	if parent == "" {
		parent = "."
	}

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := s.dialShell(sessCtx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	sess := shellio.New(conn, shellio.WithMaxOutput(defaultGetMaxOutput))
	defer func() { _ = sess.Close() }()

	// pipefail so a tar failure (e.g. missing path) propagates instead of being
	// masked by base64's success; -w0 keeps the base64 a single decodable token;
	// `--` so a remote name beginning with '-' is treated as a path, not a flag.
	cmd := "set -o pipefail; tar czf - -C " + shellQuote1(parent) + " -- " + shellQuote1(base) + " | base64 -w0"
	label := "get from " + s.id
	res, err := sess.Run(sessCtx, cmd)
	if err != nil {
		return s.mapShellErr(sessCtx, err, label)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: remote step failed (exit %d): %s", label, res.ExitCode, truncateForErr(res.Output))
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(res.Output)))
	if err != nil {
		return fmt.Errorf("%s: decode response: %w", label, err)
	}
	if err := os.MkdirAll(localDir, 0o750); err != nil {
		return err
	}
	if err := untar(raw, localDir); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// transferStep runs one remote step (output discarded) and maps failures to
// typed errors. label prefixes messages; the remote command (not the token) is
// safe to surface.
func (s *Sandbox) transferStep(ctx context.Context, sess *shellio.Session, step, label string) error {
	res, err := sess.Run(ctx, step)
	if err != nil {
		return s.mapShellErr(ctx, err, label)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: remote step failed (exit %d): %s", label, res.ExitCode, truncateForErr(res.Output))
	}
	return nil
}

// mapShellErr classifies a shellio failure into a typed sentinel: ctx
// deadline→ErrTimeout, ctx cancel→context error, protocol→ErrConnClosed.
func (s *Sandbox) mapShellErr(ctx context.Context, err error, label string) error {
	if ce := ctx.Err(); ce != nil {
		if errors.Is(ce, context.DeadlineExceeded) {
			return fmt.Errorf("%s: %w", label, ErrTimeout)
		}
		return ce
	}
	if errors.Is(err, shellio.ErrProtocol) {
		return fmt.Errorf("%s: %w", label, ErrConnClosed)
	}
	return fmt.Errorf("%s: %w", label, err)
}

// randSuffix returns a short random hex string for unique remote temp paths.
func randSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// truncateForErr bounds remote output included in an error message.
func truncateForErr(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// tarball packs srcPath (a file or directory) into a gzip-compressed tar archive
// with entry names relative to srcPath's parent, so the basename is preserved
// (like `tar c -C <parent> <basename>`). Mode bits, directory structure, and
// symlinks are preserved.
func tarball(srcPath string) ([]byte, error) {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(srcPath)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	add := func(path string, fi os.FileInfo) error {
		link := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(parent, path)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path) //nolint:gosec // path comes from a local walk of a user-named source
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	}

	if info.IsDir() {
		err = filepath.Walk(srcPath, func(path string, fi os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			return add(path, fi)
		})
	} else {
		err = add(srcPath, info)
	}
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// untar extracts a gzip-compressed tar archive into destDir. Entry paths are
// sanitized: any entry that would escape destDir (path traversal / absolute
// path) is rejected with ErrInvalidOption.
func untar(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gunzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	cleanDest := filepath.Clean(destDir)
	sep := string(os.PathSeparator)
	var total int64
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+sep) {
			return fmt.Errorf("%w: unsafe archive entry %q", ErrInvalidOption, hdr.Name)
		}
		target := filepath.Join(cleanDest, name)
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+sep) {
			return fmt.Errorf("%w: archive entry %q escapes destination", ErrInvalidOption, hdr.Name)
		}
		// .Perm() keeps only 0o777, so setuid/setgid/sticky from a hostile archive
		// are never applied.
		mode := hdr.FileInfo().Mode().Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode); err != nil { //nolint:gosec // preserve archive dir perms (scp -r semantics)
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			n, err := writeFileFromTar(target, tr, mode)
			if err != nil {
				return err
			}
			if total += n; total > maxUntarTotalBytes {
				return fmt.Errorf("%w: archive exceeds %d bytes", ErrInvalidOption, int64(maxUntarTotalBytes))
			}
		case tar.TypeSymlink:
			// Reject a symlink whose target escapes the destination — otherwise a
			// later entry could be written THROUGH it (string path checks alone
			// don't catch symlink-resolved writes). With every symlink confined to
			// dest, writes through them also stay in dest.
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), hdr.Linkname)) //nolint:gosec // computed only to validate the target stays within destDir (checked next)
			if filepath.IsAbs(hdr.Linkname) || (resolved != cleanDest && !strings.HasPrefix(resolved, cleanDest+sep)) {
				return fmt.Errorf("%w: unsafe symlink %q → %q", ErrInvalidOption, hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Skip unsupported entry types (devices, fifos, …).
		}
	}
	return nil
}

// writeFileFromTar writes one regular-file entry (capped per file as a
// decompression-bomb guard) and returns the bytes written.
func writeFileFromTar(target string, tr *tar.Reader, mode os.FileMode) (int64, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) //nolint:gosec // mode is the archive's own perms (scp -r semantics)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(tr, maxUntarFileBytes)) //nolint:gosec // bounded by LimitReader
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

const (
	// maxUntarFileBytes bounds a single extracted file (decompression-bomb guard).
	maxUntarFileBytes = 1 << 30 // 1 GiB
	// maxUntarTotalBytes bounds the whole extraction (many-files / zip-bomb guard).
	maxUntarTotalBytes = 2 << 30 // 2 GiB
)
