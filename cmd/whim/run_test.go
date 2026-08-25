package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

func TestExitCodeError_CarriesCode(t *testing.T) {
	err := error(exitCodeError{7})
	var ece exitCodeError
	require.True(t, errors.As(err, &ece))
	assert.Equal(t, 7, ece.code)
	assert.Contains(t, err.Error(), "7")
}

func TestWhimFail_ReturnsReservedCode(t *testing.T) {
	cmd := &cobra.Command{}
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	err := whimFail(cmd, "launch", errors.New("boom"))

	var ece exitCodeError
	require.True(t, errors.As(err, &ece))
	assert.GreaterOrEqual(t, ece.code, 125, "whim failures must not collide with remote 0-124 exit codes")
	assert.Contains(t, errBuf.String(), "boom", "the underlying error is shown to the user")
}

func TestRunAndExecCommandsRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	require.Contains(t, cmds, "run")
	require.Contains(t, cmds, "exec")
	assert.NotNil(t, cmds["run"].RunE)
	assert.NotNil(t, cmds["exec"].RunE)
}

// newTestRunCmd builds a throwaway *cobra.Command with the same flags as the
// real runCmd, so tests can Set() flags (mutating Changed() state) without
// leaking that state into other tests via the package-level runCmd var.
func newTestRunCmd() *cobra.Command {
	c := &cobra.Command{Use: "run"}
	addShellFlags(c)
	addDetachFlags(c)
	return c
}

func TestRunCmd_DetachFlagsPresent(t *testing.T) {
	for _, name := range []string{"detach", "idle", "suspend-after", "auto-resume", "no-auto-resume"} {
		assert.NotNilf(t, runCmd.Flags().Lookup(name), "runCmd must define --%s", name)
	}
	assert.NotNilf(t, runCmd.Flags().ShorthandLookup("d"), "-d must be the shorthand for --detach")
}

func TestResolveIdlePolicy_NilWhenNeitherFlagSet(t *testing.T) {
	cmd := newTestRunCmd()
	got, err := resolveIdlePolicy(cmd)
	require.NoError(t, err)
	assert.Nil(t, got, "no --idle/--suspend-after set → no idle policy at all")
}

func TestResolveIdlePolicy_SetFromFlags(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "5m"))
	require.NoError(t, cmd.Flags().Set("suspend-after", "1h"))

	got, err := resolveIdlePolicy(cmd)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int32(5*60), got.MaxIdleDurationSeconds)
	assert.Equal(t, int32(3600), got.SuspendedDurationSeconds)
	assert.True(t, got.AutoResumeEnabled, "auto-resume defaults to true")
}

func TestResolveIdlePolicy_IdleAloneKeepsSuspendAfterDefault(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "10m"))

	got, err := resolveIdlePolicy(cmd)
	require.NoError(t, err)
	require.NotNil(t, got, "setting --idle alone must still produce a policy")
	assert.Equal(t, int32(10*60), got.MaxIdleDurationSeconds)
	assert.Equal(t, int32(defaultDetachedSuspendAfter/time.Second), got.SuspendedDurationSeconds,
		"an unset --suspend-after keeps its own flag default")
}

func TestResolveIdlePolicy_AutoResumeCanBeDisabled(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "5m"))
	require.NoError(t, cmd.Flags().Set("auto-resume", "false"))

	got, err := resolveIdlePolicy(cmd)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.AutoResumeEnabled)
}

func TestResolveIdlePolicy_NoAutoResumeOverridesAutoResume(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "5m"))
	require.NoError(t, cmd.Flags().Set("no-auto-resume", "true"))

	got, err := resolveIdlePolicy(cmd)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.AutoResumeEnabled, "--no-auto-resume must flip auto-resume off regardless of --auto-resume's default")
}

