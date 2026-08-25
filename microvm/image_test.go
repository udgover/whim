package microvm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

// newTestManager returns a Manager wired to the given mock.
func newTestManager(m *awsapi.Mock) *microvm.Manager {
	return microvm.NewWithAPI(m,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
	)
}

func testSpec() microvm.ImageSpec {
	return microvm.ImageSpec{
		Name:            "whim-test",
		BaseImageARN:    "arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1",
		CodeArtifactURI: "s3://my-bucket/app.zip",
		BuildRoleARN:    "arn:aws:iam::123456789012:role/whim-build",
		Egress:          microvm.EgressPublic,
	}
}

func setPublicImageVersion(m *awsapi.Mock, caps []string) {
	m.GetMicrovmImageVersionFn = func(_ context.Context, _ *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
		return &awsapi.GetMicrovmImageVersionOutput{
			Capabilities:     caps,
			EgressConnectors: publicEgressConnectors(),
		}, nil
	}
}

// --- Constructors ---

func TestNewWithAPI_ReturnsNonNil(t *testing.T) {
	mgr := microvm.NewWithAPI(&awsapi.Mock{})
	require.NotNil(t, mgr)
}

// --- BuildImage ---

func TestBuildImage_CallsCreateMicrovmImage_WithCorrectFields(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		assert.Equal(t, "whim-test", in.Name)
		assert.Equal(t, "arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1", in.BaseImageARN)
		assert.Equal(t, "s3://my-bucket/app.zip", in.CodeArtifactURI)
		assert.Equal(t, "arn:aws:iam::123456789012:role/whim-build", in.BuildRoleARN)
		assert.NotEmpty(t, in.EgressConnectors, "EgressPublic must produce a connector ARN")
		return &awsapi.CreateMicrovmImageOutput{
			ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test",
			State:    "CREATING",
		}, nil
	}
	mgr := newTestManager(mock)

	build, err := mgr.BuildImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test", build.ImageARN)
	assert.Equal(t, "CREATING", build.State)
	assert.Len(t, mock.CreateMicrovmImageCalls, 1)
}

func TestCapabilityAll_HasExpectedValue(t *testing.T) {
	// The wire value must be exactly "ALL" — the only value AWS accepts today.
	assert.Equal(t, microvm.Capability("ALL"), microvm.CapabilityAll)
}

func TestBuildImage_Capabilities_ForwardedAsStrings(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: "arn:x", State: "CREATING"}, nil
	}
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{microvm.CapabilityAll}
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.NoError(t, err)
	require.Len(t, mock.CreateMicrovmImageCalls, 1)
	assert.Equal(t, []string{"ALL"}, mock.CreateMicrovmImageCalls[0].Capabilities,
		"typed capabilities must lower to plain strings at the awsapi boundary")
}

func TestBuildImage_NoCapabilities_SendsNil(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: "arn:x", State: "CREATING"}, nil
	}
	_, err := newTestManager(mock).BuildImage(context.Background(), testSpec())
	require.NoError(t, err)
	require.Len(t, mock.CreateMicrovmImageCalls, 1)
	assert.Nil(t, mock.CreateMicrovmImageCalls[0].Capabilities,
		"a default (minimal-cap) image must send no capabilities field")
}

func TestBuildImage_RejectsUnsupportedCapability(t *testing.T) {
	mock := &awsapi.Mock{}
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{"BOGUS"}
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateMicrovmImageCalls, "an invalid capability must fail before any AWS call")
}

// createdImageWithCaps wires a mock whose image exists (CREATED) with the given
// baked capabilities, and fails the test if a rebuild is attempted.
func createdImageWithCaps(t *testing.T, caps []string) *awsapi.Mock {
	t.Helper()
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock := &awsapi.Mock{}
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED", LatestActiveImageVersion: "1.0"}, nil
	}
	setPublicImageVersion(mock, caps)
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		t.Fatalf("must not rebuild an existing image")
		return nil, nil
	}
	return mock
}

