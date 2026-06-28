package shellio_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/shellio"
)

// rwPipe adapts a separate reader and writer into one io.ReadWriter.
type rwPipe struct {
	r io.Reader
	w io.Writer
}

func (p rwPipe) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p rwPipe) Write(b []byte) (int, error) { return p.w.Write(b) }

// fakeShell emulates the remote pty over a pair of pipes. For each \r-terminated
// line the engine writes, it extracts the nonce from the embedded
// `printf '<nonce>%d\n' $?`, runs handler(cmd) on the command text, and writes
// back [echo of the line if echo][handler output][<nonce><code>\n] — exactly
// what a real shell would emit. This exercises the engine's real wire format.
type fakeShell struct {
	rw     io.ReadWriter
	srvIn  *io.PipeReader
	srvOut *io.PipeWriter
	engW   *io.PipeWriter
	engR   *io.PipeReader
	hl     func(cmd string) ([]byte, int)
	echo   bool
	once   sync.Once
}

func newFakeShell(echo bool, handler func(string) ([]byte, int)) *fakeShell {
	aR, aW := io.Pipe() // engine -> server
	bR, bW := io.Pipe() // server -> engine
	f := &fakeShell{
		rw:     rwPipe{r: bR, w: aW},
		srvIn:  aR,
		srvOut: bW,
		engW:   aW,
		engR:   bR,
		hl:     handler,
		echo:   echo,
	}
	go f.serve()
	return f
}

func (f *fakeShell) serve() {
	br := bufio.NewReader(f.srvIn)
	for {
		line, err := br.ReadString('\r')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r")
		if f.echo {
			_, _ = io.WriteString(f.srvOut, line+"\r\n")
		}
		// Model a real tty: `stty -echo` (sent during setup) turns echo off for
		// all subsequent commands.
		if strings.Contains(line, "stty -echo") {
			f.echo = false
		}
		nonce := parseNonce(line)
		out, code := f.hl(parseCmd(line))
		var b bytes.Buffer
		b.Write(out)
		fmt.Fprintf(&b, "%s%d\n", nonce, code)
		if _, err := f.srvOut.Write(b.Bytes()); err != nil {
			return
		}
	}
}

func (f *fakeShell) Close() {
	f.once.Do(func() { _ = f.engW.Close(); _ = f.srvOut.Close() })
}

// parseNonce pulls the nonce out of `... ; printf '<nonce>%d\n' $?`.
func parseNonce(line string) string {
	const p = "printf '"
	i := strings.LastIndex(line, p)
	if i < 0 {
		return ""
	}
	rest := line[i+len(p):]
	j := strings.Index(rest, "%d")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// parseCmd returns the command text before the ` ; printf '` sentinel suffix.
func parseCmd(line string) string {
	const sep = " ; printf '"
	if i := strings.LastIndex(line, sep); i >= 0 {
		return line[:i]
	}
	return line
}

// setupAware wraps a per-command handler so the engine's one-time setup command
// (stty/PS1/bind…) returns cleanly with no output.
func setupAware(h func(string) ([]byte, int)) func(string) ([]byte, int) {
	return func(cmd string) ([]byte, int) {
		if strings.HasPrefix(cmd, "stty") {
			return nil, 0
		}
		return h(cmd)
	}
}

func TestSession_RunCapturesOutputAndExitCode(t *testing.T) {
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return []byte("hello\n"), 0
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "echo hello")
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "hello\n", string(res.Output))
}

func TestSession_RunFailingExitCode(t *testing.T) {
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return []byte("nope\n"), 7
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "sh -c 'exit 7'")
	require.NoError(t, err)
	assert.Equal(t, 7, res.ExitCode, "non-zero remote exit code must be recovered")
	assert.Equal(t, "nope\n", string(res.Output))
}

func TestSession_EchoDisabledAfterSetup(t *testing.T) {
	// Start with echo ON (interactive default); setup's `stty -echo` must silence
	// it so command output is clean (no echoed command line mixed in).
	fs := newFakeShell(true, setupAware(func(string) ([]byte, int) {
		return []byte("result\n"), 0
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "mycmd --flag")
	require.NoError(t, err)
	assert.Equal(t, "result\n", string(res.Output), "echoed command line must not leak into output")
	assert.NotContains(t, string(res.Output), "mycmd")
}

func TestSession_LargeOutputIntact(t *testing.T) {
	const size = 1 << 20 // 1 MB
	big := bytes.Repeat([]byte("abcdefgh"), size/8)
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return big, 0
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "cat big")
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, len(big), len(res.Output), "1 MB output must survive intact")
	assert.True(t, bytes.Equal(big, res.Output))
}

func TestSession_BinarySafe(t *testing.T) {
	// Arbitrary bytes (NUL, 0xff, a stray ESC not forming a full sequence) must
	// pass through verbatim in raw mode.
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 'a', 0x1b, 'Z', 0x7f, '\n', 0x00}
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return payload, 0
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw, shellio.WithoutANSIStrip()).Run(context.Background(), "cat blob")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, res.Output), "raw bytes must be byte-for-byte preserved")
}

func TestSession_DecoySentinelNotMisparsed(t *testing.T) {
	// Output contains a sentinel-shaped line with a DIFFERENT nonce; the engine
	// must ignore it and terminate only on its own random nonce.
	decoy := []byte("__whim_DECOY_999\nreal output\n")
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return decoy, 3
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "echo trick")
	require.NoError(t, err)
	assert.Equal(t, 3, res.ExitCode, "must read past the decoy to the real sentinel")
	assert.Contains(t, string(res.Output), "real output")
	assert.Contains(t, string(res.Output), "__whim_DECOY_999", "decoy content is part of the output, not a terminator")
}

