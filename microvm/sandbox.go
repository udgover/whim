package microvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/internal/wsconn"
)

// ShellTokenLifetime is how long a minted shell auth token stays valid. An
// interactive session cannot outlive it (there is no reconnect in v0.1), so
// callers should keep a VM's TTL at or below this.
const ShellTokenLifetime = 30 * time.Minute

// shellTokenExpiryMinutes is ShellTokenLifetime expressed for the mint API.
const shellTokenExpiryMinutes = int32(ShellTokenLifetime / time.Minute)

// WinSize is a terminal window size for a resize event.
type WinSize struct {
	Rows uint16
	Cols uint16
}

// ShellIO wires local streams (or a terminal) to a Sandbox shell. In is
// forwarded to the remote pty; remote pty output is written to Out; Resize
// (optional) delivers terminal resize events. It is intentionally
// terminal-agnostic — the CLI owns raw-mode/SIGWINCH and passes os.Stdin/Stdout.
type ShellIO struct {
	In     io.Reader
	Out    io.Writer
	Resize <-chan WinSize
}

// Sandbox is a handle to a running MicroVM.
type Sandbox struct {
	mgr      *Manager
	id       string
	endpoint string
}

// ID returns the MicroVM identifier.
func (s *Sandbox) ID() string { return s.id }

// Endpoint returns the MicroVM's connection endpoint (a bare host; callers
// prepend wss:// for the shell or https:// for HTTP ingress).
func (s *Sandbox) Endpoint() string { return s.endpoint }

// Terminate terminates the MicroVM. It is idempotent: an already-gone VM is
// treated as success, so it is safe to defer.
func (s *Sandbox) Terminate(ctx context.Context) error {
	err := s.mgr.api.TerminateMicrovm(ctx, &awsapi.TerminateMicrovmInput{MicrovmIdentifier: s.id})
	if err != nil && !errors.Is(err, awsapi.ErrNotFound) {
		return fmt.Errorf("terminate microvm %q: %w", s.id, err)
	}
	return nil
}

// Suspend pauses the running MicroVM (warm reuse): execution stops but disk and
// in-memory state are preserved for a later Resume. A gone VM reads as
// ErrTerminated.
func (s *Sandbox) Suspend(ctx context.Context) error { return s.mgr.Suspend(ctx, s.id) }

// Resume restarts a suspended MicroVM. A gone VM reads as ErrTerminated.
func (s *Sandbox) Resume(ctx context.Context) error { return s.mgr.Resume(ctx, s.id) }

// Suspend pauses a MicroVM by ID (warm reuse), preserving disk + in-memory state
// for a later Resume. Used by `whim suspend`; a gone VM reads as ErrTerminated.
func (m *Manager) Suspend(ctx context.Context, id string) error {
	err := m.api.SuspendMicrovm(ctx, &awsapi.SuspendMicrovmInput{MicrovmIdentifier: id})
	if errors.Is(err, awsapi.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrTerminated, id)
	}
	if err != nil {
		return fmt.Errorf("suspend microvm %q: %w", id, err)
	}
	return nil
}

// Resume restarts a suspended MicroVM by ID. Used by `whim resume`; a gone VM
// reads as ErrTerminated. (A suspended VM is not RUNNING, so this is by ID rather
// than via Attach.)
func (m *Manager) Resume(ctx context.Context, id string) error {
	err := m.api.ResumeMicrovm(ctx, &awsapi.ResumeMicrovmInput{MicrovmIdentifier: id})
	if errors.Is(err, awsapi.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrTerminated, id)
	}
	if err != nil {
		return fmt.Errorf("resume microvm %q: %w", id, err)
	}
	return nil
}

// shellIngressConnectorARN returns the managed SHELL_INGRESS connector ARN for
// the given region — required for CreateMicrovmShellAuthToken + the wss:// attach.
func shellIngressConnectorARN(region string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:aws:network-connector:aws-network-connector:SHELL_INGRESS", region)
}

