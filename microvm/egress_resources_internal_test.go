package microvm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

// --- naming ---

func TestNewNoPublicEgressNames_DefaultPrefix(t *testing.T) {
	got := newNoPublicEgressNames("")
	assert.Equal(t, NoPublicEgressNames{
		VPC:           "whim-no-public-egress-vpc",
		SubnetA:       "whim-no-public-egress-subnet-a",
		SubnetB:       "whim-no-public-egress-subnet-b",
		RouteTable:    "whim-no-public-egress-rt",
		SecurityGroup: "whim-no-public-egress-sg",
		NetworkACL:    "whim-no-public-egress-acl",
		OperatorRole:  "whim-no-public-egress-operator-role",
		Connector:     "whim-no-public-egress",
	}, got)
}

func TestNewNoPublicEgressNames_CustomPrefix(t *testing.T) {
	got := newNoPublicEgressNames("acme")
	assert.Equal(t, "acme-no-public-egress-vpc", got.VPC)
	assert.Equal(t, "acme-no-public-egress", got.Connector)
}

func TestNoPublicEgressSpec_Names_ConnectorNameOverride(t *testing.T) {
	spec := NoPublicEgressSpec{NamePrefix: "acme", ConnectorName: "custom-connector"}
	got := spec.names()
	assert.Equal(t, "custom-connector", got.Connector)
	assert.Equal(t, "acme-no-public-egress-vpc", got.VPC, "override only replaces the connector name")
}

func TestNoPublicEgressSpec_Names_DefaultConnectorName(t *testing.T) {
	spec := NoPublicEgressSpec{}
	got := spec.names()
	assert.Equal(t, "whim-no-public-egress", got.Connector)
}

// --- ownership tags ---

func TestNoPublicEgressTags(t *testing.T) {
	got := NoPublicEgressTags("rg-1")
	assert.Equal(t, map[string]string{
		"ManagedBy":         "whim",
		"Purpose":           "no-public-egress",
		"WhimResourceGroup": "rg-1",
	}, got)
}

func TestIsNoPublicEgressOwned(t *testing.T) {
	for _, c := range []struct {
		name string
		tags map[string]string
		want bool
	}{
		{"nil tags", nil, false},
		{"empty tags", map[string]string{}, false},
		{"fully owned", map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress"}, true},
		{"owned plus extra tags", map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "Name": "x"}, true},
		{"missing purpose", map[string]string{"ManagedBy": "whim"}, false},
		{"missing managed-by", map[string]string{"Purpose": "no-public-egress"}, false},
		{"wrong managed-by value", map[string]string{"ManagedBy": "someone-else", "Purpose": "no-public-egress"}, false},
		{"wrong purpose value", map[string]string{"ManagedBy": "whim", "Purpose": "other"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isNoPublicEgressOwned(c.tags))
		})
	}
}

func TestRequireNoPublicEgressOwnership_Owned(t *testing.T) {
	err := requireNoPublicEgressOwnership("vpc-1", map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress"})
	assert.NoError(t, err)
}

func TestRequireNoPublicEgressOwnership_NotOwned(t *testing.T) {
	err := requireNoPublicEgressOwnership("vpc-1", map[string]string{"Name": "some-other-vpc"})
	require := assert.New(t)
	require.Error(err)
	require.ErrorIs(err, ErrResourceNotOwned)
	require.Contains(err.Error(), "vpc-1")
}

// --- violation distinguishability ---

// TestNoPublicEgressViolation_DistinguishesCases proves every failure reason
// (public route, SG outbound, unowned resource, missing connector, and
// topology mismatch — e.g. Task 2.3's NACL-association drift) is matched by
// errors.Is against its own sentinel and rejected by every other sentinel —
// so CLI code can branch on exactly one cause.
func TestNoPublicEgressViolation_DistinguishesCases(t *testing.T) {
	sentinels := []error{ErrPublicRoute, ErrSecurityGroupEgress, ErrResourceNotOwned, ErrConnectorMissing, ErrTopologyMismatch, ErrConnectorNotActive}
	for i, s := range sentinels {
		v := &NoPublicEgressViolation{Err: s, ResourceID: "res-1"}
		for j, other := range sentinels {
			if i == j {
				assert.Truef(t, errors.Is(v, other), "%v should match its own sentinel", s)
				continue
			}
			assert.Falsef(t, errors.Is(v, other), "%v must not match a different sentinel %v", s, other)
		}
	}
}

func TestNoPublicEgressViolation_Error_WithDetail(t *testing.T) {
	v := &NoPublicEgressViolation{Err: ErrPublicRoute, ResourceID: "rtb-1", Detail: "route to igw-1 for 0.0.0.0/0"}
	assert.Contains(t, v.Error(), "rtb-1")
	assert.Contains(t, v.Error(), "route to igw-1 for 0.0.0.0/0")
}

func TestNoPublicEgressViolation_Error_WithoutDetail(t *testing.T) {
	v := &NoPublicEgressViolation{Err: ErrConnectorMissing, ResourceID: "whim-no-public-egress"}
	assert.Contains(t, v.Error(), "whim-no-public-egress")
	assert.Contains(t, v.Error(), ErrConnectorMissing.Error())
}

// --- route table validation (Task 2.2) ---

const testVPCCIDR = "10.99.0.0/24"

func ownedTags() map[string]string {
	return map[string]string{"ManagedBy": "whim", "Purpose": "no-public-egress", "WhimResourceGroup": "rg-1"}
}

// explicitAssociation is a shorthand for the common case: rt is associated
// with exactly one subnet, explicitly (not via the implicit main-table
// fallback tested separately below).
func explicitAssociation(subnetID string) []awsapi.RouteTableAssociation {
	return []awsapi.RouteTableAssociation{{SubnetID: subnetID}}
}

func TestValidateNoPublicEgressRouteTable_LocalOnly_Allowed(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         ownedTags(),
		Associations: explicitAssociation("subnet-1"),
		Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local", State: "active"}},
	}
	assert.NoError(t, validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR))
}

