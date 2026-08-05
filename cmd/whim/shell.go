package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/udgover/whim/microvm"
)

// shellLaunchPollInterval is the VM-state poll cadence while waiting for RUNNING.
// Deliberately well below the Manager's 5s default so the shell pops in ~2s
// (provisioning is fast; the 5s default just adds dead wait).
const shellLaunchPollInterval = 500 * time.Millisecond

// defaultShellTTL is the server-side cleanup backstop for an interactive shell.
const defaultShellTTL = 25 * time.Minute

// shellLaunchTimeout bounds provisioning so a stuck VM fails fast instead of
// polling all the way to its TTL (~25m).
const shellLaunchTimeout = 3 * time.Minute

// terminateGrace bounds the best-effort terminate on the way out.
const terminateGrace = 30 * time.Second

// terminateQuietly best-effort terminates sb on a detached context (so it runs
// even when the caller's ctx was canceled), warning on failure — the server-side
// TTL is the ultimate backstop. Shared by `whim shell` and `whim run`.
func terminateQuietly(cmd *cobra.Command, sb *microvm.Sandbox) {
	ctx, cancel := context.WithTimeout(context.Background(), terminateGrace)
	defer cancel()
	if err := sb.Terminate(ctx); err != nil {
		printErr(cmd, "warning: terminate %s: %v\n", sb.ID(), err)
	}
}

func init() {
	addShellFlags(shellCmd)
	rootCmd.AddCommand(shellCmd)

	// Bare `whim` (no subcommand) drops straight into a shell.
	addShellFlags(rootCmd)
	rootCmd.RunE = runShell
}

func addShellFlags(cmd *cobra.Command) {
	cmd.Flags().String("image", "",
		"image name or ARN to launch (default: the cached image from `whim init`)")
	cmd.Flags().Duration("ttl", defaultShellTTL,
		"server-side max lifetime for the VM — cleanup backstop, must be ≤ 8h")
	cmd.Flags().Bool("privileged", false,
		"launch the whim-privileged image (all OS capabilities: mount, netns, eBPF, nested containers); auto-built on first use")
}

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Launch an ephemeral MicroVM and attach an interactive root shell",
	Long: `Launch a throwaway MicroVM from the default image and attach an
interactive root shell over a WebSocket. The VM is terminated when you
disconnect.

Press Ctrl-] to disconnect (every other key, including Ctrl-C, goes to the
remote shell). A server-side TTL also terminates the VM as a backstop.`,
	Args: cobra.NoArgs,
	RunE: runShell,
}

func runShell(cmd *cobra.Command, _ []string) error {
	// SIGINT before raw mode (e.g. during launch) cancels ctx so the terminate
	// defer runs instead of leaking a VM. Once the shell is in raw mode, Ctrl-C
	// is delivered to the remote pty as a byte, not a signal to this process.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	imageARN, err := resolveShellImageARN(ctx, cmd, cfg)
	if err != nil {
		return err
	}
	ttl, _ := cmd.Flags().GetDuration("ttl")
	// No shell-token-lifetime warning here: live testing confirmed an established
	// shell connection is never re-validated against its token, so --ttl has no
	// interaction with ShellTokenLifetime to warn about. --ttl alone bounds the
	// session (the VM is terminated on disconnect, or at --ttl regardless).

	mgr := microvm.NewFromConfig(cfg, microvm.WithPollInterval(shellLaunchPollInterval))

	// Bound provisioning so a stuck VM fails fast instead of polling to the TTL.
	launchCtx, cancelLaunch := context.WithTimeout(ctx, shellLaunchTimeout)
	defer cancelLaunch()
	launchOpts := []microvm.LaunchOption{microvm.WithTTL(ttl)}
	if expected, ok, eerr := cachedNoPublicEgressExpectation(cmd); eerr != nil {
		return eerr
	} else if ok {
		launchOpts = append(launchOpts, microvm.WithExpectedNoPublicEgress(*expected))
	}
	// Status goes to stderr so stdout carries only the raw pty stream.
	printErr(cmd, "Launching MicroVM…\n")
	start := time.Now()
	sb, err := mgr.Launch(launchCtx, imageARN, launchOpts...)
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	// Always terminate on the way out (detached ctx so it runs even on cancel).
	defer terminateQuietly(cmd, sb)
	printErr(cmd, "Connected to %s in %s. Press Ctrl-] to disconnect.\n",
		sb.ID(), time.Since(start).Round(time.Millisecond))

	return runInteractiveShell(ctx, cmd, sb)
}

