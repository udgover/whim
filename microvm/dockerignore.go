package microvm

import (
	"fmt"
	"path"
	"strings"
)

// ignoreMatcher applies a documented subset of .dockerignore semantics to
// forward-slash, root-relative paths.
//
// Supported: blank lines, '#' comments, '!' negation, leading '/' anchoring,
// trailing '/' directory matches, and '*'/'?' single-segment globs. The last
// matching pattern wins. Unsupported constructs — '**' cross-segment globs and
// '[...]' character classes — are rejected at parse time (fail closed) rather
// than silently mishandled, since a wrong ignore decision could leak files the
// author meant to exclude.
type ignoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	segments []string
	negate   bool
	dirOnly  bool
}

// parseDockerignore parses .dockerignore content into a matcher. It returns an
// error wrapping ErrInvalidSource when a line uses an unsupported construct.
func parseDockerignore(data []byte) (*ignoreMatcher, error) {
	m := &ignoreMatcher{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var negate bool
		if strings.HasPrefix(line, "!") {
			negate = true
			line = strings.TrimSpace(line[1:])
		}
		if strings.Contains(line, "**") {
			return nil, fmt.Errorf("%w: .dockerignore '**' is not supported in the MVP subset (pattern %q)", ErrInvalidSource, line)
		}
		if strings.ContainsAny(line, "[]") {
			return nil, fmt.Errorf("%w: .dockerignore character classes are not supported in the MVP subset (pattern %q)", ErrInvalidSource, line)
		}
		dirOnly := strings.HasSuffix(line, "/")
		trimmed := strings.Trim(line, "/")
		if trimmed == "" {
			continue
		}
		m.patterns = append(m.patterns, ignorePattern{
			segments: strings.Split(trimmed, "/"),
			negate:   negate,
			dirOnly:  dirOnly,
		})
	}
	return m, nil
}

// ignored reports whether the given root-relative path is excluded. The last
// matching pattern decides; a '!' pattern re-includes.
func (m *ignoreMatcher) ignored(p string) bool {
	segs := strings.Split(p, "/")
	ignored := false
	for _, pat := range m.patterns {
		if pat.matches(segs) {
			ignored = !pat.negate
		}
	}
	return ignored
}

// hasNegation reports whether any pattern re-includes (starts with '!'). When
// true, an ignored directory cannot be safely pruned during a walk, because a
// negation might re-include a file beneath it.
func (m *ignoreMatcher) hasNegation() bool {
	for _, p := range m.patterns {
		if p.negate {
			return true
		}
	}
	return false
}

// dirIgnored reports whether every file beneath directory p would be excluded,
// so a walk may skip the subtree entirely. It is only sound to act on when
// hasNegation is false. A directory matches when a pattern's segments match it
// as a prefix (whether or not the pattern carried a trailing slash).
func (m *ignoreMatcher) dirIgnored(p string) bool {
	segs := strings.Split(p, "/")
	ignored := false
	for _, pat := range m.patterns {
		if pat.matchesDir(segs) {
			ignored = !pat.negate
		}
	}
	return ignored
}

// matchesDir reports whether the pattern matches the directory segments as a
// prefix (pattern no longer than the path, every pattern segment matching).
func (p ignorePattern) matchesDir(segs []string) bool {
	n := len(p.segments)
	if n == 0 || len(segs) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if ok, _ := path.Match(p.segments[i], segs[i]); !ok {
			return false
		}
	}
	return true
}

// matches reports whether the pattern matches the path segments, either as an
// exact path (non-directory patterns) or as a directory prefix that excludes
// everything beneath it.
func (p ignorePattern) matches(segs []string) bool {
	n := len(p.segments)
	if n == 0 || len(segs) < n {
		return false
	}
	for i := 0; i < n; i++ {
		// Per-segment glob: '*'/'?' do not cross '/'. Character classes were
		// rejected at parse time, so path.Match cannot error here.
		if ok, _ := path.Match(p.segments[i], segs[i]); !ok {
			return false
		}
	}
	if len(segs) == n {
		return !p.dirOnly // exact match only excludes files for non-dir patterns
	}
	return true // path lies under a matched directory prefix
}
