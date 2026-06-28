package microvm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/udgover/whim/internal/shellio"
)

// ExecResult is the outcome of Sandbox.Exec.
type ExecResult struct {
	// Output is the combined stdout+stderr of the command. A pty merges the two
	// streams, so they cannot be separated over this transport (the guest-agent
	// path would be required for separate streams); ANSI escapes are stripped.
	Output []byte
	// ExitCode is the command's exit status.
	ExitCode int
}

// execConfig is the resolved Exec configuration. Reserved for future options
// (environment, stdin, working directory); none are defined yet.
type execConfig struct{}

// ExecOption configures a single Exec call.
type ExecOption func(*execConfig)

// Exec runs argv in the MicroVM and returns its combined output and exit code.
//
// argv is executed by the remote shell after each element is shell-quoted, so
// spaces and metacharacters in arguments are passed literally — there is no word
// splitting or injection from argument contents. Canceling ctx interrupts the
// remote command (SIGINT, then connection teardown). The shell token is never
// logged.
//
// A non-zero ExitCode is returned with a nil error; err is non-nil only for
// transport/protocol failures (ErrConnClosed), timeouts (ErrTimeout), or
// cancellation (context.Canceled).
func (s *Sandbox) Exec(ctx context.Context, argv []string, opts ...ExecOption) (*ExecResult, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: argv is required", ErrInvalidOption)
	}
	// A newline/CR/NUL in an argument would desync the single-line pty command
	// framing (the line is submitted with \r); reject before doing any work.
	for _, a := range argv {
		if strings.ContainsAny(a, "\n\r\x00") {
			return nil, fmt.Errorf("%w: argument contains a control character (newline, CR, or NUL)", ErrInvalidOption)
		}
	}
	var cfg execConfig
	for _, o := range opts {
		o(&cfg)
	}

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := s.dialShell(sessCtx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	sess := shellio.New(conn)
	defer func() { _ = sess.Close() }() // tear down the reader goroutine, even on cancel
	res, err := sess.Run(sessCtx, shellQuote(argv))
	if err != nil {
		// Classify: a canceled/expired session is not a connection fault.
		if ctxErr := sessCtx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return nil, fmt.Errorf("exec on %q: %w", s.id, ErrTimeout)
			}
			return nil, ctxErr // context.Canceled
		}
		if errors.Is(err, shellio.ErrProtocol) {
			return nil, fmt.Errorf("exec on %q: %w", s.id, ErrConnClosed)
		}
		return nil, fmt.Errorf("exec on %q: %w", s.id, err)
	}
	return &ExecResult{Output: res.Output, ExitCode: res.ExitCode}, nil
}

// shellQuote single-quotes each argv element so the remote shell receives every
// argument verbatim, then joins with spaces. Example: ["sh","-c","exit 7"] →
// `'sh' '-c' 'exit 7'`.
func shellQuote(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote1(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote1 single-quotes one string (escaping an embedded single quote as
// '\”) so the remote shell treats it as a single literal token.
func shellQuote1(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
