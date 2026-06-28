package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// whimExitCode is the process exit code for whim-level failures (config, launch,
// exec transport), following the timeout(1)/env(1) convention. Remote commands
// usually exit 0–124, so 125 distinguishes "whim broke" from "the command
// failed" in the common case; if the remote command itself exits 125 the two
// are indistinguishable (an accepted limitation of the convention).
const whimExitCode = 125

func init() {
	addShellFlags(runCmd) // --image, --ttl
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(execCmd)
}

var runCmd = &cobra.Command{
	Use:   "run [flags] -- <cmd> [args...]",
	Short: "Launch an ephemeral MicroVM, run a command, stream output, and terminate",
	Long: `run launches a throwaway MicroVM, executes the command, streams its combined
output, propagates the command's exit code, and terminates the VM.

  whim run -- echo hello
  whim run -- sh -c 'exit 7'     # whim exits 7

Use -- to separate whim flags from the remote command. whim-level failures exit
with code 125, distinct from the command's own exit code.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRun,
}

var execCmd = &cobra.Command{
	Use:   "exec <microvm-id> -- <cmd> [args...]",
	Short: "Run a command in an existing whim MicroVM (does not terminate it)",
	Long: `exec runs a command in an already-running MicroVM and propagates its exit
code. Unlike run, it neither launches nor terminates the VM.

  whim exec microvm-abc123 -- ps aux`,
	Args: cobra.MinimumNArgs(2),
	RunE: runExec,
}

func runRun(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return whimFail(cmd, "load AWS config", err)
	}
	imageARN, err := resolveShellImageARN(ctx, cmd, cfg)
	if err != nil {
		return whimFail(cmd, "resolve image", err)
	}
	ttl, _ := cmd.Flags().GetDuration("ttl")
	mgr := microvm.NewFromConfig(cfg, microvm.WithPollInterval(shellLaunchPollInterval))

	launchCtx, cancelLaunch := context.WithTimeout(ctx, shellLaunchTimeout)
	defer cancelLaunch()
	printErr(cmd, "Launching MicroVM…\n")
	sb, err := mgr.Launch(launchCtx, imageARN, microvm.WithTTL(ttl))
	if err != nil {
		return whimFail(cmd, "launch", err)
	}
	defer terminateQuietly(cmd, sb)

	return execAndPropagate(ctx, cmd, sb, args)
}

func runExec(cmd *cobra.Command, args []string) error {
	id, argv := args[0], args[1:]
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return whimFail(cmd, "load AWS config", err)
	}
	sb, err := microvm.NewFromConfig(cfg).Attach(ctx, id)
	if err != nil {
		return whimFail(cmd, "attach", err)
	}
	// exec attaches to a VM it does not own — never terminate it.
	return execAndPropagate(ctx, cmd, sb, argv)
}

// execAndPropagate runs argv on sb, streams the combined output to stdout, and
// returns an exitCodeError carrying the remote exit code (nil if 0). A
// whim-level exec failure becomes whimExitCode.
func execAndPropagate(ctx context.Context, cmd *cobra.Command, sb *microvm.Sandbox, argv []string) error {
	res, err := sb.Exec(ctx, argv)
	if err != nil {
		return whimFail(cmd, "exec", err)
	}
	_, _ = cmd.OutOrStdout().Write(res.Output)
	if res.ExitCode != 0 {
		return exitCodeError{res.ExitCode}
	}
	return nil
}

// whimFail prints a whim-level error to stderr and returns the reserved whim
// exit code (125), distinguishing whim failures from remote command exit codes.
func whimFail(cmd *cobra.Command, msg string, err error) error {
	printErr(cmd, "error: %s: %v\n", msg, err)
	return exitCodeError{whimExitCode}
}
