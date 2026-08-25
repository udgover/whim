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

func publicEgressConnectors() []string {
	return []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"}
}

func setImageEgress(m *awsapi.Mock, imageEgress []string) {
	m.GetMicrovmImageFn = func(_ context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: in.ImageIdentifier, State: "CREATED", LatestActiveImageVersion: "1"}, nil
	}
	m.GetMicrovmImageVersionFn = func(_ context.Context, _ *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
		return &awsapi.GetMicrovmImageVersionOutput{EgressConnectors: imageEgress}, nil
	}
}

// runningMockWithImageEgress returns a mock whose image has the given egress
// connectors, RunMicrovm succeeds, and GetMicrovm reports RUNNING immediately,
// capturing the RunMicrovm input for assertions.
func runningMockWithImageEgress(captured **awsapi.RunMicrovmInput, imageEgress []string) *awsapi.Mock {
	m := &awsapi.Mock{}
	var launchedEgress []string
	m.RunMicrovmFn = func(_ context.Context, in *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		if captured != nil {
			*captured = in
		}
		launchedEgress = append([]string(nil), in.EgressNetworkConnectors...)
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-123", Endpoint: "mvm-123.lambda-microvm.us-east-1.on.aws", State: "PENDING"}, nil
	}
	m.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{
			MicrovmID:               "mvm-123",
			Endpoint:                "mvm-123.lambda-microvm.us-east-1.on.aws",
			State:                   "RUNNING",
			EgressNetworkConnectors: append([]string(nil), launchedEgress...),
		}, nil
	}
	setImageEgress(m, imageEgress)
	return m
}

// runningMock returns a mock with a public-egress image.
func runningMock(captured **awsapi.RunMicrovmInput) *awsapi.Mock {
	return runningMockWithImageEgress(captured, publicEgressConnectors())
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
	// Default launch egress inherits the image's public connector.
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

func TestLaunch_EgressNone_RequiresConnector(t *testing.T) {
	mgr := newTestManager(runningMock(nil))
	_, err := mgr.Launch(context.Background(), testImageARN, microvm.WithEgress(microvm.EgressNone))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestLaunch_EgressNone_SendsConnector(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	mock := runningMock(&in)
	configureSafeNoPublicEgressMock(mock)
	mgr := newTestManager(mock)
	connector := testNoPublicConnector
	_, err := mgr.Launch(context.Background(), testImageARN, microvm.WithEgressConnector(microvm.EgressNone, connector))
	require.NoError(t, err)
	assert.Equal(t, []string{connector}, in.EgressNetworkConnectors)
}

func TestLaunch_DefaultEgressInheritsImageConnector(t *testing.T) {
	var in *awsapi.RunMicrovmInput
	connector := "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	mgr := newTestManager(runningMockWithImageEgress(&in, []string{connector}))
	_, err := mgr.Launch(context.Background(), testImageARN)
	require.NoError(t, err)
	assert.Equal(t, []string{connector}, in.EgressNetworkConnectors)
}

func TestLaunch_EgressNone_RevalidatesTopologyBeforeRun(t *testing.T) {
	mock := safeNoPublicEgressMock()
	mock.DescribeSecurityGroupsFn = func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
		return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{
			ID: "sg-safe", VPCID: "vpc-safe",
			EgressRules: []awsapi.SecurityGroupRule{{IPProtocol: "-1", CIDRIPv4: "0.0.0.0/0"}},
		}}}, nil
	}

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN,
		microvm.WithEgressConnector(microvm.EgressNone, testNoPublicConnector))

	require.ErrorIs(t, err, microvm.ErrSecurityGroupEgress)
	assert.Empty(t, mock.RunMicrovmCalls, "drifted isolation must fail before launching")
}

func TestLaunch_ExpectedNoPublicEgressUsesBakedConnectorAndValidatesTopology(t *testing.T) {
	var input *awsapi.RunMicrovmInput
	mock := runningMockWithImageEgress(&input, []string{testNoPublicConnector})
	configureSafeNoPublicEgressMock(mock)
	expected := microvm.NoPublicEgressResources{
		ConnectorARN:     testNoPublicConnector,
		VPCID:            "vpc-safe",
		SubnetIDs:        []string{"subnet-safe"},
		RouteTableIDs:    []string{"rtb-safe"},
		SecurityGroupIDs: []string{"sg-safe"},
	}

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN,
		microvm.WithExpectedNoPublicEgress(expected))

	require.NoError(t, err)
	require.NotNil(t, input)
	assert.Equal(t, []string{testNoPublicConnector}, input.EgressNetworkConnectors)
	assert.NotEmpty(t, mock.GetMicrovmImageVersionCalls, "launch must inherit and verify the image's baked connector")
}