// TestValidateNoPublicEgressRouteTable_ImplicitMainAssociation_Allowed
// covers a route table with no explicit subnet associations at all — every
// subnet without an explicit association falls back to the VPC's main route
// table, so this must still be treated as governing the subnet.
func TestValidateNoPublicEgressRouteTable_ImplicitMainAssociation_Allowed(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         ownedTags(),
		Associations: []awsapi.RouteTableAssociation{{Main: true}},
		Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
	}
	assert.NoError(t, validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR))
}

func TestValidateNoPublicEgressRouteTable_NoRoutes_Allowed(t *testing.T) {
	rt := awsapi.RouteTable{ID: "rtb-1", Tags: ownedTags(), Associations: explicitAssociation("subnet-1")}
	assert.NoError(t, validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR))
}

// TestValidateNoPublicEgressRouteTable_RejectsNonLocalTargets is a
// deny-by-default check: it covers every route-target field EC2 can
// populate, proving rejection does not depend on an allow-list of
// "known public-capable" target types (internet gateway, NAT gateway,
// transit gateway, peering, egress-only/carrier gateway are the
// spec-named ones; instance, network interface, local gateway, core
// network, route-server next-hop, ODB network, and prefix list are the
// "or other public-capable target" case).
func TestValidateNoPublicEgressRouteTable_RejectsNonLocalTargets(t *testing.T) {
	for _, c := range []struct {
		name  string
		route awsapi.Route
	}{
		{"ipv4 default via igw", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", GatewayID: "igw-1"}},
		{"ipv6 default via igw", awsapi.Route{DestinationIPv6CIDRBlock: "::/0", GatewayID: "igw-1"}},
		{"nat gateway", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", NatGatewayID: "nat-1"}},
		{"transit gateway", awsapi.Route{DestinationCIDRBlock: "10.0.0.0/8", TransitGatewayID: "tgw-1"}},
		{"vpc peering", awsapi.Route{DestinationCIDRBlock: "192.168.0.0/16", VPCPeeringConnectionID: "pcx-1"}},
		{"egress-only igw", awsapi.Route{DestinationIPv6CIDRBlock: "::/0", EgressOnlyInternetGatewayID: "eigw-1"}},
		{"carrier gateway", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", CarrierGatewayID: "cagw-1"}},
		{"instance (nat instance pattern)", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", InstanceID: "i-1"}},
		{"network interface", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", NetworkInterfaceID: "eni-1"}},
		{"local gateway (outposts)", awsapi.Route{DestinationCIDRBlock: "192.168.0.0/16", LocalGatewayID: "lgw-1"}},
		{"core network (cloud wan)", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", CoreNetworkARN: "arn:aws:networkmanager::123456789012:core-network/core-1"}},
		{"route-server next-hop", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", IPAddress: "10.0.0.9"}},
		{"odb network", awsapi.Route{DestinationCIDRBlock: "10.0.0.0/8", ODBNetworkARN: "arn:aws:odb:us-east-1:123456789012:network/odb-1"}},
		{"prefix list gateway endpoint", awsapi.Route{DestinationPrefixListID: "pl-1", GatewayID: "vpce-1"}},
		{"non-local gateway id set, no other field", awsapi.Route{DestinationCIDRBlock: "10.0.0.0/8", GatewayID: "vgw-1"}},
		{"local gateway id but wrong destination CIDR (secondary/drifted CIDR)", awsapi.Route{DestinationCIDRBlock: "10.100.0.0/24", GatewayID: "local"}},
		{"local gateway id but 0.0.0.0/0 destination (defensive: AWS would never produce this)", awsapi.Route{DestinationCIDRBlock: "0.0.0.0/0", GatewayID: "local"}},
		{"local gateway id but ::/0 destination (defensive)", awsapi.Route{DestinationIPv6CIDRBlock: "::/0", GatewayID: "local"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := awsapi.RouteTable{ID: "rtb-1", Tags: ownedTags(), Associations: explicitAssociation("subnet-1"), Routes: []awsapi.Route{c.route}}
			err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPublicRoute)
			assert.NotErrorIs(t, err, ErrResourceNotOwned, "must be distinguishable from an ownership violation")
			var v *NoPublicEgressViolation
			require.ErrorAs(t, err, &v)
			assert.Equal(t, "rtb-1", v.ResourceID)
			assert.NotEmpty(t, v.Detail)
		})
	}
}

// TestValidateNoPublicEgressRouteTable_MixedLocalAndPublic_Rejected checks
// that one bad route fails the whole table even alongside an otherwise-valid
// local route.
func TestValidateNoPublicEgressRouteTable_MixedLocalAndPublic_Rejected(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         ownedTags(),
		Associations: explicitAssociation("subnet-1"),
		Routes: []awsapi.Route{
			{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"},
			{DestinationCIDRBlock: "0.0.0.0/0", GatewayID: "igw-1"},
		},
	}
	assert.ErrorIs(t, validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR), ErrPublicRoute)
}

func TestValidateNoPublicEgressRouteTable_UnownedTable_Rejected(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         map[string]string{"Name": "someone-elses-table"},
		Associations: explicitAssociation("subnet-1"),
		Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
	}
	err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResourceNotOwned)
	assert.NotErrorIs(t, err, ErrPublicRoute, "must be distinguishable from a public-route violation")
}

// TestValidateNoPublicEgressRouteTable_OwnershipCheckedFirst checks that an
// unowned table with a public route reports the ownership violation, not
// the route violation — ownership is a cheaper, more fundamental failure and
// should not be masked by route content.
func TestValidateNoPublicEgressRouteTable_OwnershipCheckedFirst(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         nil,
		Associations: explicitAssociation("subnet-1"),
		Routes:       []awsapi.Route{{DestinationCIDRBlock: "0.0.0.0/0", GatewayID: "igw-1"}},
	}
	err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
	assert.ErrorIs(t, err, ErrResourceNotOwned)
}

// TestValidateNoPublicEgressRouteTable_DriftedAssociation_Rejected is the
// core "complete topology" check: an otherwise-perfect, Whim-owned,
// local-only route table is rejected if it does not actually govern the
// managed subnet — e.g. it governs some other subnet in the same VPC.
func TestValidateNoPublicEgressRouteTable_DriftedAssociation_Rejected(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         ownedTags(),
		Associations: explicitAssociation("subnet-other"),
		Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
	}
	err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTopologyMismatch)
	assert.NotErrorIs(t, err, ErrResourceNotOwned)
	assert.NotErrorIs(t, err, ErrPublicRoute)
}

