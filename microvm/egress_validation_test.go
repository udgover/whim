package microvm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

const (
	testNoPublicConnector = "arn:aws:lambda:us-east-1:123456789012:network-connector:custom-isolated"
	testNoPublicVPCCIDR   = "10.90.0.0/24"
)

func safeNoPublicEgressMock() *awsapi.Mock {
	mock := &awsapi.Mock{}
	configureSafeNoPublicEgressMock(mock)
	return mock
}

func configureSafeNoPublicEgressMock(mock *awsapi.Mock) {
	mock.GetNetworkConnectorFn = func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
		return &awsapi.GetNetworkConnectorOutput{
			ARN:              testNoPublicConnector,
			Name:             "custom-isolated",
			State:            awsapi.NetworkConnectorStateActive,
			SubnetIDs:        []string{"subnet-safe"},
			SecurityGroupIDs: []string{"sg-safe"},
		}, nil
	}
	mock.DescribeSubnetsFn = func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
		return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-safe", VPCID: "vpc-safe"}}}, nil
	}
	mock.GetVPCFn = func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
		return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-safe", CIDRBlock: testNoPublicVPCCIDR}}, nil
	}
	mock.DescribeRouteTablesFn = func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
		return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
			ID:           "rtb-safe",
			VPCID:        "vpc-safe",
			Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-safe"}},
			Routes:       []awsapi.Route{{DestinationCIDRBlock: testNoPublicVPCCIDR, GatewayID: "local"}},
		}}}, nil
	}
	mock.DescribeSecurityGroupsFn = func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
		return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: "sg-safe", VPCID: "vpc-safe"}}}, nil
	}
}

func TestValidateNoPublicEgressConnector_AcceptsSafeCallerManagedTopology(t *testing.T) {
	resources, err := newTestManager(safeNoPublicEgressMock()).ValidateNoPublicEgressConnector(
		context.Background(),
		microvm.NoPublicEgressResources{ConnectorARN: testNoPublicConnector},
	)

	require.NoError(t, err)
	assert.Equal(t, "vpc-safe", resources.VPCID)
	assert.Equal(t, []string{"subnet-safe"}, resources.SubnetIDs)
	assert.Equal(t, []string{"rtb-safe"}, resources.RouteTableIDs)
	assert.Equal(t, []string{"sg-safe"}, resources.SecurityGroupIDs)
}

func TestValidateNoPublicEgressConnector_RejectsExpectedTopologyMismatch(t *testing.T) {
	expected := microvm.NoPublicEgressResources{
		ConnectorARN:     testNoPublicConnector,
		SubnetIDs:        []string{"subnet-expected"},
		SecurityGroupIDs: []string{"sg-safe"},
	}

	_, err := newTestManager(safeNoPublicEgressMock()).ValidateNoPublicEgressConnector(context.Background(), expected)

	require.ErrorIs(t, err, microvm.ErrTopologyMismatch)
}

func TestValidateNoPublicEgressConnector_RejectsMismatchedEC2ResponseIDs(t *testing.T) {
	mock := safeNoPublicEgressMock()
	mock.DescribeSecurityGroupsFn = func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
		return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: "sg-other", VPCID: "vpc-safe"}}}, nil
	}

	_, err := newTestManager(mock).ValidateNoPublicEgressConnector(
		context.Background(), microvm.NoPublicEgressResources{ConnectorARN: testNoPublicConnector},
	)

	require.ErrorIs(t, err, microvm.ErrTopologyMismatch)
}

func TestValidateNoPublicEgressConnector_RejectsPublicRoute(t *testing.T) {
	mock := safeNoPublicEgressMock()
	mock.DescribeRouteTablesFn = func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
		return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
			ID:           "rtb-public",
			VPCID:        "vpc-safe",
			Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-safe"}},
			Routes: []awsapi.Route{
				{DestinationCIDRBlock: testNoPublicVPCCIDR, GatewayID: "local"},
				{DestinationCIDRBlock: "0.0.0.0/0", NatGatewayID: "nat-public"},
			},
		}}}, nil
	}

	_, err := newTestManager(mock).ValidateNoPublicEgressConnector(
		context.Background(), microvm.NoPublicEgressResources{ConnectorARN: testNoPublicConnector},
	)

	require.ErrorIs(t, err, microvm.ErrPublicRoute)
}

func TestValidateNoPublicEgressConnector_PrefersExplicitSubnetRouteTableOverMain(t *testing.T) {
	mock := safeNoPublicEgressMock()
	mock.DescribeRouteTablesFn = func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
		return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{
			{
				ID:           "rtb-main-safe",
				VPCID:        "vpc-safe",
				Associations: []awsapi.RouteTableAssociation{{Main: true}},
				Routes:       []awsapi.Route{{DestinationCIDRBlock: testNoPublicVPCCIDR, GatewayID: "local"}},
			},
			{
				ID:           "rtb-explicit-public",
				VPCID:        "vpc-safe",
				Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-safe"}},
				Routes: []awsapi.Route{
					{DestinationCIDRBlock: testNoPublicVPCCIDR, GatewayID: "local"},
					{DestinationCIDRBlock: "0.0.0.0/0", NatGatewayID: "nat-public"},
				},
			},
		}}, nil
	}

	_, err := newTestManager(mock).ValidateNoPublicEgressConnector(
		context.Background(), microvm.NoPublicEgressResources{ConnectorARN: testNoPublicConnector},
	)

	require.ErrorIs(t, err, microvm.ErrPublicRoute)
}

func TestEnsureNetworkConnector_ExistingConnectorMustMatchSuppliedTopology(t *testing.T) {
	mock := safeNoPublicEgressMock()

	_, err := newTestManager(mock).EnsureNetworkConnector(context.Background(), microvm.NetworkConnectorSpec{
		Name:             "custom-isolated",
		SubnetIDs:        []string{"subnet-requested"},
		SecurityGroupIDs: []string{"sg-safe"},
	})

	require.ErrorIs(t, err, microvm.ErrTopologyMismatch)
	assert.Empty(t, mock.CreateNetworkConnectorCalls)
}