// dialShell mints a fresh shell auth token and opens the shell WebSocket
// (session_init skipped). Shared by Shell and Exec. The token is never logged.
func (s *Sandbox) dialShell(ctx context.Context) (*wsconn.Conn, error) {
	tok, err := s.mgr.api.CreateShellAuthToken(ctx, &awsapi.CreateShellAuthTokenInput{
		MicrovmIdentifier: s.id,
		ExpirationMinutes: shellTokenExpiryMinutes,
	})
	if err != nil {
		return nil, fmt.Errorf("create shell token for %q: %w", s.id, err)
	}
	conn, err := wsconn.Dial(ctx, s.endpoint, tok.HeaderKey, tok.HeaderValue)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Shell attaches an interactive shell to the running MicroVM: it mints a shell
// auth token, opens the WebSocket (skipping the session_init frame), and pumps
// sio.In → remote pty and remote pty → sio.Out. The shell token is never logged.
//
// sio.Out is REQUIRED (ErrInvalidOption otherwise); sio.In is optional.
//
// It returns:
//   - nil if the remote closed the stream cleanly. This is rare in practice: the
//     shell endpoint does NOT close when the remote shell exits, so callers
//     normally end a session by canceling ctx.
//   - context.Canceled or ErrTimeout when ctx is canceled or hits its deadline.
//   - ErrConnClosed when the connection drops abnormally.
//
// Lifecycle caveat: sio.In is copied by a background goroutine. When Shell
// returns, that goroutine may still be blocked in sio.In.Read (e.g. on an
// os.Stdin awaiting a keystroke) until sio.In next yields data or EOFs; it
// consumes no more bytes once the connection is closed, but it is not joined.
// Library callers that need deterministic cleanup should pass an io.Reader they
// can close (or that observes ctx) and must not reuse sio.In across calls while
// a prior goroutine may still be blocked on it. A session cannot outlive the
// shell token (ShellTokenLifetime) — there is no reconnect in v0.1.
func (s *Sandbox) Shell(ctx context.Context, sio ShellIO) error {
	if sio.Out == nil {
		return fmt.Errorf("%w: ShellIO.Out is required", ErrInvalidOption)
	}
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := s.dialShell(sessCtx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Drain resize events so senders never block.
	// TODO: forward to the remote pty — the resize control-frame wire-format
	// is unvalidated, so size changes are dropped for now.
	if sio.Resize != nil {
		go func() {
			for {
				select {
				case <-sessCtx.Done():
					return
				case _, ok := <-sio.Resize:
					if !ok {
						return
					}
				}
			}
		}()
	}

	// Pump local input → remote pty in the background (see lifecycle caveat).
	if sio.In != nil {
		go func() { _, _ = io.Copy(conn, sio.In) }()
	}

	// Pump remote pty → local output; returns on remote close (EOF) or ctx end.
	_, copyErr := io.Copy(sio.Out, conn)
	if copyErr == nil {
		return nil // remote closed the stream cleanly
	}
	// Classify the failure: a canceled or expired session is not a connection
	// fault, so prefer the ctx cause when ctx is done.
	if ctxErr := sessCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("shell session %q: %w", s.id, ErrTimeout)
		}
		return ctxErr // context.Canceled — the caller treats this as a clean disconnect
	}
	return fmt.Errorf("shell session %q: %w: %v", s.id, ErrConnClosed, copyErr)
}

