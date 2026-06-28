package main

import (
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

func init() {
	rootCmd.AddCommand(suspendCmd)
	rootCmd.AddCommand(resumeCmd)
}

var suspendCmd = &cobra.Command{
	Use:   "suspend <microvm-id>",
	Short: "Suspend a running whim MicroVM (warm reuse; disk + memory preserved)",
	Long: `suspend pauses a running MicroVM, preserving its disk and in-memory state
for a later 'whim resume'. The server-side TTL still applies.

  whim suspend microvm-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runSuspend,
}

var resumeCmd = &cobra.Command{
	Use:   "resume <microvm-id>",
	Short: "Resume a suspended whim MicroVM",
	Long: `resume restarts a previously suspended MicroVM.

  whim resume microvm-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runResume,
}

func runSuspend(cmd *cobra.Command, args []string) error {
	return suspendResume(cmd, args[0], "suspend")
}

func runResume(cmd *cobra.Command, args []string) error {
	return suspendResume(cmd, args[0], "resume")
}

// suspendResume runs a by-id suspend or resume (no Attach: a suspended VM is not
// RUNNING). whim-level failures exit 125.
func suspendResume(cmd *cobra.Command, id, action string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return whimFail(cmd, "load AWS config", err)
	}
	mgr := microvm.NewFromConfig(cfg)

	if action == "suspend" {
		err = mgr.Suspend(ctx, id)
	} else {
		err = mgr.Resume(ctx, id)
	}
	if err != nil {
		return whimFail(cmd, action, err)
	}
	printOut(cmd, "%s\n", suspendDoneMsg(action, id))
	return nil
}

// suspendDoneMsg renders the success line with correct past tense (avoids the
// naive action+"d" which would print "suspendd").
func suspendDoneMsg(action, id string) string {
	past := map[string]string{"suspend": "suspended", "resume": "resumed"}[action]
	return past + " " + id
}
