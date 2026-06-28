// Package shellio turns the raw interactive pty exposed by a MicroVM shell
// (session_init → raw bytes, bracketed-paste on, runs as root) into reliable
// one-shot command execution with recovered exit codes.
//
// The technique (SPEC §4): once per session disable echo, clear the prompt and
// bracketed paste; then for each command write `<cmd> ; printf '<nonce><code>\n'`
// and read the merged pty stream until the random per-command nonce appears
// followed by the exit code. The nonce is long and random so command output
// cannot reproduce it, and the terminator is anchored on digits so an echoed
// copy of the `printf` format (which has a literal %d) is never mistaken for it.
//
// It operates over any io.ReadWriter (the wsconn.Conn shell stream), so the
// parser is fully testable without AWS.
package shellio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var (
	// ErrProtocol indicates the stream ended or misbehaved before a sentinel was
	// seen — the exec result could not be recovered.
	ErrProtocol = errors.New("shellio: protocol error")
	// ErrOutputTooLarge indicates a command produced more output than the
	// configured maximum before its sentinel (see WithMaxOutput).
	ErrOutputTooLarge = errors.New("shellio: output exceeded maximum")
)

// readChunk is the per-Read buffer size for draining the pty stream.
const readChunk = 32 * 1024

// ctrlC is the byte that, on a pty, raises SIGINT in the foreground process
// group — sent to interrupt the remote command when ctx is canceled.
const ctrlC = 0x03

// defaultInterruptGrace is how long, after sending SIGINT, to keep reading for
// the command's sentinel before giving up and letting the caller tear down.
const defaultInterruptGrace = 2 * time.Second

// defaultSetupTimeout bounds the one-time session setup so a never-ready shell
// (dropped input / hung pty) fails fast instead of hanging until the VM TTL.
const defaultSetupTimeout = 30 * time.Second

// defaultMaxOutput caps a single command's buffered output to bound memory.
// WithMaxOutput(0) disables the cap.
const defaultMaxOutput = 32 << 20 // 32 MiB

// sentinelTailMax bounds the exit-code digits + CR/LF after the nonce; used as
// the re-scan overlap so a sentinel split across read chunks is still matched.
const sentinelTailMax = 24

// setupCmd puts the interactive shell into a quiet, parseable state: no input
// echo, empty prompt, no PROMPT_COMMAND, bracketed paste off. Failures are
// tolerated (2>/dev/null) so a minimal shell still proceeds.
const setupCmd = "stty -echo 2>/dev/null; PS1=''; PS2=''; PROMPT_COMMAND=''; bind 'set enable-bracketed-paste off' 2>/dev/null"

// ansiSeq matches the ANSI/VT escape sequences a shell emits (CSI like the
// bracketed-paste toggles, plus OSC and a few two-byte escapes) plus carriage
// returns, so they can be stripped from human-readable output.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][AB0-2]|\r`)

// Result is the outcome of a single Run.
type Result struct {
	Output   []byte // combined stdout+stderr (a pty merges them); ANSI-stripped unless disabled
	ExitCode int
}

// Session executes commands over a single shell stream. It is not safe for
// concurrent Run calls — one command is in flight at a time, like a terminal.
//
// A single background goroutine reads the stream for the whole session (started
// lazily) so the wsconn.Conn single-reader contract is never violated; commands
// consume from it sequentially. Call Close to release that goroutine (Exec does
// so via defer); it also exits when the underlying stream is closed.
//
// A Session must NOT be reused after a Run is canceled mid-command: the
// interrupted command's late output/sentinel would corrupt the next command.
// (Sandbox.Exec uses one command per session, so it is unaffected.)
type Session struct {
	rw             io.ReadWriter
	nonceFn        func() string
	stripANSI      bool
	interruptGrace time.Duration
	setupTimeout   time.Duration
	maxOutput      int
	started        bool

	readerOnce sync.Once
	readCh     chan readResult
	leftover   []byte // bytes read past the previous command's sentinel
	done       chan struct{}
	closeOnce  sync.Once
}

// Option configures a Session.
type Option func(*Session)

// WithNonce overrides the per-command nonce generator (tests use a deterministic
// one). Production uses a cryptographically random nonce.
func WithNonce(fn func() string) Option { return func(s *Session) { s.nonceFn = fn } }

// WithoutANSIStrip returns Output verbatim (no ANSI stripping) — for
// binary-faithful transfers rather than human-readable command output.
func WithoutANSIStrip() Option { return func(s *Session) { s.stripANSI = false } }

// WithInterruptGrace sets how long Run keeps reading for the sentinel after
// sending SIGINT on ctx cancel, before giving up. Defaults to 2s.
func WithInterruptGrace(d time.Duration) Option {
	return func(s *Session) { s.interruptGrace = d }
}

// WithSetupTimeout bounds the one-time session setup (default 30s) so a
// never-ready shell fails fast instead of hanging.
func WithSetupTimeout(d time.Duration) Option {
	return func(s *Session) { s.setupTimeout = d }
}

// WithMaxOutput caps a single command's buffered output (default 32 MiB); 0
// disables the cap. Output past the cap fails with ErrOutputTooLarge.
func WithMaxOutput(n int) Option { return func(s *Session) { s.maxOutput = n } }

