package microvm

import (
	"context"
	"fmt"

	"github.com/udgover/whim/internal/awsapi"
)

// ValidateNoPublicEgressConnector proves that the connector recorded in
// expected is ACTIVE and currently points only at VPC resources with local-only
// routes and zero security-group egress. Non-empty topology fields in expected
// are also compared exactly, so a connector changed after build fails closed.
// Caller-managed resources do not need Whim ownership tags; when ResourceGroup
// is set, every discovered resource must carry that exact WhimResourceGroup tag.
func (m *Manager) ValidateNoPublicEgressConnector(ctx context.Context, expected NoPublicEgressResources) (*NoPublicEgressResources, error) {
	if expected.ConnectorARN == "" {
		return nil, fmt.Errorf("%w: no-public-egress connector ARN is required", ErrInvalidOption)
	}
	if isInternetEgressConnector(expected.ConnectorARN) {
		return nil, fmt.Errorf("%w: EgressNone cannot use the managed INTERNET_EGRESS connector", ErrInvalidOption)
	}

	connector, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: expected.ConnectorARN})
	if err != nil {
		return nil, fmt.Errorf("get no-public-egress connector %q: %w", expected.ConnectorARN, err)
	}
	if isInternetEgressConnector(connector.ARN) || isInternetEgressConnector(connector.Name) {
		return nil, fmt.Errorf("%w: EgressNone cannot use the managed INTERNET_EGRESS connector", ErrInvalidOption)
	}
	if connector.ARN != expected.ConnectorARN {
		return nil, topologyMismatch(connector.Name, "connector ARN", []string{connector.ARN}, []string{expected.ConnectorARN})
	}
	if connector.State != awsapi.NetworkConnectorStateActive {
		return nil, &NoPublicEgressViolation{
			Err:        ErrConnectorNotActive,
			ResourceID: connector.Name,
			Detail:     fmt.Sprintf("state is %s", connector.State),
		}
	}
	if len(connector.SubnetIDs) == 0 || len(connector.SecurityGroupIDs) == 0 {
		return nil, &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: connector.Name,
			Detail:     "connector must reference at least one subnet and one security group",
		}
	}
	if len(expected.SubnetIDs) > 0 && !sameStringSet(connector.SubnetIDs, expected.SubnetIDs) {
		return nil, topologyMismatch(connector.Name, "subnets", connector.SubnetIDs, expected.SubnetIDs)
	}
	expectedSecurityGroups := expectedSecurityGroupIDs(expected)
	if len(expectedSecurityGroups) > 0 && !sameStringSet(connector.SecurityGroupIDs, expectedSecurityGroups) {
		return nil, topologyMismatch(connector.Name, "security groups", connector.SecurityGroupIDs, expectedSecurityGroups)
	}

	subnetsOut, err := m.api.DescribeSubnets(ctx, &awsapi.DescribeSubnetsInput{SubnetIDs: connector.SubnetIDs})
	if err != nil {
		return nil, fmt.Errorf("describe no-public-egress subnets: %w", err)
	}
	if len(subnetsOut.Items) != len(connector.SubnetIDs) {
		return nil, &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: connector.Name,
			Detail:     fmt.Sprintf("connector references %d subnets but EC2 returned %d", len(connector.SubnetIDs), len(subnetsOut.Items)),
		}
	}
	returnedSubnetIDs := make([]string, 0, len(subnetsOut.Items))
	for _, subnet := range subnetsOut.Items {
		returnedSubnetIDs = append(returnedSubnetIDs, subnet.ID)
	}
	if !sameStringSet(returnedSubnetIDs, connector.SubnetIDs) {
		return nil, topologyMismatch(connector.Name, "subnets returned by EC2", returnedSubnetIDs, connector.SubnetIDs)
	}

	var vpcID string
	for _, subnet := range subnetsOut.Items {
		if expected.ResourceGroup != "" {
			if err := requireResourceGroup(subnet.ID, subnet.Tags, expected.ResourceGroup); err != nil {
				return nil, err
			}
		}
		if vpcID == "" {
			vpcID = subnet.VPCID
		}
		if subnet.VPCID != vpcID {
			return nil, &NoPublicEgressViolation{
				Err:        ErrTopologyMismatch,
				ResourceID: connector.Name,
				Detail:     fmt.Sprintf("subnets span VPCs %s and %s", vpcID, subnet.VPCID),
			}
		}
	}
	if expected.VPCID != "" && vpcID != expected.VPCID {
		return nil, topologyMismatch(connector.Name, "VPC", []string{vpcID}, []string{expected.VPCID})
	}

	vpcOut, err := m.api.GetVPC(ctx, &awsapi.GetVPCInput{VPCID: vpcID})
	if err != nil {
		return nil, fmt.Errorf("get no-public-egress VPC %q: %w", vpcID, err)
	}
	if expected.ResourceGroup != "" {
		if err := requireResourceGroup(vpcOut.VPC.ID, vpcOut.VPC.Tags, expected.ResourceGroup); err != nil {
			return nil, err
		}
	}

	routeTablesOut, err := m.api.DescribeRouteTables(ctx, &awsapi.DescribeRouteTablesInput{VPCID: vpcID})
	if err != nil {
		return nil, fmt.Errorf("describe no-public-egress route tables for VPC %q: %w", vpcID, err)
	}
	routeTableIDs := make([]string, 0, len(subnetsOut.Items))
	for _, subnet := range subnetsOut.Items {
		rt, ok := routeTableForSubnet(routeTablesOut.Items, subnet.ID)
		if !ok {
			return nil, fmt.Errorf("%w: no route table governs subnet %s", awsapi.ErrNotFound, subnet.ID)
		}
		if expected.ResourceGroup != "" {
			if err := requireResourceGroup(rt.ID, rt.Tags, expected.ResourceGroup); err != nil {
				return nil, err
			}
		}
		if err := validateNoPublicEgressRouteTableShape(rt, subnet.ID, vpcOut.VPC.CIDRBlock); err != nil {
			return nil, err
		}
		routeTableIDs = appendUnique(routeTableIDs, rt.ID)
	}
	expectedRouteTables := expectedRouteTableIDs(expected)
	if len(expectedRouteTables) > 0 && !sameStringSet(routeTableIDs, expectedRouteTables) {
		return nil, topologyMismatch(connector.Name, "route tables", routeTableIDs, expectedRouteTables)
	}

	securityGroupsOut, err := m.api.DescribeSecurityGroups(ctx, &awsapi.DescribeSecurityGroupsInput{GroupIDs: connector.SecurityGroupIDs})
	if err != nil {
		return nil, fmt.Errorf("describe no-public-egress security groups: %w", err)
	}
	if len(securityGroupsOut.Items) != len(connector.SecurityGroupIDs) {
		return nil, &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: connector.Name,
			Detail:     fmt.Sprintf("connector references %d security groups but EC2 returned %d", len(connector.SecurityGroupIDs), len(securityGroupsOut.Items)),
		}
	}
	returnedSecurityGroupIDs := make([]string, 0, len(securityGroupsOut.Items))
	for _, sg := range securityGroupsOut.Items {
		returnedSecurityGroupIDs = append(returnedSecurityGroupIDs, sg.ID)
	}
	if !sameStringSet(returnedSecurityGroupIDs, connector.SecurityGroupIDs) {
		return nil, topologyMismatch(connector.Name, "security groups returned by EC2", returnedSecurityGroupIDs, connector.SecurityGroupIDs)
	}
	for _, sg := range securityGroupsOut.Items {
		if sg.VPCID != vpcID {
			return nil, topologyMismatch(sg.ID, "VPC", []string{sg.VPCID}, []string{vpcID})
		}
		if expected.ResourceGroup != "" {
			if err := requireResourceGroup(sg.ID, sg.Tags, expected.ResourceGroup); err != nil {
				return nil, err
			}
		}
		if err := validateNoPublicEgressSecurityGroupShape(sg); err != nil {
			return nil, err
		}
	}

	resources := &NoPublicEgressResources{
		VPCID:            vpcID,
		SubnetIDs:        append([]string(nil), connector.SubnetIDs...),
		RouteTableIDs:    append([]string(nil), routeTableIDs...),
		SecurityGroupIDs: append([]string(nil), connector.SecurityGroupIDs...),
		ConnectorARN:     connector.ARN,
		ResourceGroup:    expected.ResourceGroup,
	}
	if len(routeTableIDs) == 1 {
		resources.RouteTableID = routeTableIDs[0]
	}
	if len(connector.SecurityGroupIDs) == 1 {
		resources.SecurityGroupID = connector.SecurityGroupIDs[0]
	}
	return resources, nil
}

