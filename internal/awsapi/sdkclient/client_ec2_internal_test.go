package sdkclient

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

func TestMapTags(t *testing.T) {
	assert.Nil(t, mapTags(nil))
	assert.Equal(t, map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress"}, mapTags([]ec2types.Tag{
		{Key: aws.String("ManagedBy"), Value: aws.String("whim")},
		{Key: aws.String("Purpose"), Value: aws.String("no-public-egress")},
	}))
}

func TestMapVPC(t *testing.T) {
	got := mapVPC(ec2types.Vpc{
		VpcId:     aws.String("vpc-1"),
		CidrBlock: aws.String("10.99.0.0/24"),
		Tags:      []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("whim-no-public-egress-vpc")}},
	})
	assert.Equal(t, awsapi.VPC{
		ID:        "vpc-1",
		CIDRBlock: "10.99.0.0/24",
		Tags:      map[string]string{"Name": "whim-no-public-egress-vpc"},
	}, got)
}

func TestMapSubnet(t *testing.T) {
	got := mapSubnet(ec2types.Subnet{
		SubnetId:         aws.String("subnet-1"),
		VpcId:            aws.String("vpc-1"),
		AvailabilityZone: aws.String("us-east-1a"),
		CidrBlock:        aws.String("10.99.0.0/25"),
	})
	assert.Equal(t, awsapi.Subnet{
		ID:               "subnet-1",
		VPCID:            "vpc-1",
		AvailabilityZone: "us-east-1a",
		CIDRBlock:        "10.99.0.0/25",
	}, got)
}

// TestMapRoute_LocalOnly checks the shape of the single route Whim allows:
// a local VPC route with no public-capable target populated.
func TestMapRoute_LocalOnly(t *testing.T) {
	got := mapRoute(ec2types.Route{
		DestinationCidrBlock: aws.String("10.99.0.0/24"),
		GatewayId:            aws.String("local"),
		State:                ec2types.RouteStateActive,
	})
	assert.Equal(t, awsapi.Route{
		DestinationCIDRBlock: "10.99.0.0/24",
		GatewayID:            "local",
		State:                "active",
	}, got)
}

// TestMapRoute_AllTargetFields checks that every route-target field the EC2
// Route type can populate is carried across the awsapi boundary — not just
// the well-known public-capable ones — since Milestone 2 validation rejects
// anything that isn't an exact local-only route rather than enumerating
// which target types count as "public-capable".
func TestMapRoute_AllTargetFields(t *testing.T) {
	got := mapRoute(ec2types.Route{
		DestinationCidrBlock:        aws.String("0.0.0.0/0"),
		DestinationIpv6CidrBlock:    aws.String("::/0"),
		DestinationPrefixListId:     aws.String("pl-1"),
		GatewayId:                   aws.String("igw-1"),
		NatGatewayId:                aws.String("nat-1"),
		TransitGatewayId:            aws.String("tgw-1"),
		VpcPeeringConnectionId:      aws.String("pcx-1"),
		EgressOnlyInternetGatewayId: aws.String("eigw-1"),
		CarrierGatewayId:            aws.String("cagw-1"),
		InstanceId:                  aws.String("i-1"),
		NetworkInterfaceId:          aws.String("eni-1"),
		LocalGatewayId:              aws.String("lgw-1"),
		CoreNetworkArn:              aws.String("arn:aws:networkmanager::123456789012:core-network/core-1"),
		IpAddress:                   aws.String("10.0.0.9"),
		OdbNetworkArn:               aws.String("arn:aws:odb:us-east-1:123456789012:network/odb-1"),
		State:                       ec2types.RouteStateActive,
	})
	assert.Equal(t, awsapi.Route{
		DestinationCIDRBlock:        "0.0.0.0/0",
		DestinationIPv6CIDRBlock:    "::/0",
		DestinationPrefixListID:     "pl-1",
		GatewayID:                   "igw-1",
		NatGatewayID:                "nat-1",
		TransitGatewayID:            "tgw-1",
		VPCPeeringConnectionID:      "pcx-1",
		EgressOnlyInternetGatewayID: "eigw-1",
		CarrierGatewayID:            "cagw-1",
		InstanceID:                  "i-1",
		NetworkInterfaceID:          "eni-1",
		LocalGatewayID:              "lgw-1",
		CoreNetworkARN:              "arn:aws:networkmanager::123456789012:core-network/core-1",
		IPAddress:                   "10.0.0.9",
		ODBNetworkARN:               "arn:aws:odb:us-east-1:123456789012:network/odb-1",
		State:                       "active",
	}, got)
}