func TestEnsureImage_ReusesWhenCapabilitiesMatch(t *testing.T) {
	mock := createdImageWithCaps(t, []string{"ALL"})
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{microvm.CapabilityAll}

	got, err := newTestManager(mock).EnsureImage(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test", got)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestEnsureImage_RejectsReuse_WantPrivilegedHaveNot(t *testing.T) {
	mock := createdImageWithCaps(t, nil) // existing image has no caps
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{microvm.CapabilityAll}

	_, err := newTestManager(mock).EnsureImage(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrCapabilityMismatch,
		"a privileged request must not silently reuse an unprivileged image")
}

func TestEnsureImage_RejectsReuse_HavePrivilegedWantNot(t *testing.T) {
	mock := createdImageWithCaps(t, []string{"ALL"}) // existing image is privileged
	spec := testSpec()                               // request has no caps

	_, err := newTestManager(mock).EnsureImage(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrCapabilityMismatch,
		"a non-privileged request must not silently reuse a privileged image")
}

func TestEnsureImage_ReusesIgnoringDuplicateCapabilities(t *testing.T) {
	mock := createdImageWithCaps(t, []string{"ALL", "ALL"}) // duplicated in the response
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{microvm.CapabilityAll}

	got, err := newTestManager(mock).EnsureImage(context.Background(), spec)
	require.NoError(t, err, "duplicate capabilities denote the same set and must still match")
	assert.NotEmpty(t, got)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestEnsureImage_RejectsDuplicateNoPublicEgressConnectors(t *testing.T) {
	mock := createdImageWithCaps(t, nil)
	const connector = "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	mock.GetMicrovmImageVersionFn = func(_ context.Context, _ *awsapi.GetMicrovmImageVersionInput) (*awsapi.GetMicrovmImageVersionOutput, error) {
		return &awsapi.GetMicrovmImageVersionOutput{EgressConnectors: []string{connector, connector}}, nil
	}
	spec := testSpec()
	spec.Egress = microvm.EgressNone
	spec.EgressConnectorARN = connector

	_, err := newTestManager(mock).EnsureImage(context.Background(), spec)

	require.ErrorIs(t, err, microvm.ErrEgressMismatch)
}

// deleteRefusingMock fails the test if any delete is attempted, proving
// validation runs before the destructive step.
func deleteRefusingMock(t *testing.T) *awsapi.Mock {
	t.Helper()
	mock := &awsapi.Mock{}
	mock.DeleteMicrovmImageFn = func(_ context.Context, _ *awsapi.DeleteMicrovmImageInput) error {
		t.Fatalf("ForceRebuildImage must validate the spec before deleting")
		return nil
	}
	return mock
}

func TestForceRebuildImage_ValidatesCapabilityBeforeDelete(t *testing.T) {
	mock := deleteRefusingMock(t)
	spec := testSpec()
	spec.Capabilities = []microvm.Capability{"BOGUS"}

	_, err := newTestManager(mock).ForceRebuildImage(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.DeleteMicrovmImageCalls, "the existing image must survive an invalid spec")
}

func TestForceRebuildImage_ValidatesEgressBeforeDelete(t *testing.T) {
	mock := deleteRefusingMock(t)
	spec := testSpec()
	spec.Egress = microvm.EgressNone // connector ARN required

	_, err := newTestManager(mock).ForceRebuildImage(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.DeleteMicrovmImageCalls)
}

func TestForceRebuildImage_ValidatesRequiredFieldsBeforeDelete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*microvm.ImageSpec)
	}{
		{"missing name", func(s *microvm.ImageSpec) { s.Name = "" }},
		{"missing base image ARN", func(s *microvm.ImageSpec) { s.BaseImageARN = "" }},
		{"missing code artifact URI", func(s *microvm.ImageSpec) { s.CodeArtifactURI = "" }},
		{"missing build role ARN", func(s *microvm.ImageSpec) { s.BuildRoleARN = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := deleteRefusingMock(t)
			spec := testSpec()
			tc.mutate(&spec)

			_, err := newTestManager(mock).ForceRebuildImage(context.Background(), spec)
			require.Error(t, err)
			assert.ErrorIs(t, err, microvm.ErrInvalidOption)
			assert.Empty(t, mock.DeleteMicrovmImageCalls, "the existing image must survive an incomplete spec")
		})
	}
}

func TestBuildImage_EgressNone_RequiresConnector(t *testing.T) {
	mock := &awsapi.Mock{}
	spec := testSpec()
	spec.Egress = microvm.EgressNone
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestBuildImage_EgressNone_SendsConnector(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		assert.Equal(t, []string{"arn:connector"}, in.EgressConnectors)
		return &awsapi.CreateMicrovmImageOutput{ImageARN: "arn:x", State: "CREATING"}, nil
	}
	spec := testSpec()
	spec.Egress = microvm.EgressNone
	spec.EgressConnectorARN = "arn:connector"
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.NoError(t, err)
}

