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

func TestBuildImage_EgressNone_SendsEmptyConnectors(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.CreateMicrovmImageFn = func(_ context.Context, in *awsapi.CreateMicrovmImageInput) (*awsapi.CreateMicrovmImageOutput, error) {
		assert.Empty(t, in.EgressConnectors)
		return &awsapi.CreateMicrovmImageOutput{ImageARN: "arn:x", State: "CREATING"}, nil
	}
	spec := testSpec()
	spec.Egress = microvm.EgressNone
	_, err := newTestManager(mock).BuildImage(context.Background(), spec)
	require.NoError(t, err)
}

func TestBuildImage_EgressVPC_Errors(t *testing.T) {
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