// TestValidateNoPublicEgressRouteTable_ExplicitAssociationElsewhere_NotImplicitMain
// checks that a route table with explicit (but non-matching) subnet
// associations is never treated as an implicit main-table fallback for a
// different subnet, even if it also happens to be the VPC's main table.
func TestValidateNoPublicEgressRouteTable_ExplicitAssociationElsewhere_NotImplicitMain(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:   "rtb-1",
		Tags: ownedTags(),
		Associations: []awsapi.RouteTableAssociation{
			{SubnetID: "subnet-other"},
			{Main: true},
		},
		Routes: []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
	}
	err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
	assert.ErrorIs(t, err, ErrTopologyMismatch)
}

func TestValidateNoPublicEgressRouteTable_AssociationCheckedBeforeRoutes(t *testing.T) {
	rt := awsapi.RouteTable{
		ID:           "rtb-1",
		Tags:         ownedTags(),
		Associations: explicitAssociation("subnet-other"),
		Routes:       []awsapi.Route{{DestinationCIDRBlock: "0.0.0.0/0", GatewayID: "igw-1"}},
	}
	err := validateNoPublicEgressRouteTable(rt, "subnet-1", testVPCCIDR)
	assert.ErrorIs(t, err, ErrTopologyMismatch, "association drift should surface before route content")
}

// --- routeTableAssociatedWithSubnet (pure helper) ---

func TestRouteTableAssociatedWithSubnet(t *testing.T) {
	for _, c := range []struct {
		name string
		rt   awsapi.RouteTable
		want bool
	}{
		{"explicit match", awsapi.RouteTable{Associations: explicitAssociation("subnet-1")}, true},
		{"explicit non-match", awsapi.RouteTable{Associations: explicitAssociation("subnet-other")}, false},
		{"no associations at all", awsapi.RouteTable{}, false},
		{"implicit main, no explicit associations", awsapi.RouteTable{Associations: []awsapi.RouteTableAssociation{{Main: true}}}, true},
		{"explicit elsewhere plus main is not implicit for us", awsapi.RouteTable{Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-other"}, {Main: true}}}, false},
		{"multiple explicit, one matches", awsapi.RouteTable{Associations: []awsapi.RouteTableAssociation{{SubnetID: "subnet-other"}, {SubnetID: "subnet-1"}}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, routeTableAssociatedWithSubnet(c.rt, "subnet-1"))
		})
	}
}

// --- security group validation (Task 2.3) ---

func TestValidateNoPublicEgressSecurityGroup_NoEgress_Allowed(t *testing.T) {
	sg := awsapi.SecurityGroup{ID: "sg-1", Tags: ownedTags()}
	assert.NoError(t, validateNoPublicEgressSecurityGroup(sg))
}

func TestValidateNoPublicEgressSecurityGroup_RejectsAnyEgressRule(t *testing.T) {
	for _, c := range []struct {
		name string
		rule awsapi.SecurityGroupRule
	}{
		{"cidr ipv4", awsapi.SecurityGroupRule{IPProtocol: "-1", CIDRIPv4: "0.0.0.0/0"}},
		{"cidr ipv6", awsapi.SecurityGroupRule{IPProtocol: "-1", CIDRIPv6: "::/0"}},
		{"prefix list", awsapi.SecurityGroupRule{IPProtocol: "tcp", FromPort: aws.Int32(443), ToPort: aws.Int32(443), PrefixListID: "pl-1"}},
		{"referenced security group", awsapi.SecurityGroupRule{IPProtocol: "tcp", ReferencedGroupID: "sg-2"}},
		{"narrow single-port rule (not just wide-open)", awsapi.SecurityGroupRule{IPProtocol: "tcp", FromPort: aws.Int32(53), ToPort: aws.Int32(53), CIDRIPv4: "10.0.0.0/8"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sg := awsapi.SecurityGroup{ID: "sg-1", Tags: ownedTags(), EgressRules: []awsapi.SecurityGroupRule{c.rule}}
			err := validateNoPublicEgressSecurityGroup(sg)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSecurityGroupEgress)
			var v *NoPublicEgressViolation
			require.ErrorAs(t, err, &v)
			assert.Equal(t, "sg-1", v.ResourceID)
			assert.NotEmpty(t, v.Detail)
		})
	}
}

