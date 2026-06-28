// Package awsapi defines a narrow interface over the lambdamicrovms SDK client.
// All library code depends on this interface, never the concrete SDK type,
// so the entire AWS surface is mockable in unit tests.
package awsapi

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned (wrapped) when a requested resource does not exist.
// Callers match it with errors.Is to distinguish absence from other failures.
var ErrNotFound = errors.New("awsapi: resource not found")

// RunMicrovmInput mirrors the fields of the SDK's RunMicrovmInput that whim uses.
type RunMicrovmInput struct {
	ImageIdentifier          string
	ImageVersion             *string
	IngressNetworkConnectors []string
	EgressNetworkConnectors  []string
	MaximumDurationInSeconds *int32
	ExecutionRoleARN         *string
	IdlePolicy               *IdlePolicy
}

// RunMicrovmOutput mirrors the fields we consume from RunMicrovmOutput.
type RunMicrovmOutput struct {
	MicrovmID string
	Endpoint  string
	State     string
}

// GetMicrovmInput mirrors GetMicrovmInput fields used by whim.
type GetMicrovmInput struct {
	MicrovmIdentifier string
}

// GetMicrovmOutput mirrors the fields we consume.
type GetMicrovmOutput struct {
	MicrovmID string
	Endpoint  string
	State     string
}

// TerminateMicrovmInput mirrors TerminateMicrovmInput.
type TerminateMicrovmInput struct {
	MicrovmIdentifier string
}

// SuspendMicrovmInput mirrors SuspendMicrovmInput.
type SuspendMicrovmInput struct {
	MicrovmIdentifier string
}

// ResumeMicrovmInput mirrors ResumeMicrovmInput.
type ResumeMicrovmInput struct {
	MicrovmIdentifier string
}

// CreateShellAuthTokenInput mirrors CreateMicrovmShellAuthTokenInput.
type CreateShellAuthTokenInput struct {
	MicrovmIdentifier string
	ExpirationMinutes int32
}

// CreateShellAuthTokenOutput holds the auth token header key/value.
type CreateShellAuthTokenOutput struct {
	HeaderKey   string
	HeaderValue string
}

// ListMicrovmsInput mirrors the fields used for listing.
type ListMicrovmsInput struct {
	ImageIdentifier *string
}

// MicrovmSummary is a single item from the list response. (Microvms cannot be
// tagged — TagResource rejects them — so ownership is derived from ImageARN.)
type MicrovmSummary struct {
	MicrovmID string
	ImageARN  string
	State     string
	StartedAt time.Time
}

// ListMicrovmsOutput wraps the list of VMs.
type ListMicrovmsOutput struct {
	Items []MicrovmSummary
}

// CreateMicrovmImageInput mirrors CreateMicrovmImageInput fields used by whim.
type CreateMicrovmImageInput struct {
	Name             string
	BaseImageARN     string
	CodeArtifactURI  string
	BuildRoleARN     string
	EgressConnectors []string
}

// CreateMicrovmImageOutput holds the resulting image ARN and version.
type CreateMicrovmImageOutput struct {
	ImageARN     string
	ImageVersion string
	State        string
}

// GetMicrovmImageInput holds the image ARN to look up.
type GetMicrovmImageInput struct {
	ImageIdentifier string
}

// GetMicrovmImageOutput mirrors the fields we consume.
type GetMicrovmImageOutput struct {
	ImageARN                 string
	Name                     string
	State                    string
	LatestActiveImageVersion string
}

// DeleteMicrovmImageInput identifies an image to delete.
type DeleteMicrovmImageInput struct {
	ImageIdentifier string
}

// ListMicrovmImagesInput optionally filters the image listing by name.
type ListMicrovmImagesInput struct {
	NameFilter *string
}

// MicrovmImageSummary is a single image in a list response.
type MicrovmImageSummary struct {
	Name                     string
	ImageARN                 string
	State                    string
	LatestActiveImageVersion string
	CreatedAt                time.Time
}

// ListMicrovmImagesOutput wraps the fully-paginated image list.
type ListMicrovmImagesOutput struct {
	Items []MicrovmImageSummary
}

// IdlePolicy mirrors the SDK IdlePolicy struct.
type IdlePolicy struct {
	AutoResumeEnabled        bool
	MaxIdleDurationSeconds   int32
	SuspendedDurationSeconds int32
}

// API is the narrow interface over lambdamicrovms that all library code uses.
// Swap in a Mock for unit tests; use Client (wrapping the real SDK) for production.
type API interface {
	RunMicrovm(ctx context.Context, in *RunMicrovmInput) (*RunMicrovmOutput, error)
	GetMicrovm(ctx context.Context, in *GetMicrovmInput) (*GetMicrovmOutput, error)
	TerminateMicrovm(ctx context.Context, in *TerminateMicrovmInput) error
	SuspendMicrovm(ctx context.Context, in *SuspendMicrovmInput) error
	ResumeMicrovm(ctx context.Context, in *ResumeMicrovmInput) error
	CreateShellAuthToken(ctx context.Context, in *CreateShellAuthTokenInput) (*CreateShellAuthTokenOutput, error)
	ListMicrovms(ctx context.Context, in *ListMicrovmsInput) (*ListMicrovmsOutput, error)
	CreateMicrovmImage(ctx context.Context, in *CreateMicrovmImageInput) (*CreateMicrovmImageOutput, error)
	GetMicrovmImage(ctx context.Context, in *GetMicrovmImageInput) (*GetMicrovmImageOutput, error)
	DeleteMicrovmImage(ctx context.Context, in *DeleteMicrovmImageInput) error
	ListMicrovmImages(ctx context.Context, in *ListMicrovmImagesInput) (*ListMicrovmImagesOutput, error)
}