// Attach returns a Sandbox handle for an already-running MicroVM by ID, without
// launching anything — used by `whim exec <id>`. It verifies the VM is RUNNING
// and resolves its endpoint. A gone VM reads as ErrTerminated. The caller does
// NOT own the VM's lifecycle (Attach never terminates it).
//
// NOTE: Attach does not verify whim-ownership — any RUNNING MicroVM ID in the
// account can be attached (and exec'd into as root). VMs are untagged in v0.1;
// ownership enforcement (refusing foreign VMs) is intentionally not done here.
func (m *Manager) Attach(ctx context.Context, id string) (*Sandbox, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: microvm id is required", ErrInvalidOption)
	}
	g, err := m.api.GetMicrovm(ctx, &awsapi.GetMicrovmInput{MicrovmIdentifier: id})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrTerminated, id)
		}
		return nil, fmt.Errorf("get microvm %q: %w", id, err)
	}
	switch g.State {
	case "RUNNING":
		return &Sandbox{mgr: m, id: id, endpoint: g.Endpoint}, nil
	case "TERMINATING", "TERMINATED":
		return nil, fmt.Errorf("%w: %q is %s", ErrTerminated, id, g.State)
	default:
		return nil, fmt.Errorf("%w: microvm %q is %s, not RUNNING", ErrInvalidOption, id, g.State)
	}
}

// Launch runs a new MicroVM from imageARN and waits until it reaches RUNNING.
//
// It always sets a server-side TTL (default 25m, cap 8h) as the cleanup
// backstop, attaches the SHELL_INGRESS connector (or WithIngress override), and
// applies the configured egress mode — all connector ARNs derived from the
// Manager's region. The returned Sandbox must be Terminated by the caller.
func (m *Manager) Launch(ctx context.Context, imageARN string, opts ...LaunchOption) (*Sandbox, error) {
	if imageARN == "" {
		return nil, fmt.Errorf("%w: imageARN is required", ErrInvalidOption)
	}
	cfg, err := ApplyLaunchOptions(opts...)
	if err != nil {
		return nil, err
	}
	// Defensive: never launch without a valid TTL backstop.
	if cfg.TTL <= 0 || cfg.TTL > maxTTL {
		return nil, fmt.Errorf("%w: TTL %s out of range (0, 8h]", ErrInvalidOption, cfg.TTL)
	}

	ingress := cfg.IngressOverride
	if ingress == "" {
		ingress = shellIngressConnectorARN(m.region)
	}
	egress, err := egressConnectors(cfg.Egress, m.region)
	if err != nil {
		return nil, err
	}
	ttlSeconds := int32(cfg.TTL / time.Second)

	in := &awsapi.RunMicrovmInput{
		ImageIdentifier:          imageARN,
		IngressNetworkConnectors: []string{ingress},
		EgressNetworkConnectors:  egress,
		MaximumDurationInSeconds: &ttlSeconds,
	}
	if cfg.IdlePolicy != nil {
		in.IdlePolicy = &awsapi.IdlePolicy{
			AutoResumeEnabled:        cfg.IdlePolicy.AutoResumeEnabled,
			MaxIdleDurationSeconds:   cfg.IdlePolicy.MaxIdleDurationSeconds,
			SuspendedDurationSeconds: cfg.IdlePolicy.SuspendedDurationSeconds,
		}
	}

	out, err := m.api.RunMicrovm(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("run microvm: %w", err)
	}
	sb := &Sandbox{mgr: m, id: out.MicrovmID, endpoint: out.Endpoint}

	if err := m.poll(ctx, func(ctx context.Context) (bool, error) {
		g, gerr := m.api.GetMicrovm(ctx, &awsapi.GetMicrovmInput{MicrovmIdentifier: sb.id})
		if gerr != nil {
			return false, fmt.Errorf("polling microvm %q: %w", sb.id, gerr)
		}
		switch g.State {
		case "RUNNING":
			if g.Endpoint != "" {
				sb.endpoint = g.Endpoint
			}
			return true, nil
		case "TERMINATING", "TERMINATED":
			return false, fmt.Errorf("%w: microvm %q entered %s during provisioning", ErrVMProvisionFailed, sb.id, g.State)
		default: // PENDING and any other transient state
			m.log().Debug("microvm provisioning", "id", sb.id, "state", g.State)
			return false, nil
		}
	}); err != nil {
		return nil, err
	}
	return sb, nil
}
