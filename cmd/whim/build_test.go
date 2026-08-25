package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

func TestBuildCmd_Registered(t *testing.T) {
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "build" {
			found = c
		}
	}
	require.NotNil(t, found, "build command must be registered")
	assert.NotNil(t, found.RunE)
}

func TestBuildCmd_RequiresExactlyOneSource(t *testing.T) {
	require.Error(t, buildCmd.Args(buildCmd, []string{}), "zero args rejected")
	require.Error(t, buildCmd.Args(buildCmd, []string{"a", "b"}), "two args rejected")
	require.NoError(t, buildCmd.Args(buildCmd, []string{"./src"}), "exactly one source accepted")
}

func TestBuildFlags_Present(t *testing.T) {
	for _, name := range []string{
		"name", "egress", "egress-connector", "egress-connector-name",
		"egress-subnet", "egress-security-group", "egress-operator-role",
		"egress-auto-provision", "egress-resource-prefix", "egress-strict-dns",
		"egress-vpc-cidr", "egress-subnet-cidr",
		"force", "context-subdir", "json", "privileged",
	} {
		assert.NotNilf(t, buildCmd.Flags().Lookup(name), "build must define --%s", name)
	}
	assert.Equal(t, "public", buildCmd.Flags().Lookup("egress").DefValue, "egress defaults to public")
	assert.Equal(t, "false", buildCmd.Flags().Lookup("egress-auto-provision").DefValue)
	assert.Equal(t, "false", buildCmd.Flags().Lookup("egress-strict-dns").DefValue)
	assert.NotEmpty(t, buildCmd.Flags().Lookup("egress-vpc-cidr").DefValue, "bare --egress-auto-provision must work without extra flags")
	assert.NotEmpty(t, buildCmd.Flags().Lookup("egress-subnet-cidr").DefValue)
}

func TestBuildFlags_NameRequired(t *testing.T) {
	f := buildCmd.Flags().Lookup("name")
	require.NotNil(t, f)
	assert.Contains(t, f.Annotations, cobra.BashCompOneRequiredFlag,
		"--name must be marked required so a missing name fails before any AWS call")
}

func TestBuildEgress_Mapping(t *testing.T) {
	got, err := parseEgress("public")
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressPublic, got)

	got, err = parseEgress("none")
	require.NoError(t, err)
	assert.Equal(t, microvm.EgressNone, got)

	_, err = parseEgress("vpc")
	require.Error(t, err, "unsupported egress must fail (before any AWS call)")
	_, err = parseEgress("")
	require.Error(t, err)
}

func TestBuildImageNetworkSettings_RuntimeNoneBuildsWithPublicEgress(t *testing.T) {
	buildEgress, buildConnector := buildImageNetworkSettings(microvm.EgressNone, "arn:isolated")

	assert.Equal(t, microvm.EgressPublic, buildEgress)
	assert.Empty(t, buildConnector, "the isolated connector is a runtime policy, not an image-build connector")
}

func newTestBuildCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "build"}
	addBuildFlags(cmd)
	return cmd
}

func configureCallerManagedTopology(mock *awsapi.Mock, subnetID, securityGroupID string) {
	mock.DescribeSubnetsFn = func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
		return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: subnetID, VPCID: "vpc-custom"}}}, nil
	}
	mock.GetVPCFn = func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
		return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-custom", CIDRBlock: "10.90.0.0/24"}}, nil
	}
	mock.DescribeRouteTablesFn = func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
		return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
			ID:           "rtb-custom",
			VPCID:        "vpc-custom",
			Associations: []awsapi.RouteTableAssociation{{SubnetID: subnetID}},
			Routes:       []awsapi.Route{{DestinationCIDRBlock: "10.90.0.0/24", GatewayID: "local"}},
		}}}, nil
	}
	mock.DescribeSecurityGroupsFn = func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
		return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: securityGroupID, VPCID: "vpc-custom"}}}, nil
	}
}

func TestResolveBuildEgressConnector_PublicRejectsConnectorFlags(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-connector", "arn:connector"))
	mgr := microvm.NewWithAPI(&awsapi.Mock{}, microvm.WithRegion("us-east-1"))

	_, _, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressPublic)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--egress none")
}

