//go:build integration

// Package microvm_test integration suite — real AWS, opt-in.
// Run with: WHIM_INTEGRATION=1 go test -tags=integration ./microvm/ -v
package microvm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

// integrationManager builds a Manager from the ambient AWS config (skips the
// test unless WHIM_INTEGRATION=1). Uses a short poll interval for fast launch.
func integrationManager(t *testing.T, ctx context.Context) (*microvm.Manager, string) {
	t.Helper()
	if os.Getenv("WHIM_INTEGRATION") != "1" {
		t.Skip("set WHIM_INTEGRATION=1 to run real-AWS integration tests")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Region)
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	require.NoError(t, err)
	mgr := microvm.NewFromConfig(cfg,
		microvm.WithAccountID(aws.ToString(ident.Account)),
		microvm.WithPollInterval(500*time.Millisecond),
	)
	image := os.Getenv("WHIM_TEST_IMAGE")
	if image == "" {
		image = "whim-default"
	}
	return mgr, mgr.ImageARN(image)
}

// TestIntegration_ShellEcho launches a real VM, attaches a scripted shell that
// runs `echo MARKER_$((6*7)); exit`, and asserts the *computed* output MARKER_42
// comes back — proving the shell actually executed (not just echoed input).
func TestIntegration_ShellEcho(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	// Two shell-session behaviors the interactive client must tolerate:
	//   1. the SHELL_INGRESS ws does NOT close when the remote shell exits;
	//   2. the shell has a multi-second readiness delay after connect.
	// So we read until MARKER_42 appears (cancel on match) under a generous
	// safety deadline. MARKER_42 is computed (6*7) — proving execution, not echo.
	shellCtx, shellCancel := context.WithTimeout(ctx, 30*time.Second)
	defer shellCancel()
	out := &cancelOnMarker{marker: "MARKER_42", cancel: shellCancel}
	_ = sb.Shell(shellCtx, microvm.ShellIO{
		In:  strings.NewReader("echo MARKER_$((6*7))\n"),
		Out: out,
	})
	t.Logf("shell output: %q", out.buf.String())
	assert.Contains(t, out.buf.String(), "MARKER_42", "shell must execute the command (computed 6*7=42), not just echo it")
}

// cancelOnMarker is an io.Writer that cancels its context once the accumulated
// output contains marker — so an integration read ends promptly on success
// rather than waiting out the full deadline (the ws never closes on its own).
type cancelOnMarker struct {
	buf    bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (w *cancelOnMarker) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.cancel != nil && strings.Contains(w.buf.String(), w.marker) {
		w.cancel()
		w.cancel = nil
	}
	return n, err
}

// TestIntegration_PutGet launches a real VM, uploads a tree (text + binary +
// subdir + exec bit), downloads it back, and asserts a byte-exact round-trip.
func TestIntegration_PutGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(10*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	// Build a local payload directory.
	src := filepath.Join(t.TempDir(), "payload")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello whim\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755))
	bin := make([]byte, 200*1024) // 200 KiB binary → multi-chunk upload
	for i := range bin {
		bin[i] = byte(i * 7)
	}
	require.NoError(t, os.WriteFile(filepath.Join(src, "data.bin"), bin, 0o644))

	require.NoError(t, sb.Put(ctx, src, "/tmp/whim-it"), "Put")
	back := t.TempDir()
	require.NoError(t, sb.Get(ctx, "/tmp/whim-it/payload", back), "Get")

	want := treeDigest(t, src)
	got := treeDigest(t, filepath.Join(back, "payload"))
	assert.Equal(t, want, got, "round-trip must be byte-exact (content + mode)")
	t.Logf("round-trip ok: %d files match", len(want))
}

