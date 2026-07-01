package microvm

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stageLocalSource packages a local build source into a build-context zip. A
// regular-file source is treated as a single Dockerfile; a directory source is
// walked. Missing paths return a wrapped error before any upload.
func stageLocalSource(path, contextSubdir string, caps stagingCaps) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat source %q: %w", path, err)
	}
	if info.IsDir() {
		return stageLocalDirectory(path, contextSubdir, caps)
	}
	return stageLocalDockerfile(path, caps)
}

// stageLocalDockerfile reads a single local Dockerfile and packages it as the
// sole entry of a build-context zip, re-rooted to "Dockerfile" regardless of
// the file's on-disk name. It includes no surrounding context. Missing,
// directory, and unreadable paths return wrapped errors before any upload.
func stageLocalDockerfile(path string, caps stagingCaps) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat dockerfile %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: %q is a directory, expected a Dockerfile", ErrInvalidSource, path)
	}
	content, err := readCapped(path, caps.maxUncompressedBytes)
	if err != nil {
		return nil, fmt.Errorf("read dockerfile %q: %w", path, err)
	}
	if int64(len(content)) > caps.maxUncompressedBytes {
		return nil, fmt.Errorf("%w: dockerfile exceeds uncompressed cap of %d bytes", ErrSourceTooLarge, caps.maxUncompressedBytes)
	}
	return buildContextZip([]zipEntry{{name: dockerfileName, content: content}}, caps)
}

// stageLocalDirectory walks a local directory build context and packages its
// regular files (and in-root symlinks) into a build-context zip. The effective
// root is root, optionally descended into contextSubdir. Symlinks whose target
// escapes the effective root are rejected; a Dockerfile must exist at the
// effective root. File-count, uncompressed, and compressed caps are enforced.
func stageLocalDirectory(root, contextSubdir string, caps stagingCaps) ([]byte, error) {
	sub, err := safeContextSubdir(contextSubdir)
	if err != nil {
		return nil, err
	}
	effectiveRoot := root
	if sub != "" {
		// Resolve symlinks on both the source root and the descended context
		// directory, then require the latter to stay within the former. A
		// lexical check on the subdir string is not enough: a symlinked path
		// component (e.g. sub -> /outside, or a/ctx where a -> /outside) would
		// otherwise let the walk start outside the source root. Walking the
		// resolved path also lets a legitimate in-root symlinked subdir work,
		// since filepath.WalkDir will not descend a symlink given as its root.
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve source root %q: %w", root, err)
		}
		resolvedSub, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(sub)))
		if err != nil {
			return nil, fmt.Errorf("resolve context %q: %w", sub, err)
		}
		rel, err := filepath.Rel(resolvedRoot, resolvedSub)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("%w: context-subdir %q escapes the source root", ErrInvalidSource, sub)
		}
		effectiveRoot = resolvedSub
	}
	if info, err := os.Stat(effectiveRoot); err != nil {
		return nil, fmt.Errorf("stat context %q: %w", effectiveRoot, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%w: context %q is not a directory", ErrInvalidSource, effectiveRoot)
	}

	ignore, err := loadDockerignore(effectiveRoot)
	if err != nil {
		return nil, err
	}

	var entries []zipEntry
	var totalUncompressed int64
	var haveDockerfile bool

	walkErr := filepath.WalkDir(effectiveRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == effectiveRoot {
			return nil
		}
		relOS, err := filepath.Rel(effectiveRoot, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)

		if d.IsDir() {
			// Skip whole ignored subtrees instead of walking and discarding
			// every file — but only when no negation could re-include something
			// beneath them.
			if !ignore.hasNegation() && ignore.dirIgnored(rel) {
				return filepath.SkipDir
			}
			return nil // directories are implied by their files' paths
		}

		// The Dockerfile is always kept, even if ignore rules would exclude it.
		if rel != dockerfileName && ignore.ignored(rel) {
			return nil
		}

		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("%w: read symlink %q: %v", ErrInvalidSource, rel, err)
			}
			if err := checkSymlinkTarget(rel, filepath.ToSlash(target)); err != nil {
				return err
			}
			if len(entries)+1 > caps.maxFiles {
				return fmt.Errorf("%w: context has more than %d files", ErrSourceTooLarge, caps.maxFiles)
			}
			entries = append(entries, zipEntry{name: rel, mode: fs.ModeSymlink | 0o777, content: []byte(target)})
			return nil
		}

		if !d.Type().IsRegular() {
			return nil // skip devices, sockets, fifos
		}

		if len(entries)+1 > caps.maxFiles {
			return fmt.Errorf("%w: context has more than %d files", ErrSourceTooLarge, caps.maxFiles)
		}
		content, err := readCapped(p, caps.maxUncompressedBytes-totalUncompressed)
		if err != nil {
			return err
		}
		totalUncompressed += int64(len(content))
		if totalUncompressed > caps.maxUncompressedBytes {
			return fmt.Errorf("%w: context exceeds uncompressed cap of %d bytes", ErrSourceTooLarge, caps.maxUncompressedBytes)
		}
		if rel == dockerfileName {
			haveDockerfile = true
		}
		entries = append(entries, zipEntry{name: rel, content: content})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if !haveDockerfile {
		return nil, fmt.Errorf("%w: no %s at the effective context root", ErrInvalidSource, dockerfileName)
	}
	return buildContextZip(entries, caps)
}

// loadDockerignore reads and parses the .dockerignore at the context root, if
// present. A missing file yields an empty matcher that ignores nothing.
func loadDockerignore(root string) (*ignoreMatcher, error) {
	data, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return parseDockerignore(nil)
		}
		return nil, fmt.Errorf("read .dockerignore: %w", err)
	}
	return parseDockerignore(data)
}

// readCapped reads at most limit bytes from the file at p (plus one extra byte
// so the caller can detect overflow against the cap) and returns its bytes.
func readCapped(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", p, err)
	}
	return content, nil
}
