package microvm

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// dockerfileName is the canonical path of the Dockerfile at the build-context
// root. The Lambda MicroVM build expects it here regardless of how the source
// named or nested it.
const dockerfileName = "Dockerfile"

// zipEntry is one file to include in a build-context zip. name is a
// forward-slash path relative to the archive root. A zero mode writes a plain
// regular-file entry; a non-zero mode is applied verbatim (used to preserve
// symlink entries when re-rooting an archive).
type zipEntry struct {
	name    string
	mode    fs.FileMode
	content []byte
}

// buildContextZip packages entries into a deterministic in-memory zip and
// returns its bytes, enforcing the compressed-size cap. Callers that walk a
// source enforce the uncompressed and file-count caps as they collect entries;
// this function is the single place that produces the archive bytes and checks
// the compressed result.
//
// Staging is intentionally all in-memory (no temp files, no new deps), so peak
// memory is roughly the uncompressed context (bounded by maxUncompressedBytes,
// default 1 GiB) plus the compressed output. Callers embedding this at scale
// should size maxUncompressedBytes accordingly; a streaming/temp-file path is a
// possible future optimization.
func buildContextZip(entries []zipEntry, caps stagingCaps) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		var (
			fw  io.Writer
			err error
		)
		if e.mode == 0 {
			fw, err = w.Create(e.name)
		} else {
			hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			hdr.SetMode(e.mode)
			fw, err = w.CreateHeader(hdr)
		}
		if err != nil {
			return nil, fmt.Errorf("zip create %q: %w", e.name, err)
		}
		if _, err := fw.Write(e.content); err != nil {
			return nil, fmt.Errorf("zip write %q: %w", e.name, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("zip finalize: %w", err)
	}
	if int64(buf.Len()) > caps.maxCompressedBytes {
		return nil, fmt.Errorf("%w: staged archive is %d bytes, exceeds compressed cap of %d",
			ErrSourceTooLarge, buf.Len(), caps.maxCompressedBytes)
	}
	return buf.Bytes(), nil
}

// rerootZip validates an input zip archive and produces a normalized build
// context zip with the Dockerfile at the root.
//
// Validation rejects (with ErrInvalidSource) absolute paths, ".." traversal,
// backslash/volume tricks, duplicate effective paths, and symlinks whose target
// escapes the archive root; oversized archives are rejected with
// ErrSourceTooLarge. A single wrapping top-level directory (as produced by
// forge archive downloads) is stripped by default, then contextSubdir — which
// cannot escape root — is descended into before requiring the Dockerfile.
func rerootZip(archive []byte, contextSubdir string, caps stagingCaps) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("%w: open archive: %v", ErrInvalidSource, err)
	}

	type item struct {
		name    string
		mode    fs.FileMode
		content []byte
	}
	var items []item
	seen := make(map[string]struct{})
	var totalUncompressed int64

	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue // directory entry; carried implicitly by file paths
		}
		clean, err := safeArchivePath(f.Name)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[clean]; dup {
			return nil, fmt.Errorf("%w: duplicate path in archive: %q", ErrInvalidSource, clean)
		}
		seen[clean] = struct{}{}

		if len(items)+1 > caps.maxFiles {
			return nil, fmt.Errorf("%w: archive has more than %d files", ErrSourceTooLarge, caps.maxFiles)
		}

		// Read with a hard limit so a lying header or expanding entry cannot
		// blow past the uncompressed cap regardless of declared sizes.
		remaining := caps.maxUncompressedBytes - totalUncompressed
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: open entry %q: %v", ErrInvalidSource, clean, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, remaining+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: read entry %q: %v", ErrInvalidSource, clean, err)
		}
		totalUncompressed += int64(len(content))
		if totalUncompressed > caps.maxUncompressedBytes {
			return nil, fmt.Errorf("%w: archive exceeds uncompressed cap of %d bytes",
				ErrSourceTooLarge, caps.maxUncompressedBytes)
		}

		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			if err := checkSymlinkTarget(clean, string(content)); err != nil {
				return nil, err
			}
		}
		items = append(items, item{name: clean, mode: mode, content: content})
	}

	// Strip a single wrapping top-level directory, then descend contextSubdir.
	names := make([]string, len(items))
	for i := range items {
		names[i] = items[i].name
	}
	prefix := singleTopLevelDir(names)
	sub, err := safeContextSubdir(contextSubdir)
	if err != nil {
		return nil, err
	}
	if sub != "" {
		prefix += sub + "/"
	}

	var entries []zipEntry
	var haveDockerfile bool
	for _, it := range items {
		if prefix != "" && !strings.HasPrefix(it.name, prefix) {
			continue
		}
		name := strings.TrimPrefix(it.name, prefix)
		if name == "" {
			continue
		}
		// Re-validate symlinks against the re-rooted name: a target that stayed
		// in-root for the whole archive can escape once a wrapping directory is
		// stripped or contextSubdir is descended into.
		if it.mode&fs.ModeSymlink != 0 {
			if err := checkSymlinkTarget(name, string(it.content)); err != nil {
				return nil, err
			}
		}
		if name == dockerfileName {
			haveDockerfile = true
		}
		entries = append(entries, zipEntry{name: name, mode: it.mode, content: it.content})
	}
	if !haveDockerfile {
		return nil, fmt.Errorf("%w: no %s at the effective build-context root", ErrInvalidSource, dockerfileName)
	}
	return buildContextZip(entries, caps)
}