func TestResolveBuildEgressConnector_ReusesExistingActiveConnector(t *testing.T) {
	const connector = "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	mock := &awsapi.Mock{}
	mock.GetNetworkConnectorFn = func(_ context.Context, _ *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		return &awsapi.GetNetworkConnectorOutput{
			ARN: connector, Name: defaultNoEgressConnectorName, State: awsapi.NetworkConnectorStateActive,
			SubnetIDs: []string{"subnet-a"}, SecurityGroupIDs: []string{"sg-a"},
		}, nil
	}
	configureCallerManagedTopology(mock, "subnet-a", "sg-a")
	cmd := newTestBuildCmd()
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"), microvm.WithPollInterval(0))

	got, resources, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.NoError(t, err)
	assert.Equal(t, connector, got)
	require.NotNil(t, resources)
	assert.Equal(t, []string{"subnet-a"}, resources.SubnetIDs)
	assert.Empty(t, mock.CreateNetworkConnectorCalls)
}

func TestResolveBuildEgressConnector_MissingWithoutVpcInputsFails(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetNetworkConnectorFn = func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		return nil, awsapi.ErrNotFound
	}
	cmd := newTestBuildCmd()
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"))

	_, _, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Empty(t, mock.CreateNetworkConnectorCalls)
}

func TestResolveBuildEgressConnector_PublicRejectsNewProvisioningFlags(t *testing.T) {
	for _, flagSet := range []func(*cobra.Command){
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-resource-prefix", "acme")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-strict-dns", "true")) },
	} {
		cmd := newTestBuildCmd()
		flagSet(cmd)
		mgr := microvm.NewWithAPI(&awsapi.Mock{}, microvm.WithRegion("us-east-1"))
		_, _, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressPublic)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--egress none")
	}
}

// --- validateEgressRequirement (Task 4.1) ---

func TestValidateEgressRequirement_Public_NoFlags_OK(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, validateEgressRequirement(cmd, microvm.EgressPublic))
}

// TestValidateEgressRequirement_Public_RejectsConnectorFlags checks that
// --egress public with any egress-* connector/provisioning flag is rejected
// by validateEgressRequirement itself — called right after parseEgress in
// runBuild, before resolveBuildEnv's STS/bootstrap AWS calls — rather than
// only later inside resolveBuildEgressConnector, which runs after those AWS
// calls have already happened.
func TestValidateEgressRequirement_Public_RejectsConnectorFlags(t *testing.T) {
	for _, flagSet := range []func(*cobra.Command){
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-connector", "arn:connector")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-resource-prefix", "acme")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-strict-dns", "true")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-vpc-cidr", "10.0.0.0/24")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-subnet-cidr", "10.0.0.0/25")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role")) },
		func(cmd *cobra.Command) { require.NoError(t, cmd.Flags().Set("egress-connector-name", "foo")) },
	} {
		cmd := newTestBuildCmd()
		flagSet(cmd)
		err := validateEgressRequirement(cmd, microvm.EgressPublic)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--egress none")
	}
}

func TestValidateEgressRequirement_None_Bare_Fails(t *testing.T) {
	cmd := newTestBuildCmd()
	err := validateEgressRequirement(cmd, microvm.EgressNone)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
	assert.Contains(t, err.Error(), "--egress-auto-provision")
}

func TestValidateEgressRequirement_None_WithExplicitConnector_OK(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-connector", "arn:connector"))
	assert.NoError(t, validateEgressRequirement(cmd, microvm.EgressNone))
}

func TestValidateEgressRequirement_None_WithAutoProvision_OK(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
	assert.NoError(t, validateEgressRequirement(cmd, microvm.EgressNone))
}

func TestValidateEgressRequirement_None_RawSubnetSGWithExplicitConnectorName_OK(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-connector-name", "my-connector"))
	require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
	require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
	assert.NoError(t, validateEgressRequirement(cmd, microvm.EgressNone))
}

// TestValidateEgressRequirement_None_RawSubnetSGWithDefaultConnectorName_Fails
// closes the gap where bare "--egress none --egress-subnet ... --egress-security-group ..."
// used to silently depend on the "whim-no-egress" default connector name:
// that default must now be named explicitly.
func TestValidateEgressRequirement_None_RawSubnetSGWithDefaultConnectorName_Fails(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
	require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
	err := validateEgressRequirement(cmd, microvm.EgressNone)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
}