func TestSession_StripsANSI(t *testing.T) {
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return []byte("\x1b[?2004l\x1b[0;32mgreen\x1b[0m\n"), 0
	}))
	defer fs.Close()

	res, err := shellio.New(fs.rw).Run(context.Background(), "ls --color")
	require.NoError(t, err)
	assert.Equal(t, "green\n", string(res.Output), "ANSI escapes (incl. bracketed-paste toggle) must be stripped")
}

// blockingRW records everything written and blocks forever on Read (until
// closed), modelling a hung remote command that never emits its sentinel.
type blockingRW struct {
	mu  sync.Mutex
	w   bytes.Buffer
	rel chan struct{}
}

func newBlockingRW() *blockingRW { return &blockingRW{rel: make(chan struct{})} }

func (b *blockingRW) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.w.Write(p)
	b.mu.Unlock()
	return len(p), nil
}

func (b *blockingRW) Read(p []byte) (int, error) {
	<-b.rel
	return 0, io.EOF
}

func (b *blockingRW) wrote(c byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.IndexByte(b.w.Bytes(), c) >= 0
}

// floodRW always returns data on Read (never a sentinel, never EOF) and discards
// writes — models a remote that keeps emitting, used to exercise the reader's
// teardown when the consumer has gone.
type floodRW struct{}

func (floodRW) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
func (floodRW) Write(p []byte) (int, error) { return len(p), nil }

// scriptRW returns a fixed sequence of chunks on successive Reads (then EOF),
// letting a test control exactly how bytes are framed across Read boundaries.
type scriptRW struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (r *scriptRW) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	if n < len(r.chunks[0]) {
		r.chunks[0] = r.chunks[0][n:]
	} else {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}
func (r *scriptRW) Write(p []byte) (int, error) { return len(p), nil }

func TestSession_OutputCapExceeded(t *testing.T) {
	fs := newFakeShell(false, setupAware(func(string) ([]byte, int) {
		return bytes.Repeat([]byte("x"), 256*1024), 0
	}))
	defer fs.Close()

	sess := shellio.New(fs.rw, shellio.WithMaxOutput(64*1024))
	defer sess.Close()
	_, err := sess.Run(context.Background(), "cat big")
	require.ErrorIs(t, err, shellio.ErrOutputTooLarge, "output past the cap must abort, not OOM")
}

func TestSession_SetupTimeout(t *testing.T) {
	rw := newBlockingRW()
	t.Cleanup(func() { close(rw.rel) }) // unblock the reader so it exits

	sess := shellio.New(rw,
		shellio.WithSetupTimeout(30*time.Millisecond),
		shellio.WithInterruptGrace(10*time.Millisecond),
	)
	start := time.Now()
	_, err := sess.Run(context.Background(), "echo hi")
	require.Error(t, err, "a never-ready setup must fail, not hang")
	assert.Less(t, time.Since(start), 2*time.Second, "setup must time out fast")
}

func TestSession_CloseUnblocksReader(t *testing.T) {
	before := runtime.NumGoroutine()
	sess := shellio.New(floodRW{}, shellio.WithMaxOutput(64*1024))

	_, err := sess.Run(context.Background(), "flood")
	require.Error(t, err) // ErrOutputTooLarge: the flood trips the cap

	require.NoError(t, sess.Close())
	// Poll synchronously (assert.Eventually would spawn its own goroutine and skew
	// the count). The reader, blocked on a full readCh send, must observe done.
	var n int
	for i := 0; i < 200; i++ {
		if n = runtime.NumGoroutine(); n <= before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, n, before, "the reader goroutine must exit after Close even when blocked on a full channel")
}

func TestSession_SentinelSplitAcrossReads(t *testing.T) {
	const nonce = "__SPLIT__"
	out := strings.Repeat("z", 50)
	rw := &scriptRW{chunks: [][]byte{
		[]byte(nonce + "0\n"),     // setup sentinel
		[]byte(out + nonce[:4]),   // command output + first half of the nonce
		[]byte(nonce[4:] + "5\n"), // rest of the nonce + exit code (split sentinel)
	}}
	sess := shellio.New(rw, shellio.WithNonce(func() string { return nonce }))

	res, err := sess.Run(context.Background(), "cmd")
	require.NoError(t, err)
	assert.Equal(t, 5, res.ExitCode, "a sentinel split across two reads must still parse")
	assert.Equal(t, out, string(res.Output))
}

func TestSession_ContextCancelInterruptsAndReturns(t *testing.T) {
	rw := newBlockingRW()
	sess := shellio.New(rw, shellio.WithInterruptGrace(20*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	_, err := sess.Run(ctx, "sleep 999")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "a hung command must end when ctx is canceled")
	assert.True(t, rw.wrote(0x03), "ctx cancel must send Ctrl-C (0x03 / SIGINT) to the remote")
}

// eofRW accepts writes but its reads return EOF immediately — a stream that
// closes before any sentinel is seen.
type eofRW struct{}

func (eofRW) Read([]byte) (int, error)    { return 0, io.EOF }
func (eofRW) Write(p []byte) (int, error) { return len(p), nil }

func TestSession_StreamClosedBeforeSentinel(t *testing.T) {
	_, err := shellio.New(eofRW{}).Run(context.Background(), "echo hi")
	require.ErrorIs(t, err, shellio.ErrProtocol, "a closed stream must be a protocol error, not a hang")
}