func TestBuildImage_EgressNone_RejectsInternetEgressConnector(t *testing.T) {
	mock := &awsapi.Mock{}
	spec := testSpec()
	spec.Egress = microvm.EgressNone
	spec.EgressConnectorARN = publicEgressConnectors()[0]

	_, err := newTestManager(mock).BuildImage(context.Background(), spec)

	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateMicrovmImageCalls, "INTERNET_EGRESS must never be recorded as egress none")
}

func TestBuildImage_EgressVPC_RequiresConnector(t *testing.T) {
	mock := &awsapi.Mock{}
	spec := testSpec()
	spec.Egress = microvm.EgressVPC
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestBuildImage_APIError_Propagated(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return nil, fmt.Errorf("access denied")
	}
	_, err := newTestManager(mock).BuildImage(context.Background(), testSpec())
	require.Error(t, err)
}

// --- GetImage ---

func TestGetImage_CallsAPIWithIdentifier(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, in *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		assert.Equal(t, arn, in.ImageIdentifier)
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
	}
	build, err := newTestManager(mock).GetImage(context.Background(), arn)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", build.State)
}

func TestGetImage_NotFound_ReturnsErrImageNotFound(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return nil, awsapi.ErrNotFound // seam-level not-found
	}
	_, err := newTestManager(mock).GetImage(context.Background(), "arn:does-not-exist")
	require.ErrorIs(t, err, microvm.ErrImageNotFound, "GetImage must translate seam not-found to ErrImageNotFound")
}

func TestGetImage_OtherError_NotTranslated(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return nil, fmt.Errorf("throttled")
	}
	_, err := newTestManager(mock).GetImage(context.Background(), "arn:x")
	require.Error(t, err)
	require.NotErrorIs(t, err, microvm.ErrImageNotFound)
}

// --- EnsureImage ---

func TestEnsureImage_ExistingCreated_SkipsBuild(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
	}
	setPublicImageVersion(mock, nil)
	got, err := newTestManager(mock).EnsureImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Empty(t, mock.CreateMicrovmImageCalls, "must not build when image already CREATED")
}

func TestEnsureImage_NotFound_BuildsThenPolls(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	getCount := 0
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		getCount++
		if getCount == 1 {
			return nil, awsapi.ErrNotFound // first call: absent → triggers build
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil // poll: ready
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	setPublicImageVersion(mock, nil)

	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	got, err := mgr.EnsureImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Len(t, mock.CreateMicrovmImageCalls, 1)
}

func TestEnsureImage_PollsUntilCreated(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	getCount := 0
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		getCount++
		if getCount <= 2 {
			return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil // still building
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	setPublicImageVersion(mock, nil)
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)

	// first GetImage: image exists in CREATING state → build skipped, poll directly
	got, err := mgr.EnsureImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
}

func TestEnsureImage_CreateFailed_ReturnsErrImageBuildFailed(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return nil, awsapi.ErrNotFound
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	call := 0
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		call++
		if call == 1 {
			return nil, awsapi.ErrNotFound
		}
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATE_FAILED"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	_, err := mgr.EnsureImage(context.Background(), testSpec())
	require.ErrorIs(t, err, microvm.ErrImageBuildFailed)
}

func TestEnsureImage_ContextCanceled_StopsPolling(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	_, err := mgr.EnsureImage(ctx, testSpec())
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

func TestEnsureImage_UnexpectedState_DoesNotBuild(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "DELETING"}, nil
	}
	_, err := newTestManager(mock).EnsureImage(context.Background(), testSpec())
	require.ErrorIs(t, err, microvm.ErrImageBuildFailed)
	assert.Empty(t, mock.CreateMicrovmImageCalls, "must not build when the image exists in an unexpected state")
}

func TestEnsureImage_Updated_ReturnsWithoutBuild(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "UPDATED"}, nil
	}
	setPublicImageVersion(mock, nil)
	got, err := newTestManager(mock).EnsureImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Empty(t, mock.CreateMicrovmImageCalls)
}

func TestEnsureImage_TransientError_DoesNotBuild(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		return nil, fmt.Errorf("throttled: slow down") // not ErrNotFound
	}
	_, err := newTestManager(mock).EnsureImage(context.Background(), testSpec())
	require.Error(t, err)
	assert.Empty(t, mock.CreateMicrovmImageCalls, "a transient GetImage error must not trigger a build")
}

// --- DeleteImage ---