func TestValidateEgressRequirement_None_StrictDNS_Rejected(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-connector", "arn:connector"))
	require.NoError(t, cmd.Flags().Set("egress-strict-dns", "true"))
	err := validateEgressRequirement(cmd, microvm.EgressNone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "egress-strict-dns")
	assert.Contains(t, err.Error(), "not implemented")
}

// TestValidateEgressRequirement_None_ConnectorAndAutoProvisionTogether_Rejected
// checks that combining two connector-selection mechanisms is rejected as
// ambiguous rather than silently picking one.
func TestValidateEgressRequirement_None_ConnectorAndAutoProvisionTogether_Rejected(t *testing.T) {
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-connector", "arn:connector"))
	require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
	err := validateEgressRequirement(cmd, microvm.EgressNone)
	require.Error(t, err)
	assert.ErrorIs(t, err, microvm.ErrInvalidOption)
}

// TestValidateEgressRequirement_None_RejectsForeignFlags covers flags that
// belong to a connector-selection mode *other* than the one chosen — these
// used to pass validation and then be silently ignored by
// resolveBuildEgressConnector, which is exactly the ambiguity this rule
// exists to prevent.
func TestValidateEgressRequirement_None_RejectsForeignFlags(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*cobra.Command)
	}{
		{"auto-provision + raw subnet", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
			require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
		}},
		{"auto-provision + raw security group", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
			require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
		}},
		{"explicit connector + vpc cidr", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-vpc-cidr", "10.0.0.0/24"))
		}},
		{"explicit connector + subnet cidr", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-subnet-cidr", "10.0.0.0/25"))
		}},
		{"explicit connector + resource prefix", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-resource-prefix", "acme"))
		}},
		{"explicit connector + raw subnet", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
		}},
		{"explicit connector + raw security group", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
		}},
		{"explicit connector + connector-name", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-connector-name", "foo"))
		}},
		{"explicit connector + operator role", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector", "arn:x"))
			require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
		}},
		{"raw subnet/SG + resource prefix", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector-name", "foo"))
			require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
			require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
			require.NoError(t, cmd.Flags().Set("egress-resource-prefix", "acme"))
		}},
		{"raw subnet/SG + vpc cidr", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector-name", "foo"))
			require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
			require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
			require.NoError(t, cmd.Flags().Set("egress-vpc-cidr", "10.0.0.0/24"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := newTestBuildCmd()
			c.setup(cmd)
			err := validateEgressRequirement(cmd, microvm.EgressNone)
			require.Error(t, err)
			assert.ErrorIs(t, err, microvm.ErrInvalidOption)
		})
	}
}

// TestValidateEgressRequirement_None_AllowsModeOwnFlags is the converse of
// the foreign-flags test: each mode's own flags, including the ones shared
// between auto-provision and the raw path (--egress-connector-name,
// --egress-operator-role), must still be accepted together.
func TestValidateEgressRequirement_None_AllowsModeOwnFlags(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*cobra.Command)
	}{
		{"auto-provision + resource prefix + cidrs + connector-name + operator role", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
			require.NoError(t, cmd.Flags().Set("egress-resource-prefix", "acme"))
			require.NoError(t, cmd.Flags().Set("egress-vpc-cidr", "10.0.0.0/24"))
			require.NoError(t, cmd.Flags().Set("egress-subnet-cidr", "10.0.0.0/25"))
			require.NoError(t, cmd.Flags().Set("egress-connector-name", "my-connector"))
			require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
		}},
		{"raw subnet/SG + operator role", func(cmd *cobra.Command) {
			require.NoError(t, cmd.Flags().Set("egress-connector-name", "foo"))
			require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
			require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
			require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := newTestBuildCmd()
			c.setup(cmd)
			assert.NoError(t, validateEgressRequirement(cmd, microvm.EgressNone))
		})
	}
}

