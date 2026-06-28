package microvm_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// shellWSServer is a fake shell endpoint that speaks the shellio sentinel
// protocol: session_init, then for each \r-terminated command line it parses the
// nonce from the embedded printf, runs handler(cmd), and replies with
// [output][<nonce><code>\n] — like a real (echo-off) shell. capture, if set,
// records each command's text.
func shellWSServer(handler func(cmd string) ([]byte, int), capture *[]string) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"session_init"}`))

		var line []byte
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			line = append(line, data...)
			for {
				i := bytes.IndexByte(line, '\r')
				if i < 0 {
					break
				}
				cmdline := string(line[:i])
				line = line[i+1:]
				cmd := execParseCmd(cmdline)
				if capture != nil {
					mu.Lock()
					*capture = append(*capture, cmd)
					mu.Unlock()
				}
				out, code := handler(cmd)
				var resp bytes.Buffer
				resp.Write(out)
				fmt.Fprintf(&resp, "%s%d\n", execParseNonce(cmdline), code)
				if err := c.Write(ctx, websocket.MessageBinary, resp.Bytes()); err != nil {
					return
				}
			}
		}
	}))
}

func execParseNonce(line string) string {
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

func execParseCmd(line string) string {
	const sep = " ; printf '"
	if i := strings.LastIndex(line, sep); i >= 0 {
		return line[:i]
	}
	return line
}

// execHandler answers the one-time setup command cleanly, delegating real
// commands to h.
func execHandler(h func(string) ([]byte, int)) func(string) ([]byte, int) {
	return func(cmd string) ([]byte, int) {
		if strings.HasPrefix(cmd, "stty") {
			return nil, 0
		}
		return h(cmd)
	}
}

func TestExec_RunsCommandAndExitCode(t *testing.T) {
	srv := shellWSServer(execHandler(func(string) ([]byte, int) {
		return []byte("hi\n"), 0
	}), nil)
	defer srv.Close()

	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	res, err := sb.Exec(context.Background(), []string{"echo", "hi"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "hi\n", string(res.Output))
}

func TestExec_PropagatesFailingExitCode(t *testing.T) {
	srv := shellWSServer(execHandler(func(string) ([]byte, int) {
		return nil, 7
	}), nil)
	defer srv.Close()

	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	res, err := sb.Exec(context.Background(), []string{"sh", "-c", "exit 7"})
	require.NoError(t, err)
	assert.Equal(t, 7, res.ExitCode, "remote exit code must propagate")
}

func TestExec_QuotesArguments(t *testing.T) {
	var seen []string
	srv := shellWSServer(execHandler(func(string) ([]byte, int) { return nil, 0 }), &seen)
	defer srv.Close()

	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	_, err = sb.Exec(context.Background(), []string{"echo", "a b", "$HOME"})
	require.NoError(t, err)

	// The real command (not the stty setup) must arrive fully single-quoted so
	// spaces and metacharacters are inert.
	var cmd string
	for _, c := range seen {
		if !strings.HasPrefix(c, "stty") {
			cmd = c
		}
	}
	assert.Equal(t, `'echo' 'a b' '$HOME'`, cmd)
}

func TestExec_RejectsControlCharArgs(t *testing.T) {
	mock := shellMock("unused")
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	// A newline/CR/NUL in an arg would desync the pty command framing.
	for _, bad := range []string{"echo\nrm -rf /", "a\rb", "x\x00y"} {
		_, err := sb.Exec(context.Background(), []string{"echo", bad})
		require.ErrorIs(t, err, microvm.ErrInvalidOption, "arg %q must be rejected", bad)
	}
	assert.Empty(t, mock.CreateShellAuthTokenCalls, "must reject before minting a token")
}

func TestExec_RequiresArgv(t *testing.T) {
	mock := shellMock("unused")
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	_, err = sb.Exec(context.Background(), nil)
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateShellAuthTokenCalls, "must validate before minting a token")
}

func TestExec_ConnClosedBeforeSentinel(t *testing.T) {
	// Endpoint sends session_init then closes — no sentinel ever arrives.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session_init"}`))
		c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	_, err = sb.Exec(context.Background(), []string{"echo", "hi"})
	require.ErrorIs(t, err, microvm.ErrConnClosed)
}
