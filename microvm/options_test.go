package microvm_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/udgover/whim/microvm"
)

// --- WithTTL ---

func TestWithTTL_DefaultIs25m(t *testing.T) {
	cfg := microvm.DefaultLaunchConfig()
	assert.Equal(t, 25*time.Minute, cfg.TTL)
}

func TestWithTTL_Zero_Errors(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithTTL(0))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestWithTTL_Negative_Errors(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithTTL(-1 * time.Second))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestWithTTL_ExactCap_Accepted(t *testing.T) {
	cfg, err := microvm.ApplyLaunchOptions(microvm.WithTTL(8 * time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 8*time.Hour, cfg.TTL)
}

func TestWithTTL_OverCap_Errors(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithTTL(8*time.Hour + time.Second))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestWithTTL_ValidDuration_Set(t *testing.T) {
	cfg, err := microvm.ApplyLaunchOptions(microvm.WithTTL(45 * time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, cfg.TTL)
}

// --- WithIngress ---

func TestDefaultIngress_NoOverride(t *testing.T) {
	// Default = no override; SHELL_INGRESS is resolved (region-derived) at launch.
	cfg := microvm.DefaultLaunchConfig()
	assert.Empty(t, cfg.IngressOverride)
}

func TestWithIngress_SetsOverride(t *testing.T) {
	custom := "arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:ALL_INGRESS"
	cfg, err := microvm.ApplyLaunchOptions(microvm.WithIngress(custom))
	require.NoError(t, err)
	assert.Equal(t, custom, cfg.IngressOverride)
}

// --- WithEgress ---

func TestWithEgress_NoneRequiresConnector(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithEgress(microvm.EgressNone))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestWithEgress_DefaultInheritsImage(t *testing.T) {
	cfg := microvm.DefaultLaunchConfig()
	assert.False(t, cfg.EgressExplicit)
}

func TestWithEgress_Public(t *testing.T) {
	cfg, err := microvm.ApplyLaunchOptions(microvm.WithEgress(microvm.EgressPublic))
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressPublic, cfg.Egress)
	assert.True(t, cfg.EgressExplicit)
}

func TestWithEgress_VPCRequiresConnector(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithEgress(microvm.EgressVPC))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestWithEgressConnector_None(t *testing.T) {
	const connector = "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	cfg, err := microvm.ApplyLaunchOptions(microvm.WithEgressConnector(microvm.EgressNone, connector))
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressNone, cfg.Egress)
	assert.Equal(t, connector, cfg.EgressConnectorARN)
	assert.True(t, cfg.EgressExplicit)
}

func TestWithExpectedNoPublicEgress_RecordsValidationWithoutOverridingImage(t *testing.T) {
	expected := microvm.NoPublicEgressResources{
		ConnectorARN:     testNoPublicConnector,
		SubnetIDs:        []string{"subnet-safe"},
		SecurityGroupIDs: []string{"sg-safe"},
	}

	cfg, err := microvm.ApplyLaunchOptions(microvm.WithExpectedNoPublicEgress(expected))

	require.NoError(t, err)
	require.NotNil(t, cfg.ExpectedNoPublicEgress)
	assert.Equal(t, expected, *cfg.ExpectedNoPublicEgress)
	assert.False(t, cfg.EgressExplicit, "validation must not replace the connector baked into image metadata")
}

func TestWithEgressConnector_PublicErrors(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithEgressConnector(microvm.EgressPublic, "arn:custom"))
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

// --- Composition ---

func TestOptions_ComposeCorrectly(t *testing.T) {
	cfg, err := microvm.ApplyLaunchOptions(
		microvm.WithTTL(30*time.Minute),
		microvm.WithIngress("arn:custom"),
		microvm.WithEgressConnector(microvm.EgressNone, "arn:connector"),
	)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, cfg.TTL)
	assert.Equal(t, "arn:custom", cfg.IngressOverride)
	assert.Equal(t, microvm.EgressNone, cfg.Egress)
	assert.Equal(t, "arn:connector", cfg.EgressConnectorARN)
}

func TestApplyLaunchOptions_ReturnsFirstError(t *testing.T) {
	// A later valid option must not mask an earlier invalid one.
	_, err := microvm.ApplyLaunchOptions(
		microvm.WithTTL(0),
		microvm.WithEgress(microvm.EgressNone),
	)
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
}

// --- WithLogger ---

func TestWithLogger_AcceptsNilLogger(t *testing.T) {
	_, err := microvm.ApplyLaunchOptions(microvm.WithLogger(nil))
	require.NoError(t, err)
}

func TestWithLogger_AcceptsRealLogger(t *testing.T) {
	logger := slog.Default()
	_, err := microvm.ApplyLaunchOptions(microvm.WithLogger(logger))
	require.NoError(t, err)
}
