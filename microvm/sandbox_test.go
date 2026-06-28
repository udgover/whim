package microvm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

const testImageARN = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default"

// runningMock returns a mock whose RunMicrovm succeeds and GetMicrovm reports
// RUNNING immediately, capturing the RunMicrovm input for assertions.
func runningMock(captured **awsapi.RunMicrovmInput) *awsapi.Mock {
	m := &awsapi.Mock{}
	m.RunMicrovmFn = func(_ context.Context, in *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		if captured != nil {
			*captured = in
		}
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-123", Endpoint: "mvm-123.lambda-microvm.us-east-1.on.aws", State: "PENDING"}, nil
	}
	m.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-123", Endpoint: "mvm-123.lambda-microvm.us-east-1.on.aws", State: "RUNNING"}, nil
	}
	return m
}

func TestLaunch_PassesTTLIngressEgress(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mock := runningMock(&in)
	mgr := newTestManager(mock) // region us-east-1, account 123456789012

	sb, err := mgr.Launch(context.Background(), testImageARN, microvm.WithTTL(30*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, sb)
	require.NotNil(t, in)

	assert.Equal(t, testImageARN, in.ImageIdentifier)
	// TTL set and within the 8h cap (28800s).
	require.NotNil(t, in.MaximumDurationInSeconds)
	assert.Equal(t, int32(1800), *in.MaximumDurationInSeconds)
	assert.LessOrEqual(t, *in.MaximumDurationInSeconds, int32(28800))
	// Ingress = region-derived SHELL_INGRESS.
	require.Len(t, in.IngressNetworkConnectors, 1)
	assert.Contains(t, in.IngressNetworkConnectors[0], "us-east-1")
	assert.Contains(t, in.IngressNetworkConnectors[0], "SHELL_INGRESS")
	// Default egress = public → INTERNET_EGRESS connector.
	require.Len(t, in.EgressNetworkConnectors, 1)
	assert.Contains(t, in.EgressNetworkConnectors[0], "INTERNET_EGRESS")
}

func TestLaunch_DefaultTTLApplied(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mgr := newTestManager(runningMock(&in))
	_, err := mgr.Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	require.NotNil(t, in.MaximumDurationInSeconds)
	assert.Equal(t, int32(25*60), *in.MaximumDurationInSeconds, "default TTL must be 25m")
}

func TestLaunch_TTLNeverExceeds8h(t *testing.T) {
	mgr := newTestManager(runningMock(nil))
	_, err := mgr.Launch(context.Background(), testImageARN, microvm.WithTTL(9*time.Hour))
	require.ErrorIs(t, err, microvm.ErrInvalidOption, "TTL over 8h must be rejected, never launched")
}

func TestLaunch_RequiresImageARN(t *testing.T) {
	mock := runningMock(nil)
	_, err := newTestManager(mock).Launch(context.Background(), "")
	require.Error(t, err)
	assert.Empty(t, mock.RunMicrovmCalls, "must not call RunMicrovm without an image ARN")
}

func TestLaunch_EgressNone_OmitsEgressConnector(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mgr := newTestManager(runningMock(&in))
	_, err := mgr.Launch(context.Background(), testImageARN, microvm.WithEgress(microvm.EgressNone))
	require.NoError(t, err)
	assert.Empty(t, in.EgressNetworkConnectors, "EgressNone must send no egress connector")
}

func TestLaunch_IngressOverride(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mgr := newTestManager(runningMock(&in))
	custom := "arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:ALL_INGRESS"
	_, err := mgr.Launch(context.Background(), testImageARN, microvm.WithIngress(custom))
	require.NoError(t, err)
	require.Len(t, in.IngressNetworkConnectors, 1)
	assert.Equal(t, custom, in.IngressNetworkConnectors[0])
}

func TestLaunch_PollsPendingToRunning(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	calls := 0
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		calls++
		if calls < 3 {
			return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
		}
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", Endpoint: "ep-x", State: "RUNNING"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	sb, err := mgr.Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	assert.Equal(t, "mvm-x", sb.ID())
	assert.Equal(t, "ep-x", sb.Endpoint())
}

func TestLaunch_TerminatedDuringProvisioning_Fails(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", State: "TERMINATED"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"), microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	_, err := mgr.Launch(context.Background(), testImageARN)
	require.ErrorIs(t, err, microvm.ErrVMProvisionFailed)
}