func TestResolveIdlePolicy_RejectsZero(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "0s"))

	_, err := resolveIdlePolicy(cmd)
	require.Error(t, err, "--idle 0s is meaningless (never runs) and must be rejected, not sent to AWS")
	assert.Contains(t, err.Error(), "--idle")
}

func TestResolveIdlePolicy_RejectsExcessivelyLarge(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("suspend-after", "999999h"))

	_, err := resolveIdlePolicy(cmd)
	require.Error(t, err, "an absurd duration must fail fast instead of silently wrapping int32 before reaching AWS")
	assert.Contains(t, err.Error(), "--suspend-after")
}

func TestResolveIdlePolicy_RejectsWithoutDetach(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("idle", "5m")) // no --detach

	_, err := resolveIdlePolicy(cmd)
	require.Error(t, err, "idle policy only makes sense on a persistent (-d) box; an ephemeral run/shell terminates before any idle timer could fire")
	assert.Contains(t, err.Error(), "-d")
}

func TestResolveRunTTL_DetachDefaultsHigh(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	assert.Equal(t, defaultDetachedTTL, resolveRunTTL(cmd))
}

func TestResolveRunTTL_ExplicitTTLWinsEvenDetached(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("ttl", "3h"))
	assert.Equal(t, 3*time.Hour, resolveRunTTL(cmd))
}

func TestResolveRunTTL_NonDetachedUnchanged(t *testing.T) {
	cmd := newTestRunCmd()
	assert.Equal(t, defaultShellTTL, resolveRunTTL(cmd), "ephemeral run/shell keep the short TTL backstop")
}

func TestRunArgs_DetachAloneOK(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	assert.NoError(t, runArgs(cmd, nil))
}

func TestRunArgs_DetachWithCommandErrors(t *testing.T) {
	cmd := newTestRunCmd()
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	err := runArgs(cmd, []string{"echo", "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-d")
}

func TestRunArgs_NoDetachRequiresCommand(t *testing.T) {
	cmd := newTestRunCmd()
	assert.Error(t, runArgs(cmd, nil), "run without -d must still require a command")
}

func TestRunArgs_NoDetachWithCommandOK(t *testing.T) {
	cmd := newTestRunCmd()
	assert.NoError(t, runArgs(cmd, []string{"echo", "hi"}))
}

// runnableTestRunCmd builds a runCmd-shaped command that can actually execute
// runRun end to end (unlike newTestRunCmd, which only needs flags for the pure
// helpers above): it adds --region/--profile, so buildAWSConfig resolves a
// fixed region instead of probing the ambient environment.
func runnableTestRunCmd() *cobra.Command {
	c := newTestRunCmd()
	c.Flags().String("region", "us-east-1", "")
	c.Flags().String("profile", "", "")
	c.SetContext(context.Background())
	return c
}

// TestRunDetach_RecordsRunMicrovmAndNeverTerminates exercises the actual
// runRun code path via the newManager seam (not resolveIdlePolicy in
// isolation): -d must call RunMicrovm exactly once, must NEVER call
// TerminateMicrovm (detach's entire purpose), must map the idle-policy flags
// onto the real RunMicrovmInput, and must print just the bare id.
func TestRunDetach_RecordsRunMicrovmAndNeverTerminates(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-detached", Endpoint: "example.invalid", State: "RUNNING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{
			MicrovmID: "mvm-detached", Endpoint: "example.invalid", State: "RUNNING",
			EgressNetworkConnectors: []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"},
		}, nil
	}
	mock.GetMicrovmImageFn = func(_ context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: in.ImageIdentifier, State: "CREATED", LatestActiveImageVersion: "1"}, nil
	}
	mock.GetMicrovmImageVersionFn = func(_ context.Context, _ *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
		return &awsapi.GetMicrovmImageVersionOutput{
			EgressConnectors: []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"},
		}, nil
	}

	orig := newManager
	defer func() { newManager = orig }()
	newManager = func(cfg aws.Config, opts ...microvm.Option) *microvm.Manager {
		opts = append(opts, microvm.WithRegion(cfg.Region))
		return microvm.NewWithAPI(mock, opts...)
	}

	cmd := runnableTestRunCmd()
	require.NoError(t, cmd.Flags().Set("image", "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default"))
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	require.NoError(t, cmd.Flags().Set("idle", "5m"))
	require.NoError(t, cmd.Flags().Set("suspend-after", "1h"))
	require.NoError(t, cmd.Flags().Set("no-auto-resume", "true"))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runRun(cmd, nil)
	require.NoError(t, err)

	require.Len(t, mock.RunMicrovmCalls, 1, "run -d must launch exactly once")
	assert.Empty(t, mock.TerminateMicrovmCalls, "run -d must never terminate — that's the entire point of detach")

	idle := mock.RunMicrovmCalls[0].IdlePolicy
	require.NotNil(t, idle, "idle-policy flags must reach the real RunMicrovmInput, not just the resolveIdlePolicy helper")
	assert.Equal(t, int32(5*60), idle.MaxIdleDurationSeconds)
	assert.Equal(t, int32(3600), idle.SuspendedDurationSeconds)
	assert.False(t, idle.AutoResumeEnabled, "--no-auto-resume must flip AutoResumeEnabled off end to end")

	assert.Equal(t, "mvm-detached\n", out.String(), "run -d prints just the bare id, no timestamp, so it's scriptable")
}

