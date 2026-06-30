package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// fullCommitSHA matches a full git SHA-1 (40) or SHA-256 (64) hex commit id.
// Such a ref pins an immutable source; branches and tags are moving refs.
var fullCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)

// githubSource is a GitHub shorthand lowered to a concrete HTTPS archive URL.
type githubSource struct {
	url       string // codeload.github.com zip archive URL (token-free)
	ref       string // branch, tag, or commit SHA
	immutable bool   // true when ref is a full commit SHA
}

// parseGitHubShorthand recognizes CLI-only GitHub shorthand and lowers it to a
// codeload archive URL. It accepts "github.com/org/repo[@ref]" and
// "git+https://github.com/org/repo[@ref]"; a plain "https://github.com/..." URL
// is left for the library to handle as a direct HTTPS source.
//
// The returned bool reports whether s looked like GitHub shorthand at all, so
// the caller can pass non-GitHub sources straight through. A recognized but
// malformed shorthand returns (…, true, err).
func parseGitHubShorthand(s string) (githubSource, bool, error) {
	var body string
	switch {
	case strings.HasPrefix(s, "git+https://github.com/"):
		body = strings.TrimPrefix(s, "git+https://")
	case strings.HasPrefix(s, "github.com/"):
		body = s
	default:
		return githubSource{}, false, nil
	}

	path := strings.TrimPrefix(body, "github.com/")
	ref := "HEAD"
	immutable := false
	if at := strings.LastIndex(path, "@"); at >= 0 {
		ref = path[at+1:]
		path = path[:at]
		immutable = fullCommitSHA.MatchString(ref)
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")

	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || ref == "" {
		return githubSource{}, true, fmt.Errorf("invalid GitHub shorthand %q (want github.com/org/repo@ref)", s)
	}
	return githubSource{
		url:       fmt.Sprintf("https://codeload.github.com/%s/%s/zip/%s", parts[0], parts[1], ref),
		ref:       ref,
		immutable: immutable,
	}, true, nil
}

// githubToken returns the GitHub token from the environment, if set. Read at
// build time only; never logged or persisted.
func githubToken() string {
	return os.Getenv("GITHUB_TOKEN")
}

// githubAuthHeader returns the Authorization header for a private GitHub
// archive download, or nil when no token is set. The token is only ever sent
// as a request header — never embedded in a URL, printed, or persisted.
func githubAuthHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "token " + token}
}