func TestLaunch_ContextCanceled_AbortsPoll(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil // never RUNNING
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"), microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	_, err := mgr.Launch(ctx, testImageARN)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

func TestAttach_RunningReturnsSandbox(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, Endpoint: "ep-attach", State: "RUNNING"}, nil
	}
	sb, err := newTestManager(mock).Attach(context.Background(), "mvm-existing")
	require.NoError(t, err)
	assert.Equal(t, "mvm-existing", sb.ID())
	assert.Equal(t, "ep-attach", sb.Endpoint())
}

func TestAttach_RequiresID(t *testing.T) {
	mock := &awsapi.Mock{}
	_, err := newTestManager(mock).Attach(context.Background(), "")
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.GetMicrovmCalls, "must validate before calling the API")
}

func TestAttach_NotFound_IsTerminated(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return nil, awsapi.ErrNotFound
	}
	_, err := newTestManager(mock).Attach(context.Background(), "gone")
	require.ErrorIs(t, err, microvm.ErrTerminated, "an absent VM reads as terminated")
}

func TestAttach_NotRunning_Fails(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-p", State: "PENDING"}, nil
	}
	_, err := newTestManager(mock).Attach(context.Background(), "mvm-p")
	require.Error(t, err, "a non-RUNNING VM cannot be attached")
}

func TestSandbox_Terminate_CallsAPI(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mock := runningMock(&in)
	mgr := newTestManager(mock)
	sb, err := mgr.Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	require.NoError(t, sb.Terminate(context.Background()))
	require.Len(t, mock.TerminateMicrovmCalls, 1)
	assert.Equal(t, sb.ID(), mock.TerminateMicrovmCalls[0].MicrovmIdentifier)
}

func TestSandbox_Suspend_CallsAPI(t *testing.T) {
	mock := runningMock(nil)
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	require.NoError(t, sb.Suspend(context.Background()))
	require.Len(t, mock.SuspendMicrovmCalls, 1)
	assert.Equal(t, sb.ID(), mock.SuspendMicrovmCalls[0].MicrovmIdentifier)
}

func TestSandbox_Resume_CallsAPI(t *testing.T) {
	mock := runningMock(nil)
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	require.NoError(t, sb.Resume(context.Background()))
	require.Len(t, mock.ResumeMicrovmCalls, 1)
	assert.Equal(t, sb.ID(), mock.ResumeMicrovmCalls[0].MicrovmIdentifier)
}

func TestSandbox_Suspend_NotFound_IsTerminated(t *testing.T) {
	mock := runningMock(nil)
	mock.SuspendMicrovmFn = func(_ context.Context, _ *awsapi.SuspendMicrovmInput) error {
		return awsapi.ErrNotFound
	}
	sb, err := newTestManager(mock).Launch(context.Background(), testImageARN)
	require.NoError(t, err)

	require.ErrorIs(t, sb.Suspend(context.Background()), microvm.ErrTerminated, "suspending a gone VM reads as terminated")
}

func TestSandbox_Terminate_IdempotentOnNotFound(t *testing.T) {
	mock := runningMock(nil)
	mock.TerminateMicrovmFn = func(_ context.Context, _ *awsapi.TerminateMicrovmInput) error {
		return awsapi.ErrNotFound
	}
	mgr := newTestManager(mock)
	sb, err := mgr.Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	assert.NoError(t, sb.Terminate(context.Background()), "terminating an already-gone VM is a no-op")
}
