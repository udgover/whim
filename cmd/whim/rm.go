package main

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(rmCmd)
}

var rmCmd = &cobra.Command{
	Use:   "rm <microvm-id> [microvm-id...]",
	Short: "Terminate MicroVMs by id (no ownership check; idempotent on already-gone)",
	Long: `rm terminates one or more MicroVMs by id. Like suspend/resume/exec — and
unlike gc — it acts on any id you name, without checking whim-ownership first.
Terminating an already-gone VM is a no-op, not an error, and a failure on one
id does not stop the rest from being attempted.

  whim rm microvm-abc123
  whim rm microvm-abc123 microvm-def456

For bulk cleanup of only your own whim-owned VMs, use 'whim gc' instead.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRm,
}

// runRm terminates every id, continuing past a failure on any one of them
// (Docker-style), and reports the reserved whim exit code if any failed.
// Successful terminations print the bare id (no timestamp), matching `run -d`
// so `whim rm $(whim ps -q)` composes cleanly.
func runRm(cmd *cobra.Command, ids []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return whimFail(cmd, "load AWS config", err)
	}
	mgr := newManager(cfg)

	failed := false
	for _, id := range ids {
		if err := mgr.Terminate(ctx, id); err != nil {
			printErr(cmd, "error: rm %s: %v\n", id, err)
			failed = true
			continue
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), id)
	}
	if failed {
		return exitCodeError{whimExitCode}
	}
	return nil
}
