//go:build integration

// Package microvm_test integration suite — real AWS, opt-in.
// Run with: WHIM_INTEGRATION=1 go test -tags=integration ./microvm/ -v
package microvm_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/internal/awsapi/sdkclient"
	"github.com/udgover/whim/internal/wsconn"
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

// TestIntegration_ConcurrentAttachIndependentPtys is the permanent regression
// test for A2 (SPEC-persistent-boxes.md §4/§6): two concurrent Exec calls
// against the same MicroVM must land on independent ptys, not a shared or
// serialized session. First observed live at CHECKPOINT 2 (2026-07-02) via the
// CLI (`whim exec` x2); this exercises the same guarantee at the library level
// so it's caught automatically, not just re-verified by hand.
func TestIntegration_ConcurrentAttachIndependentPtys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	run := func() (*microvm.ExecResult, error) {
		ec, ecancel := context.WithTimeout(ctx, 60*time.Second)
		defer ecancel()
		return sb.Exec(ec, []string{"sh", "-c", "tty; id"})
	}

	var r1, r2 *microvm.ExecResult
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r1, e1 = run() }()
	go func() { defer wg.Done(); r2, e2 = run() }()
	wg.Wait()

	require.NoError(t, e1)
	require.NoError(t, e2)
	t.Logf("session1: %q", string(r1.Output))
	t.Logf("session2: %q", string(r2.Output))
	assert.Contains(t, string(r1.Output), "uid=0(root)")
	assert.Contains(t, string(r2.Output), "uid=0(root)")

	tty1 := strings.SplitN(string(r1.Output), "\n", 2)[0]
	tty2 := strings.SplitN(string(r2.Output), "\n", 2)[0]
	assert.NotEqual(t, tty1, tty2, "concurrent connections must get independent ptys, not a shared/serialized session")
}

// TestIntegration_IdleSuspendFreezesBackgroundJob is the permanent regression
// test for A3 (SPEC-persistent-boxes.md §4/§6): a detached box with a running
// background job must auto-suspend after its idle window elapses with no
// endpoint traffic, and the job must actually freeze — not just the VM's
// reported state flipping — proven by a gap in a heartbeat log spanning the
// suspension.
func TestIntegration_IdleSuspendFreezesBackgroundJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	const idleSeconds = 60
	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(10*time.Minute), microvm.WithIdlePolicy(microvm.IdlePolicy{
		AutoResumeEnabled:        true,
		MaxIdleDurationSeconds:   idleSeconds,
		SuspendedDurationSeconds: 3600,
	}))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s idle=%ds", sb.ID(), idleSeconds)

	// nohup+disown detaches the heartbeat from this Exec's session so it
	// survives after the WS closes — a background job, not a foreground one.
	// The 300-iteration cap (600s) is well above this test's total budget, so
	// it never interferes with the line-count comparison below; it exists only
	// so a stuck test run doesn't leave an infinite loop alive on the VM.
	const heartbeatIntervalSeconds = 2
	start := time.Now()
	_, err = sb.Exec(ctx, []string{"sh", "-c",
		"nohup sh -c 'i=0; while [ $i -lt 300 ]; do date +%s >> /tmp/hb.log; i=$((i+1)); sleep 2; done' >/dev/null 2>&1 & disown; echo started"})
	require.NoError(t, err, "start background heartbeat")

	// Discovered live (2026-07-02): AWS's idle-to-actually-suspended latency
	// runs measurably past the raw idle threshold (observed ~15-25s beyond
	// idleSeconds before the guest actually froze), so a short buffer leaves
	// too little confirmed-frozen time for a reliable margin. +90s instead of
	// a tighter buffer gives a solid, non-flaky window of real suspension
	// before we check and resume.
	waitFor := idleSeconds + 90
	t.Logf("no further traffic sent; waiting %ds for auto-suspend...", waitFor)
	time.Sleep(time.Duration(waitFor) * time.Second)

	state := waitForState(t, ctx, mgr, sb.ID(), "SUSPENDED", 30*time.Second)
	require.Equal(t, "SUSPENDED", state, "box must auto-suspend after idle with no endpoint traffic")
	t.Log("confirmed SUSPENDED")

	// Attach resumes transparently (the persistent-boxes contract: any use wakes it).
	sb2, err := mgr.Attach(ctx, sb.ID())
	require.NoError(t, err, "Attach must resume a SUSPENDED VM")
	res, err := sb2.Exec(ctx, []string{"cat", "/tmp/hb.log"})
	require.NoError(t, err)
	hostElapsed := time.Since(start)

	// Also discovered live: the guest's own clock likely pauses during
	// suspension too, so consecutive `date +%s` timestamps recorded INSIDE the
	// VM show only a short gap even across a much longer real suspend —
	// gap-based measurement from guest timestamps is unreliable. Instead
	// compare the heartbeat's line COUNT against what continuous, unfrozen
	// execution would have produced over the HOST-measured wall-clock elapsed
	// time (start to now, measured by this test process, not the guest): a
	// job that actually froze during suspension logs far fewer lines than one
	// that ran the whole time.
	lines := len(strings.Fields(string(res.Output)))
	expectedIfNeverFrozen := int(hostElapsed.Seconds()) / heartbeatIntervalSeconds
	t.Logf("heartbeat: %d lines logged vs. %d expected if never frozen (host-elapsed %s)",
		lines, expectedIfNeverFrozen, hostElapsed.Round(time.Second))
	assert.Less(t, lines, expectedIfNeverFrozen*3/4,
		"the background job must have logged measurably fewer heartbeats than continuous execution would, proving it froze while suspended")
}

