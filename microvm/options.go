package microvm

import (
	"fmt"
	"log/slog"
	"time"
)

const (
	maxTTL = 8 * time.Hour
	// defaultTTL is below the default shell-token lifetime (~30 min) so a
	// session never outlives its usable shell without reconnect support.
	defaultTTL = 25 * time.Minute
)

// EgressMode controls the outbound network policy, fixed at image-build time
// and asserted at launch.
type EgressMode int

const (
	// EgressPublic enables outbound internet access via the managed INTERNET_EGRESS connector.
	EgressPublic EgressMode = iota
	// EgressNone produces an airgapped VM with no outbound network connectivity.
	EgressNone
	// EgressVPC routes outbound traffic through a customer-managed VPC connector (v0.2).
	EgressVPC
)

// Resources specifies the baseline vCPU/memory allocation for a MicroVM.
// The service may burst to 4x the baseline during peak activity.
type Resources struct {
	MinimumMemoryMiB int32
}

// IdlePolicy controls automatic suspend/resume behaviour.
type IdlePolicy struct {
	AutoResumeEnabled        bool
	MaxIdleDurationSeconds   int32
	SuspendedDurationSeconds int32
}

// LaunchConfig is the resolved intent for a single Manager.Launch call.
// Build it via ApplyLaunchOptions; do not construct directly. Connector ARNs
// are derived from the Manager's region at launch time, not stored here.
type LaunchConfig struct {
	TTL    time.Duration
	Egress EgressMode
	// IngressOverride, when non-empty, replaces the default SHELL_INGRESS
	// connector; the default (empty) is resolved to a region-specific
	// SHELL_INGRESS ARN at launch.
	IngressOverride string
	Resources       *Resources
	IdlePolicy      *IdlePolicy
	Logger          *slog.Logger
}

// DefaultLaunchConfig returns a LaunchConfig with all safe defaults applied.
func DefaultLaunchConfig() LaunchConfig {
	return LaunchConfig{
		TTL:    defaultTTL,
		Egress: EgressPublic,
	}
}

// LaunchOption is a functional option for Launch.
type LaunchOption func(*LaunchConfig) error

// ApplyLaunchOptions starts from DefaultLaunchConfig, applies all opts in
// order, and returns the resolved config or the first validation error.
func ApplyLaunchOptions(opts ...LaunchOption) (LaunchConfig, error) {
	cfg := DefaultLaunchConfig()
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return LaunchConfig{}, err
		}
	}
	return cfg, nil
}

// WithTTL sets the server-side maximum duration for the MicroVM.
// Must be > 0 and ≤ 8 hours. The default is 25 minutes.
func WithTTL(d time.Duration) LaunchOption {
	return func(cfg *LaunchConfig) error {
		if d <= 0 {
			return fmt.Errorf("%w: TTL must be > 0, got %s", ErrInvalidOption, d)
		}
		if d > maxTTL {
			return fmt.Errorf("%w: TTL must be ≤ %s, got %s", ErrInvalidOption, maxTTL, d)
		}
		cfg.TTL = d
		return nil
	}
}

// WithIngress overrides the default SHELL_INGRESS connector with the given ARN.
func WithIngress(connectorARN string) LaunchOption {
	return func(cfg *LaunchConfig) error {
		cfg.IngressOverride = connectorARN
		return nil
	}
}

// WithEgress sets the egress policy. EgressNone produces an airgapped VM.
// The actual connector ARNs are derived from the region at launch.
// EgressVPC is not yet supported (v0.2).
func WithEgress(mode EgressMode) LaunchOption {
	return func(cfg *LaunchConfig) error {
		switch mode {
		case EgressNone, EgressPublic:
			cfg.Egress = mode
			return nil
		case EgressVPC:
			return fmt.Errorf("%w: EgressVPC is not supported in v0.1", ErrInvalidOption)
		default:
			return fmt.Errorf("%w: unknown egress mode %d", ErrInvalidOption, mode)
		}
	}
}

// WithResources sets the baseline resource allocation.
func WithResources(r Resources) LaunchOption {
	return func(cfg *LaunchConfig) error {
		cfg.Resources = &r
		return nil
	}
}

// WithIdlePolicy configures automatic suspend/resume behaviour.
func WithIdlePolicy(p IdlePolicy) LaunchOption {
	return func(cfg *LaunchConfig) error {
		cfg.IdlePolicy = &p
		return nil
	}
}

// WithLogger sets an optional structured logger. Passing nil is a no-op.
// The library never logs shell tokens.
func WithLogger(l *slog.Logger) LaunchOption {
	return func(cfg *LaunchConfig) error {
		cfg.Logger = l
		return nil
	}
}

// ImageSpec describes a MicroVM image to build or look up.
type ImageSpec struct {
	// Name is the unique image name within the AWS account.
	Name string
	// BaseImageARN is the managed base image ARN (from ListManagedMicrovmImages).
	BaseImageARN string
	// CodeArtifactURI is the s3://bucket/key URI of the zipped Dockerfile.
	CodeArtifactURI string
	// BuildRoleARN is the IAM role ARN Lambda assumes during the build.
	BuildRoleARN string
	// Egress controls which egress connectors are baked into the image.
	// This is fixed at build time and inherited by every run.
	Egress EgressMode
	// Resources sets the baseline memory allocation for the image.
	Resources *Resources
}