func TestResolveBuildEgressConnector_CreatesWithVpcInputs(t *testing.T) {
	const connector = "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-egress"
	mock := &awsapi.Mock{}
	lookupCalls := 0
	mock.GetNetworkConnectorFn = func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return nil, awsapi.ErrNotFound
		}
		return &awsapi.GetNetworkConnectorOutput{
			ARN: connector, Name: defaultNoEgressConnectorName, State: awsapi.NetworkConnectorStateActive,
			SubnetIDs: []string{"subnet-a"}, SecurityGroupIDs: []string{"sg-a"},
		}, nil
	}
	mock.CreateNetworkConnectorFn = func(_ context.Context, in *awsapi.CreateNetworkConnectorInput) (*awsapi.CreateNetworkConnectorOutput, error) {
		assert.Equal(t, defaultNoEgressConnectorName, in.Name)
		assert.Equal(t, []string{"subnet-a"}, in.SubnetIDs)
		assert.Equal(t, []string{"sg-a"}, in.SecurityGroupIDs)
		assert.Equal(t, "arn:role", in.OperatorRoleARN)
		return &awsapi.CreateNetworkConnectorOutput{
			ARN:   connector,
			Name:  defaultNoEgressConnectorName,
			State: awsapi.NetworkConnectorStateActive,
		}, nil
	}
	configureCallerManagedTopology(mock, "subnet-a", "sg-a")
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-subnet", "subnet-a"))
	require.NoError(t, cmd.Flags().Set("egress-security-group", "sg-a"))
	require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"))

	got, resources, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.NoError(t, err)
	assert.Equal(t, connector, got)
	require.NotNil(t, resources)
	assert.Equal(t, []string{"subnet-a"}, resources.SubnetIDs)
	require.Len(t, mock.CreateNetworkConnectorCalls, 1)
}

// --- resolveBuildEgressConnector: --egress-auto-provision (Task 4.3) ---

func TestResolveBuildEgressConnector_AutoProvision_ReusesExisting(t *testing.T) {
	mock := &awsapi.Mock{}
	mock.GetNetworkConnectorFn = func(_ context.Context, in *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		assert.Equal(t, "whim-no-public-egress", in.Identifier)
		return &awsapi.GetNetworkConnectorOutput{
			ARN:  "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress",
			Name: "whim-no-public-egress", State: "ACTIVE",
			SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"},
		}, nil
	}
	mock.DescribeSubnetsFn = func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
		return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-1", VPCID: "vpc-1", Tags: map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "WhimResourceGroup": "rg-1"}}}}, nil
	}
	mock.GetVPCFn = func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
		return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-1", CIDRBlock: "10.242.99.0/24", Tags: map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "WhimResourceGroup": "rg-1"}}}, nil
	}
	mock.DescribeRouteTablesFn = func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
		return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
			ID: "rtb-1", VPCID: "vpc-1", Tags: map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "WhimResourceGroup": "rg-1"},
			Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-1"}},
			Routes:       []awsapi.Route{{DestinationCIDRBlock: "10.242.99.0/24", GatewayID: "local"}},
		}}}, nil
	}
	mock.DescribeSecurityGroupsFn = func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
		return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: "sg-1", VPCID: "vpc-1", Tags: map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "WhimResourceGroup": "rg-1"}}}}, nil
	}
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"))

	got, resources, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", got)
	require.NotNil(t, resources)
	assert.Equal(t, "vpc-1", resources.VPCID)
	assert.Equal(t, "sg-1", resources.SecurityGroupID)
	assert.Empty(t, mock.CreateVPCCalls, "reuse must never create a second resource group")
}