func TestLaunch_ExpectedNoPublicEgressRejectsBakedConnectorMismatch(t *testing.T) {
	mock := runningMockWithImageEgress(nil, publicEgressConnectors())
	configureSafeNoPublicEgressMock(mock)

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN,
		microvm.WithExpectedNoPublicEgress(microvm.NoPublicEgressResources{ConnectorARN: testNoPublicConnector}))

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
	assert.Empty(t, mock.RunMicrovmCalls)
}

func TestLaunch_ExpectedNoPublicEgressValidatesMatchingRuntimeOverride(t *testing.T) {
	var input *awsapi.RunMicrovmInput
	mock := runningMock(&input)
	configureSafeNoPublicEgressMock(mock)
	expected := microvm.NoPublicEgressResources{
		ConnectorARN:     testNoPublicConnector,
		VPCID:            "vpc-safe",
		SubnetIDs:        []string{"subnet-safe"},
		RouteTableIDs:    []string{"rtb-safe"},
		SecurityGroupIDs: []string{"sg-safe"},
	}

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN,
		microvm.WithEgressConnector(microvm.EgressNone, testNoPublicConnector),
		microvm.WithExpectedNoPublicEgress(expected))

	require.NoError(t, err)
	require.NotNil(t, input)
	assert.Equal(t, []string{testNoPublicConnector}, input.EgressNetworkConnectors)
	assert.Empty(t, mock.GetMicrovmImageVersionCalls,
		"an explicit runtime connector must not depend on the image's public build connector")
}

func TestLaunch_ExpectedNoPublicEgressRejectsMismatchedRuntimeOverride(t *testing.T) {
	mock := runningMock(nil)

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN,
		microvm.WithEgressConnector(microvm.EgressNone,
			"arn:aws:lambda:us-east-1:123456789012:network-connector:other"),
		microvm.WithExpectedNoPublicEgress(microvm.NoPublicEgressResources{
			ConnectorARN: testNoPublicConnector,
		}))

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
	assert.Empty(t, mock.RunMicrovmCalls)
}

func TestLaunch_RejectsImageWithEmptyEgressBeforeRun(t *testing.T) {
	mock := runningMockWithImageEgress(nil, nil)

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN)

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
	assert.Empty(t, mock.RunMicrovmCalls, "empty image metadata must never reach AWS's public-default fallback")
}

func TestLaunch_RejectsRuntimeEgressMetadataMismatchAndTerminates(t *testing.T) {
	mock := runningMock(nil)
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{
			MicrovmID:               "mvm-123",
			State:                   "RUNNING",
			EgressNetworkConnectors: publicEgressConnectors(),
		}, nil
	}
	wantConnector := "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	setImageEgress(mock, []string{wantConnector})

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN)

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
	require.Len(t, mock.TerminateMicrovmCalls, 1, "a VM with unexpected egress metadata must be terminated")
	assert.Equal(t, "mvm-123", mock.TerminateMicrovmCalls[0].MicrovmIdentifier)
}

func TestLaunch_RejectsDuplicateRuntimeEgressMetadataAndTerminates(t *testing.T) {
	mock := runningMock(nil)
	wantConnector := "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	setImageEgress(mock, []string{wantConnector})
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{
			MicrovmID:               "mvm-123",
			State:                   "RUNNING",
			EgressNetworkConnectors: []string{wantConnector, wantConnector},
		}, nil
	}

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN)

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
	require.Len(t, mock.TerminateMicrovmCalls, 1, "runtime metadata must contain the exact connector list, including cardinality")
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
	setImageEgress(mock, publicEgressConnectors())
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	calls := 0
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		calls++
		if calls < 3 {
			return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
		}
		return &awsapi.GetMicrovmOutput{
			MicrovmID: "mvm-x", Endpoint: "ep-x", State: "RUNNING",
			EgressNetworkConnectors: publicEgressConnectors(),
		}, nil
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
	setImageEgress(mock, publicEgressConnectors())
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

func TestLaunch_GetMicrovmFailureTerminatesUnverifiedVM(t *testing.T) {
	mock := &awsapi.Mock{}
	setImageEgress(mock, publicEgressConnectors())
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return nil, errors.New("metadata unavailable")
	}

	_, err := newTestManager(mock).Launch(context.Background(), testImageARN)

	require.ErrorContains(t, err, "metadata unavailable")
	require.Len(t, mock.TerminateMicrovmCalls, 1, "a VM whose runtime metadata cannot be verified must be terminated")
	assert.Equal(t, "mvm-x", mock.TerminateMicrovmCalls[0].MicrovmIdentifier)
}

