package sdkclient

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/udgover/whim/internal/awsapi"
)

// mapTags flattens the EC2 tag-list shape into the plain map awsapi callers
// expect. Returns nil for no tags, matching the SDK's own "absent" shape.
func mapTags(tags []ec2types.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

func mapVPC(v ec2types.Vpc) awsapi.VPC {
	return awsapi.VPC{
		ID:        aws.ToString(v.VpcId),
		CIDRBlock: aws.ToString(v.CidrBlock),
		Tags:      mapTags(v.Tags),
	}
}

func mapSubnet(s ec2types.Subnet) awsapi.Subnet {
	return awsapi.Subnet{
		ID:               aws.ToString(s.SubnetId),
		VPCID:            aws.ToString(s.VpcId),
		AvailabilityZone: aws.ToString(s.AvailabilityZone),
		CIDRBlock:        aws.ToString(s.CidrBlock),
		Tags:             mapTags(s.Tags),
	}
}

// mapRoute carries across every route-target field the EC2 Route type can
// populate — see the awsapi.Route doc comment for why this is a complete
// field mirror rather than a curated "known public-capable targets" subset.
func mapRoute(r ec2types.Route) awsapi.Route {
	return awsapi.Route{
		DestinationCIDRBlock:        aws.ToString(r.DestinationCidrBlock),
		DestinationIPv6CIDRBlock:    aws.ToString(r.DestinationIpv6CidrBlock),
		DestinationPrefixListID:     aws.ToString(r.DestinationPrefixListId),
		GatewayID:                   aws.ToString(r.GatewayId),
		NatGatewayID:                aws.ToString(r.NatGatewayId),
		TransitGatewayID:            aws.ToString(r.TransitGatewayId),
		VPCPeeringConnectionID:      aws.ToString(r.VpcPeeringConnectionId),
		EgressOnlyInternetGatewayID: aws.ToString(r.EgressOnlyInternetGatewayId),
		CarrierGatewayID:            aws.ToString(r.CarrierGatewayId),
		InstanceID:                  aws.ToString(r.InstanceId),
		NetworkInterfaceID:          aws.ToString(r.NetworkInterfaceId),
		LocalGatewayID:              aws.ToString(r.LocalGatewayId),
		CoreNetworkARN:              aws.ToString(r.CoreNetworkArn),
		IPAddress:                   aws.ToString(r.IpAddress),
		ODBNetworkARN:               aws.ToString(r.OdbNetworkArn),
		State:                       string(r.State),
	}
}

func mapRouteTableAssociation(a ec2types.RouteTableAssociation) awsapi.RouteTableAssociation {
	return awsapi.RouteTableAssociation{
		SubnetID: aws.ToString(a.SubnetId),
		Main:     aws.ToBool(a.Main),
	}
}

func mapRouteTable(rt ec2types.RouteTable) awsapi.RouteTable {
	routes := make([]awsapi.Route, len(rt.Routes))
	for i, r := range rt.Routes {
		routes[i] = mapRoute(r)
	}
	assocs := make([]awsapi.RouteTableAssociation, len(rt.Associations))
	for i, a := range rt.Associations {
		assocs[i] = mapRouteTableAssociation(a)
	}
	return awsapi.RouteTable{
		ID:           aws.ToString(rt.RouteTableId),
		VPCID:        aws.ToString(rt.VpcId),
		Routes:       routes,
		Associations: assocs,
		Tags:         mapTags(rt.Tags),
	}
}

// mapSecurityGroupRules expands one IpPermission into one SecurityGroupRule
// per distinct target (CIDR, IPv6 CIDR, prefix list, or referenced security
// group), each sharing the permission's protocol and port range. AWS lets a
// single permission carry multiple targets; Whim's MVP rejects any egress
// rule outright, but flattening here lets a validation error name exactly
// which target must be removed.
func mapSecurityGroupRules(p ec2types.IpPermission) []awsapi.SecurityGroupRule {
	var rules []awsapi.SecurityGroupRule
	proto := aws.ToString(p.IpProtocol)
	for _, r := range p.IpRanges {
		rules = append(rules, awsapi.SecurityGroupRule{
			IPProtocol: proto,
			FromPort:   p.FromPort,
			ToPort:     p.ToPort,
			CIDRIPv4:   aws.ToString(r.CidrIp),
		})
	}
	for _, r := range p.Ipv6Ranges {
		rules = append(rules, awsapi.SecurityGroupRule{
			IPProtocol: proto,
			FromPort:   p.FromPort,
			ToPort:     p.ToPort,
			CIDRIPv6:   aws.ToString(r.CidrIpv6),
		})
	}
	for _, pl := range p.PrefixListIds {
		rules = append(rules, awsapi.SecurityGroupRule{
			IPProtocol:   proto,
			FromPort:     p.FromPort,
			ToPort:       p.ToPort,
			PrefixListID: aws.ToString(pl.PrefixListId),
		})
	}
	for _, g := range p.UserIdGroupPairs {
		rules = append(rules, awsapi.SecurityGroupRule{
			IPProtocol:        proto,
			FromPort:          p.FromPort,
			ToPort:            p.ToPort,
			ReferencedGroupID: aws.ToString(g.GroupId),
		})
	}
	return rules
}

func mapSecurityGroup(sg ec2types.SecurityGroup) awsapi.SecurityGroup {
	var egress []awsapi.SecurityGroupRule
	for _, p := range sg.IpPermissionsEgress {
		egress = append(egress, mapSecurityGroupRules(p)...)
	}
	return awsapi.SecurityGroup{
		ID:          aws.ToString(sg.GroupId),
		VPCID:       aws.ToString(sg.VpcId),
		GroupName:   aws.ToString(sg.GroupName),
		EgressRules: egress,
		Tags:        mapTags(sg.Tags),
	}
}

func mapNetworkACLAssociation(a ec2types.NetworkAclAssociation) awsapi.NetworkACLAssociation {
	return awsapi.NetworkACLAssociation{SubnetID: aws.ToString(a.SubnetId)}
}

func mapNetworkACL(n ec2types.NetworkAcl) awsapi.NetworkACL {
	assocs := make([]awsapi.NetworkACLAssociation, len(n.Associations))
	for i, a := range n.Associations {
		assocs[i] = mapNetworkACLAssociation(a)
	}
	return awsapi.NetworkACL{
		ID:           aws.ToString(n.NetworkAclId),
		VPCID:        aws.ToString(n.VpcId),
		IsDefault:    aws.ToBool(n.IsDefault),
		Associations: assocs,
		Tags:         mapTags(n.Tags),
	}
}

func mapAvailabilityZone(z ec2types.AvailabilityZone) awsapi.AvailabilityZone {
	return awsapi.AvailabilityZone{
		ZoneName: aws.ToString(z.ZoneName),
		State:    string(z.State),
	}
}

// routeTableFilters and networkACLFilters build the EC2 filter set used to
// find the route table / network ACL governing a specific managed subnet
// (association.subnet-id) and, optionally, narrow by VPC.
func routeTableFilters(vpcID, subnetID string) []ec2types.Filter {
	var filters []ec2types.Filter
	if vpcID != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("vpc-id"), Values: []string{vpcID}})
	}
	if subnetID != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("association.subnet-id"), Values: []string{subnetID}})
	}
	return filters
}