// resolveShellImageARN picks the image to launch: an explicit --image (ARN used
// verbatim, bare name resolved against the account) takes precedence; otherwise
// the cached default image from `whim init`.
func resolveShellImageARN(ctx context.Context, cmd *cobra.Command, cfg aws.Config) (string, error) {
	image, _ := cmd.Flags().GetString("image")
	// --privileged always launches the well-known whim-privileged image, so an
	// explicit --image alongside it is ambiguous; reject before any AWS call.
	if privileged, _ := cmd.Flags().GetBool("privileged"); privileged {
		if image != "" {
			return "", errors.New("--privileged and --image are mutually exclusive: --privileged always launches the whim-privileged image")
		}
		return ensurePrivilegedImage(ctx, cfg, cmd)
	}
	if image == "" {
		wcfg, err := LoadConfig()
		if err != nil {
			return "", fmt.Errorf("load whim config: %w", err)
		}
		if wcfg.ImageARN == "" {
			return "", errors.New("no default image configured — run 'whim init' first (or pass --image)")
		}
		return wcfg.ImageARN, nil
	}
	if strings.HasPrefix(image, "arn:") {
		return image, nil
	}
	wcfg, err := LoadConfig()
	if err != nil {
		return "", fmt.Errorf("load whim config: %w", err)
	}
	if arn, ok := wcfg.Image(image); ok && arn != "" {
		return arn, nil
	}
	// A bare name needs the account ID to build the full ARN.
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("resolve account ID: %w", err)
	}
	// Format directly (matches Manager.ImageARN) rather than spin up a throwaway
	// Manager just to build a string.
	return fmt.Sprintf("arn:aws:lambda:%s:%s:microvm-image:%s", cfg.Region, aws.ToString(id.Account), image), nil
}

func cachedNoPublicEgressExpectation(cmd *cobra.Command) (*microvm.NoPublicEgressResources, bool, error) {
	image, _ := cmd.Flags().GetString("image")
	if image == "" {
		return nil, false, nil
	}
	wcfg, err := LoadConfig()
	if err != nil {
		return nil, false, fmt.Errorf("load whim config: %w", err)
	}
	if strings.HasPrefix(image, "arn:") {
		var found bool
		for name, arn := range wcfg.Images {
			if arn == image {
				image = name
				found = true
				break
			}
		}
		if !found {
			return nil, false, nil
		}
	}
	egress, ok := wcfg.Egress(image)
	if !ok || egress == "" {
		return nil, false, nil
	}
	mode, err := parseEgress(egress)
	if err != nil {
		return nil, false, fmt.Errorf("cached egress for image %q: %w", image, err)
	}
	if mode != microvm.EgressNone {
		return nil, false, nil
	}
	connector, _ := wcfg.EgressConnector(image)
	if connector == "" {
		return nil, false, fmt.Errorf("cached image %q was built with --egress none before Whim recorded connector ARNs; rebuild it with --force", image)
	}
	expected := &microvm.NoPublicEgressResources{ConnectorARN: connector}
	if rg, found := wcfg.EgressResourceGroup(image); found {
		if rg.ConnectorARN != "" && rg.ConnectorARN != connector {
			return nil, false, fmt.Errorf("cached image %q has connector %q but recorded topology belongs to %q", image, connector, rg.ConnectorARN)
		}
		expected.VPCID = rg.VPCID
		expected.SubnetIDs = append([]string(nil), rg.SubnetIDs...)
		expected.RouteTableID = rg.RouteTableID
		expected.RouteTableIDs = append([]string(nil), rg.RouteTableIDs...)
		expected.SecurityGroupID = rg.SecurityGroupID
		expected.SecurityGroupIDs = append([]string(nil), rg.SecurityGroupIDs...)
		expected.NetworkACLID = rg.NetworkACLID
		expected.ResourceGroup = rg.ResourceGroup
	}
	return expected, true, nil
}