// New builds a Session over rw. Setup is performed lazily on the first Run.
func New(rw io.ReadWriter, opts ...Option) *Session {
	s := &Session{
		rw:             rw,
		nonceFn:        randomNonce,
		stripANSI:      true,
		interruptGrace: defaultInterruptGrace,
		setupTimeout:   defaultSetupTimeout,
		maxOutput:      defaultMaxOutput,
		done:           make(chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Close releases the session's background reader goroutine. It is idempotent and
// safe to defer. After Close the Session must not be used again.
func (s *Session) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// randomNonce returns a long, unguessable per-command sentinel token.
func randomNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "__whim_" + hex.EncodeToString(b[:]) + "_"
}

// Run executes cmd and returns its combined output and exit code. The first call
// performs one-time session setup. ctx is checked between operations; mid-read
// cancellation is wired via the underlying stream.
func (s *Session) Run(ctx context.Context, cmd string) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.startReader()
	if !s.started {
		// Bound setup only — a never-ready shell must fail fast, but the command
		// itself may legitimately run long (caller's ctx governs that).
		setupCtx, cancel := context.WithTimeout(ctx, s.setupTimeout)
		_, _, err := s.command(setupCtx, setupCmd)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("shellio setup: %w", err)
		}
		s.started = true
	}
	out, code, err := s.command(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return &Result{Output: out, ExitCode: code}, nil
}

// command writes one sentinel-framed command and reads until its sentinel.
func (s *Session) command(ctx context.Context, cmd string) (output []byte, code int, err error) {
	nonce := s.nonceFn()
	// `<cmd> ; printf '<nonce>%d\n' $?` submitted with \r. $? is cmd's exit code
	// (printf's args are evaluated before printf runs).
	line := cmd + " ; printf '" + nonce + "%d\\n' $?\r"
	if _, err := s.rw.Write([]byte(line)); err != nil {
		return nil, 0, fmt.Errorf("write command: %w", err)
	}
	return s.readUntilSentinel(ctx, nonce)
}

type readResult struct {
	data []byte
	err  error
}

// startReader launches the session's single stream reader (once). It forwards
// every chunk to readCh and, on error, sends it and closes the channel. It exits
// when the caller closes the underlying stream.
func (s *Session) startReader() {
	s.readerOnce.Do(func() {
		s.readCh = make(chan readResult, 8)
		go func() {
			chunk := make([]byte, readChunk)
			for {
				n, rerr := s.rw.Read(chunk)
				if n > 0 {
					select {
					case s.readCh <- readResult{data: append([]byte(nil), chunk[:n]...)}:
					case <-s.done: // Close called and the consumer is gone — don't block
						return
					}
				}
				if rerr != nil {
					select {
					case s.readCh <- readResult{err: rerr}:
					case <-s.done:
					}
					close(s.readCh)
					return
				}
			}
		}()
	})
}

// readUntilSentinel consumes the shared reader until <nonce><digits>\n, returning
// the bytes before it (the output) and the parsed exit code. On ctx cancel it
// sends SIGINT (Ctrl-C) to the remote, then keeps reading for interruptGrace (the
// interrupted command may still print its sentinel, e.g. exit 130) before giving
// up and letting the caller tear the connection down.
func (s *Session) readUntilSentinel(ctx context.Context, nonce string) ([]byte, int, error) {
	// Anchored on ≥1 digit + newline so an echoed `printf '<nonce>%d\n'` (where a
	// literal % follows the nonce) is never matched as the terminator.
	term := regexp.MustCompile(regexp.QuoteMeta(nonce) + `(\d+)\r?\n`)
	overlap := len(nonce) + sentinelTailMax

	var buf bytes.Buffer
	scanned := 0 // buf is scanned for the sentinel only beyond scanned-overlap

	// finish scans the unscanned tail (plus overlap, so a sentinel split across
	// chunks is still caught) and, on a match, returns the ANSI-stripped output
	// before it and the exit code, stashing any trailing bytes as leftover.
	finish := func() (bool, []byte, int) {
		from := scanned - overlap
		if from < 0 {
			from = 0
		}
		loc := term.FindSubmatchIndex(buf.Bytes()[from:])
		scanned = buf.Len()
		if loc == nil {
			return false, nil, 0
		}
		b := buf.Bytes()
		start, end := from+loc[0], from+loc[1]
		exit, _ := strconv.Atoi(string(b[from+loc[2] : from+loc[3]]))
		s.leftover = append([]byte(nil), b[end:]...)
		output := b[:start]
		if s.stripANSI {
			output = ansiSeq.ReplaceAll(output, nil)
		}
		return true, append([]byte(nil), output...), exit
	}

	// Carry over any bytes the previous command read past its sentinel.
	if len(s.leftover) > 0 {
		buf.Write(s.leftover)
		s.leftover = nil
		if done, out, code := finish(); done {
			return out, code, nil
		}
	}

	ctxDone := ctx.Done()
	var grace <-chan time.Time
	for {
		select {
		case <-ctxDone:
			ctxDone = nil                        // handle cancellation once
			_, _ = s.rw.Write([]byte{ctrlC})     // SIGINT to the remote foreground group
			grace = time.After(s.interruptGrace) // then allow it to print its sentinel
		case <-grace:
			return nil, 0, ctx.Err()
		case r, ok := <-s.readCh:
			if !ok {
				return nil, 0, fmt.Errorf("%w: stream closed before sentinel", ErrProtocol)
			}
			if len(r.data) > 0 {
				buf.Write(r.data)
				if done, out, code := finish(); done {
					return out, code, nil
				}
				if s.maxOutput > 0 && buf.Len() > s.maxOutput {
					return nil, 0, fmt.Errorf("%w: %d bytes without a sentinel", ErrOutputTooLarge, buf.Len())
				}
			}
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return nil, 0, fmt.Errorf("%w: stream closed before sentinel", ErrProtocol)
				}
				return nil, 0, r.err
			}
		}
	}
}
