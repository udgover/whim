package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// buildJSON is the stable, redacted result emitted by `whim build --json`.
type buildJSON struct {
	Name   string `json:"name"`
	ARN    string `json:"arn"`
	Source string `json:"source"`
	Cached bool   `json:"cached"`
	Egress string `json:"egress"`
}

// renderBuildJSON writes one redacted build result object. Account IDs (when
// WHIM_REDACT_ACCOUNT=1) and source credentials are masked consistently.
func renderBuildJSON(w io.Writer, name, arn, source, egress string, cached bool) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(buildJSON{
		Name:   name,
		ARN:    redactAccountID(arn),
		Source: redactAccountID(redactSourceCreds(source)),
		Cached: cached,
		Egress: egress,
	})
}

func init() {
	buildCmd.Flags().String("name", "", "image name (required; the build cache key)")
	buildCmd.Flags().String("egress", "public", "outbound network policy: public|none")
	buildCmd.Flags().Bool("force", false, "delete and rebuild even if the image exists (destructive)")
	buildCmd.Flags().String("context-subdir", "", "build-context subdirectory to descend into before locating the Dockerfile")
	buildCmd.Flags().Bool("json", false, "print a single JSON object instead of human progress")
	_ = buildCmd.MarkFlagRequired("name")
	rootCmd.AddCommand(buildCmd)
}

var buildCmd = &cobra.Command{
	Use:   "build <source>",
	Short: "Build a custom MicroVM image from a source context",
	Long: `whim build builds a custom MicroVM image from a build source and caches its
ARN by name for use with 'whim shell --image', 'whim run', etc.

The source is one of:

  ./dir                     a local build context (Dockerfile at its root)
  ./Dockerfile              a single local Dockerfile
  s3://bucket/key           an S3 zip archive or raw Dockerfile
  https://host/path         an HTTPS zip archive or raw Dockerfile

  whim build ./app --name whim-app
  whim build ./app --name whim-app --egress none --force
  whim build s3://my-bucket/app.zip --name whim-s3 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runBuild,
}

// parseEgress maps the --egress flag to a microvm.EgressMode, rejecting any
// value other than the two supported modes before any AWS call is made.
func parseEgress(s string) (microvm.EgressMode, error) {
	switch s {
	case "public":
		return microvm.EgressPublic, nil
	case "none":
		return microvm.EgressNone, nil
	default:
		return 0, fmt.Errorf("unsupported --egress %q (use 'public' or 'none')", s)
	}
}

func runBuild(cmd *cobra.Command, args []string) error {
	displaySource := args[0]
	source := displaySource
	name, _ := cmd.Flags().GetString("name")
	egressFlag, _ := cmd.Flags().GetString("egress")
	force, _ := cmd.Flags().GetBool("force")
	contextSubdir, _ := cmd.Flags().GetString("context-subdir")
	jsonOut, _ := cmd.Flags().GetBool("json")

	// Validate flags before touching AWS.
	egress, err := parseEgress(egressFlag)
	if err != nil {
		return err
	}

	// Lower GitHub shorthand (CLI-only) to a concrete HTTPS archive URL, and
	// carry GITHUB_TOKEN as a request header — never in the URL, output, or config.
	var httpsHeaders map[string]string
	var sourceIdentity string
	if gh, ok, gerr := parseGitHubShorthand(source); gerr != nil {
		return gerr
	} else if ok {
		source = gh.url
		httpsHeaders = githubAuthHeader(githubToken())
		if gh.immutable {
			sourceIdentity = gh.ref
		}
	}

	// Under --json, the object is the only thing on stdout: route progress
	// chatter (here and in resolveBuildEnv) to a discard writer and keep the
	// real stdout for the final object.
	out := cmd.OutOrStdout()
	if jsonOut {
		cmd.SetOut(io.Discard)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), imageBuildTimeout)
	defer cancel()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	// Shared bootstrap: caller identity, artifact bucket, build role, base image.
	env, err := resolveBuildEnv(ctx, cfg, cmd)
	if err != nil {
		return err
	}

	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(env.accountID))
	opts := microvm.BuildFromSourceOptions{
		Name:           name,
		ArtifactBucket: env.bucket,
		BaseImageARN:   env.baseImageARN,
		BuildRoleARN:   env.buildRoleARN,
		Egress:         egress,
		Force:          force,
		ContextSubdir:  contextSubdir,
		HTTPSHeaders:   httpsHeaders,
		SourceIdentity: sourceIdentity,
	}

	// Determine cache status for reporting: a non-force build over an existing
	// image reuses it rather than rebuilding.
	cached := false
	if !force {
		if _, gerr := mgr.GetImage(ctx, mgr.ImageARN(name)); gerr == nil {
			cached = true
		}
	}

	if force {
		printOut(cmd, "  Force: deleting then rebuilding image %q…\n", name)
	} else {
		printOut(cmd, "  Building image %q (this takes a few minutes if not cached)…\n", name)
	}
	arn, err := mgr.BuildFromSource(ctx, source, opts)
	if err != nil {
		return fmt.Errorf("build image: %w", err)
	}
	printOut(cmd, "  Image ready: %s\n", arn)

	// Cache the custom image ARN by name, preserving the default image_arn.
	cfgFile, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfgFile.SetImage(name, arn)
	if err := SaveConfig(cfgFile); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	printOut(cmd, "  Saved image %q to %s\n", name, ConfigPath())

	if jsonOut {
		return renderBuildJSON(out, name, arn, displaySource, egressFlag, cached)
	}
	return nil
}