// safeArchivePath validates one archive entry name and returns its cleaned,
// forward-slash, root-relative form. It rejects absolute paths, Windows volume
// and backslash tricks, and any path that escapes the archive root via "..".
func safeArchivePath(name string) (string, error) {
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("%w: backslash in archive path: %q", ErrInvalidSource, name)
	}
	if strings.HasPrefix(name, "/") || hasDriveLetter(name) {
		return "", fmt.Errorf("%w: absolute path in archive: %q", ErrInvalidSource, name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", fmt.Errorf("%w: path escapes archive root: %q", ErrInvalidSource, name)
	}
	return clean, nil
}

// hasDriveLetter reports whether p begins with a Windows drive-letter root
// such as "C:" or "C:/". It deliberately does not flag a colon elsewhere in a
// name (e.g. the POSIX-legal "a:b.txt"), which is not an absolute path.
func hasDriveLetter(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
		return false
	}
	return len(p) == 2 || p[2] == '/' || p[2] == '\\'
}

// checkSymlinkTarget rejects a symlink whose target is absolute or resolves
// outside the archive root. linkName is the cleaned path of the symlink itself.
func checkSymlinkTarget(linkName, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || hasDriveLetter(target) {
		return fmt.Errorf("%w: symlink %q targets an absolute path", ErrInvalidSource, linkName)
	}
	resolved := path.Clean(path.Join(path.Dir(linkName), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%w: symlink %q escapes archive root", ErrInvalidSource, linkName)
	}
	return nil
}

// safeContextSubdir validates a caller-supplied --context-subdir and returns
// its cleaned, root-relative form ("" means no descent). It cannot escape root.
func safeContextSubdir(sub string) (string, error) {
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return "", nil
	}
	if strings.ContainsRune(sub, '\\') {
		return "", fmt.Errorf("%w: backslash in context-subdir: %q", ErrInvalidSource, sub)
	}
	clean := path.Clean(sub)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: context-subdir escapes archive root: %q", ErrInvalidSource, sub)
	}
	return clean, nil
}

// singleTopLevelDir returns "dir/" when every name shares one wrapping
// top-level directory, or "" otherwise (e.g. a Dockerfile already at root).
func singleTopLevelDir(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var root string
	for i, name := range names {
		slash := strings.IndexByte(name, '/')
		if slash < 0 {
			return "" // a file sits at the root → nothing to strip
		}
		top := name[:slash]
		if i == 0 {
			root = top
		} else if top != root {
			return "" // more than one top-level directory
		}
	}
	return root + "/"
}
