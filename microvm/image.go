package microvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udgover/whim/internal/awsapi"
)

// MicroVM image lifecycle states (mirror the lambdamicrovms SDK enum).
const (
	imageStateCreated      = "CREATED"
	imageStateCreating     = "CREATING"
	imageStateCreateFailed = "CREATE_FAILED"
	imageStateUpdating     = "UPDATING"
	imageStateUpdated      = "UPDATED"
	imageStateUpdateFailed = "UPDATE_FAILED"
)

// ImageBuild represents the current build state of a MicroVM image.
type ImageBuild struct {
	ImageARN     string
	ImageVersion string
	State        string
}

// ImageSummary describes one MicroVM image in a listing.
type ImageSummary struct {
	Name      string
	ARN       string
	State     string
	Version   string
	CreatedAt time.Time
}

// ImageARN returns the canonical ARN whim derives for the given image name.
func (m *Manager) ImageARN(name string) string {
	return m.imageARN(name)
}

// ListImages returns all MicroVM images in the account/region.
func (m *Manager) ListImages(ctx context.Context) ([]ImageSummary, error) {
	out, err := m.api.ListMicrovmImages(ctx, &awsapi.ListMicrovmImagesInput{})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	summaries := make([]ImageSummary, 0, len(out.Items))
	for _, it := range out.Items {
		summaries = append(summaries, ImageSummary{
			Name:      it.Name,
			ARN:       it.ImageARN,
			State:     it.State,
			Version:   it.LatestActiveImageVersion,
			CreatedAt: it.CreatedAt,
		})
	}
	return summaries, nil
}

// egressConnectors returns the connector ARNs to embed in the image for the
// requested egress mode. Egress is fixed at build time and inherited by runs.
func egressConnectors(mode EgressMode, region string) ([]string, error) {
	switch mode {
	case EgressPublic:
		return []string{
			fmt.Sprintf("arn:aws:lambda:%s:aws:network-connector:aws-network-connector:INTERNET_EGRESS", region),
		}, nil
	case EgressNone:
		return nil, nil
	case EgressVPC:
		return nil, fmt.Errorf("%w: EgressVPC is not supported in v0.1", ErrInvalidOption)
	default:
		return nil, fmt.Errorf("%w: unknown egress mode %d", ErrInvalidOption, mode)
	}
}

// BuildImage submits an asynchronous image build and returns the initial state.
// It does NOT poll for completion — call EnsureImage for the full build-and-wait
// lifecycle, or use GetImage to poll manually.
func (m *Manager) BuildImage(ctx context.Context, spec ImageSpec) (*ImageBuild, error) {
	connectors, err := egressConnectors(spec.Egress, m.region)
	if err != nil {
		return nil, err
	}
	out, err := m.api.CreateMicrovmImage(ctx, &awsapi.CreateMicrovmImageInput{
		Name:             spec.Name,
		BaseImageARN:     spec.BaseImageARN,
		CodeArtifactURI:  spec.CodeArtifactURI,
		BuildRoleARN:     spec.BuildRoleARN,
		EgressConnectors: connectors,
	})
	if err != nil {
		return nil, fmt.Errorf("build image %q: %w", spec.Name, err)
	}
	return &ImageBuild{
		ImageARN:     out.ImageARN,
		ImageVersion: out.ImageVersion,
		State:        out.State,
	}, nil
}

// GetImage retrieves the current state of an image by its full ARN. A missing
// image is reported as ErrImageNotFound (matchable with errors.Is).
func (m *Manager) GetImage(ctx context.Context, identifier string) (*ImageBuild, error) {
	out, err := m.api.GetMicrovmImage(ctx, &awsapi.GetMicrovmImageInput{
		ImageIdentifier: identifier,
	})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return nil, fmt.Errorf("get image %q: %w", identifier, ErrImageNotFound)
		}
		return nil, fmt.Errorf("get image %q: %w", identifier, err)
	}
	return &ImageBuild{
		ImageARN:     out.ImageARN,
		ImageVersion: out.LatestActiveImageVersion,
		State:        out.State,
	}, nil
}

