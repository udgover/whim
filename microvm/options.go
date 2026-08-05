package microvm

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	maxTTL = 8 * time.Hour
	// defaultTTL is the server-side cleanup backstop for ephemeral VMs (shell/run/
	// exec): short by design, so a forgotten or crashed client doesn't leave a
	// billed VM running. It is not related to the shell-token lifetime — an
	// established shell connection is never re-validated against its token and
	// can outlive it fine (see ShellTokenLifetime's doc comment).
	defaultTTL = 25 * time.Minute
)

// EgressMode controls the outbound connector set Whim records for an image and
// applies when launching a MicroVM. AWS associates network connectors at
// run-microvm time.
type EgressMode int

const (
	// EgressPublic enables outbound internet access via the managed INTERNET_EGRESS connector.
	EgressPublic EgressMode = iota
	// EgressNone routes through a caller-managed isolated VPC connector. AWS
	// does not currently expose a managed NO_EGRESS connector, so the connector's
	// VPC routing and security controls must deny public internet access.
	EgressNone
	// EgressVPC routes outbound traffic through a customer-managed VPC connector.
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
	TTL time.Duration
	// Egress is applied only when EgressExplicit is true. Otherwise Launch
	// derives egress from the image's latest active version.
	Egress             EgressMode
	EgressConnectorARN string
	EgressExplicit     bool
	// ExpectedNoPublicEgress validates the connector baked into image metadata
	// without replacing it. This lets callers fail closed on topology drift while
	// preserving the image-version connector as the launch source of truth.
	ExpectedNoPublicEgress *NoPublicEgressResources
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

// WithEgress sets the egress policy. EgressPublic uses the AWS-managed
// INTERNET_EGRESS connector. EgressNone and EgressVPC require
// WithEgressConnector because AWS does not expose a managed NO_EGRESS connector.
func WithEgress(mode EgressMode) LaunchOption {
	return func(cfg *LaunchConfig) error {
		switch mode {
		case EgressPublic:
			cfg.Egress = mode
			cfg.EgressConnectorARN = ""
			cfg.EgressExplicit = true
			return nil
		case EgressNone:
			return fmt.Errorf("%w: EgressNone requires WithEgressConnector with an isolated VPC network connector ARN", ErrInvalidOption)
		case EgressVPC:
			return fmt.Errorf("%w: EgressVPC requires WithEgressConnector with a VPC network connector ARN", ErrInvalidOption)
		default:
			return fmt.Errorf("%w: unknown egress mode %d", ErrInvalidOption, mode)
		}
	}
}

// WithEgressConnector routes runtime egress through a caller-managed Lambda Core
// network connector ARN. For EgressNone semantics, the referenced VPC subnets,
// route tables, security groups, and NACLs must deny public internet paths.
func WithEgressConnector(mode EgressMode, connectorARN string) LaunchOption {
	return func(cfg *LaunchConfig) error {
		connectorARN = strings.TrimSpace(connectorARN)
		if connectorARN == "" {
			return fmt.Errorf("%w: egress connector ARN is required", ErrInvalidOption)
		}
		switch mode {
		case EgressNone, EgressVPC:
			cfg.Egress = mode
			cfg.EgressConnectorARN = connectorARN
			cfg.EgressExplicit = true
			return nil
		case EgressPublic:
			return fmt.Errorf("%w: EgressPublic uses the managed INTERNET_EGRESS connector, not a custom connector", ErrInvalidOption)
		default:
			return fmt.Errorf("%w: unknown egress mode %d", ErrInvalidOption, mode)
		}
	}
}

// WithExpectedNoPublicEgress requires the image's baked connector to match
// resources.ConnectorARN, then revalidates its live VPC topology before launch.
// It does not override image metadata. Populate the other resource fields to
// detect identifier drift as well as unsafe route/security-group changes.
func WithExpectedNoPublicEgress(resources NoPublicEgressResources) LaunchOption {
	return func(cfg *LaunchConfig) error {
		resources.ConnectorARN = strings.TrimSpace(resources.ConnectorARN)
		if resources.ConnectorARN == "" {
			return fmt.Errorf("%w: expected no-public-egress connector ARN is required", ErrInvalidOption)
		}
		if isInternetEgressConnector(resources.ConnectorARN) {
			return fmt.Errorf("%w: EgressNone cannot use the managed INTERNET_EGRESS connector", ErrInvalidOption)
		}
		copy := resources
		copy.SubnetIDs = append([]string(nil), resources.SubnetIDs...)
		copy.RouteTableIDs = append([]string(nil), resources.RouteTableIDs...)
		copy.SecurityGroupIDs = append([]string(nil), resources.SecurityGroupIDs...)
		cfg.ExpectedNoPublicEgress = &copy
		return nil
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
	// Egress controls which egress connectors Whim records for the image and
	// mirrors at launch time.
	Egress EgressMode
	// EgressConnectorARN is required for EgressNone or EgressVPC. For
	// EgressNone semantics, it must point at a VPC connector whose networking
	// denies public internet paths.
	EgressConnectorARN string
	// Resources sets the baseline memory allocation for the image.
	Resources *Resources
	// Capabilities grants elevated Linux OS capabilities to the image's
	// runtime environment (AWS additionalOsCapabilities). Fixed at build time.
	// AWS supports exactly one value today: CapabilityAll. Leave nil for the
	// default, minimal-capability image.
	Capabilities []Capability
}

// Capability is an elevated Linux OS capability granted to a MicroVM image via
// AWS's additionalOsCapabilities. Modelled as a slice-friendly string type so
// the surface stays forward-compatible if AWS adds finer-grained values.
type Capability string

// CapabilityAll grants all available OS capabilities to the image runtime,
// enabling privileged operations such as mounting filesystems, creating network
// namespaces, and running eBPF programs. It is the only value AWS supports today.
const CapabilityAll Capability = "ALL"
