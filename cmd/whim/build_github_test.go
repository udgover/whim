package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fullSHA = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"

func TestGitHubShorthand_LowersToArchiveURL(t *testing.T) {
	gh, ok, err := parseGitHubShorthand("github.com/org/repo@v1.2.3")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "https://codeload.github.com/org/repo/zip/v1.2.3", gh.url)
}

func TestGitHubShorthand_GitPlusHTTPS(t *testing.T) {
	gh, ok, err := parseGitHubShorthand("git+https://github.com/org/repo@" + fullSHA)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "https://codeload.github.com/org/repo/zip/"+fullSHA, gh.url)
}

func TestGitHubShorthand_DefaultRefWhenOmitted(t *testing.T) {
	gh, ok, err := parseGitHubShorthand("github.com/org/repo")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "https://codeload.github.com/org/repo/zip/HEAD", gh.url)
	assert.False(t, gh.immutable, "a defaulted ref is a moving ref")
}

func TestGitHubShorthand_FullSHAisImmutable(t *testing.T) {
	gh, _, err := parseGitHubShorthand("github.com/org/repo@" + fullSHA)
	require.NoError(t, err)
	assert.True(t, gh.immutable, "a full commit SHA is an immutable source identity")

	gh, _, err = parseGitHubShorthand("github.com/org/repo@main")
	require.NoError(t, err)
	assert.False(t, gh.immutable, "a branch ref is treated as moving")
}

func TestGitHubShorthand_NotShorthandPassesThrough(t *testing.T) {
	for _, s := range []string{
		"./local/dir",
		"s3://bucket/app.zip",
		"https://example.com/app.zip",
		"https://github.com/org/repo/archive/main.zip", // direct HTTPS archive, not shorthand
	} {
		_, ok, err := parseGitHubShorthand(s)
		require.NoError(t, err, s)
		assert.Falsef(t, ok, "%q must not be treated as GitHub shorthand", s)
	}
}

func TestGitHubShorthand_InvalidRejected(t *testing.T) {
	for _, s := range []string{"github.com/org", "github.com/org/", "github.com/"} {
		_, ok, err := parseGitHubShorthand(s)
		assert.Truef(t, ok, "%q is recognized as a github attempt", s)
		require.Errorf(t, err, "%q is an invalid shorthand", s)
	}
}

func TestBuildAuth_GitHubTokenHeader(t *testing.T) {
	h := githubAuthHeader("ghp_secrettoken")
	require.NotNil(t, h)
	assert.Equal(t, "token ghp_secrettoken", h["Authorization"])

	assert.Nil(t, githubAuthHeader(""), "no token yields no header")
}

func TestTokenRedact_TokenNeverInURL(t *testing.T) {
	// The token must travel only in the Authorization header, never in the URL.
	gh, ok, err := parseGitHubShorthand("github.com/org/private@" + fullSHA)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotContains(t, gh.url, "ghp_", "lowered URL must be token-free")

	h := githubAuthHeader("ghp_secrettoken")
	assert.NotContains(t, gh.url, "ghp_secrettoken")
	assert.Equal(t, "token ghp_secrettoken", h["Authorization"])
	assert.False(t, strings.Contains(gh.url, "secrettoken"))
}