func TestMapRouteTable(t *testing.T) {
	got := mapRouteTable(ec2types.RouteTable{
		RouteTableId: aws.String("rtb-1"),
		VpcId:        aws.String("vpc-1"),
		Routes: []ec2types.Route{
			{DestinationCidrBlock: aws.String("10.99.0.0/24"), GatewayId: aws.String("local")},
		},
		Associations: []ec2types.RouteTableAssociation{
			{SubnetId: aws.String("subnet-1"), Main: aws.Bool(false)},
			{Main: aws.Bool(true)},
		},
		Tags: []ec2types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("whim")}},
	})
	assert.Equal(t, awsapi.RouteTable{
		ID:    "rtb-1",
		VPCID: "vpc-1",
		Routes: []awsapi.Route{
			{DestinationCIDRBlock: "10.99.0.0/24", GatewayID: "local"},
		},
		Associations: []awsapi.RouteTableAssociation{
			{SubnetID: "subnet-1", Main: false},
			{SubnetID: "", Main: true},
		},
		Tags: map[string]string{"ManagedBy": "whim"},
	}, got)
}

// TestMapSecurityGroup_NoEgress checks the allowed shape: zero egress rules.
func TestMapSecurityGroup_NoEgress(t *testing.T) {
	got := mapSecurityGroup(ec2types.SecurityGroup{
		GroupId:   aws.String("sg-1"),
		VpcId:     aws.String("vpc-1"),
		GroupName: aws.String("whim-no-public-egress-sg"),
	})
	assert.Empty(t, got.EgressRules)
	assert.Equal(t, "sg-1", got.ID)
}

// TestMapSecurityGroup_ExpandsEgressTargets checks that every distinct
// egress rule target (CIDR, IPv6 CIDR, prefix list, referenced group) becomes
// its own SecurityGroupRule entry sharing the parent permission's protocol
// and ports, so validation can report exactly what must be removed.
func TestMapSecurityGroup_ExpandsEgressTargets(t *testing.T) {
	got := mapSecurityGroup(ec2types.SecurityGroup{
		GroupId: aws.String("sg-1"),
		VpcId:   aws.String("vpc-1"),
		IpPermissionsEgress: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("-1"),
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
				Ipv6Ranges: []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}},
			},
			{
				IpProtocol:    aws.String("tcp"),
				FromPort:      aws.Int32(443),
				ToPort:        aws.Int32(443),
				PrefixListIds: []ec2types.PrefixListId{{PrefixListId: aws.String("pl-1")}},
				UserIdGroupPairs: []ec2types.UserIdGroupPair{
					{GroupId: aws.String("sg-2")},
				},
			},
		},
	})
	assert.ElementsMatch(t, []awsapi.SecurityGroupRule{
		{IPProtocol: "-1", CIDRIPv4: "0.0.0.0/0"},
		{IPProtocol: "-1", CIDRIPv6: "::/0"},
		{IPProtocol: "tcp", FromPort: aws.Int32(443), ToPort: aws.Int32(443), PrefixListID: "pl-1"},
		{IPProtocol: "tcp", FromPort: aws.Int32(443), ToPort: aws.Int32(443), ReferencedGroupID: "sg-2"},
	}, got.EgressRules)
}

func TestMapNetworkACL(t *testing.T) {
	got := mapNetworkACL(ec2types.NetworkAcl{
		NetworkAclId: aws.String("acl-1"),
		VpcId:        aws.String("vpc-1"),
		IsDefault:    aws.Bool(false),
		Associations: []ec2types.NetworkAclAssociation{
			{SubnetId: aws.String("subnet-1")},
		},
		Tags: []ec2types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("whim")}},
	})
	assert.Equal(t, awsapi.NetworkACL{
		ID:           "acl-1",
		VPCID:        "vpc-1",
		IsDefault:    false,
		Associations: []awsapi.NetworkACLAssociation{{SubnetID: "subnet-1"}},
		Tags:         map[string]string{"ManagedBy": "whim"},
	}, got)
}

func TestMapAvailabilityZone(t *testing.T) {
	got := mapAvailabilityZone(ec2types.AvailabilityZone{
		ZoneName: aws.String("us-east-1a"),
		State:    ec2types.AvailabilityZoneStateAvailable,
	})
	assert.Equal(t, awsapi.AvailabilityZone{ZoneName: "us-east-1a", State: "available"}, got)
}

// TestRouteTableFilters and TestNetworkACLFilters check the EC2 filter
// construction Whim relies on to find the route table / NACL governing a
// specific managed subnet, since that's how Task 2.2/2.3 validation looks
// them up.
func TestRouteTableFilters(t *testing.T) {
	got := routeTableFilters("vpc-1", "subnet-1")
	assert.ElementsMatch(t, []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{"vpc-1"}},
		{Name: aws.String("association.subnet-id"), Values: []string{"subnet-1"}},
	}, got)

	assert.Empty(t, routeTableFilters("", ""))
}