// TestIntegration_OpenShellPreventsIdleSuspend answers A2b
// (SPEC-persistent-boxes.md §4): does an open-but-silent shell WebSocket count
// as "traffic" for AWS's idle-suspend detection? Confirmed live (2026-07-02):
// YES — merely holding a shell connection open, even sending zero bytes, keeps
// the box awake; idle-suspend does not fire while a shell is attached. This
// was the opposite of the test's original hypothesis; corrected to match the
// observed behavior rather than the guess.
func TestIntegration_OpenShellPreventsIdleSuspend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	const idleSeconds = 60
	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(10*time.Minute), microvm.WithIdlePolicy(microvm.IdlePolicy{
		AutoResumeEnabled:        true,
		MaxIdleDurationSeconds:   idleSeconds,
		SuspendedDurationSeconds: 3600,
	}))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s idle=%ds", sb.ID(), idleSeconds)

	waitFor := idleSeconds + 30
	shellCtx, shellCancel := context.WithTimeout(ctx, time.Duration(waitFor+15)*time.Second)
	defer shellCancel()
	shellDone := make(chan error, 1)
	go func() {
		// Never writes; blocks until shellCtx ends. Holds the WS open with no
		// traffic, so any suspend (or lack thereof) we observe can't be
		// attributed to bytes we sent.
		shellDone <- sb.Shell(shellCtx, microvm.ShellIO{In: blockingReader{ctx: shellCtx}, Out: io.Discard})
	}()

	t.Logf("shell open, sending nothing; waiting %ds to see if idle-suspend fires anyway...", waitFor)
	time.Sleep(time.Duration(waitFor) * time.Second)
	// waitForState lets a transient SUSPENDING settle before we read the
	// answer, in case a future platform change makes this fire after all.
	state := waitForState(t, ctx, mgr, sb.ID(), "SUSPENDED", 30*time.Second)
	t.Logf("state after %ds with an open, silent shell: %s", waitFor, state)

	shellCancel()
	<-shellDone

	assert.Equal(t, "RUNNING", state,
		"confirmed live 2026-07-02: an open shell connection itself counts as keeping the box awake — idle-suspend does not fire while a shell is attached, even silent")
}

// blockingReader never yields data; Read blocks until ctx ends. Used to hold a
// Shell connection open without ever sending a byte.
type blockingReader struct{ ctx context.Context }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// TestIntegration_ResumeOnUseForExecPutGet is the permanent regression test
// for A4 (SPEC-persistent-boxes.md §4/§6): exec, put, and get all resume a
// SUSPENDED box transparently via the shared Attach path. (Interactive
// exec -it uses the identical Attach call, just followed by Shell instead of
// Exec, so it is covered by the same guarantee — see TestAttach_Suspended_*
// in sandbox_test.go for the mocked unit coverage of Attach itself.)
func TestIntegration_ResumeOnUseForExecPutGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(10*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	require.NoError(t, sb.Suspend(ctx))
	t.Log("suspended")
	require.Equal(t, "SUSPENDED", waitForState(t, ctx, mgr, sb.ID(), "SUSPENDED", 30*time.Second),
		"Suspend's effect briefly reads SUSPENDING before settling")

	// exec: Attach must resume it and the command must actually run.
	sbExec, err := mgr.Attach(ctx, sb.ID())
	require.NoError(t, err, "Attach (exec's path) must resume a SUSPENDED VM")
	res, err := sbExec.Exec(ctx, []string{"sh", "-c", "echo $((6*7))"})
	require.NoError(t, err)
	assert.Equal(t, "42", strings.TrimSpace(string(res.Output)))
	require.Equal(t, "RUNNING", microvmState(t, ctx, mgr, sb.ID()), "must be RUNNING again after resume-on-use")

	require.NoError(t, sb.Suspend(ctx))
	require.Equal(t, "SUSPENDED", waitForState(t, ctx, mgr, sb.ID(), "SUSPENDED", 30*time.Second),
		"wait for the SUSPENDING transient to settle before Attach — see the known-gap note below")
	t.Log("suspended again, for put")

	// put: Attach must resume it and the upload must land.
	//
	// NOTE (discovered live, 2026-07-02): Attach only special-cases "SUSPENDED";
	// if it observes the transient "SUSPENDING" state instead (a real race if
	// Attach is called immediately after Suspend, before AWS settles), it falls
	// into the default branch and returns ErrInvalidOption instead of waiting
	// and resuming. This test deliberately waits for a settled SUSPENDED first
	// so it exercises the intended behavior rather than that race. The gap
	// itself is tracked as an open question in SPEC-persistent-boxes.md, not
	// fixed here — fixing it needs its own live validation of what
	// ResumeMicrovm does when called during SUSPENDING, which is outside this
	// slice's scope (docs + integration suite).
	sbPut, err := mgr.Attach(ctx, sb.ID())
	require.NoError(t, err, "Attach (put's path) must resume a SUSPENDED VM")
	src := filepath.Join(t.TempDir(), "resume-put.txt")
	require.NoError(t, os.WriteFile(src, []byte("resume-on-use\n"), 0o644))
	require.NoError(t, sbPut.Put(ctx, src, "/tmp/whim-resume-put"))

	require.NoError(t, sb.Suspend(ctx))
	require.Equal(t, "SUSPENDED", waitForState(t, ctx, mgr, sb.ID(), "SUSPENDED", 30*time.Second))
	t.Log("suspended again, for get")

	// get: Attach must resume it and the download must return the uploaded content.
	sbGet, err := mgr.Attach(ctx, sb.ID())
	require.NoError(t, err, "Attach (get's path) must resume a SUSPENDED VM")
	back := t.TempDir()
	require.NoError(t, sbGet.Get(ctx, "/tmp/whim-resume-put/resume-put.txt", back))
	got, err := os.ReadFile(filepath.Join(back, "resume-put.txt"))
	require.NoError(t, err)
	assert.Equal(t, "resume-on-use\n", string(got))
}

