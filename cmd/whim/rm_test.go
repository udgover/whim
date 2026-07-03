package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

func TestRmCmdRegistered(t *testing.T) {
	cmds := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		cmds[c.Name()] = c
	}
	require.Contains(t, cmds, "rm")
	assert.NotNil(t, cmds["rm"].RunE)
}

// runnableRmCmd mirrors runnableTestRunCmd/runnableExecCmd: a fresh command
// with --region/--profile so buildAWSConfig resolves deterministically.
func runnableRmCmd() *cobra.Command {
	c := &cobra.Command{Use: "rm"}
	c.Flags().String("region", "us-east-1", "")
	c.Flags().String("profile", "", "")
	c.SetContext(context.Background())
	return c
}

func TestRunRm_TerminatesEachID(t *testing.T) {
	mock := &awsapi.Mock{}
	orig := newManager
	defer func() { newManager = orig }()
	newManager = func(_ aws.Config, opts ...microvm.Option) *microvm.Manager {
		return microvm.NewWithAPI(mock, opts...)
	}

	cmd := runnableRmCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runRm(cmd, []string{"mvm-a", "mvm-b"})
	require.NoError(t, err)

	var terminated []string
	for _, c := range mock.TerminateMicrovmCalls {
		terminated = append(terminated, c.MicrovmIdentifier)
	}
	assert.Equal(t, []string{"mvm-a", "mvm-b"}, terminated, "rm must terminate every named id, regardless of ownership")
	assert.Equal(t, "mvm-a\nmvm-b\n", out.String(), "each terminated id is printed bare, so it's scriptable like run -d")
}

func TestRunRm_IdempotentOnAlreadyGone(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.TerminateMicrovmFn = func(_ context.Context, _ *awsapi.TerminateMicrovmInput) error {
		return awsapi.ErrNotFound
	}
	orig := newManager
	defer func() { newManager = orig }()
	newManager = func(_ aws.Config, opts ...microvm.Option) *microvm.Manager {
		return microvm.NewWithAPI(mock, opts...)
	}

	cmd := runnableRmCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runRm(cmd, []string{"mvm-gone"})
	require.NoError(t, err, "terminating an already-gone id must be a success, not an error")
	assert.Equal(t, "mvm-gone\n", out.String())
}

func TestRunRm_ContinuesPastFailureAndReportsWhimExitCode(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.TerminateMicrovmFn = func(_ context.Context, in *awsapi.TerminateMicrovmInput) error {
		if in.MicrovmIdentifier == "mvm-bad" {
			return errors.New("access denied")
		}
		return nil
	}
	orig := newManager
	defer func() { newManager = orig }()
	newManager = func(_ aws.Config, opts ...microvm.Option) *microvm.Manager {
		return microvm.NewWithAPI(mock, opts...)
	}

	cmd := runnableRmCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runRm(cmd, []string{"mvm-good-1", "mvm-bad", "mvm-good-2"})
	var ece exitCodeError
	require.ErrorAs(t, err, &ece, "a failure on one id must be reported, not swallowed")
	assert.Equal(t, whimExitCode, ece.code)

	var terminated []string
	for _, c := range mock.TerminateMicrovmCalls {
		terminated = append(terminated, c.MicrovmIdentifier)
	}
	assert.Equal(t, []string{"mvm-good-1", "mvm-bad", "mvm-good-2"}, terminated,
		"a failure on one id must not stop the rest from being attempted")
	assert.Contains(t, out.String(), "mvm-good-1")
	assert.Contains(t, out.String(), "mvm-good-2")
	assert.NotContains(t, out.String(), "mvm-bad\n", "the failed id is not reported as a success")
	assert.Contains(t, errOut.String(), "access denied")
}