func TestRunDetach_CachedEgressNoneValidatesAndOverridesPublicBuildConnector(t *testing.T) {
	t.Setenv("WHIM_CONFIG_DIR", t.TempDir())
	const connector = "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	cfgFile := &Config{}
	cfgFile.SetImage("airgap", "arn:aws:lambda:us-east-1:123456789012:microvm-image:airgap")
	cfgFile.SetEgress("airgap", "none")
	cfgFile.SetEgressConnector("airgap", connector)
	cfgFile.SetEgressResourceGroup("airgap", EgressResourceGroup{
		VPCID: "vpc-custom", SubnetIDs: []string{"subnet-a"}, RouteTableIDs: []string{"rtb-custom"},
		SecurityGroupIDs: []string{"sg-a"}, ConnectorARN: connector,
	})
	require.NoError(t, SaveConfig(cfgFile))

	mock := &awsapi.Mock{}
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-airgap", Endpoint: "example.invalid", State: "RUNNING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{
			MicrovmID: "mvm-airgap", Endpoint: "example.invalid", State: "RUNNING",
			EgressNetworkConnectors: []string{connector},
		}, nil
	}
	mock.GetMicrovmImageFn = func(_ context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: in.ImageIdentifier, State: "CREATED", LatestActiveImageVersion: "1"}, nil
	}
	mock.GetMicrovmImageVersionFn = func(context.Context, *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
		return &awsapi.GetMicrovmImageVersionOutput{
			EgressConnectors: []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"},
		}, nil
	}
	mock.GetNetworkConnectorFn = func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		return &awsapi.GetNetworkConnectorOutput{
			ARN: connector, Name: "whim-no-egress", State: awsapi.NetworkConnectorStateActive,
			SubnetIDs: []string{"subnet-a"}, SecurityGroupIDs: []string{"sg-a"},
		}, nil
	}
	configureCallerManagedTopology(mock, "subnet-a", "sg-a")

	orig := newManager
	defer func() { newManager = orig }()
	newManager = func(cfg aws.Config, opts ...microvm.Option) *microvm.Manager {
		opts = append(opts, microvm.WithRegion(cfg.Region))
		return microvm.NewWithAPI(mock, opts...)
	}

	cmd := runnableTestRunCmd()
	require.NoError(t, cmd.Flags().Set("image", "airgap"))
	require.NoError(t, cmd.Flags().Set("detach", "true"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := runRun(cmd, nil)
	require.NoError(t, err)
	require.Len(t, mock.RunMicrovmCalls, 1)
	assert.Equal(t, []string{connector}, mock.RunMicrovmCalls[0].EgressNetworkConnectors,
		"cached --egress none must launch with the recorded connector")
	assert.Empty(t, mock.GetMicrovmImageVersionCalls,
		"the explicit runtime policy must not inherit the image's public build connector")
}

func TestExecCmd_InteractiveFlagsPresent(t *testing.T) {
	assert.NotNil(t, execCmd.Flags().Lookup("interactive"), "execCmd must define --interactive")
	assert.NotNil(t, execCmd.Flags().ShorthandLookup("i"), "-i must be the shorthand for --interactive")
	assert.NotNil(t, execCmd.Flags().Lookup("tty"), "execCmd must define --tty")
	assert.NotNil(t, execCmd.Flags().ShorthandLookup("t"), "-t must be the shorthand for --tty")
}

// newTestExecCmd mirrors newTestRunCmd: a throwaway command with execCmd's
// flags, so tests can Set() them without leaking Changed() state.
func newTestExecCmd() *cobra.Command {
	c := &cobra.Command{Use: "exec"}
	addExecFlags(c)
	return c
}

func TestExecArgs_InteractiveRequiresExactlyID(t *testing.T) {
	cmd := newTestExecCmd()
	require.NoError(t, cmd.Flags().Set("tty", "true"))
	assert.NoError(t, execArgs(cmd, []string{"mvm-1"}), "-it plus just the id must be accepted")

	err := execArgs(cmd, []string{"mvm-1", "sh"})
	require.Error(t, err, "-it takes no trailing command — the shell endpoint can't be pointed at one")
	assert.Contains(t, err.Error(), "-it")
}

func TestExecArgs_NonInteractiveUnchanged(t *testing.T) {
	cmd := newTestExecCmd()
	assert.Error(t, execArgs(cmd, []string{"mvm-1"}), "without -it, a command is still required")
	assert.NoError(t, execArgs(cmd, []string{"mvm-1", "echo", "hi"}))
}

// runnableExecCmd mirrors runnableTestRunCmd: adds --region/--profile so
// buildAWSConfig resolves deterministically instead of probing the environment.
func runnableExecCmd() *cobra.Command {
	c := newTestExecCmd()
	c.Flags().String("region", "us-east-1", "")
	c.Flags().String("profile", "", "")
	c.SetContext(context.Background())
	return c
}

// TestRunExec_Interactive_CallsShellAndNeverTerminates proves exec -it's
// routing without driving the real terminal-facing shell loop (which
// runInteractiveShell's own doc comment says is validated live, not in unit
// tests): it stubs runInteractiveShellFn to record the sandbox it was handed,
// and asserts TerminateMicrovm is never called — interactive or not, exec
// never owns the VM's lifecycle.
func TestRunExec_Interactive_CallsShellAndNeverTerminates(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, Endpoint: "example.invalid", State: "RUNNING"}, nil
	}

	origMgr := newManager
	defer func() { newManager = origMgr }()
	newManager = func(_ aws.Config, opts ...microvm.Option) *microvm.Manager {
		return microvm.NewWithAPI(mock, opts...)
	}

	var gotSandboxID string
	origShell := runInteractiveShellFn
	defer func() { runInteractiveShellFn = origShell }()
	runInteractiveShellFn = func(_ context.Context, _ *cobra.Command, sb *microvm.Sandbox) error {
		gotSandboxID = sb.ID()
		return nil
	}

	cmd := runnableExecCmd()
	require.NoError(t, cmd.Flags().Set("tty", "true"))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runExec(cmd, []string{"mvm-it"})
	require.NoError(t, err)
	assert.Equal(t, "mvm-it", gotSandboxID, "the interactive path must Attach and hand the resulting sandbox to the shell runner")
	assert.Empty(t, mock.TerminateMicrovmCalls, "exec never owns VM lifecycle, interactive or not")
}
