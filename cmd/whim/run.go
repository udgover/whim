package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

// newManager builds the microvm.Manager used by run/exec. It is a
// package-level var — not a plain function call — solely so tests can swap in
// microvm.NewWithAPI(mock, ...) instead of hitting real AWS; production always
// uses this default.
var newManager = func(cfg aws.Config, opts ...microvm.Option) *microvm.Manager {
	return microvm.NewFromConfig(cfg, opts...)
}

// whimExitCode is the process exit code for whim-level failures (config, launch,
// exec transport), following the timeout(1)/env(1) convention. Remote commands
// usually exit 0–124, so 125 distinguishes "whim broke" from "the command
// failed" in the common case; if the remote command itself exits 125 the two
// are indistinguishable (an accepted limitation of the convention).
const whimExitCode = 125

// Defaults for `whim run -d`'s idle policy and TTL. The idle-policy flag
// defaults only take effect once the caller sets --idle or --suspend-after
// (see resolveIdlePolicy) — a bare `-d` launches a box with no idle policy at
// all. defaultDetachedTTL is high (the platform's 8h cap) rather than the
// short defaultShellTTL backstop, since a persistent box is meant to outlive
// one sitting.
const (
	defaultDetachedIdle         = 15 * time.Minute
	defaultDetachedSuspendAfter = 2 * time.Hour
	defaultDetachedTTL          = 8 * time.Hour
	// maxIdlePolicyDuration bounds --idle/--suspend-after defensively at the
	// platform's ~8h run-lifetime scale, so an absurd flag value fails fast
	// with a clear error instead of wrapping when narrowed to int32 seconds.
	maxIdlePolicyDuration = 8 * time.Hour
)