func TestValidateNoPublicEgressSecurityGroup_UnownedRejected(t *testing.T) {
	sg := awsapi.SecurityGroup{ID: "sg-1", Tags: map[string]string{"Name": "someone-elses-sg"}}
	err := validateNoPublicEgressSecurityGroup(sg)
	assert.ErrorIs(t, err, ErrResourceNotOwned)
	assert.NotErrorIs(t, err, ErrSecurityGroupEgress)
}

func TestValidateNoPublicEgressSecurityGroup_OwnershipCheckedFirst(t *testing.T) {
	sg := awsapi.SecurityGroup{
		ID:          "sg-1",
		Tags:        nil,
		EgressRules: []awsapi.SecurityGroupRule{{IPProtocol: "-1", CIDRIPv4: "0.0.0.0/0"}},
	}
	assert.ErrorIs(t, validateNoPublicEgressSecurityGroup(sg), ErrResourceNotOwned)
}

// --- network ACL validation (Task 2.3) ---

func TestValidateNoPublicEgressNetworkACL_AssociatedWithSubnet_Allowed(t *testing.T) {
	acl := awsapi.NetworkACL{
		ID:           "acl-1",
		Tags:         ownedTags(),
		Associations: []awsapi.NetworkACLAssociation{{SubnetID: "subnet-1"}},
	}
	assert.NoError(t, validateNoPublicEgressNetworkACL(acl, "subnet-1"))
}