// treeDigest maps each file's relative path to "mode:sha256" for tree comparison.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p) //nolint:gosec // test paths
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = fmt.Sprintf("%o:%x", fi.Mode().Perm(), sum)
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestIntegration_Exec launches a real VM and exercises Sandbox.Exec end-to-end
// over the pty+sentinel engine: basic output, exit-code propagation, and a
// computed result (proving real execution). Each Exec is bounded by its own
// timeout so a dropped-input hang fails fast rather than waiting out the test.
func TestIntegration_Exec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	exec := func(argv ...string) *microvm.ExecResult {
		ec, ecancel := context.WithTimeout(ctx, 60*time.Second)
		defer ecancel()
		start := time.Now()
		res, err := sb.Exec(ec, argv)
		require.NoError(t, err, "Exec %v", argv)
		t.Logf("exec %v -> exit=%d (%s) output=%q", argv, res.ExitCode, time.Since(start).Round(time.Millisecond), res.Output)
		return res
	}

	// Basic output.
	r := exec("echo", "exec-works")
	assert.Equal(t, 0, r.ExitCode)
	assert.Contains(t, string(r.Output), "exec-works")

	// Exit-code propagation.
	r = exec("sh", "-c", "exit 7")
	assert.Equal(t, 7, r.ExitCode, "remote exit code must propagate")

	// Computed output proves real execution (not echo).
	r = exec("sh", "-c", "echo $((6*7))")
	assert.Equal(t, "42", strings.TrimSpace(string(r.Output)))

	// stderr is merged into Output (pty), and a non-existent command is non-zero.
	r = exec("sh", "-c", "echo to-stderr 1>&2; exit 3")
	assert.Equal(t, 3, r.ExitCode)
	assert.Contains(t, string(r.Output), "to-stderr", "stderr is merged into combined output")
}

// TestIntegration_SuspendResume launches a real VM, writes a marker to disk,
// suspends and resumes it, and verifies the marker survived (state intact).
func TestIntegration_SuspendResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(10*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	const marker = "SUSPEND_STATE_42"
	_, err = sb.Exec(ctx, []string{"sh", "-c", "echo " + marker + " > /tmp/marker"})
	require.NoError(t, err, "write marker")

	require.NoError(t, sb.Suspend(ctx), "Suspend")
	t.Log("suspended")
	require.NoError(t, sb.Resume(ctx), "Resume")
	t.Log("resumed")

	res, err := sb.Exec(ctx, []string{"cat", "/tmp/marker"})
	require.NoError(t, err, "read marker after resume")
	assert.Equal(t, marker, strings.TrimSpace(string(res.Output)), "disk state must survive suspend/resume")
	assert.Equal(t, 0, res.ExitCode)
}

// TestIntegration_LaunchAndTerminate launches a real MicroVM from the whim
// default image, waits for RUNNING, then terminates it. Validates launch→terminate end-to-end.
func TestIntegration_LaunchAndTerminate(t *testing.T) {
	if os.Getenv("WHIM_INTEGRATION") != "1" {
		t.Skip("set WHIM_INTEGRATION=1 to run real-AWS integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Region, "a region must be configured")

	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	require.NoError(t, err)

	mgr := microvm.NewFromConfig(cfg, microvm.WithAccountID(aws.ToString(ident.Account)))

	imageName := os.Getenv("WHIM_TEST_IMAGE")
	if imageName == "" {
		imageName = "whim-default"
	}
	arn := mgr.ImageARN(imageName)
	t.Logf("launching from image %s", arn)

	start := time.Now()
	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err, "Launch should reach RUNNING")
	t.Logf("RUNNING after %s: id=%s endpoint=%s", time.Since(start).Round(time.Millisecond), sb.ID(), sb.Endpoint())

	// Guarantee cleanup even if an assertion below fails.
	terminated := false
	defer func() {
		if !terminated {
			_ = sb.Terminate(context.Background())
		}
	}()

	require.NotEmpty(t, sb.ID())
	require.NotEmpty(t, sb.Endpoint())

	require.NoError(t, sb.Terminate(ctx), "Terminate should succeed")
	terminated = true
	t.Logf("terminated %s", sb.ID())
}