func networkACLFilters(vpcID, subnetID string) []ec2types.Filter {
	var filters []ec2types.Filter
	if vpcID != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("vpc-id"), Values: []string{vpcID}})
	}
	if subnetID != "" {
		filters = append(filters, ec2types.Filter{Name: aws.String("association.subnet-id"), Values: []string{subnetID}})
	}
	return filters
}

// GetVPC delegates to EC2 DescribeVpcs for a single VPC ID. A missing VPC is
// reported as awsapi.ErrNotFound.
func (c *Client) GetVPC(ctx context.Context, in *awsapi.GetVPCInput) (*awsapi.GetVPCOutput, error) {
	out, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{in.VPCID},
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	if len(out.Vpcs) == 0 {
		return nil, awsapi.ErrNotFound
	}
	return &awsapi.GetVPCOutput{VPC: mapVPC(out.Vpcs[0])}, nil
}

// DescribeSubnets delegates to EC2 DescribeSubnets for the given subnet IDs.
func (c *Client) DescribeSubnets(ctx context.Context, in *awsapi.DescribeSubnetsInput) (*awsapi.DescribeSubnetsOutput, error) {
	out, err := c.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		SubnetIds: in.SubnetIDs,
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	items := make([]awsapi.Subnet, len(out.Subnets))
	for i, s := range out.Subnets {
		items[i] = mapSubnet(s)
	}
	return &awsapi.DescribeSubnetsOutput{Items: items}, nil
}

type describeRouteTablesFunc func(context.Context, *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)

func describeAllRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, fetch describeRouteTablesFunc) ([]ec2types.RouteTable, error) {
	var tables []ec2types.RouteTable
	var token *string
	for {
		pageIn := *in
		pageIn.NextToken = token
		out, err := fetch(ctx, &pageIn)
		if err != nil {
			return nil, err
		}
		tables = append(tables, out.RouteTables...)
		next := aws.ToString(out.NextToken)
		if next == "" {
			return tables, nil
		}
		if next == aws.ToString(token) {
			return nil, fmt.Errorf("EC2 DescribeRouteTables returned repeated pagination token %q", next)
		}
		token = aws.String(next)
	}
}

// DescribeRouteTables delegates to EC2 DescribeRouteTables, follows every
// page, and filters by VPC and/or by the subnet the route table is associated
// with. Complete pagination is security-sensitive: validation must see a
// subnet's explicit table even when EC2 returns the VPC's main table first.
func (c *Client) DescribeRouteTables(ctx context.Context, in *awsapi.DescribeRouteTablesInput) (*awsapi.DescribeRouteTablesOutput, error) {
	routeTables, err := describeAllRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: routeTableFilters(in.VPCID, in.SubnetID),
	}, func(ctx context.Context, in *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
		return c.ec2.DescribeRouteTables(ctx, in)
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	items := make([]awsapi.RouteTable, len(routeTables))
	for i, rt := range routeTables {
		items[i] = mapRouteTable(rt)
	}
	return &awsapi.DescribeRouteTablesOutput{Items: items}, nil
}

// DescribeSecurityGroups delegates to EC2 DescribeSecurityGroups for the
// given group IDs.
func (c *Client) DescribeSecurityGroups(ctx context.Context, in *awsapi.DescribeSecurityGroupsInput) (*awsapi.DescribeSecurityGroupsOutput, error) {
	out, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: in.GroupIDs,
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	items := make([]awsapi.SecurityGroup, len(out.SecurityGroups))
	for i, sg := range out.SecurityGroups {
		items[i] = mapSecurityGroup(sg)
	}
	return &awsapi.DescribeSecurityGroupsOutput{Items: items}, nil
}