func routeTableForSubnet(routeTables []awsapi.RouteTable, subnetID string) (awsapi.RouteTable, bool) {
	for _, rt := range routeTables {
		for _, association := range rt.Associations {
			if association.SubnetID == subnetID {
				return rt, true
			}
		}
	}
	for _, rt := range routeTables {
		for _, association := range rt.Associations {
			if association.Main {
				return rt, true
			}
		}
	}
	return awsapi.RouteTable{}, false
}

func expectedSecurityGroupIDs(resources NoPublicEgressResources) []string {
	if len(resources.SecurityGroupIDs) > 0 {
		return resources.SecurityGroupIDs
	}
	if resources.SecurityGroupID != "" {
		return []string{resources.SecurityGroupID}
	}
	return nil
}

func expectedRouteTableIDs(resources NoPublicEgressResources) []string {
	if len(resources.RouteTableIDs) > 0 {
		return resources.RouteTableIDs
	}
	if resources.RouteTableID != "" {
		return []string{resources.RouteTableID}
	}
	return nil
}

func topologyMismatch(resourceID, field string, have, want []string) error {
	return &NoPublicEgressViolation{
		Err:        ErrTopologyMismatch,
		ResourceID: resourceID,
		Detail:     fmt.Sprintf("%s %v do not match expected %v", field, have, want),
	}
}

func requireResourceGroup(resourceID string, tags map[string]string, expected string) error {
	actual, err := noPublicEgressResourceGroup(resourceID, tags)
	if err != nil {
		return err
	}
	if actual != expected {
		return topologyMismatch(resourceID, "resource group", []string{actual}, []string{expected})
	}
	return nil
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}