func TestValidateNoPublicEgressNetworkACL_DriftedAssociation_Rejected(t *testing.T) {
	acl := awsapi.NetworkACL{
		ID:           "acl-1",
		Tags:         ownedTags(),
		Associations: []awsapi.NetworkACLAssociation{{SubnetID: "subnet-other"}},
	}
	err := validateNoPublicEgressNetworkACL(acl, "subnet-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTopologyMismatch)
	assert.NotErrorIs(t, err, ErrResourceNotOwned, "must be distinguishable from an ownership violation")
	var v *NoPublicEgressViolation
	require.ErrorAs(t, err, &v)
	assert.Equal(t, "acl-1", v.ResourceID)
	assert.Contains(t, v.Detail, "subnet-1")
}

func TestValidateNoPublicEgressNetworkACL_NoAssociations_Rejected(t *testing.T) {
	acl := awsapi.NetworkACL{ID: "acl-1", Tags: ownedTags()}
	assert.ErrorIs(t, validateNoPublicEgressNetworkACL(acl, "subnet-1"), ErrTopologyMismatch)
}

func TestValidateNoPublicEgressNetworkACL_UnownedRejected(t *testing.T) {
	acl := awsapi.NetworkACL{
		ID:           "acl-1",
		Tags:         map[string]string{"Name": "someone-elses-acl"},
		Associations: []awsapi.NetworkACLAssociation{{SubnetID: "subnet-1"}},
	}
	err := validateNoPublicEgressNetworkACL(acl, "subnet-1")
	assert.ErrorIs(t, err, ErrResourceNotOwned)
	assert.NotErrorIs(t, err, ErrTopologyMismatch)
}

func TestValidateNoPublicEgressNetworkACL_OwnershipCheckedFirst(t *testing.T) {
	acl := awsapi.NetworkACL{ID: "acl-1", Tags: nil}
	assert.ErrorIs(t, validateNoPublicEgressNetworkACL(acl, "subnet-1"), ErrResourceNotOwned)
}

// --- connector-to-VPC topology validation (Task 2.4) ---

func TestValidateNoPublicEgressConnector_ActiveMatchingTopology_Allowed(t *testing.T) {
	c := NoPublicEgressConnector{
		Name:             "whim-no-public-egress",
		State:            "ACTIVE",
		SubnetIDs:        []string{"subnet-1"},
		SecurityGroupIDs: []string{"sg-1"},
	}
	assert.NoError(t, validateNoPublicEgressConnector(c, []string{"subnet-1"}, []string{"sg-1"}))
}

// TestValidateNoPublicEgressConnector_SubnetOrderIndependent checks that
// topology comparison is set-based, not order-sensitive — AWS gives no
// ordering guarantee for SubnetIds/SecurityGroupIds.
func TestValidateNoPublicEgressConnector_SubnetOrderIndependent(t *testing.T) {
	c := NoPublicEgressConnector{
		State:            "ACTIVE",
		SubnetIDs:        []string{"subnet-2", "subnet-1"},
		SecurityGroupIDs: []string{"sg-1"},
	}
	assert.NoError(t, validateNoPublicEgressConnector(c, []string{"subnet-1", "subnet-2"}, []string{"sg-1"}))
}

func TestValidateNoPublicEgressConnector_RejectsNonActiveStates(t *testing.T) {
	for _, state := range []string{"PENDING", "INACTIVE", "FAILED", "DELETING", "DELETE_FAILED"} {
		t.Run(state, func(t *testing.T) {
			c := NoPublicEgressConnector{Name: "whim-no-public-egress", State: state, SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"}}
			err := validateNoPublicEgressConnector(c, []string{"subnet-1"}, []string{"sg-1"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrConnectorNotActive)
			assert.NotErrorIs(t, err, ErrTopologyMismatch, "must be distinguishable from a topology violation")
			var v *NoPublicEgressViolation
			require.ErrorAs(t, err, &v)
			assert.Contains(t, v.Detail, state)
		})
	}
}

func TestValidateNoPublicEgressConnector_RejectsSubnetMismatch(t *testing.T) {
	c := NoPublicEgressConnector{Name: "whim-no-public-egress", State: "ACTIVE", SubnetIDs: []string{"subnet-unexpected"}, SecurityGroupIDs: []string{"sg-1"}}
	err := validateNoPublicEgressConnector(c, []string{"subnet-1"}, []string{"sg-1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTopologyMismatch)
	assert.NotErrorIs(t, err, ErrConnectorNotActive)
}

func TestValidateNoPublicEgressConnector_RejectsSecurityGroupMismatch(t *testing.T) {
	c := NoPublicEgressConnector{Name: "whim-no-public-egress", State: "ACTIVE", SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-unexpected"}}
	err := validateNoPublicEgressConnector(c, []string{"subnet-1"}, []string{"sg-1"})
	assert.ErrorIs(t, err, ErrTopologyMismatch)
}

// --- findNoPublicEgressConnector (Task 2.4, Manager discovery) ---

func TestFindNoPublicEgressConnector_ActiveMatch_Reused(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(_ context.Context, in *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			assert.Equal(t, "whim-no-public-egress", in.Identifier)
			return &awsapi.GetNetworkConnectorOutput{
				ARN:              "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress",
				Name:             "whim-no-public-egress",
				State:            "ACTIVE",
				SubnetIDs:        []string{"subnet-1"},
				SecurityGroupIDs: []string{"sg-1"},
			}, nil
		},
	}
	mgr := NewWithAPI(m)
	res, err := mgr.findNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{}, []string{"subnet-1"}, "sg-1")
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", res.ConnectorARN)
	assert.Equal(t, []string{"subnet-1"}, res.SubnetIDs)
	assert.Equal(t, "sg-1", res.SecurityGroupID)
}

func TestFindNoPublicEgressConnector_Missing_ReturnsErrConnectorMissing(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.findNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{}, []string{"subnet-1"}, "sg-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectorMissing)
}

func TestFindNoPublicEgressConnector_Pending_Rejected(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{State: "PENDING", SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"}}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.findNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{}, []string{"subnet-1"}, "sg-1")
	assert.ErrorIs(t, err, ErrConnectorNotActive)
}

func TestFindNoPublicEgressConnector_Failed_Rejected(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{State: "FAILED", SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"}}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.findNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{}, []string{"subnet-1"}, "sg-1")
	assert.ErrorIs(t, err, ErrConnectorNotActive)
}

func TestFindNoPublicEgressConnector_TopologyMismatch_Rejected(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{State: "ACTIVE", SubnetIDs: []string{"subnet-unexpected"}, SecurityGroupIDs: []string{"sg-1"}}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.findNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{}, []string{"subnet-1"}, "sg-1")
	assert.ErrorIs(t, err, ErrTopologyMismatch)
}

// --- operator role validation (Task 3.2 — existing-role mode only; Whim
// never creates the IAM role itself, per the human decision on Task 3.2) ---

func validTrustPolicy() string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
}

func TestTrustsLambdaService(t *testing.T) {
	for _, c := range []struct {
		name string
		doc  string
		want bool
	}{
		{"single service string", `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`, true},
		{"single statement object, not array (valid per AWS IAM policy grammar)", `{"Statement":{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}}`, true},
		{"service array", `{"Statement":[{"Effect":"Allow","Principal":{"Service":["ec2.amazonaws.com","lambda.amazonaws.com"]},"Action":"sts:AssumeRole"}]}`, true},
		{"action array", `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`, true},
		{"wrong service only", `{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`, false},
		{"deny effect", `{"Statement":[{"Effect":"Deny","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`, false},
		{"wrong action", `{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRoleWithWebIdentity"}]}`, false},
		{"empty statements", `{"Statement":[]}`, false},
		{"malformed json", `not json`, false},
		{"empty string", ``, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, trustsLambdaService(c.doc))
		})
	}
}

func TestValidateNoPublicEgressOperatorRole_TrustedWithAttachedPolicy_Allowed(t *testing.T) {
	role := NoPublicEgressOperatorRole{
		ARN:                      "arn:aws:iam::123456789012:role/whim-no-public-egress-operator-role",
		AssumeRolePolicyDocument: validTrustPolicy(),
		AttachedPolicyARNs:       []string{"arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"},
	}
	assert.NoError(t, validateNoPublicEgressOperatorRole(role))
}

func TestValidateNoPublicEgressOperatorRole_TrustedWithInlinePolicy_Allowed(t *testing.T) {
	role := NoPublicEgressOperatorRole{
		ARN:                      "arn:aws:iam::123456789012:role/custom-role",
		AssumeRolePolicyDocument: validTrustPolicy(),
		InlinePolicyNames:        []string{"custom-network-interface-policy"},
	}
	assert.NoError(t, validateNoPublicEgressOperatorRole(role))
}

func TestValidateNoPublicEgressOperatorRole_NoPolicies_Rejected(t *testing.T) {
	role := NoPublicEgressOperatorRole{
		ARN:                      "arn:aws:iam::123456789012:role/empty-role",
		AssumeRolePolicyDocument: validTrustPolicy(),
	}
	err := validateNoPublicEgressOperatorRole(role)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOperatorRoleInvalid)
	var v *NoPublicEgressViolation
	require.ErrorAs(t, err, &v)
	assert.Equal(t, role.ARN, v.ResourceID)
}

func TestValidateNoPublicEgressOperatorRole_WrongTrust_Rejected(t *testing.T) {
	role := NoPublicEgressOperatorRole{
		ARN:                      "arn:aws:iam::123456789012:role/ec2-trusted-role",
		AssumeRolePolicyDocument: `{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		AttachedPolicyARNs:       []string{"arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"},
	}
	assert.ErrorIs(t, validateNoPublicEgressOperatorRole(role), ErrOperatorRoleInvalid)
}

func TestRoleNameFromARNOrName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"whim-role", "whim-role"},
		{"arn:aws:iam::123456789012:role/whim-role", "whim-role"},
		{"arn:aws:iam::123456789012:role/some/path/whim-role", "whim-role"},
	} {
		assert.Equal(t, c.want, roleNameFromARNOrName(c.in))
	}
}

func TestValidateOperatorRole_Manager_Valid(t *testing.T) {
	m := &awsapi.Mock{
		GetRoleFn: func(_ context.Context, in *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
			assert.Equal(t, "whim-role", in.RoleName)
			return &awsapi.GetRoleOutput{ARN: "arn:aws:iam::123456789012:role/whim-role", RoleName: "whim-role", AssumeRolePolicyDocument: validTrustPolicy()}, nil
		},
		ListAttachedRolePoliciesFn: func(context.Context, *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
			return &awsapi.ListAttachedRolePoliciesOutput{Items: []awsapi.AttachedPolicy{{PolicyName: "AWSLambdaNetworkConnectorOperatorPolicy", PolicyARN: "arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"}}}, nil
		},
		ListRolePoliciesFn: func(context.Context, *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
			return &awsapi.ListRolePoliciesOutput{}, nil
		},
	}
	mgr := NewWithAPI(m)
	err := mgr.validateNoPublicEgressOperatorRole(context.Background(), "arn:aws:iam::123456789012:role/whim-role")
	assert.NoError(t, err)
}

func TestValidateOperatorRole_Manager_NotFound(t *testing.T) {
	m := &awsapi.Mock{
		GetRoleFn: func(context.Context, *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
			return nil, awsapi.ErrNotFound
		},
	}
	mgr := NewWithAPI(m)
	err := mgr.validateNoPublicEgressOperatorRole(context.Background(), "whim-role")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOperatorRoleInvalid)
}

func TestValidateOperatorRole_Manager_NoPolicies(t *testing.T) {
	m := &awsapi.Mock{
		GetRoleFn: func(context.Context, *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
			return &awsapi.GetRoleOutput{ARN: "arn:aws:iam::123456789012:role/whim-role", AssumeRolePolicyDocument: validTrustPolicy()}, nil
		},
		ListAttachedRolePoliciesFn: func(context.Context, *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
			return &awsapi.ListAttachedRolePoliciesOutput{}, nil
		},
		ListRolePoliciesFn: func(context.Context, *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
			return &awsapi.ListRolePoliciesOutput{}, nil
		},
	}
	mgr := NewWithAPI(m)
	err := mgr.validateNoPublicEgressOperatorRole(context.Background(), "whim-role")
	assert.ErrorIs(t, err, ErrOperatorRoleInvalid)
}

// --- EnsureNoPublicEgressConnector (Task 3.3) ---

// validRoleMocks wires GetRole/ListAttachedRolePolicies/ListRolePolicies to
// report a valid, Whim-usable operator role, for tests that exercise the
// creation path.
func validRoleMocks(m *awsapi.Mock) {
	m.GetRoleFn = func(_ context.Context, in *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
		return &awsapi.GetRoleOutput{ARN: "arn:aws:iam::123456789012:role/" + in.RoleName, RoleName: in.RoleName, AssumeRolePolicyDocument: validTrustPolicy()}, nil
	}
	m.ListAttachedRolePoliciesFn = func(context.Context, *awsapi.ListAttachedRolePoliciesInput) (*awsapi.ListAttachedRolePoliciesOutput, error) {
		return &awsapi.ListAttachedRolePoliciesOutput{Items: []awsapi.AttachedPolicy{{PolicyName: "AWSLambdaNetworkConnectorOperatorPolicy", PolicyARN: "arn:aws:iam::aws:policy/AWSLambdaNetworkConnectorOperatorPolicy"}}}, nil
	}
	m.ListRolePoliciesFn = func(context.Context, *awsapi.ListRolePoliciesInput) (*awsapi.ListRolePoliciesOutput, error) {
		return &awsapi.ListRolePoliciesOutput{}, nil
	}
}

func creationSpec() NoPublicEgressSpec {
	return NoPublicEgressSpec{
		OperatorRoleARN: "arn:aws:iam::123456789012:role/whim-operator",
		VPCCIDRBlock:    testVPCCIDR,
		SubnetCIDRBlock: "10.99.0.0/25",
	}
}

func TestEnsureNoPublicEgressConnector_ReusesValidExisting(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{
				ARN:  "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress",
				Name: "whim-no-public-egress", State: "ACTIVE",
				SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"},
			}, nil
		},
		DescribeSubnetsFn: func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
			return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
		GetVPCFn: func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
			return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-1", CIDRBlock: testVPCCIDR, Tags: ownedTags()}}, nil
		},
		DescribeRouteTablesFn: func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
			return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
				ID: "rtb-1", VPCID: "vpc-1", Tags: ownedTags(),
				Associations: explicitAssociation("subnet-1"),
				Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
			}}}, nil
		},
		DescribeSecurityGroupsFn: func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
			return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: "sg-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
	}
	mgr := NewWithAPI(m)
	res, err := mgr.EnsureNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{})
	require.NoError(t, err)
	assert.Equal(t, "vpc-1", res.VPCID)
	assert.Equal(t, []string{"subnet-1"}, res.SubnetIDs)
	assert.Equal(t, "rtb-1", res.RouteTableID)
	assert.Equal(t, "sg-1", res.SecurityGroupID)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", res.ConnectorARN)
	assert.Equal(t, "rg-1", res.ResourceGroup, "reuse must preserve the stable managed resource-group identity")
	assert.Empty(t, m.CreateVPCCalls, "must not create anything when a valid connector already exists")
}

func TestEnsureNoPublicEgressConnector_RejectsMismatchedManagedResourceGroup(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{
				ARN: "arn:connector", Name: "whim-no-public-egress", State: "ACTIVE",
				SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"},
			}, nil
		},
		DescribeSubnetsFn: func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
			return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
		GetVPCFn: func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
			tags := ownedTags()
			tags["WhimResourceGroup"] = "rg-other"
			return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-1", CIDRBlock: testVPCCIDR, Tags: tags}}, nil
		},
	}

	_, err := NewWithAPI(m).EnsureNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{})

	require.ErrorIs(t, err, ErrTopologyMismatch)
	assert.Empty(t, m.CreateVPCCalls, "identity drift must fail closed rather than create replacements")
}

func TestEnsureNoPublicEgressConnector_ExistingConnectorButUnsafeSG_FailsClosed(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{ARN: "arn:...", Name: "whim-no-public-egress", State: "ACTIVE", SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"}}, nil
		},
		DescribeSubnetsFn: func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
			return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
		GetVPCFn: func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
			return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-1", CIDRBlock: testVPCCIDR, Tags: ownedTags()}}, nil
		},
		DescribeRouteTablesFn: func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
			return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
				ID: "rtb-1", VPCID: "vpc-1", Tags: ownedTags(),
				Associations: explicitAssociation("subnet-1"),
				Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
			}}}, nil
		},
		DescribeSecurityGroupsFn: func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
			return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{
				ID: "sg-1", VPCID: "vpc-1", Tags: ownedTags(),
				EgressRules: []awsapi.SecurityGroupRule{{IPProtocol: "-1", CIDRIPv4: "0.0.0.0/0"}},
			}}}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), creationSpec())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSecurityGroupEgress)
	assert.Empty(t, m.CreateVPCCalls, "an unsafe existing resource group must never trigger creating a second one")
}

func TestEnsureNoPublicEgressConnector_CreatesFresh_WhenNothingExists(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
		CreateVPCFn: func(_ context.Context, in *awsapi.CreateVPCInput) (*awsapi.CreateVPCOutput, error) {
			assert.Equal(t, testVPCCIDR, in.CIDRBlock)
			return &awsapi.CreateVPCOutput{VPCID: "vpc-new"}, nil
		},
		DescribeAvailabilityZonesFn: func(context.Context) (*awsapi.DescribeAvailabilityZonesOutput, error) {
			return &awsapi.DescribeAvailabilityZonesOutput{Items: []awsapi.AvailabilityZone{{ZoneName: "us-east-1a", State: "available"}}}, nil
		},
		CreateSubnetFn: func(_ context.Context, in *awsapi.CreateSubnetInput) (*awsapi.CreateSubnetOutput, error) {
			assert.Equal(t, "vpc-new", in.VPCID)
			assert.Equal(t, "us-east-1a", in.AvailabilityZone)
			return &awsapi.CreateSubnetOutput{SubnetID: "subnet-new"}, nil
		},
		CreateRouteTableFn: func(_ context.Context, in *awsapi.CreateRouteTableInput) (*awsapi.CreateRouteTableOutput, error) {
			assert.Equal(t, "vpc-new", in.VPCID)
			return &awsapi.CreateRouteTableOutput{RouteTableID: "rtb-new"}, nil
		},
		AssociateRouteTableFn: func(_ context.Context, in *awsapi.AssociateRouteTableInput) (*awsapi.AssociateRouteTableOutput, error) {
			assert.Equal(t, "rtb-new", in.RouteTableID)
			assert.Equal(t, "subnet-new", in.SubnetID)
			return &awsapi.AssociateRouteTableOutput{AssociationID: "rtbassoc-1"}, nil
		},
		CreateSecurityGroupFn: func(_ context.Context, in *awsapi.CreateSecurityGroupInput) (*awsapi.CreateSecurityGroupOutput, error) {
			assert.Equal(t, "vpc-new", in.VPCID)
			return &awsapi.CreateSecurityGroupOutput{SecurityGroupID: "sg-new"}, nil
		},
		RevokeAllSecurityGroupEgressFn: func(_ context.Context, in *awsapi.RevokeAllSecurityGroupEgressInput) error {
			assert.Equal(t, "sg-new", in.SecurityGroupID)
			return nil
		},
		CreateNetworkConnectorFn: func(_ context.Context, in *awsapi.CreateNetworkConnectorInput) (*awsapi.CreateNetworkConnectorOutput, error) {
			assert.Equal(t, []string{"subnet-new"}, in.SubnetIDs)
			assert.Equal(t, []string{"sg-new"}, in.SecurityGroupIDs)
			assert.Equal(t, "arn:aws:iam::123456789012:role/whim-operator", in.OperatorRoleARN)
			return &awsapi.CreateNetworkConnectorOutput{ARN: "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", Name: "whim-no-public-egress", State: "ACTIVE"}, nil
		},
	}
	validRoleMocks(m)
	mgr := NewWithAPI(m, WithPollInterval(time.Millisecond))
	res, err := mgr.EnsureNoPublicEgressConnector(context.Background(), creationSpec())
	require.NoError(t, err)
	assert.Equal(t, "vpc-new", res.VPCID)
	assert.Equal(t, []string{"subnet-new"}, res.SubnetIDs)
	assert.Equal(t, "rtb-new", res.RouteTableID)
	assert.Equal(t, "sg-new", res.SecurityGroupID)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", res.ConnectorARN)
	assert.NotEmpty(t, res.ResourceGroup)
	assert.NotEqual(t, "whim-no-public-egress", res.ResourceGroup, "resource-group identity must not be the reusable connector name")
	assert.Equal(t, res.ResourceGroup, m.CreateVPCCalls[0].Tags["WhimResourceGroup"])
}

func TestEnsureNoPublicEgressConnector_MissingOperatorRole_FailsClosedBeforeCreating(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
	}
	mgr := NewWithAPI(m)
	spec := creationSpec()
	spec.OperatorRoleARN = ""
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOperatorRoleInvalid)
	assert.Empty(t, m.CreateVPCCalls)
}

func TestEnsureNoPublicEgressConnector_InvalidOperatorRole_FailsClosedBeforeCreating(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
		GetRoleFn: func(context.Context, *awsapi.GetRoleInput) (*awsapi.GetRoleOutput, error) {
			return &awsapi.GetRoleOutput{ARN: "arn:aws:iam::123456789012:role/whim-operator", AssumeRolePolicyDocument: `{"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), creationSpec())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOperatorRoleInvalid)
	assert.Empty(t, m.CreateVPCCalls)
}

func TestEnsureNoPublicEgressConnector_MissingCIDRBlocks_FailsClosedBeforeCreating(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
	}
	validRoleMocks(m)
	mgr := NewWithAPI(m)
	spec := creationSpec()
	spec.VPCCIDRBlock = ""
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidOption)
	assert.Empty(t, m.CreateVPCCalls)
}

func TestEnsureNoPublicEgressConnector_PartialCreationFailure_ReturnsActionableError(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return nil, awsapi.ErrNotFound
		},
		CreateVPCFn: func(context.Context, *awsapi.CreateVPCInput) (*awsapi.CreateVPCOutput, error) {
			return &awsapi.CreateVPCOutput{VPCID: "vpc-new"}, nil
		},
		DescribeAvailabilityZonesFn: func(context.Context) (*awsapi.DescribeAvailabilityZonesOutput, error) {
			return &awsapi.DescribeAvailabilityZonesOutput{Items: []awsapi.AvailabilityZone{{ZoneName: "us-east-1a", State: "available"}}}, nil
		},
		CreateSubnetFn: func(context.Context, *awsapi.CreateSubnetInput) (*awsapi.CreateSubnetOutput, error) {
			return nil, fmt.Errorf("subnet CIDR overlaps")
		},
	}
	validRoleMocks(m)
	mgr := NewWithAPI(m)
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), creationSpec())
	require.Error(t, err)
	var partial *PartialNoPublicEgressCreationError
	require.ErrorAs(t, err, &partial)
	assert.Equal(t, "CreateSubnet", partial.Step)
	assert.Equal(t, "vpc-new", partial.Resources.VPCID, "the error must name what was already created")
	assert.Contains(t, err.Error(), "vpc-new")
	assert.Contains(t, err.Error(), "subnet CIDR overlaps")
}

