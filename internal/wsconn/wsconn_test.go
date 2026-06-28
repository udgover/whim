package wsconn_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/wsconn"
)

const (
	testHeaderKey = "X-aws-proxy-auth"
	testToken     = "secret-token-value-xyz"
)

// fakeShellServer mimics the MicroVM shell endpoint: it sends a session_init
// JSON frame, then echoes every subsequent message. onHeader (if set) receives
// the request headers for assertions.
func fakeShellServer(onHeader func(http.Header)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onHeader != nil {
			onHeader(r.Header.Clone())
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "")
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"session_init","session_id":"test-123"}`))
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if err := c.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}))
}

func TestDial_SkipsSessionInit_AndRoundTrips(t *testing.T) {
	srv := fakeShellServer(nil)
	defer srv.Close()

	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)
	defer conn.Close()

	// First bytes we read must be our echoed payload, NOT the session_init frame.
	_, err = conn.Write([]byte("hello-pty"))
	require.NoError(t, err)

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello-pty", string(buf[:n]), "session_init must be skipped; first read is the echoed payload")
}

func TestDial_SendsAuthHeader(t *testing.T) {
	var got http.Header
	srv := fakeShellServer(func(h http.Header) { got = h })
	defer srv.Close()

	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, testToken, got.Get(testHeaderKey), "auth token must be sent as the header")
}

func TestDial_BareHost_BecomesWSS(t *testing.T) {
	// A bare host must dial wss:// (will fail to connect, but the scheme is the point).
	_, err := wsconn.Dial(context.Background(), "nonexistent.invalid", testHeaderKey, testToken)
	require.Error(t, err) // can't connect, but it tried wss://
}

func TestConn_RoundTripMultipleWrites(t *testing.T) {
	srv := fakeShellServer(nil)
	defer srv.Close()
	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)
	defer conn.Close()

	for _, msg := range []string{"one", "two", "three"} {
		_, err := conn.Write([]byte(msg))
		require.NoError(t, err)
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, msg, string(buf[:n]))
	}
}

func TestConn_Close_AbortsReads(t *testing.T) {
	srv := fakeShellServer(nil)
	defer srv.Close()
	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)

	require.NoError(t, conn.Close())
	// After close, reads must fail rather than block.
	done := make(chan error, 1)
	go func() {
		_, e := conn.Read(make([]byte, 8))
		done <- e
	}()
	select {
	case e := <-done:
		assert.Error(t, e)
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return after Close")
	}
}

func TestConn_NormalClosure_IsEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"session_init"}`))
		c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Read(make([]byte, 8))
	assert.ErrorIs(t, err, io.EOF, "remote normal closure must read as io.EOF so io.Copy stops cleanly")
}

// io.Copy must work over Conn (it's the basis for Shell's stdin/stdout pump).
func TestConn_ImplementsReadWriteCloser(t *testing.T) {
	var _ io.ReadWriteCloser = (*wsconn.Conn)(nil)
	assert.True(t, strings.HasPrefix(testHeaderKey, "X-aws"))
}

func TestConn_SkipsEmptyFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"session_init"}`))
		_ = c.Write(ctx, websocket.MessageBinary, []byte{})          // empty frame
		_ = c.Write(ctx, websocket.MessageBinary, []byte("payload")) // real data
		c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	conn, err := wsconn.Dial(context.Background(), srv.URL, testHeaderKey, testToken)
	require.NoError(t, err)
	defer conn.Close()

	// A single Read must skip the empty frame and return the real payload, not (0,nil).
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(buf[:n]), "empty ws frames must be skipped, not surfaced as (0,nil)")
}