// EnsureImage idempotently returns the ARN of a usable image matching spec.
//
// If the image already exists, its state is interpreted and the image is never
// rebuilt (which would conflict on the unique name): CREATED/UPDATED return
// immediately, CREATING/UPDATING are polled to completion, failure states return
// ErrImageBuildFailed, and any other state is reported rather than rebuilt.
// A fresh build is submitted only when the image is genuinely absent
// (ErrImageNotFound); any other lookup error is returned, never built over.
func (m *Manager) EnsureImage(ctx context.Context, spec ImageSpec) (string, error) {
	arn := m.imageARN(spec.Name)

	build, err := m.GetImage(ctx, arn)
	switch {
	case err == nil:
		// Image exists — interpret its state; never re-build an existing image.
		switch build.State {
		case imageStateCreated, imageStateUpdated:
			return arn, nil
		case imageStateCreating, imageStateUpdating:
			return m.pollUntilCreated(ctx, arn)
		case imageStateCreateFailed, imageStateUpdateFailed:
			return "", fmt.Errorf("%w: image %q is in %s state", ErrImageBuildFailed, spec.Name, build.State)
		default:
			return "", fmt.Errorf("%w: image %q is in unexpected state %s", ErrImageBuildFailed, spec.Name, build.State)
		}
	case !errors.Is(err, ErrImageNotFound):
		// A real error (permissions, throttling, transient) — do not blindly build.
		return "", err
	}

	// Image is genuinely absent — submit a fresh build.
	if _, err := m.BuildImage(ctx, spec); err != nil {
		return "", err
	}
	m.log().Info("image build submitted", "name", spec.Name, "arn", arn)
	return m.pollUntilCreated(ctx, arn)
}

// DeleteImage deletes the image with the given ARN. It is idempotent: an
// already-absent image is treated as success.
func (m *Manager) DeleteImage(ctx context.Context, identifier string) error {
	err := m.api.DeleteMicrovmImage(ctx, &awsapi.DeleteMicrovmImageInput{ImageIdentifier: identifier})
	if err != nil && !errors.Is(err, awsapi.ErrNotFound) {
		return fmt.Errorf("delete image %q: %w", identifier, err)
	}
	return nil
}

// ForceRebuildImage deletes any existing image with spec's name, waits for the
// deletion to fully complete (the name must be free to rebuild), then builds it
// fresh. Destructive: the prior image and its versions are removed.
func (m *Manager) ForceRebuildImage(ctx context.Context, spec ImageSpec) (string, error) {
	arn := m.imageARN(spec.Name)
	if err := m.DeleteImage(ctx, arn); err != nil {
		return "", err
	}
	// Wait until the image is fully gone (GetImage reports not-found) before
	// rebuilding — a lingering DELETING/CREATED resource would conflict on the name.
	if err := m.poll(ctx, func(ctx context.Context) (bool, error) {
		_, gerr := m.GetImage(ctx, arn)
		if errors.Is(gerr, ErrImageNotFound) {
			return true, nil
		}
		if gerr != nil {
			return false, gerr
		}
		return false, nil // still present — keep waiting
	}); err != nil {
		return "", fmt.Errorf("waiting for image %q deletion: %w", spec.Name, err)
	}
	return m.EnsureImage(ctx, spec)
}

// pollUntilCreated polls GetImage for the given ARN until the image reaches a
// usable state (CREATED/UPDATED), a terminal failure, or ctx is canceled.
func (m *Manager) pollUntilCreated(ctx context.Context, arn string) (string, error) {
	var finalARN string
	err := m.poll(ctx, func(ctx context.Context) (bool, error) {
		build, err := m.GetImage(ctx, arn)
		if err != nil {
			return false, fmt.Errorf("polling image %q: %w", arn, err)
		}
		switch build.State {
		case imageStateCreated, imageStateUpdated:
			finalARN = build.ImageARN
			return true, nil
		case imageStateCreateFailed, imageStateUpdateFailed:
			return false, fmt.Errorf("%w: image %q failed (state %s)", ErrImageBuildFailed, arn, build.State)
		default:
			m.log().Debug("image still building", "arn", arn, "state", build.State)
			return false, nil
		}
	})
	if err != nil {
		return "", err
	}
	return finalARN, nil
}
