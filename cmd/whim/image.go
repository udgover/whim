package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

func init() {
	// Shared across all image subcommands (and any added later).
	imageCmd.PersistentFlags().BoolP("quiet", "q", false,
		"machine output: names only (ls) / suppress per-image messages (rm)")
	imageCmd.PersistentFlags().Bool("json", false, "output JSON")

	imageCmd.AddCommand(imageLsCmd)
	imageCmd.AddCommand(imageRmCmd)
	rootCmd.AddCommand(imageCmd)
}

var imageCmd = &cobra.Command{
	Use:   "image",
	Short: "Manage whim MicroVM images",
}

var imageLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List MicroVM images in your account",
	Long: `List MicroVM images.

  whim image ls            # table
  whim image ls -q         # names only (e.g. whim image rm $(whim image ls -q))
  whim image ls --json     # JSON array`,
	Args: cobra.NoArgs,
	RunE: runImageLs,
}

var imageRmCmd = &cobra.Command{
	Use:   "rm <name|arn> [name|arn...]",
	Short: "Delete one or more MicroVM images",
	Long: `Delete MicroVM images. Errors (exit non-zero) if an image does not exist.

  whim image rm whim-test          # delete by name
  whim image rm -q whim-test       # suppress success chatter (errors still shown)
  whim image rm --json whim-test   # JSON result array`,
	Args: cobra.MinimumNArgs(1),
	RunE: runImageRm,
}

// outputMode captures the shared -q/--json formatting choice.
type outputMode struct {
	quiet bool
	json  bool
}

// newOutputMode validates the quiet/json combination.
func newOutputMode(quiet, jsonOut bool) (outputMode, error) {
	if quiet && jsonOut {
		return outputMode{}, errors.New("--quiet and --json are mutually exclusive")
	}
	return outputMode{quiet: quiet, json: jsonOut}, nil
}

// imageOutputMode reads the shared flags off cmd.
func imageOutputMode(cmd *cobra.Command) (outputMode, error) {
	q, _ := cmd.Flags().GetBool("quiet")
	j, _ := cmd.Flags().GetBool("json")
	return newOutputMode(q, j)
}

// imageJSON is the machine-readable shape for `image ls --json`.
type imageJSON struct {
	Name      string `json:"name"`
	ARN       string `json:"arn"`
	State     string `json:"state"`
	Version   string `json:"version,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// renderImageList writes the image list to w in the requested mode. Pure
// (no AWS, no globals) so it is directly unit-testable. It renders into a buffer
// first, then writes redacted (WHIM_REDACT_ACCOUNT) — redacting after the
// tabwriter lays out columns keeps the table aligned.
func renderImageList(w io.Writer, images []microvm.ImageSummary, mode outputMode) error {
	var buf bytes.Buffer
	switch {
	case mode.json:
		arr := make([]imageJSON, 0, len(images))
		for _, img := range images {
			j := imageJSON{Name: img.Name, ARN: img.ARN, State: img.State, Version: img.Version}
			if !img.CreatedAt.IsZero() {
				j.CreatedAt = img.CreatedAt.UTC().Format(time.RFC3339)
			}
			arr = append(arr, j)
		}
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(arr); err != nil {
			return err
		}

	case mode.quiet:
		for _, img := range images {
			fmt.Fprintln(&buf, img.Name)
		}

	default:
		if len(images) == 0 {
			fmt.Fprintln(&buf, "No images found. Run 'whim init' to build the default image.")
		} else {
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tVERSION\tCREATED\tARN")
			for _, img := range images {
				created := "-"
				if !img.CreatedAt.IsZero() {
					created = img.CreatedAt.Format("2006-01-02 15:04")
				}
				version := img.Version
				if version == "" {
					version = "-"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", img.Name, img.State, version, created, img.ARN)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, redactAccountID(buf.String()))
	return err
}

func runImageLs(cmd *cobra.Command, _ []string) error {
	mode, err := imageOutputMode(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	images, err := microvm.NewFromConfig(cfg).ListImages(ctx)
	if err != nil {
		return err
	}
	return renderImageList(cmd.OutOrStdout(), images, mode)
}

// rmResult is the machine-readable per-image outcome for `image rm --json`.
type rmResult struct {
	Name   string `json:"name,omitempty"`
	ARN    string `json:"arn"`
	Status string `json:"status"` // deleted | not_found | error
	Error  string `json:"error,omitempty"`
}

func runImageRm(cmd *cobra.Command, args []string) error {
	mode, err := imageOutputMode(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return fmt.Errorf("resolve account ID: %w", err)
	}
	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(aws.ToString(id.Account)))
	verbose := !mode.quiet && !mode.json

	results := make([]rmResult, 0, len(args))
	failures := 0
	for _, arg := range args {
		arn := arg
		res := rmResult{ARN: arg}
		if !strings.HasPrefix(arg, "arn:") {
			arn = mgr.ImageARN(arg)
			res.Name = arg
			res.ARN = arn
		}
		// Existence pre-check so a missing image is reported (Docker-style).
		if _, gerr := mgr.GetImage(ctx, arn); gerr != nil {
			if errors.Is(gerr, microvm.ErrImageNotFound) {
				res.Status = "not_found"
				if verbose {
					printErr(cmd, "no such image: %s\n", arg)
				}
			} else {
				res.Status = "error"
				res.Error = gerr.Error()
				if verbose {
					printErr(cmd, "failed to look up %s: %v\n", arg, gerr)
				}
			}
			failures++
			results = append(results, res)
			continue
		}
		if derr := mgr.DeleteImage(ctx, arn); derr != nil {
			res.Status = "error"
			res.Error = derr.Error()
			if verbose {
				printErr(cmd, "failed to delete %s: %v\n", arg, derr)
			}
			failures++
			results = append(results, res)
			continue
		}
		res.Status = "deleted"
		results = append(results, res)
		if verbose {
			printOut(cmd, "deleted %s\n", arg)
		}
		clearCacheIfMatches(cmd, arn, verbose)
	}

	if mode.json {
		if err := renderRmResults(cmd.OutOrStdout(), results); err != nil {
			return err
		}
	}
	if failures > 0 {
		// Per-image detail is already emitted above; return a summary so the
		// process exits non-zero without duplicating it.
		return fmt.Errorf("%d of %d image(s) could not be removed", failures, len(args))
	}
	return nil
}

// renderRmResults writes the `image rm --json` result array, redacted
// (WHIM_REDACT_ACCOUNT) — the ARNs in the results carry the account, so this
// goes through the same buffer→redact→write path as the list renderers.
func renderRmResults(w io.Writer, results []rmResult) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return err
	}
	_, err := io.WriteString(w, redactAccountID(buf.String()))
	return err
}

// clearCacheIfMatches clears the cached default-image ARN if it matches the
// image just deleted, so the cache never points at a removed image.
func clearCacheIfMatches(cmd *cobra.Command, arn string, verbose bool) {
	cfg, err := LoadConfig()
	if err != nil {
		return
	}
	clearedDefault := cfg.ImageARN == arn
	if clearedDefault {
		cfg.ImageARN = ""
	}
	// Prune any custom image entry pointing at the deleted ARN; unrelated custom
	// images are preserved.
	prunedCustom := cfg.removeImageByARN(arn)
	if !clearedDefault && !prunedCustom {
		return
	}
	if err := SaveConfig(cfg); err != nil {
		if verbose {
			printErr(cmd, "warning: failed to update cached images: %v\n", err)
		}
		return
	}
	if verbose && clearedDefault {
		printOut(cmd, "cleared cached default image (was %s); run 'whim init' to rebuild\n", arn)
	}
}