func TestEnsureNoPublicEgressConnector_ReusesPendingConnector_PollsToActive(t *testing.T) {
	calls := 0
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			calls++
			state := "PENDING"
			if calls > 1 {
				state = "ACTIVE"
			}
			return &awsapi.GetNetworkConnectorOutput{
				ARN:  "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress",
				Name: "whim-no-public-egress", State: state,
				SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"},
			}, nil
		},
		DescribeSubnetsFn: func(context.Context, *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
			return &awsapi.DescribeSubnetsOutput{Items: []awsapi.Subnet{{ID: "subnet-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
		GetVPCFn: func(context.Context, *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
			return &awsapi.GetVPCOutput{VPC: awsapi.VPC{ID: "vpc-1", CIDRBlock: testVPCCIDR, Tags: ownedTags()}}, nil
		},
		DescribeRouteTablesFn: func(context.Context, *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
			return &awsapi.DescribeRouteTablesOutput{Items: []awsapi.RouteTable{{
				ID: "rtb-1", VPCID: "vpc-1", Tags: ownedTags(),
				Associations: explicitAssociation("subnet-1"),
				Routes:       []awsapi.Route{{DestinationCIDRBlock: testVPCCIDR, GatewayID: "local"}},
			}}}, nil
		},
		DescribeSecurityGroupsFn: func(context.Context, *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
			return &awsapi.DescribeSecurityGroupsOutput{Items: []awsapi.SecurityGroup{{ID: "sg-1", VPCID: "vpc-1", Tags: ownedTags()}}}, nil
		},
	}
	mgr := NewWithAPI(m, WithPollInterval(time.Millisecond))
	res, err := mgr.EnsureNoPublicEgressConnector(context.Background(), NoPublicEgressSpec{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", res.ConnectorARN)
	assert.GreaterOrEqual(t, calls, 2, "must poll rather than immediately fail on a found-but-pending connector")
}

func TestEnsureNoPublicEgressConnector_ReuseFoundConnectorFailed_FailsClosed(t *testing.T) {
	m := &awsapi.Mock{
		GetNetworkConnectorFn: func(context.Context, *awsapi.GetNetworkConnectorInput) (*awsapi.GetNetworkConnectorOutput, error) {
			return &awsapi.GetNetworkConnectorOutput{Name: "whim-no-public-egress", State: "FAILED", SubnetIDs: []string{"subnet-1"}, SecurityGroupIDs: []string{"sg-1"}}, nil
		},
	}
	mgr := NewWithAPI(m)
	_, err := mgr.EnsureNoPublicEgressConnector(context.Background(), creationSpec())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectorNotActive)
	assert.Empty(t, m.CreateVPCCalls, "a FAILED existing connector must not trigger creating a second resource group")
}