func TestLaunch_ContextCanceled_AbortsPoll(t *testing.T) {
	mock := &awsapi.Mock{}
	setImageEgress(mock, publicEgressConnectors())
	mock.RunMicrovmFn = func(_ context.Context, _ *awsapi.RunMicrovmInput) (*awsapi.RunMicrovmOutput, error) {
		return &awsapi.RunMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil
	}
	mock.GetMicrovmFn = func(_ context.Context, _ *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: "mvm-x", State: "PENDING"}, nil // never RUNNING
	}
	mock.TerminateMicrovmFn = func(cleanupCtx context.Context, _ *awsapi.TerminateMicrovmInput) error {
		assert.NoError(t, cleanupCtx.Err(), "cleanup must not reuse the expired launch context")
		return nil
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
	require.Len(t, mock.TerminateMicrovmCalls, 1, "a timed-out launch must terminate its unverified VM")
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
	assert.Empty(t, mock.ResumeMicrovmCalls, "an already-RUNNING VM must never be resumed")
}

// TestAttach_Suspended_ResumesAndPolls covers the persistent-boxes headline
// behavior: exec/put/get/interactive-attach all route through Attach, so
// resuming here transparently wakes a suspended box on any use.
func TestAttach_Suspended_ResumesAndPolls(t *testing.T) {
	mock := &awsapi.Mock{}
	calls := 0
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		calls++
		if calls == 1 {
			return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, State: "SUSPENDED"}, nil
		}
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, Endpoint: "ep-resumed", State: "RUNNING"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"), microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)

	sb, err := mgr.Attach(context.Background(), "mvm-susp")
	require.NoError(t, err)
	assert.Equal(t, "mvm-susp", sb.ID())
	assert.Equal(t, "ep-resumed", sb.Endpoint())
	require.Len(t, mock.ResumeMicrovmCalls, 1, "Attach must resume a SUSPENDED VM")
	assert.Equal(t, "mvm-susp", mock.ResumeMicrovmCalls[0].MicrovmIdentifier)
}

func TestAttach_Suspended_TerminatedWhileResuming(t *testing.T) {
	mock := &awsapi.Mock{}
	calls := 0
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		calls++
		if calls == 1 {
			return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, State: "SUSPENDED"}, nil
		}
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, State: "TERMINATED"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"), microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)

	_, err := mgr.Attach(context.Background(), "mvm-gone-mid-resume")
	require.ErrorIs(t, err, microvm.ErrTerminated, "a VM that dies while resuming must surface as terminated, not a timeout")
}

func TestAttach_Suspended_StuckNeverRunning_ContextDeadline(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, State: "SUSPENDED"}, nil // never reaches RUNNING
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"), microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := mgr.Attach(ctx, "mvm-stuck")
	require.Error(t, err, "a VM stuck non-RUNNING while resuming must fail on ctx deadline, not hang forever")
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

func TestAttach_Suspended_ResumeAPIFails(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmFn = func(_ context.Context, in *awsapi.GetMicrovmInput) (*awsapi.GetMicrovmOutput, error) {
		return &awsapi.GetMicrovmOutput{MicrovmID: in.MicrovmIdentifier, State: "SUSPENDED"}, nil
	}
	mock.ResumeMicrovmFn = func(_ context.Context, _ *awsapi.ResumeMicrovmInput) error {
		return errors.New("resume denied")
	}

	_, err := newTestManager(mock).Attach(context.Background(), "mvm-resume-fail")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resume denied")
	assert.Empty(t, mock.GetMicrovmCalls[1:], "must not poll after a failed Resume call")
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

// TestManager_Terminate_CallsAPI covers by-id termination (used by `whim rm`),
// which must work without first Attach-ing — Attach requires RUNNING/SUSPENDED
// and would wrongly refuse to terminate a VM stuck in some other state.
func TestManager_Terminate_CallsAPI(t *testing.T) {
	mock := &awsapi.Mock{}
	mgr := newTestManager(mock)

	require.NoError(t, mgr.Terminate(context.Background(), "mvm-bare"))
	require.Len(t, mock.TerminateMicrovmCalls, 1)
	assert.Equal(t, "mvm-bare", mock.TerminateMicrovmCalls[0].MicrovmIdentifier)
}

func TestManager_Terminate_IdempotentOnNotFound(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.TerminateMicrovmFn = func(_ context.Context, _ *awsapi.TerminateMicrovmInput) error {
		return awsapi.ErrNotFound
	}
	require.NoError(t, newTestManager(mock).Terminate(context.Background(), "mvm-gone"),
		"terminating an already-gone VM by id is a no-op, matching Sandbox.Terminate")
}

func TestManager_Terminate_SurfacesOtherErrors(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.TerminateMicrovmFn = func(_ context.Context, _ *awsapi.TerminateMicrovmInput) error {
		return errors.New("access denied")
	}
	err := newTestManager(mock).Terminate(context.Background(), "mvm-denied")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
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
