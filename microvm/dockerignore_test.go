package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerignoreMatching(t *testing.T) {
	lines := []string{
		"# a comment",
		"",
		"*.log",
		"!keep.log",
		"node_modules/",
		"/build",
		"docs/*.md",
	}
	m, err := parseDockerignore([]byte(strings.Join(lines, "\n")))
	require.NoError(t, err)

	cases := map[string]bool{
		"app.log":             true,  // *.log
		"keep.log":            false, // negated after *.log
		"node_modules/x/y.js": true,  // directory prefix
		"build":               true,  // /build anchored exact
		"build/out.txt":       true,  // under matched dir
		"docs/readme.md":      true,  // docs/*.md
		"docs/sub/deep.md":    false, // no ** support: prefix seg mismatch
		"src/app.log":         false, // *.log is a single top-level segment glob
		"src/main.go":         false, // unmatched
	}
	for p, want := range cases {
		assert.Equalf(t, want, m.ignored(p), "ignored(%q)", p)
	}
}

func TestDockerignoreUnsupportedRejected(t *testing.T) {
	for _, pat := range []string{"**/foo", "src/**", "file[abc].txt"} {
		_, err := parseDockerignore([]byte(pat))
		require.Errorf(t, err, "pattern %q should be rejected", pat)
		assert.Truef(t, errors.Is(err, ErrInvalidSource), "pattern %q: want ErrInvalidSource, got %v", pat, err)
	}
}

func TestDockerignoreEmptyIgnoresNothing(t *testing.T) {
	m, err := parseDockerignore(nil)
	require.NoError(t, err)
	assert.False(t, m.ignored("anything/at/all.txt"))
}

func TestDockerignoreExcludesInDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.env"), []byte("token"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("*.env\n"), 0o644))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "keep.txt")
	assert.NotContains(t, entries, "secret.env")
}

func TestDockerignoreIgnoredSubdirExcluded(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("node_modules/\n"), 0o644))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	assert.Contains(t, entries, "Dockerfile")
	assert.NotContains(t, entries, "node_modules/pkg/index.js")
}

func TestDockerignoreNegationReincludesUnderIgnoredDir(t *testing.T) {
	// Pruning must not fire when a negation could re-include something beneath
	// an otherwise-ignored directory.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "keep.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "skip.txt"), []byte("skip"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("sub/\n!sub/keep.txt\n"), 0o644))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	assert.Contains(t, entries, "sub/keep.txt", "negation must re-include the file")
	assert.NotContains(t, entries, "sub/skip.txt")
}

func TestDockerignoreKeepsDockerfile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\n"), 0o644))
	// A wildcard that would otherwise exclude everything, including Dockerfile.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("*\n"), 0o644))

	zipped, err := stageLocalDirectory(dir, "", defaultStagingCaps())
	require.NoError(t, err)

	entries := unzipEntries(t, zipped)
	assert.Contains(t, entries, "Dockerfile", "Dockerfile must survive even a catch-all ignore")
	assert.NotContains(t, entries, "app.go")
}