func TestResolveBuildEgressConnector_AutoProvision_CreatesWithDefaultCIDRs(t *testing.T) {
	mock := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
		DescribeAvailabilityZonesFn: func(context.Context) (*awsapi.DescribeAvailabilityZonesOutput, error) {
			return &awsapi.DescribeAvailabilityZonesOutput{Items: []awsapi.AvailabilityZone{{ZoneName: "us-east-1a", State: "available"}}}, nil
		},
		CreateVPCFn: func(_ context.Context, in *awsapi.CreateVPCInput) (*awsapi.CreateVPCOutput, error) {
			assert.NotEmpty(t, in.CIDRBlock, "bare --egress-auto-provision must supply a default VPC CIDR")
			return &awsapi.CreateVPCOutput{VPCID: "vpc-new"}, nil
		},
		CreateSubnetFn: func(_ context.Context, in *awsapi.CreateSubnetInput) (*awsapi.CreateSubnetOutput, error) {
			assert.NotEmpty(t, in.CIDRBlock, "bare --egress-auto-provision must supply a default subnet CIDR")
			return &awsapi.CreateSubnetOutput{SubnetID: "subnet-new"}, nil
		},
		CreateRouteTableFn: func(context.Context, *awsapi.CreateRouteTableInput) (*awsapi.CreateRouteTableOutput, error) {
			return &awsapi.CreateRouteTableOutput{RouteTableID: "rtb-new"}, nil
		},
		AssociateRouteTableFn: func(context.Context, *awsapi.AssociateRouteTableInput) (*awsapi.AssociateRouteTableOutput, error) {
			return &awsapi.AssociateRouteTableOutput{}, nil
		},
		CreateSecurityGroupFn: func(context.Context, *awsapi.CreateSecurityGroupInput) (*awsapi.CreateSecurityGroupOutput, error) {
			return &awsapi.CreateSecurityGroupOutput{SecurityGroupID: "sg-new"}, nil
		},
		RevokeAllSecurityGroupEgressFn: func(context.Context, *awsapi.RevokeAllSecurityGroupEgressInput) error { return nil },
		CreateNetworkConnectorFn: func(_ context.Context, in *awsapi.CreateNetworkConnectorInput) (*awsapi.CreateNetworkConnectorOutput, error) {
			assert.Equal(t, "arn:role", in.OperatorRoleARN)
			return &awsapi.CreateNetworkConnectorOutput{ARN: "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", Name: "whim-no-public-egress", State: "ACTIVE"}, nil
		},
		GetRoleFn: func(_ context.Context, in *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
			return &awsapi.GetRoleOutput{ARN: "arn:role", AssumeRolePolicyDocument: `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`}, nil
		},
		ListAttachedRolePoliciesFn: func(context.Context, *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
			return &awsapi.ListAttachedRolePoliciesOutput{Items: []awsapi.AttachedPolicy{{PolicyARN: "arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"}}}, nil
		},
		ListRolePoliciesFn: func(context.Context, *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
			return &awsapi.ListRolePoliciesOutput{}, nil
		},
	}
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
	require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"), microvm.WithPollInterval(time.Millisecond))

	got, resources, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", got)
	require.NotNil(t, resources)
	assert.Equal(t, "vpc-new", resources.VPCID)
	require.Len(t, mock.CreateVPCCalls, 1)
}

func TestResolveBuildEgressConnector_AutoProvision_RespectsCIDROverrides(t *testing.T) {
	mock := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
		CreateVPCFn: func(_ context.Context, in *awsapi.CreateVPCInput) (*awsapi.CreateVPCOutput, error) {
			assert.Equal(t, "192.168.50.0/24", in.CIDRBlock)
			return &awsapi.CreateVPCOutput{VPCID: "vpc-new"}, nil
		},
		DescribeAvailabilityZonesFn: func(context.Context) (*awsapi.DescribeAvailabilityZonesOutput, error) {
			return &awsapi.DescribeAvailabilityZonesOutput{Items: []awsapi.AvailabilityZone{{ZoneName: "us-east-1a", State: "available"}}}, nil
		},
		CreateSubnetFn: func(_ context.Context, in *awsapi.CreateSubnetInput) (*awsapi.CreateSubnetOutput, error) {
			assert.Equal(t, "192.168.50.0/25", in.CIDRBlock)
			return nil, fmt.Errorf("stop test here")
		},
	}
	cmd := newTestBuildCmd()
	require.NoError(t, cmd.Flags().Set("egress-auto-provision", "true"))
	require.NoError(t, cmd.Flags().Set("egress-operator-role", "arn:role"))
	require.NoError(t, cmd.Flags().Set("egress-vpc-cidr", "192.168.50.0/24"))
	require.NoError(t, cmd.Flags().Set("egress-subnet-cidr", "192.168.50.0/25"))
	mgr := microvm.NewWithAPI(mock, microvm.WithRegion("us-east-1"))
	// Reuse the valid-role mock helper defined for the default-CIDR test's
	// sibling cases would duplicate setup; assert.Equal above already
	// verifies what this test exists for (CIDR plumbing) before the
	// deliberate stop error short-circuits the rest of creation.
	mock.GetRoleFn = func(context.Context, *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
		return &awsapi.GetRoleOutput{ARN: "arn:role", AssumeRolePolicyDocument: `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`}, nil
	}
	mock.ListAttachedRolePoliciesFn = func(context.Context, *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
		return &awsapi.ListAttachedRolePoliciesOutput{Items: []awsapi.AttachedPolicy{{PolicyARN: "arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"}}}, nil
	}
	mock.ListRolePoliciesFn = func(context.Context, *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
		return &awsapi.ListRolePoliciesOutput{}, nil
	}

	_, _, err := resolveBuildEgressConnector(context.Background(), cmd, mgr, microvm.EgressNone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop test here")
}