func TestDeleteImage_NotFound_IsIdempotent(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.DeleteMicrovmImageFn = func(_ context.Context, _ *awsapi.DeleteMicrovmImageInput) error {
		return awsapi.ErrNotFound
	}
	err := newTestManager(mock).DeleteImage(context.Background(), "arn:gone")
	require.NoError(t, err, "deleting an absent image is a no-op")
}

func TestDeleteImage_PropagatesOtherErrors(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.DeleteMicrovmImageFn = func(_ context.Context, _ *awsapi.DeleteMicrovmImageInput) error {
		return fmt.Errorf("access denied")
	}
	err := newTestManager(mock).DeleteImage(context.Background(), "arn:x")
	require.Error(t, err)
}

// --- ForceRebuildImage ---

func TestForceRebuildImage_DeletesWaitsAndRebuilds(t *testing.T) {
	mock := &awsapi.Mock{}
	const arn = "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test"
	mock.DeleteMicrovmImageFn = func(_ context.Context, _ *awsapi.DeleteMicrovmImageInput) error { return nil }
	getCount := 0
	mock.GetMicrovmImageFn = func(_ context.Context, _ *awsapi.GetMicrovmImageInput) (*awsapi.GetMicrovmImageOutput, error) {
		getCount++
		switch getCount {
		case 1:
			return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "DELETING"}, nil // still deleting
		case 2, 3:
			return nil, awsapi.ErrNotFound // gone; then EnsureImage sees absent → build
		default:
			return &awsapi.GetMicrovmImageOutput{ImageARN: arn, State: "CREATED"}, nil // post-build poll
		}
	}
	mock.CreateMicrovmImageFn = func(_ context.Context, _ *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		return &awsapi.CreateMicrovmImageOutput{ImageARN: arn, State: "CREATING"}, nil
	}
	mgr := microvm.NewWithAPI(mock,
		microvm.WithRegion("us-east-1"),
		microvm.WithAccountID("123456789012"),
		microvm.WithPollInterval(time.Millisecond),
	)
	got, err := mgr.ForceRebuildImage(context.Background(), testSpec())
	require.NoError(t, err)
	assert.Equal(t, arn, got)
	assert.Len(t, mock.DeleteMicrovmImageCalls, 1)
	assert.Len(t, mock.CreateMicrovmImageCalls, 1, "must rebuild exactly once after deletion")
}

// --- ListImages / ImageARN ---

func TestListImages_MapsSummaries(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.ListMicrovmImagesFn = func(_ context.Context, _ *awsapi.ListMicrovmImagesInput) (*awsapi.ListMicrovmImagesOutput, error) {
		return &awsapi.ListMicrovmImagesOutput{Items: []awsapi.MicrovmImageSummary{
			{Name: "whim-default", ImageARN: "arn:a:whim-default", State: "CREATED", LatestActiveImageVersion: "1.0"},
			{Name: "whim-test", ImageARN: "arn:a:whim-test", State: "CREATING"},
		}}, nil
	}
	imgs, err := newTestManager(mock).ListImages(context.Background())
	require.NoError(t, err)
	require.Len(t, imgs, 2)
	assert.Equal(t, "whim-default", imgs[0].Name)
	assert.Equal(t, "CREATED", imgs[0].State)
	assert.Equal(t, "1.0", imgs[0].Version)
	assert.Equal(t, "whim-test", imgs[1].Name)
}

func TestListImages_Empty(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.ListMicrovmImagesFn = func(_ context.Context, _ *awsapi.ListMicrovmImagesInput) (*awsapi.ListMicrovmImagesOutput, error) {
		return &awsapi.ListMicrovmImagesOutput{}, nil
	}
	imgs, err := newTestManager(mock).ListImages(context.Background())
	require.NoError(t, err)
	assert.Empty(t, imgs)
}

func TestListImages_ErrorPropagated(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.ListMicrovmImagesFn = func(_ context.Context, _ *awsapi.ListMicrovmImagesInput) (*awsapi.ListMicrovmImagesOutput, error) {
		return nil, fmt.Errorf("access denied")
	}
	_, err := newTestManager(mock).ListImages(context.Background())
	require.Error(t, err)
}

func TestImageARN_DerivesFromName(t *testing.T) {
	mgr := newTestManager(&awsapi.Mock{})
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-x", mgr.ImageARN("whim-x"))
}

// imageARN is the canonical ARN whim derives from a spec name.
func imageARN(region, accountID, name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:microvm-image:%s", region, accountID, name)
}

func TestImageARN_Format(t *testing.T) {
	got := imageARN("us-east-1", "123456789012", "whim-test")
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test", got)
}