func init() {
	addShellFlags(runCmd) // --image, --ttl
	addDetachFlags(runCmd)
	addExecFlags(execCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(execCmd)
}

// addExecFlags registers -i/-t on execCmd. Split out from init() so tests can
// attach them to a throwaway *cobra.Command, matching addDetachFlags.
func addExecFlags(cmd *cobra.Command) {
	cmd.Flags().BoolP("interactive", "i", false,
		"keep stdin open (used with -t for an interactive shell)")
	cmd.Flags().BoolP("tty", "t", false,
		"allocate a pty and attach an interactive shell, like docker exec -it")
}

// isInteractiveExec reports whether -i or -t was set. whim's shell transport
// doesn't distinguish "stdin open" from "full pty" (Shell is always both), so
// either flag alone switches exec into interactive-attach mode.
func isInteractiveExec(cmd *cobra.Command) bool {
	i, _ := cmd.Flags().GetBool("interactive")
	t, _ := cmd.Flags().GetBool("tty")
	return i || t
}

// addDetachFlags registers the -d/--detach persistent-box flags. Split out
// from init() so tests can attach them to a throwaway *cobra.Command instead
// of mutating the package-level runCmd, whose flag Changed() state would
// otherwise leak between tests.
func addDetachFlags(cmd *cobra.Command) {
	cmd.Flags().BoolP("detach", "d", false,
		"launch a persistent MicroVM and print its id, instead of running a command")
	cmd.Flags().Duration("idle", defaultDetachedIdle,
		"max time with no shell traffic before auto-suspend (requires -d)")
	cmd.Flags().Duration("suspend-after", defaultDetachedSuspendAfter,
		"max time suspended before auto-terminate (requires -d; see --idle)")
	cmd.Flags().Bool("auto-resume", true,
		"resume automatically when traffic arrives at a suspended box")
	cmd.Flags().Bool("no-auto-resume", false,
		"disable auto-resume (overrides --auto-resume; pflag has no --no- negation, so this is registered explicitly)")
}

var runCmd = &cobra.Command{
	Use:   "run [flags] -- <cmd> [args...]",
	Short: "Launch a MicroVM: run a command and terminate, or -d for a persistent box",
	Long: `run launches a throwaway MicroVM, executes the command, streams its combined
output, propagates the command's exit code, and terminates the VM.

  whim run -- echo hello
  whim run -- sh -c 'exit 7'     # whim exits 7

With -d/--detach, run launches a persistent MicroVM instead: it prints the
MicroVM id and returns without running a command or terminating the VM. Manage
it with 'whim ps', 'whim suspend'/'whim resume', or 'whim gc'.

  whim run -d --idle 15m --suspend-after 2h

Use -- to separate whim flags from the remote command. whim-level failures exit
with code 125, distinct from the command's own exit code.`,
	Args: runArgs,
	RunE: runRun,
}

// runArgs requires a command (-- <cmd>) unless -d/--detach is set, in which
// case run launches a persistent box and returns without running anything.
// Passing both -d and a command is rejected rather than silently ignored.
func runArgs(cmd *cobra.Command, args []string) error {
	detach, _ := cmd.Flags().GetBool("detach")
	if !detach {
		return cobra.MinimumNArgs(1)(cmd, args)
	}
	if len(args) > 0 {
		return fmt.Errorf("whim run -d launches a persistent box and does not take a command (got %q); drop -d to run it, or drop the command to launch detached", strings.Join(args, " "))
	}
	return nil
}

// resolveIdlePolicy builds an IdlePolicy from --idle/--suspend-after/
// --auto-resume/--no-auto-resume. It returns nil unless the caller explicitly
// set --idle or --suspend-after — a bare `-d` with neither launches a box that
// never auto-suspends. Idle-policy flags only make sense on a persistent (-d)
// box (an ephemeral run/shell terminates before any idle timer could fire),
// so setting them without -d is rejected rather than silently ignored.
func resolveIdlePolicy(cmd *cobra.Command) (*microvm.IdlePolicy, error) {
	if !cmd.Flags().Changed("idle") && !cmd.Flags().Changed("suspend-after") {
		return nil, nil
	}
	if detach, _ := cmd.Flags().GetBool("detach"); !detach {
		return nil, fmt.Errorf("--idle/--suspend-after require -d/--detach: idle policy only applies to a persistent box")
	}
	idle, _ := cmd.Flags().GetDuration("idle")
	suspendAfter, _ := cmd.Flags().GetDuration("suspend-after")
	autoResume, _ := cmd.Flags().GetBool("auto-resume")
	if noAutoResume, _ := cmd.Flags().GetBool("no-auto-resume"); noAutoResume {
		autoResume = false
	}

	idleSeconds, err := durationToSeconds("--idle", idle)
	if err != nil {
		return nil, err
	}
	suspendSeconds, err := durationToSeconds("--suspend-after", suspendAfter)
	if err != nil {
		return nil, err
	}
	return &microvm.IdlePolicy{
		AutoResumeEnabled:        autoResume,
		MaxIdleDurationSeconds:   idleSeconds,
		SuspendedDurationSeconds: suspendSeconds,
	}, nil
}

// durationToSeconds converts d to whole seconds as int32, bounded to
// (0, maxIdlePolicyDuration]. flag names the originating flag for the error
// message. Rejecting out-of-range values here (rather than truncating) avoids
// a silent int64→int32 wraparound reaching AWS as a nonsensical value.
func durationToSeconds(flag string, d time.Duration) (int32, error) {
	if d <= 0 {
		return 0, fmt.Errorf("%s must be > 0, got %s", flag, d)
	}
	if d > maxIdlePolicyDuration {
		return 0, fmt.Errorf("%s must be ≤ %s, got %s", flag, maxIdlePolicyDuration, d)
	}
	return int32(d / time.Second), nil
}

// resolveRunTTL returns the launch TTL: an explicit --ttl always wins. A
// detached (-d) launch with no explicit --ttl defaults to defaultDetachedTTL
// (high) rather than the short defaultShellTTL backstop used by ephemeral
// run/shell, since a persistent box is meant to outlive one sitting.
func resolveRunTTL(cmd *cobra.Command) time.Duration {
	ttl, _ := cmd.Flags().GetDuration("ttl")
	detach, _ := cmd.Flags().GetBool("detach")
	if detach && !cmd.Flags().Changed("ttl") {
		return defaultDetachedTTL
	}
	return ttl
}

var execCmd = &cobra.Command{
	Use:   "exec <microvm-id> -- <cmd> [args...]",
	Short: "Run a command in an existing whim MicroVM, or -it for an interactive shell",
	Long: `exec runs a command in an already-running MicroVM — a SUSPENDED one is
resumed automatically — and propagates its exit code. Unlike run, it neither
launches nor terminates the VM.

  whim exec microvm-abc123 -- ps aux

With -it (or -i/-t), exec attaches an interactive shell instead — the closest
whim equivalent of 'docker exec -it <container> sh'. Ctrl-] detaches; the box
keeps running. Unlike Docker, -it never takes a trailing command: the shell
endpoint always starts a fresh shell (there is no wire mechanism to select a
program), so run your command once you're attached instead. This is a
deliberate, permanent limitation of the shell transport, not a TODO.

  whim exec -it microvm-abc123`,
	Args: execArgs,
	RunE: runExec,
}

// execArgs requires exactly the MicroVM id under -it — the shell endpoint
// always starts a fresh shell, so there's no way to point it at a specific
// command. Without -it it's id + -- <cmd>, unchanged.
func execArgs(cmd *cobra.Command, args []string) error {
	if isInteractiveExec(cmd) {
		if len(args) != 1 {
			return fmt.Errorf("whim exec -it takes exactly one argument (the MicroVM id) and no command; the shell endpoint always starts a fresh shell, so run your command once you're in it")
		}
		return nil
	}
	return cobra.MinimumNArgs(2)(cmd, args)
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
	mgr := newManager(cfg, microvm.WithPollInterval(shellLaunchPollInterval))

	policy, err := resolveIdlePolicy(cmd)
	if err != nil {
		return whimFail(cmd, "resolve idle policy", err)
	}
	launchOpts := []microvm.LaunchOption{microvm.WithTTL(resolveRunTTL(cmd))}
	if policy != nil {
		launchOpts = append(launchOpts, microvm.WithIdlePolicy(*policy))
	}
	if expected, ok, eerr := cachedNoPublicEgressExpectation(cmd); eerr != nil {
		return whimFail(cmd, "resolve image egress", eerr)
	} else if ok {
		launchOpts = append(launchOpts, microvm.WithExpectedNoPublicEgress(*expected))
	}

	launchCtx, cancelLaunch := context.WithTimeout(ctx, shellLaunchTimeout)
	defer cancelLaunch()
	printErr(cmd, "Launching MicroVM…\n")
	sb, err := mgr.Launch(launchCtx, imageARN, launchOpts...)
	if err != nil {
		return whimFail(cmd, "launch", err)
	}

	if detach, _ := cmd.Flags().GetBool("detach"); detach {
		// The entire point of -d: print the id and return WITHOUT terminating.
		// The VM outlives this process; the caller manages it via
		// ps/suspend/resume/gc. No timestamp prefix — the id is meant to be
		// scriptable (e.g. `id=$(whim run -d)`).
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), sb.ID())
		return nil
	}
	defer terminateQuietly(cmd, sb)

	return execAndPropagate(ctx, cmd, sb, args)
}

// runInteractiveShellFn wraps runInteractiveShell (shell.go) behind a
// package-level var so exec -it's Attach/never-terminate routing is testable
// without driving the terminal-facing shell loop itself — that loop is
// validated live, not in unit tests (see runInteractiveShell's doc comment).
var runInteractiveShellFn = runInteractiveShell

func runExec(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return whimFail(cmd, "load AWS config", err)
	}
	sb, err := newManager(cfg).Attach(ctx, id)
	if err != nil {
		return whimFail(cmd, "attach", err)
	}
	// exec attaches to a VM it does not own — never terminate it, interactive
	// or not.
	if isInteractiveExec(cmd) {
		printErr(cmd, "Attached to %s. Press Ctrl-] to disconnect.\n", sb.ID())
		return runInteractiveShellFn(ctx, cmd, sb)
	}
	return execAndPropagate(ctx, cmd, sb, args[1:])
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
