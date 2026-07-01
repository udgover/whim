package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The escape reader is the client-side disconnect for `whim shell`: the remote
// shell's WebSocket never closes on `exit`, so a reserved
// local key (Ctrl-], telnet-style) ends the session. In raw mode every other
// key — including Ctrl-C — passes straight through to the remote pty.

func TestEscapeReader_PassesThroughWithoutEscape(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	tripped := 0
	er := newEscapeReader(src, func() { tripped++ })

	// One Read returns all bytes; bytes.Reader reports EOF only on the NEXT call,
	// so with no escape byte and no EOF yet there must be no disconnect.
	buf := make([]byte, 11)
	n, err := er.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(buf[:n]))
	assert.Equal(t, 0, tripped, "no escape and not yet EOF: bytes pass through without a disconnect")
}

func TestEscapeReader_TripsAndForwardsPrefix(t *testing.T) {
	// "abc" then Ctrl-] then "def": only "abc" is forwarded; the escape byte and
	// everything after it are dropped, and the stream ends (EOF).
	src := bytes.NewReader([]byte{'a', 'b', 'c', shellEscapeByte, 'd', 'e', 'f'})
	tripped := 0
	er := newEscapeReader(src, func() { tripped++ })

	got, err := io.ReadAll(er)
	require.NoError(t, err)
	assert.Equal(t, "abc", string(got), "bytes before the escape are forwarded; escape and after are dropped")
	assert.Equal(t, 1, tripped, "onEscape fires exactly once")
}

func TestEscapeReader_EscapeAtStart(t *testing.T) {
	src := bytes.NewReader([]byte{shellEscapeByte, 'x', 'y'})
	tripped := 0
	er := newEscapeReader(src, func() { tripped++ })

	got, err := io.ReadAll(er)
	require.NoError(t, err)
	assert.Empty(t, got, "an immediate escape forwards nothing")
	assert.Equal(t, 1, tripped)
}

func TestEscapeReader_TripsAcrossByteByByteReads(t *testing.T) {
	// Force one-byte reads (like a raw terminal delivering keystrokes) to prove
	// the trip persists across Read calls rather than relying on one buffer.
	src := iotest.OneByteReader(bytes.NewReader([]byte{'a', shellEscapeByte, 'b'}))
	tripped := 0
	er := newEscapeReader(src, func() { tripped++ })

	got, err := io.ReadAll(er)
	require.NoError(t, err)
	assert.Equal(t, "a", string(got))
	assert.Equal(t, 1, tripped, "onEscape fires once even when the escape arrives in its own Read")
}

func TestEscapeReader_EOFTriggersDisconnect(t *testing.T) {
	// A piped/redirected stdin that EOFs (no escape byte) must still end the
	// session — the remote ws never closes on its own, so EOF is the disconnect.
	src := bytes.NewReader([]byte("hello"))
	tripped := 0
	er := newEscapeReader(src, func() { tripped++ })

	got, err := io.ReadAll(er)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got), "all input before EOF is forwarded")
	assert.Equal(t, 1, tripped, "underlying EOF triggers disconnect exactly once")
}

func TestClampUint16(t *testing.T) {
	for _, c := range []struct {
		in   int
		want uint16
	}{
		{-5, 0}, {0, 0}, {24, 24}, {65535, 65535}, {65536, 65535}, {1 << 20, 65535},
	} {
		assert.Equalf(t, c.want, clampUint16(c.in), "clampUint16(%d)", c.in)
	}
}

func newTestShellCmd() *cobra.Command {
	c := &cobra.Command{Use: "shell"}
	addShellFlags(c)
	return c
}

// Image resolution precedence: explicit --image (ARN) > cached default > error.
// The bare-name branch needs STS and is exercised live, not here.

func TestResolveShellImageARN_ExplicitARNWins(t *testing.T) {
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:custom"
	cmd := newTestShellCmd()
	require.NoError(t, cmd.Flags().Set("image", arn))

	got, err := resolveShellImageARN(context.Background(), cmd, aws.Config{})
	require.NoError(t, err)
	assert.Equal(t, arn, got, "an explicit --image ARN is used verbatim, no AWS call")
}

func TestResolveShellImageARN_FallsBackToCachedDefault(t *testing.T) {
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default"
	t.Setenv("WHIM_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveConfig(&Config{ImageARN: arn}))

	got, err := resolveShellImageARN(context.Background(), newTestShellCmd(), aws.Config{})
	require.NoError(t, err)
	assert.Equal(t, arn, got, "with no --image, the cached default from `whim init` is used")
}

func TestResolveShellImageARN_NoDefaultErrors(t *testing.T) {
	t.Setenv("WHIM_CONFIG_DIR", t.TempDir()) // empty dir → no cached image

	_, err := resolveShellImageARN(context.Background(), newTestShellCmd(), aws.Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whim init", "the error must point the user at `whim init`")
}

func TestShellFlags_PrivilegedPresent(t *testing.T) {
	for _, c := range []*cobra.Command{shellCmd, runCmd, rootCmd} {
		assert.NotNilf(t, c.Flags().Lookup("privileged"),
			"%q must define --privileged", c.Name())
	}
}

func TestResolveShellImageARN_PrivilegedAndImage_Conflict(t *testing.T) {
	cmd := newTestShellCmd()
	require.NoError(t, cmd.Flags().Set("image", "arn:aws:lambda:us-east-1:123456789012:microvm-image:x"))
	require.NoError(t, cmd.Flags().Set("privileged", "true"))

	_, err := resolveShellImageARN(context.Background(), cmd, aws.Config{})
	require.Error(t, err, "--privileged with an explicit --image must be rejected before any AWS call")
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// Bare `whim` (no subcommand) must drop into a shell, so root needs a RunE and a
// registered `shell` subcommand alias.
func TestRootWiresShell(t *testing.T) {
	require.NotNil(t, rootCmd.RunE, "bare `whim` must run the shell")
	var hasShell bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "shell" {
			hasShell = true
		}
	}
	assert.True(t, hasShell, "`whim shell` subcommand must be registered")
}
