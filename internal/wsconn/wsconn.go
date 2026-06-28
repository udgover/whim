// Package wsconn handles the WebSocket connection to a MicroVM shell endpoint.
// It dials wss://<endpoint>, injects the auth header, reads/skips the
// session_init JSON frame, then exposes the connection as a bidirectional
// raw-pty byte stream (io.ReadWriteCloser).
package wsconn

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// maxFrameBytes bounds a single inbound ws message so a buggy or hostile peer
// can't exhaust memory. pty frames are tiny and even large Exec output (later)
// arrives well under this; it exists only as a ceiling, not a real limit.
const maxFrameBytes = 64 << 20 // 64 MiB

// Conn is a byte-stream view over the MicroVM shell WebSocket. After Dial has
// consumed the session_init frame, Read/Write carry raw pty bytes. Conn is an
// io.ReadWriteCloser so it composes with io.Copy.
//
// Conn is NOT safe for concurrent use by multiple readers or multiple writers:
// Read mutates readBuf and must be called from a single goroutine, as must
// Write. One concurrent reader + one concurrent writer + Close is fine (the
// usual Shell pattern: stdin→Write in one goroutine, ws→Read in another).
type Conn struct {
	ws      *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	readBuf []byte // leftover bytes from a partially-consumed ws message
}

// Dial connects to the shell endpoint, sets the auth header, and discards the
// leading session_init control frame. endpoint may be a bare host (→ wss://) or
// a full ws/wss/http/https URL. The auth token is never logged.
func Dial(ctx context.Context, endpoint, headerKey, headerValue string) (*Conn, error) {
	hdr := http.Header{}
	hdr.Set(headerKey, headerValue)

	ws, _, err := websocket.Dial(ctx, wssURL(endpoint), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("dial shell websocket: %w", err)
	}
	// pty output (and large Exec output later) can exceed the 32 KiB default;
	// raise to a generous finite ceiling rather than unlimited (-1) so a runaway
	// frame can't exhaust memory.
	ws.SetReadLimit(maxFrameBytes)

	connCtx, cancel := context.WithCancel(ctx)
	c := &Conn{ws: ws, ctx: connCtx, cancel: cancel}

	// The first frame is the session_init JSON control message — read and discard.
	if _, _, err := ws.Read(connCtx); err != nil {
		cancel()
		_ = ws.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("read session_init frame: %w", err)
	}
	return c, nil
}

// Read returns raw pty bytes, buffering across ws message boundaries. Empty ws
// frames are skipped (read the next one) rather than surfaced as (0,nil), which
// would otherwise spin io.Copy on a peer that emits zero-length frames.
func (c *Conn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			// A normal/going-away close is end-of-stream, not an error, so
			// io.Copy(out, conn) stops cleanly when the remote shell exits.
			if s := websocket.CloseStatus(err); s == websocket.StatusNormalClosure || s == websocket.StatusGoingAway {
				return 0, io.EOF
			}
			return 0, err
		}
		c.readBuf = data
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

// Write sends p as a single binary ws message (raw pty input).
func (c *Conn) Write(p []byte) (int, error) {
	if err := c.ws.Write(c.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close cancels in-flight reads/writes and closes the WebSocket.
func (c *Conn) Close() error {
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// wssURL normalizes an endpoint to a WebSocket URL. A bare host becomes wss://;
// http/https are mapped to ws/wss (used by tests); ws/wss pass through.
func wssURL(endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "ws://"), strings.HasPrefix(endpoint, "wss://"):
		return endpoint
	case strings.HasPrefix(endpoint, "https://"):
		return "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		return "ws://" + strings.TrimPrefix(endpoint, "http://")
	default:
		return "wss://" + endpoint
	}
}