// runInteractiveShell puts the terminal in raw mode, wires SIGWINCH → resize,
// and pumps os.Stdin/Stdout through Sandbox.Shell until the user presses Ctrl-]
// (clean disconnect) or the session ends. It is terminal-facing glue, validated
// live rather than in unit tests.
func runInteractiveShell(ctx context.Context, cmd *cobra.Command, sb *microvm.Sandbox) error {
	fd := int(os.Stdin.Fd())

	// Raw mode so every keystroke (incl. Ctrl-C → 0x03) reaches the remote pty.
	var restore func()
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("set terminal raw mode: %w", err)
		}
		restore = func() { _ = term.Restore(fd, oldState) }
		defer restore()
	}

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Ctrl-] is the reserved local disconnect: cancel the session so Shell
	// returns (the remote ws never closes on its own).
	var escaped atomic.Bool
	in := newEscapeReader(os.Stdin, func() { escaped.Store(true); cancel() })

	// SIGWINCH → resize channel. Wired now and fed an initial size; Sandbox.Shell
	// currently drains-and-ignores it (resize wire-format unvalidated, v0.1).
	resize := make(chan microvm.WinSize, 1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	sendSize := func() {
		cols, rows, err := term.GetSize(fd)
		if err != nil {
			return
		}
		select {
		case resize <- microvm.WinSize{Rows: clampUint16(rows), Cols: clampUint16(cols)}:
		default: // a pending size is already queued; the latest will be read next
		}
	}
	sendSize()
	go func() {
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-sigCh:
				sendSize()
			}
		}
	}()

	err := sb.Shell(sessCtx, microvm.ShellIO{In: in, Out: cmd.OutOrStdout(), Resize: resize})

	// A disconnect we triggered (Ctrl-] → cancel) is a clean exit, not an error.
	if escaped.Load() || errors.Is(err, context.Canceled) {
		if restore != nil {
			restore()
		}
		printErr(cmd, "\nDisconnected.\n")
		return nil
	}
	return err
}

// shellEscapeByte is the reserved local disconnect key for `whim shell`: Ctrl-]
// (telnet-style). The remote shell's WebSocket does not close when the remote
// shell exits, so the client needs its own way to end the
// session. Every other key — including Ctrl-C (0x03) — is forwarded to the
// remote pty unchanged, so remote processes remain interruptible.
const shellEscapeByte = 0x1d // Ctrl-]

// escapeReader wraps a local input stream (os.Stdin in raw mode) and watches for
// the escape byte. Bytes before the first escape are forwarded; the escape byte
// and everything after it are dropped and the stream ends (io.EOF), so the
// stdin→pty copy unwinds. End-of-input on the underlying reader is treated the
// same way — a piped/redirected stdin that EOFs ends the session, since the
// remote ws never closes on its own. onEscape fires exactly once — the caller
// uses it to cancel the session and trigger teardown.
//
// It is read by a single goroutine (Shell's stdin pump), so done needs no lock.
type escapeReader struct {
	r        io.Reader
	onEscape func()
	done     bool
}

func newEscapeReader(r io.Reader, onEscape func()) *escapeReader {
	return &escapeReader{r: r, onEscape: onEscape}
}

// trip ends the stream and fires onEscape once.
func (e *escapeReader) trip() {
	if !e.done {
		e.done = true
		if e.onEscape != nil {
			e.onEscape()
		}
	}
}

// clampUint16 bounds a terminal dimension into the winsize struct's unsigned
// short range (the kernel TIOCSWINSZ fields are uint16), avoiding overflow.
func clampUint16(n int) uint16 {
	switch {
	case n < 0:
		return 0
	case n > 0xFFFF:
		return 0xFFFF
	default:
		return uint16(n)
	}
}

func (e *escapeReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, io.EOF
	}
	n, err := e.r.Read(p)
	if n > 0 {
		if i := bytes.IndexByte(p[:n], shellEscapeByte); i >= 0 {
			e.trip()
			if i == 0 {
				return 0, io.EOF
			}
			return i, nil // forward the pre-escape bytes; next Read returns EOF
		}
	}
	if errors.Is(err, io.EOF) {
		e.trip() // EOF on stdin is a disconnect
	}
	return n, err
}