func TestDescribeAllRouteTablesFollowsPagination(t *testing.T) {
	var calls int
	fetch := func(_ context.Context, in *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
		calls++
		assert.Equal(t, routeTableFilters("vpc-1", ""), in.Filters)
		switch calls {
		case 1:
			assert.Nil(t, in.NextToken)
			return &ec2.DescribeRouteTablesOutput{
				RouteTables: []ec2types.RouteTable{{RouteTableId: aws.String("rtb-main")}},
				NextToken:   aws.String("page-2"),
			}, nil
		case 2:
			assert.Equal(t, "page-2", aws.ToString(in.NextToken))
			return &ec2.DescribeRouteTablesOutput{
				RouteTables: []ec2types.RouteTable{{RouteTableId: aws.String("rtb-explicit")}},
			}, nil
		default:
			t.Fatalf("unexpected page request %d", calls)
			return nil, nil
		}
	}

	got, err := describeAllRouteTables(context.Background(), &ec2.DescribeRouteTablesInput{
		Filters: routeTableFilters("vpc-1", ""),
	}, fetch)

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	require.Len(t, got, 2)
	assert.Equal(t, "rtb-main", aws.ToString(got[0].RouteTableId))
	assert.Equal(t, "rtb-explicit", aws.ToString(got[1].RouteTableId))
}

func TestNetworkACLFilters(t *testing.T) {
	got := networkACLFilters("vpc-1", "subnet-1")
	assert.ElementsMatch(t, []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{"vpc-1"}},
		{Name: aws.String("association.subnet-id"), Values: []string{"subnet-1"}},
	}, got)

	assert.Empty(t, networkACLFilters("", ""))
}

// --- EC2 creation wrappers (Task 3.1) ---
//
// EC2 uses the "ec2query" protocol (form-encoded request, XML response),
// unlike lambdacore/lambdamicrovms' JSON protocol the existing
// captureHTTPClient-based tests exercise. Hand-crafting correct query/XML
// fixtures for every creation call is high-effort for low marginal
// assurance over just trusting the SDK's own protocol marshaling; instead
// these tests assert on the pure Go values (tag specs, the hardcoded
// default-egress-rule shape) this package builds *before* handing them to
// the SDK — "asserts serialized EC2 requests where practical" per Task
// 3.1's acceptance criteria. Real wire-level verification happens via the
// Milestone 5 live/gated-integration tests, the same tradeoff already made
// for Task 1.1's EC2 read methods.

func TestEC2TagSpecification_EmptyOrNilTags_ReturnsNil(t *testing.T) {
	assert.Nil(t, ec2TagSpecification(ec2types.ResourceTypeVpc, nil))
	assert.Nil(t, ec2TagSpecification(ec2types.ResourceTypeVpc, map[string]string{}))
}

func TestEC2TagSpecification_BuildsResourceTypeAndTags(t *testing.T) {
	got := ec2TagSpecification(ec2types.ResourceTypeVpc, map[string]string{"ManagedBy": "whim"})
	require.Len(t, got, 1)
	assert.Equal(t, ec2types.ResourceTypeVpc, got[0].ResourceType)
	assert.Equal(t, []ec2types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("whim")}}, got[0].Tags)
}

func TestEC2TagSpecification_MultipleTags(t *testing.T) {
	got := ec2TagSpecification(ec2types.ResourceTypeSubnet, map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress"})
	require.Len(t, got, 1)
	assert.Equal(t, ec2types.ResourceTypeSubnet, got[0].ResourceType)
	assert.ElementsMatch(t, []ec2types.Tag{
		{Key: aws.String("ManagedBy"), Value: aws.String("whim")},
		{Key: aws.String("Purpose"), Value: aws.String("no-public-egress")},
	}, got[0].Tags)
}

// TestDefaultEgressAllRule locks in the exact shape of the rule EC2 adds
// automatically to every newly created security group, since
// RevokeAllSecurityGroupEgress must name that exact rule to remove it — a
// mismatched CIDR or protocol here would make the revoke call a silent no-op
// (EC2 does not error on revoking a non-existent rule), leaving the SG with
// its default open egress and failing the no-public-egress guarantee.
func TestDefaultEgressAllRule(t *testing.T) {
	require.Len(t, defaultEgressAllRule, 1)
	rule := defaultEgressAllRule[0]
	assert.Equal(t, "-1", aws.ToString(rule.IpProtocol))
	require.Len(t, rule.IpRanges, 1)
	assert.Equal(t, "0.0.0.0/0", aws.ToString(rule.IpRanges[0].CidrIp))
	assert.Nil(t, rule.FromPort)
	assert.Nil(t, rule.ToPort)
}
