package microvm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

// hangServer sends session_init then blocks until the client disconnects — it
// never closes on its own, mimicking the real shell endpoint.
func hangServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session_init"}`))
		<-r.Context().Done()
	}))
}

// abnormalCloseServer sends session_init then closes with a non-normal status,
// modelling an unexpected connection drop (not a clean remote exit).
func abnormalCloseServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session_init"}`))
		c.Close(websocket.StatusInternalError, "boom")
	}))
}

func TestShell_RequiresOut(t *testing.T) {
	mock := shellMock("unused")
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	err = sb.Shell(context.Background(), microvm.ShellIO{In: strings.NewReader("x")})
	require.ErrorIs(t, err, microvm.ErrInvalidOption, "Shell requires an Out writer")
	assert.Empty(t, mock.CreateShellAuthTokenCalls, "must validate before minting a token")
}

func TestShell_ContextCanceled_ReturnsContextError(t *testing.T) {
	srv := hangServer()
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err = sb.Shell(ctx, microvm.ShellIO{Out: &bytes.Buffer{}})
	require.ErrorIs(t, err, context.Canceled, "ctx cancel must surface as context.Canceled (CLI treats as clean disconnect)")
}

func TestShell_ContextDeadline_ReturnsErrTimeout(t *testing.T) {
	srv := hangServer()
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = sb.Shell(ctx, microvm.ShellIO{Out: &bytes.Buffer{}})
	require.ErrorIs(t, err, microvm.ErrTimeout, "a deadline must surface as ErrTimeout")
	assert.False(t, errors.Is(err, microvm.ErrConnClosed), "deadline is not a connection fault")
}

func TestShell_AbnormalClosure_ReturnsErrConnClosed(t *testing.T) {
	srv := abnormalCloseServer()
	defer srv.Close()
	sb, err := newTestManager(shellMock(srv.URL)).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	err = sb.Shell(context.Background(), microvm.ShellIO{Out: &bytes.Buffer{}})
	require.ErrorIs(t, err, microvm.ErrConnClosed, "an abnormal ws closure must surface as ErrConnClosed")
}

// echoOnceServer mimics the shell endpoint: send session_init, read one
// message, echo it back, then close normally.
func echoOnceServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"session_init","session_id":"x"}`))
		_, data, err := c.Read(ctx)
		if err != nil {
			c.Close(websocket.StatusInternalError, "")
			return
		}
		_ = c.Write(ctx, websocket.MessageBinary, data)
		c.Close(websocket.StatusNormalClosure, "")
	}))
}

// shellMock returns a mock that launches a RUNNING VM whose endpoint is the
// given fake ws server, and mints a shell token.
func shellMock(endpoint string) *awsapi.Mock {
	m := &awsapi.Mock{}
	m.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-sh", Endpoint: endpoint, State: "RUNNING"}, nil
	}
	m.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-sh", Endpoint: endpoint, State: "RUNNING"}, nil
	}
	m.CreateShellAuthTokenFn = func(_ context.Context, in *awsapi.CreateShellAuthTokenInput) (*awsapi.CreateShellAuthTokenOutput, error) {
		_ = in
		return &awsapi.CreateShellAuthTokenOutput{HeaderKey: "X-aws-proxy-auth", HeaderValue: "tok"}, nil
	}
	return m
}

func TestShell_PipesInputToOutput(t *testing.T) {
	srv := echoOnceServer()
	defer srv.Close()
	mock := shellMock(srv.URL)
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	var out bytes.Buffer
	err = sb.Shell(context.Background(), microvm.ShellIO{In: strings.NewReader("echo-me"), Out: &out})
	require.NoError(t, err)
	assert.Equal(t, "echo-me", out.String(), "input must round-trip through the shell ws (session_init skipped)")

	require.Len(t, mock.CreateShellAuthTokenCalls, 1, "Shell must mint a shell token")
	assert.Equal(t, sb.ID(), mock.CreateShellAuthTokenCalls[0].MicrovmIdentifier)
}

func TestShell_TokenMintError(t *testing.T) {
	srv := echoOnceServer()
	defer srv.Close()
	mock := shellMock(srv.URL)
	mock.CreateShellAuthTokenFn = func(_ context.Context, _ *awsapi.CreateShellAuthTokenInput) (*awsapi.CreateShellAuthTokenOutput, error) {
		return nil, fmt.Errorf("token denied")
	}
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	err = sb.Shell(context.Background(), microvm.ShellIO{In: strings.NewReader("x"), Out: &bytes.Buffer{}})
	require.Error(t, err, "Shell must surface a token-mint failure")
}

func TestShell_DrainsResizeChannel(t *testing.T) {
	srv := echoOnceServer()
	defer srv.Close()
	mock := shellMock(srv.URL)
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	resize := make(chan microvm.WinSize, 1)
	resize <- microvm.WinSize{Rows: 40, Cols: 120} // must not block or panic (no-op for v0.1)
	var out bytes.Buffer
	err = sb.Shell(context.Background(), microvm.ShellIO{In: strings.NewReader("hi"), Out: &out, Resize: resize})
	require.NoError(t, err)
	assert.Equal(t, "hi", out.String())
}
