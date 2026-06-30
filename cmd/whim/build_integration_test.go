//go:build integration

// Real-AWS, opt-in integration coverage for `whim build`'s CLI-specific paths
// (GitHub shorthand lowering + GITHUB_TOKEN header auth + shared bootstrap).
// Run with: WHIM_INTEGRATION=1 WHIM_TEST_GITHUB=org/repo@<full-sha> \
//           go test -tags=integration ./cmd/whim/ -v
package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// TestIntegration_BuildGitHubFullSHA exercises the CLI's GitHub shorthand path
// end-to-end: parse github.com/org/repo@<sha> -> codeload URL, attach the
// GITHUB_TOKEN header, run the shared bootstrap, and build via the library.
//
// Set WHIM_TEST_GITHUB to "org/repo@<full-sha>" for a repo whose archive has a
// Dockerfile at its root (private repos also need GITHUB_TOKEN).
func TestIntegration_BuildGitHubFullSHA(t *testing.T) {
	if os.Getenv("WHIM_INTEGRATION") != "1" {
		t.Skip("set WHIM_INTEGRATION=1 to run real-AWS integration tests")
	}
	spec := os.Getenv("WHIM_TEST_GITHUB")
	if spec == "" {
		t.Skip("set WHIM_TEST_GITHUB=org/repo@<full-sha> to run the GitHub build test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Lower the shorthand exactly as runBuild does.
	gh, ok, err := parseGitHubShorthand("github.com/" + spec)
	require.NoError(t, err)
	require.True(t, ok, "WHIM_TEST_GITHUB must be org/repo@ref")
	require.True(t, gh.immutable, "use a full commit SHA so the source is immutable")
	assert.NotContains(t, gh.url, githubToken(), "token must never appear in the URL")

	cfg, err := buildAWSConfig(ctx, &cobra.Command{})
	require.NoError(t, err)

	quiet := &cobra.Command{}
	quiet.SetOut(os.Stderr)
	env, err := resolveBuildEnv(ctx, cfg, quiet)
	require.NoError(t, err)

	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(env.accountID),
		microvm.WithPollInterval(2*time.Second))
	arn, err := mgr.BuildFromSource(ctx, gh.url, microvm.BuildFromSourceOptions{
		Name:           "whim-it-github",
		ArtifactBucket: env.bucket,
		BaseImageARN:   env.baseImageARN,
		BuildRoleARN:   env.buildRoleARN,
		Egress:         microvm.EgressPublic,
		Force:          true,
		HTTPSHeaders:   githubAuthHeader(githubToken()),
		SourceIdentity: gh.ref,
	})
	require.NoError(t, err)
	require.Contains(t, arn, ":microvm-image:whim-it-github")
	t.Cleanup(func() { _ = mgr.DeleteImage(context.Background(), arn) })
	t.Logf("built %s from %s", arn, spec)

	// The token must not have leaked into the cached/printed identity.
	assert.False(t, strings.Contains(gh.ref, githubToken()) && githubToken() != "")
}
