package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	psCmd.Flags().BoolP("quiet", "q", false, "machine output: VM ids only")
	psCmd.Flags().Bool("json", false, "output JSON")
	rootCmd.AddCommand(psCmd)

	gcCmd.Flags().Duration("older-than", 0, "only reap VMs at least this old (e.g. 1h); 0 = all")
	gcCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	rootCmd.AddCommand(gcCmd)
}

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List whim-owned MicroVMs (like docker ps)",
	Long: `List your whim MicroVMs (those launched from your account's whim images),
in any non-terminated state — running, pending, or suspended — each labeled with
its state. AWS-managed and other tools' VMs are never shown.

  whim ps
  whim ps -q                 # ids only (e.g. whim gc targets)
  whim ps --json`,
	Args: cobra.NoArgs,
	RunE: runPs,
}

var gcCmd = &cobra.Command{
	Use:   "gc [--older-than <dur>] [--yes]",
	Short: "Terminate whim-owned MicroVMs (confirms unless --yes)",
	Long: `Terminate whim-owned MicroVMs. Only VMs launched from your account's whim
images are ever touched — never AWS-managed or other tools' VMs. Prompts for
confirmation unless --yes.

  whim gc --older-than 1h     # reap VMs older than an hour
  whim gc --yes               # reap all whim VMs, no prompt`,
	Args: cobra.NoArgs,
	RunE: runGc,
}

func runPs(cmd *cobra.Command, _ []string) error {
	mode, err := imageOutputMode(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	mgr, err := ownerManager(ctx, cmd)
	if err != nil {
		return err
	}
	vms, err := mgr.List(ctx)
	if err != nil {
		return err
	}
	return renderVMList(cmd.OutOrStdout(), vms, mode)
}

func runGc(cmd *cobra.Command, _ []string) error {
	older, _ := cmd.Flags().GetDuration("older-than")
	yes, _ := cmd.Flags().GetBool("yes")
	ctx := cmd.Context()
	mgr, err := ownerManager(ctx, cmd)
	if err != nil {
		return err
	}
	vms, err := mgr.List(ctx)
	if err != nil {
		return err
	}
	targets := filterOlderThan(vms, older)
	if len(targets) == 0 {
		printOut(cmd, "No whim MicroVMs to reap.\n")
		return nil
	}

	printErr(cmd, "About to terminate %d whim MicroVM(s):\n", len(targets))
	for _, v := range targets {
		printErr(cmd, "  %s  %s  %s  %s\n", v.ID, imageName(v.ImageARN), age(v.StartedAt), v.State)
	}
	if !yes && !confirm(cmd, fmt.Sprintf("Terminate %d VM(s)?", len(targets))) {
		printErr(cmd, "Aborted.\n")
		return nil
	}

	reaped, gcErr := mgr.GC(ctx, microvm.GCFilter{OlderThan: older})
	for _, id := range reaped {
		printOut(cmd, "terminated %s\n", id)
	}
	if gcErr != nil {
		return fmt.Errorf("gc: %w", gcErr)
	}
	printOut(cmd, "Reaped %d whim MicroVM(s).\n", len(reaped))
	return nil
}

// ownerManager builds a Manager with the resolved account ID, required so List/GC
// can determine VM ownership.
func ownerManager(ctx context.Context, cmd *cobra.Command) (*microvm.Manager, error) {
	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve account ID: %w", err)
	}
	return microvm.NewFromConfig(cfg, microvm.WithAccountID(aws.ToString(id.Account))), nil
}

// filterOlderThan returns the VMs at least d old (d==0 → all); unknown-age VMs
// are excluded when d is set, matching Manager.GC.
func filterOlderThan(vms []microvm.SandboxInfo, d time.Duration) []microvm.SandboxInfo {
	if d <= 0 {
		return vms
	}
	now := time.Now()
	var out []microvm.SandboxInfo
	for _, v := range vms {
		if !v.StartedAt.IsZero() && now.Sub(v.StartedAt) >= d {
			out = append(out, v)
		}
	}
	return out
}

// vmJSON is the machine-readable shape for `whim ls --json`.
type vmJSON struct {
	ID        string `json:"id"`
	Image     string `json:"image"`
	ImageARN  string `json:"imageArn"`
	State     string `json:"state"`
	Age       string `json:"age,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
}

// renderVMList writes the VM list in the requested mode. Pure (no AWS/globals).
// It renders into a buffer first, then writes redacted (WHIM_REDACT_ACCOUNT) —
// redacting after the tabwriter lays out columns keeps the table aligned.
func renderVMList(w io.Writer, vms []microvm.SandboxInfo, mode outputMode) error {
	var buf bytes.Buffer
	switch {
	case mode.json:
		arr := make([]vmJSON, 0, len(vms))
		for _, v := range vms {
			j := vmJSON{ID: v.ID, Image: imageName(v.ImageARN), ImageARN: v.ImageARN, State: v.State}
			if !v.StartedAt.IsZero() {
				j.Age = age(v.StartedAt)
				j.StartedAt = v.StartedAt.UTC().Format(time.RFC3339)
			}
			arr = append(arr, j)
		}
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(arr); err != nil {
			return err
		}

	case mode.quiet:
		for _, v := range vms {
			fmt.Fprintln(&buf, v.ID)
		}

	default:
		if len(vms) == 0 {
			fmt.Fprintln(&buf, "No whim MicroVMs running.")
		} else {
			tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ID\tIMAGE\tAGE\tSTATE")
			for _, v := range vms {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.ID, imageName(v.ImageARN), age(v.StartedAt), v.State)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, redactAccountID(buf.String()))
	return err
}

// imageName extracts the image name from a microvm-image ARN.
func imageName(arn string) string {
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// age renders a human-ish age since t ("-" if unknown).
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}

// confirm asks a y/N question on stderr and reads the answer from cmd's input.
func confirm(cmd *cobra.Command, prompt string) bool {
	printErr(cmd, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