// DescribeNetworkACLs delegates to EC2 DescribeNetworkAcls, filtered by VPC
// and/or by an associated subnet.
func (c *Client) DescribeNetworkACLs(ctx context.Context, in *awsapi.DescribeNetworkACLsInput) (*awsapi.DescribeNetworkACLsOutput, error) {
	out, err := c.ec2.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{
		Filters: networkACLFilters(in.VPCID, in.SubnetID),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	items := make([]awsapi.NetworkACL, len(out.NetworkAcls))
	for i, n := range out.NetworkAcls {
		items[i] = mapNetworkACL(n)
	}
	return &awsapi.DescribeNetworkACLsOutput{Items: items}, nil
}

// DescribeAvailabilityZones delegates to EC2 DescribeAvailabilityZones,
// scoped (by the SDK default) to the zones the caller's account has opted
// into for the client's region.
func (c *Client) DescribeAvailabilityZones(ctx context.Context) (*awsapi.DescribeAvailabilityZonesOutput, error) {
	out, err := c.ec2.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	items := make([]awsapi.AvailabilityZone, len(out.AvailabilityZones))
	for i, z := range out.AvailabilityZones {
		items[i] = mapAvailabilityZone(z)
	}
	return &awsapi.DescribeAvailabilityZonesOutput{Items: items}, nil
}

// ec2TagSpecification builds the TagSpecifications field EC2's Create* calls
// use to tag a resource at creation time, avoiding a separate CreateTags
// round trip. Returns nil for no tags, so the request omits the field
// entirely.
func ec2TagSpecification(resourceType ec2types.ResourceType, tags map[string]string) []ec2types.TagSpecification {
	if len(tags) == 0 {
		return nil
	}
	ec2Tags := make([]ec2types.Tag, 0, len(tags))
	for k, v := range tags {
		ec2Tags = append(ec2Tags, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return []ec2types.TagSpecification{{ResourceType: resourceType, Tags: ec2Tags}}
}

// CreateVPC creates a Whim-managed VPC.
func (c *Client) CreateVPC(ctx context.Context, in *awsapi.CreateVPCInput) (*awsapi.CreateVPCOutput, error) {
	out, err := c.ec2.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock:         aws.String(in.CIDRBlock),
		TagSpecifications: ec2TagSpecification(ec2types.ResourceTypeVpc, in.Tags),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.CreateVPCOutput{VPCID: aws.ToString(out.Vpc.VpcId)}, nil
}

// CreateSubnet creates a Whim-managed subnet within an existing VPC.
func (c *Client) CreateSubnet(ctx context.Context, in *awsapi.CreateSubnetInput) (*awsapi.CreateSubnetOutput, error) {
	out, err := c.ec2.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:             aws.String(in.VPCID),
		CidrBlock:         aws.String(in.CIDRBlock),
		AvailabilityZone:  optionalString(in.AvailabilityZone),
		TagSpecifications: ec2TagSpecification(ec2types.ResourceTypeSubnet, in.Tags),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.CreateSubnetOutput{SubnetID: aws.ToString(out.Subnet.SubnetId)}, nil
}

// CreateRouteTable creates a Whim-managed route table. AWS gives it exactly
// one route (the implicit local route for the VPC's own CIDR); no further
// calls are needed to reach the no-public-egress shape.
func (c *Client) CreateRouteTable(ctx context.Context, in *awsapi.CreateRouteTableInput) (*awsapi.CreateRouteTableOutput, error) {
	out, err := c.ec2.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId:             aws.String(in.VPCID),
		TagSpecifications: ec2TagSpecification(ec2types.ResourceTypeRouteTable, in.Tags),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.CreateRouteTableOutput{RouteTableID: aws.ToString(out.RouteTable.RouteTableId)}, nil
}

// AssociateRouteTable binds a route table to a subnet, overriding that
// subnet's implicit association with the VPC's main route table.
func (c *Client) AssociateRouteTable(ctx context.Context, in *awsapi.AssociateRouteTableInput) (*awsapi.AssociateRouteTableOutput, error) {
	out, err := c.ec2.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(in.RouteTableID),
		SubnetId:     aws.String(in.SubnetID),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.AssociateRouteTableOutput{AssociationID: aws.ToString(out.AssociationId)}, nil
}

// CreateSecurityGroup creates a Whim-managed security group. AWS adds a
// default "allow all outbound" egress rule automatically; callers must
// follow up with RevokeAllSecurityGroupEgress to remove it.
func (c *Client) CreateSecurityGroup(ctx context.Context, in *awsapi.CreateSecurityGroupInput) (*awsapi.CreateSecurityGroupOutput, error) {
	out, err := c.ec2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		VpcId:             aws.String(in.VPCID),
		GroupName:         aws.String(in.GroupName),
		Description:       aws.String(in.Description),
		TagSpecifications: ec2TagSpecification(ec2types.ResourceTypeSecurityGroup, in.Tags),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.CreateSecurityGroupOutput{SecurityGroupID: aws.ToString(out.GroupId)}, nil
}

// defaultEgressAllRule is the exact rule EC2 adds automatically to every
// newly created security group.
var defaultEgressAllRule = []ec2types.IpPermission{
	{
		IpProtocol: aws.String("-1"),
		IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
	},
}

// RevokeAllSecurityGroupEgress removes the default "allow all outbound"
// egress rule EC2 adds automatically when a security group is created.
func (c *Client) RevokeAllSecurityGroupEgress(ctx context.Context, in *awsapi.RevokeAllSecurityGroupEgressInput) error {
	_, err := c.ec2.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId:       aws.String(in.SecurityGroupID),
		IpPermissions: defaultEgressAllRule,
	})
	return mapEC2Err(err)
}

// CreateNetworkACL creates a Whim-managed network ACL. A newly created
// (non-default) NACL denies all traffic by default until entries are
// added; Whim adds none, so no further calls are needed.
func (c *Client) CreateNetworkACL(ctx context.Context, in *awsapi.CreateNetworkACLInput) (*awsapi.CreateNetworkACLOutput, error) {
	out, err := c.ec2.CreateNetworkAcl(ctx, &ec2.CreateNetworkAclInput{
		VpcId:             aws.String(in.VPCID),
		TagSpecifications: ec2TagSpecification(ec2types.ResourceTypeNetworkAcl, in.Tags),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.CreateNetworkACLOutput{NetworkACLID: aws.ToString(out.NetworkAcl.NetworkAclId)}, nil
}

// ReplaceNetworkACLAssociation moves a subnet's NACL association to a new
// NACL, given the subnet's current association ID (from DescribeNetworkACLs).
func (c *Client) ReplaceNetworkACLAssociation(ctx context.Context, in *awsapi.ReplaceNetworkACLAssociationInput) (*awsapi.ReplaceNetworkACLAssociationOutput, error) {
	out, err := c.ec2.ReplaceNetworkAclAssociation(ctx, &ec2.ReplaceNetworkAclAssociationInput{
		AssociationId: aws.String(in.CurrentAssociationID),
		NetworkAclId:  aws.String(in.NetworkACLID),
	})
	if err != nil {
		return nil, mapEC2Err(err)
	}
	return &awsapi.ReplaceNetworkACLAssociationOutput{AssociationID: aws.ToString(out.NewAssociationId)}, nil
}