// TestIntegration_A1Canary_ShellSurvivesTokenExpiry is an OPTIONAL, non-gating
// canary for A1 (SPEC-persistent-boxes.md §4). CHECKPOINT 2 (2026-07-02)
// confirmed live, twice, that an established shell WebSocket survives its
// minting token's expiry — this is a property of AWS's service behavior, not
// whim's code, so there is nothing here that can regress from a whim commit.
// It is kept as an occasional check that the platform behavior hasn't
// changed, gated behind its own env var so it doesn't slow down or gate a
// normal WHIM_INTEGRATION=1 run (it needs to sleep past a token's expiry).
//
// Mints its own short-lived token directly via the internal awsapi/sdkclient
// seam (bypassing Sandbox's hardcoded ShellTokenLifetime) and dials with
// internal/wsconn — both already the library's real transport code, not a
// reimplementation.
func TestIntegration_A1Canary_ShellSurvivesTokenExpiry(t *testing.T) {
	if os.Getenv("WHIM_TEST_A1_CANARY") != "1" {
		t.Skip("set WHIM_TEST_A1_CANARY=1 (in addition to WHIM_INTEGRATION=1) to run the optional A1 canary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr, arn := integrationManager(t, ctx)

	sb, err := mgr.Launch(ctx, arn, microvm.WithTTL(5*time.Minute))
	require.NoError(t, err)
	defer func() { _ = sb.Terminate(context.Background()) }()
	t.Logf("RUNNING: id=%s", sb.ID())

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	api := sdkclient.New(cfg)

	const shortExpiryMinutes = 1
	tok, err := api.CreateShellAuthToken(ctx, &awsapi.CreateShellAuthTokenInput{
		MicrovmIdentifier: sb.ID(),
		ExpirationMinutes: shortExpiryMinutes,
	})
	require.NoError(t, err)

	conn, err := wsconn.Dial(ctx, sb.Endpoint(), tok.HeaderKey, tok.HeaderValue)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	waitFor := shortExpiryMinutes*60 + 90 // well past expiry
	t.Logf("holding the connection idle %ds past a %d-minute token...", waitFor, shortExpiryMinutes)
	time.Sleep(time.Duration(waitFor) * time.Second)

	_, err = conn.Write([]byte("echo A1_CANARY_$((1+1))\r"))
	require.NoError(t, err, "write after token expiry must still succeed if the platform behavior holds")
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	require.NoError(t, err, "read after token expiry must still succeed if the platform behavior holds")
	t.Logf("survived token expiry, got: %q", string(buf[:n]))
}

// microvmState returns id's current state via List (SUSPENDED/RUNNING/etc.),
// or "" if not found (e.g. terminated). Used instead of Attach when the test
// needs to OBSERVE the state without triggering Attach's own resume side effect.
func microvmState(t *testing.T, ctx context.Context, mgr *microvm.Manager, id string) string {
	t.Helper()
	infos, err := mgr.List(ctx)
	require.NoError(t, err)
	for _, in := range infos {
		if in.ID == id {
			return in.State
		}
	}
	return ""
}

// waitForState polls id's state until it equals want or timeout elapses.
// Discovered live (2026-07-02): Suspend's effect is not immediate — the state
// reads SUSPENDING for a beat before settling to SUSPENDED — so a single
// microvmState check right after an action can flake. Returns the last
// observed state (which may not equal want if the deadline is hit).
func waitForState(t *testing.T, ctx context.Context, mgr *microvm.Manager, id, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		last = microvmState(t, ctx, mgr, id)
		if last == want || time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(2 * time.Second):
		}
	}
}
