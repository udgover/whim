package microvm

import (
	"context"
	"fmt"
)

// BuildFromSource builds a Lambda MicroVM image from a user-provided source
// (local path, s3:// URI, or https:// URL) and returns the usable image ARN.
//
// The image Name is the reuse key. Without Force, an existing CREATED/UPDATED
// image is returned and an in-progress build is polled — neither stages the
// source. An absent image (or any Force build, which first deletes and waits
// for the old image to disappear) stages the source into the caller's artifact
// bucket and builds via EnsureImage. The library never resolves ambient
// credentials; all build inputs come from opts.
func (m *Manager) BuildFromSource(ctx context.Context, source string, opts BuildFromSourceOptions) (string, error) {
	if err := opts.validate(); err != nil {
		return "", err
	}
	classified, err := classifySource(source)
	if err != nil {
		return "", err
	}
	arn := m.imageARN(opts.Name)

	if opts.Force {
		if err := m.deleteAndWaitGone(ctx, arn, opts.Name); err != nil {
			return "", err
		}
	} else {
		resolved, found, err := m.reuseImage(ctx, arn, opts.Name, opts.Capabilities)
		if err != nil {
			return "", err
		}
		if found {
			return resolved, nil
		}
	}

	uri, err := m.stageSource(ctx, classified, opts)
	if err != nil {
		return "", fmt.Errorf("stage source %q: %w", redactSource(source), err)
	}
	return m.EnsureImage(ctx, ImageSpec{
		Name:            opts.Name,
		BaseImageARN:    opts.BaseImageARN,
		CodeArtifactURI: uri,
		BuildRoleARN:    opts.BuildRoleARN,
		Egress:          opts.Egress,
		Capabilities:    opts.Capabilities,
	})
}

// stageSource routes a classified source through the matching staging path and
// returns the s3:// artifact URI in the caller's artifact bucket.
func (m *Manager) stageSource(ctx context.Context, src classifiedSource, opts BuildFromSourceOptions) (string, error) {
	switch src.kind {
	case sourceLocal:
		zip, err := stageLocalSource(src.path, opts.ContextSubdir, opts.caps())
		if err != nil {
			return "", err
		}
		return m.uploadArtifact(ctx, opts, zip)
	case sourceHTTPS:
		return m.stageHTTPSSource(ctx, src, opts)
	case sourceS3:
		return m.stageS3Source(ctx, src, opts)
	default:
		return "", fmt.Errorf("%w: unsupported source kind", ErrInvalidSource)
	}
}

// Default staging caps, applied when BuildFromSourceOptions leaves the
// corresponding limit unset. They bound the staged build context to guard
// against zip bombs and runaway uploads before any image build is submitted.
const (
	defaultMaxCompressedBytes   int64 = 256 << 20 // 256 MiB
	defaultMaxUncompressedBytes int64 = 1 << 30   // 1 GiB
	defaultMaxFiles                   = 10000
)

// BuildFromSourceOptions carries the explicit build inputs a caller supplies to
// Manager.BuildFromSource. The library never resolves ambient defaults: the
// artifact bucket, base image, and build role are always caller-provided
// (cmd/whim owns bootstrap convenience). Name doubles as the image reuse key.
type BuildFromSourceOptions struct {
	// Name is the MicroVM image name; required. It is the reuse/cache key.
	Name string
	// ArtifactBucket is the caller-owned S3 bucket staged artifacts upload to; required.
	ArtifactBucket string
	// ArtifactPrefix is an optional key prefix within ArtifactBucket.
	ArtifactPrefix string
	// BaseImageARN is the managed/base MicroVM image to build on top of; required.
	BaseImageARN string
	// BuildRoleARN is the role the build assumes to read the staged artifact; required.
	BuildRoleARN string
	// Egress fixes the image's outbound policy; must be EgressPublic or
	// EgressNone. Note the zero value is EgressPublic, so leaving this unset
	// grants the image outbound internet — set it explicitly for an airgapped
	// (EgressNone) build. The CLI always passes an explicit value.
	Egress EgressMode
	// Force deletes and rebuilds an existing image of the same name.
	Force bool
	// ContextSubdir descends into a subdirectory of the source before locating Dockerfile.
	ContextSubdir string
	// MaxCompressedBytes caps the staged zip size; <= 0 uses the default.
	MaxCompressedBytes int64
	// MaxUncompressedBytes caps the total uncompressed context size; <= 0 uses the default.
	MaxUncompressedBytes int64
	// HTTPSHeaders are optional request headers for https:// sources (e.g. auth).
	HTTPSHeaders map[string]string
	// CacheKey optionally overrides how reuse is keyed; reserved for future use.
	CacheKey string
	// SourceIdentity records an immutable source identity (e.g. a full commit SHA).
	SourceIdentity string
	// Capabilities grants elevated OS capabilities to the built image (AWS
	// additionalOsCapabilities). Leave nil for a default, minimal-cap image.
	Capabilities []Capability
}

// stagingCaps holds the resolved size/count limits enforced while staging a
// source into a build artifact.
type stagingCaps struct {
	maxCompressedBytes   int64
	maxUncompressedBytes int64
	maxFiles             int
}

// defaultStagingCaps returns the caps applied when no caller limits are set.
func defaultStagingCaps() stagingCaps {
	return BuildFromSourceOptions{}.caps()
}

// caps resolves the effective staging limits, substituting defaults for any
// caller value left unset. The file-count cap is fixed in the MVP.
func (o BuildFromSourceOptions) caps() stagingCaps {
	caps := stagingCaps{
		maxCompressedBytes:   o.MaxCompressedBytes,
		maxUncompressedBytes: o.MaxUncompressedBytes,
		maxFiles:             defaultMaxFiles,
	}
	if caps.maxCompressedBytes <= 0 {
		caps.maxCompressedBytes = defaultMaxCompressedBytes
	}
	if caps.maxUncompressedBytes <= 0 {
		caps.maxUncompressedBytes = defaultMaxUncompressedBytes
	}
	return caps
}

// validate checks required fields and the supported egress modes. It returns
// ErrInvalidOption (wrapped with detail) on the first problem; staging and
// AWS calls only happen once options validate.
func (o BuildFromSourceOptions) validate() error {
	switch {
	case o.Name == "":
		return fmt.Errorf("%w: image name is required", ErrInvalidOption)
	case o.ArtifactBucket == "":
		return fmt.Errorf("%w: artifact bucket is required", ErrInvalidOption)
	case o.BaseImageARN == "":
		return fmt.Errorf("%w: base image ARN is required", ErrInvalidOption)
	case o.BuildRoleARN == "":
		return fmt.Errorf("%w: build role ARN is required", ErrInvalidOption)
	}
	switch o.Egress {
	case EgressPublic, EgressNone:
	default:
		return fmt.Errorf("%w: egress must be EgressPublic or EgressNone", ErrInvalidOption)
	}
	return validateCapabilities(o.Capabilities)
}
