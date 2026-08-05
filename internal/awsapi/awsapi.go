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
	MicrovmID               string
	Endpoint                string
	State                   string
	EgressNetworkConnectors []string
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
	// Capabilities are elevated OS capabilities (additionalOsCapabilities),
	// carried as plain strings across this boundary; nil for the default image.
	Capabilities []string
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

// GetMicrovmImageVersionInput identifies a specific version of an image.
type GetMicrovmImageVersionInput struct {
	ImageIdentifier string
	ImageVersion    string
}

// GetMicrovmImageVersionOutput exposes the version-level fields whim consumes.
// Capabilities mirrors additionalOsCapabilities as plain strings.
// EgressConnectors mirrors egressNetworkConnectors as connector ARNs.
type GetMicrovmImageVersionOutput struct {
	Capabilities     []string
	EgressConnectors []string
}

// Network connector states used by Lambda Core.
const (
	NetworkConnectorStatePending      = "PENDING"
	NetworkConnectorStateActive       = "ACTIVE"
	NetworkConnectorStateInactive     = "INACTIVE"
	NetworkConnectorStateFailed       = "FAILED"
	NetworkConnectorStateDeleting     = "DELETING"
	NetworkConnectorStateDeleteFailed = "DELETE_FAILED"
)

// NetworkConnectorSummary is one Lambda Core network connector list item.
type NetworkConnectorSummary struct {
	ARN   string
	Name  string
	State string
	Type  string
}

// ListNetworkConnectorsInput optionally filters connector listing by state.
type ListNetworkConnectorsInput struct {
	State string
}

// ListNetworkConnectorsOutput wraps the fully-paginated connector list.
type ListNetworkConnectorsOutput struct {
	Items []NetworkConnectorSummary
}

// GetNetworkConnectorInput identifies a connector by name, ID, or ARN.
type GetNetworkConnectorInput struct {
	Identifier string
}

// GetNetworkConnectorOutput exposes connector fields whim needs. SubnetIDs
// and SecurityGroupIDs are extracted from the connector's
// VpcEgressConfiguration (empty for a connector type that isn't VPC egress)
// so callers can validate the connector's topology without depending on the
// SDK's union configuration type.
type GetNetworkConnectorOutput struct {
	ARN              string
	Name             string
	State            string
	StateReason      string
	StateReasonCode  string
	SubnetIDs        []string
	SecurityGroupIDs []string
}

// CreateNetworkConnectorInput holds the VPC-egress connector fields used by
// Lambda Core. The VPC routing and security group rules determine whether this
// behaves as private-only or public-capable egress.
type CreateNetworkConnectorInput struct {
	Name             string
	SubnetIDs        []string
	SecurityGroupIDs []string
	OperatorRoleARN  string
	Tags             map[string]string
}

// CreateNetworkConnectorOutput exposes the newly-created connector state.
type CreateNetworkConnectorOutput struct {
	ARN   string
	Name  string
	State string
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

// VPC is the narrow read model of an Amazon VPC used to validate a
// no-public-egress topology.
type VPC struct {
	ID        string
	CIDRBlock string
	Tags      map[string]string
}

// GetVPCInput identifies a VPC by ID.
type GetVPCInput struct {
	VPCID string
}

// GetVPCOutput wraps the VPC read model.
type GetVPCOutput struct {
	VPC VPC
}

// Subnet is the narrow read model of a VPC subnet.
type Subnet struct {
	ID               string
	VPCID            string
	AvailabilityZone string
	CIDRBlock        string
	Tags             map[string]string
}

// DescribeSubnetsInput selects subnets by ID.
type DescribeSubnetsInput struct {
	SubnetIDs []string
}

// DescribeSubnetsOutput wraps the matching subnet read models.
type DescribeSubnetsOutput struct {
	Items []Subnet
}

// Route mirrors every route-target field the EC2 Route type can populate:
// the destination CIDR plus every kind of target (gateway, NAT, transit
// gateway, peering, egress-only/carrier gateway, prefix list, instance,
// network interface, local gateway, core network, route-server next-hop IP,
// and ODB network). Milestone 2 validation does not enumerate which of
// these targets are "public-capable" and reject only those — that approach
// would silently miss any future EC2 route-target type. Instead it allows
// only an exact local-only route (GatewayID == "local" matching the VPC's
// own CIDR, every other field empty) and rejects anything else. These
// fields exist so a rejection error can name exactly which target is set,
// not because validation depends on covering every target type.
type Route struct {
	DestinationCIDRBlock        string
	DestinationIPv6CIDRBlock    string
	DestinationPrefixListID     string
	GatewayID                   string
	NatGatewayID                string
	TransitGatewayID            string
	VPCPeeringConnectionID      string
	EgressOnlyInternetGatewayID string
	CarrierGatewayID            string
	InstanceID                  string
	NetworkInterfaceID          string
	LocalGatewayID              string
	CoreNetworkARN              string
	IPAddress                   string
	ODBNetworkARN               string
	State                       string
}

// RouteTableAssociation mirrors one subnet (or main-table) association for a
// route table.
type RouteTableAssociation struct {
	SubnetID string
	Main     bool
}

// RouteTable is the narrow read model of a VPC route table.
type RouteTable struct {
	ID           string
	VPCID        string
	Routes       []Route
	Associations []RouteTableAssociation
	Tags         map[string]string
}

// DescribeRouteTablesInput filters route tables by VPC and/or by an
// associated subnet — the latter is how Whim finds the route table
// governing a managed subnet.
type DescribeRouteTablesInput struct {
	VPCID    string
	SubnetID string
}

// DescribeRouteTablesOutput wraps the matching route table read models.
type DescribeRouteTablesOutput struct {
	Items []RouteTable
}

// SecurityGroupRule is one security group rule. Whim's MVP validation
// rejects any security-group egress rule outright, but keeps enough detail
// here (protocol, ports, and target) for actionable error messages.
type SecurityGroupRule struct {
	IPProtocol        string
	FromPort          *int32
	ToPort            *int32
	CIDRIPv4          string
	CIDRIPv6          string
	PrefixListID      string
	ReferencedGroupID string
}

// SecurityGroup is the narrow read model of a VPC security group.
type SecurityGroup struct {
	ID          string
	VPCID       string
	GroupName   string
	EgressRules []SecurityGroupRule
	Tags        map[string]string
}

// DescribeSecurityGroupsInput selects security groups by ID.
type DescribeSecurityGroupsInput struct {
	GroupIDs []string
}

// DescribeSecurityGroupsOutput wraps the matching security group read models.
type DescribeSecurityGroupsOutput struct {
	Items []SecurityGroup
}

// NetworkACLAssociation mirrors one subnet association for a network ACL.
type NetworkACLAssociation struct {
	SubnetID string
}

// NetworkACL is the narrow read model of a network ACL. Whim's MVP validates
// only association with managed subnets, not rule content: a NACL is not a
// DNS control and is treated as an optional additional layer, not the
// primary enforcement mechanism.
type NetworkACL struct {
	ID           string
	VPCID        string
	IsDefault    bool
	Associations []NetworkACLAssociation
	Tags         map[string]string
}

// DescribeNetworkACLsInput filters network ACLs by VPC and/or by an
// associated subnet.
type DescribeNetworkACLsInput struct {
	VPCID    string
	SubnetID string
}

// DescribeNetworkACLsOutput wraps the matching network ACL read models.
type DescribeNetworkACLsOutput struct {
	Items []NetworkACL
}

// AvailabilityZone is the narrow read model of an availability zone.
type AvailabilityZone struct {
	ZoneName string
	State    string
}

// DescribeAvailabilityZonesOutput wraps the availability zone read models.
type DescribeAvailabilityZonesOutput struct {
	Items []AvailabilityZone
}

// --- EC2 creation methods (Milestone 3, Task 3.1). These create or mutate
// real, billable AWS resources — never called by CLI code directly, only by
// Manager's no-public-egress provisioning path, and only after Milestone 2's
// read-only validation has confirmed no safe existing resource can be
// reused. ---

// CreateVPCInput holds the fields needed to create a Whim-managed VPC.
type CreateVPCInput struct {
	CIDRBlock string
	Tags      map[string]string
}

// CreateVPCOutput holds the newly created VPC's ID.
type CreateVPCOutput struct {
	VPCID string
}

// CreateSubnetInput holds the fields needed to create a Whim-managed subnet.
type CreateSubnetInput struct {
	VPCID            string
	CIDRBlock        string
	AvailabilityZone string
	Tags             map[string]string
}

// CreateSubnetOutput holds the newly created subnet's ID.
type CreateSubnetOutput struct {
	SubnetID string
}

// CreateRouteTableInput holds the fields needed to create a Whim-managed
// route table. AWS gives it exactly one route (the implicit local route for
// the VPC's own CIDR) with no further calls needed.
type CreateRouteTableInput struct {
	VPCID string
	Tags  map[string]string
}

// CreateRouteTableOutput holds the newly created route table's ID.
type CreateRouteTableOutput struct {
	RouteTableID string
}

// AssociateRouteTableInput binds a route table to a subnet, overriding that
// subnet's implicit association with the VPC's main route table.
type AssociateRouteTableInput struct {
	RouteTableID string
	SubnetID     string
}

// AssociateRouteTableOutput holds the new association's ID.
type AssociateRouteTableOutput struct {
	AssociationID string
}

// CreateSecurityGroupInput holds the fields needed to create a Whim-managed
// security group. AWS adds one default "allow all outbound" egress rule
// automatically; call RevokeAllSecurityGroupEgress immediately afterward to
// remove it, since the no-public-egress MVP requires zero egress rules.
type CreateSecurityGroupInput struct {
	VPCID       string
	GroupName   string
	Description string
	Tags        map[string]string
}

// CreateSecurityGroupOutput holds the newly created security group's ID.
type CreateSecurityGroupOutput struct {
	SecurityGroupID string
}

// RevokeAllSecurityGroupEgressInput identifies the security group to strip
// its default "allow all outbound" egress rule from.
type RevokeAllSecurityGroupEgressInput struct {
	SecurityGroupID string
}

// CreateNetworkACLInput holds the fields needed to create a Whim-managed
// network ACL. A newly created (non-default) NACL denies all traffic by
// default until entries are added; Whim adds none, so this needs no further
// calls to reach the desired deny-all shape.
type CreateNetworkACLInput struct {
	VPCID string
	Tags  map[string]string
}

// CreateNetworkACLOutput holds the newly created network ACL's ID.
type CreateNetworkACLOutput struct {
	NetworkACLID string
}

// ReplaceNetworkACLAssociationInput moves a subnet's NACL association from
// whatever it currently is (every subnet always has exactly one, typically
// the VPC's default NACL) to a new NACL. CurrentAssociationID comes from
// DescribeNetworkACLs.
type ReplaceNetworkACLAssociationInput struct {
	CurrentAssociationID string
	NetworkACLID         string
}

// ReplaceNetworkACLAssociationOutput holds the new association's ID.
type ReplaceNetworkACLAssociationOutput struct {
	AssociationID string
}

// GetRoleInput identifies an IAM role by name (not ARN — GetRole is a
// name-keyed API).
type GetRoleInput struct {
	RoleName string
}

// GetRoleOutput exposes the IAM role fields needed to validate a
// user-supplied network connector operator role. AssumeRolePolicyDocument is
// decoded to plain JSON; IAM returns it URL-encoded.
type GetRoleOutput struct {
	ARN                      string
	RoleName                 string
	AssumeRolePolicyDocument string
}

// AttachedPolicy is one managed policy attached to a role.
type AttachedPolicy struct {
	PolicyName string
	PolicyARN  string
}

// ListAttachedRolePoliciesInput identifies the role to list attached managed
// policies for.
type ListAttachedRolePoliciesInput struct {
	RoleName string
}

// ListAttachedRolePoliciesOutput wraps the fully-paginated attached-policy list.
type ListAttachedRolePoliciesOutput struct {
	Items []AttachedPolicy
}

// ListRolePoliciesInput identifies the role to list inline policy names for.
type ListRolePoliciesInput struct {
	RoleName string
}

// ListRolePoliciesOutput wraps the fully-paginated inline-policy-name list.
// Whim only needs to know an inline policy exists (for validation
// messaging); inspecting its document content is not implemented here.
type ListRolePoliciesOutput struct {
	PolicyNames []string
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
	GetMicrovmImageVersion(ctx context.Context, in *GetMicrovmImageVersionInput) (*GetMicrovmImageVersionOutput, error)
	ListNetworkConnectors(ctx context.Context, in *ListNetworkConnectorsInput) (*ListNetworkConnectorsOutput, error)
	GetNetworkConnector(ctx context.Context, in *GetNetworkConnectorInput) (*GetNetworkConnectorOutput, error)
	CreateNetworkConnector(ctx context.Context, in *CreateNetworkConnectorInput) (*CreateNetworkConnectorOutput, error)
	DeleteMicrovmImage(ctx context.Context, in *DeleteMicrovmImageInput) error
	ListMicrovmImages(ctx context.Context, in *ListMicrovmImagesInput) (*ListMicrovmImagesOutput, error)

	// EC2 read-model methods (Milestone 1, Task 1.1). These are read-only:
	// no method here creates, modifies, or deletes an EC2 resource.
	GetVPC(ctx context.Context, in *GetVPCInput) (*GetVPCOutput, error)
	DescribeSubnets(ctx context.Context, in *DescribeSubnetsInput) (*DescribeSubnetsOutput, error)
	DescribeRouteTables(ctx context.Context, in *DescribeRouteTablesInput) (*DescribeRouteTablesOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *DescribeSecurityGroupsInput) (*DescribeSecurityGroupsOutput, error)
	DescribeNetworkACLs(ctx context.Context, in *DescribeNetworkACLsInput) (*DescribeNetworkACLsOutput, error)
	DescribeAvailabilityZones(ctx context.Context) (*DescribeAvailabilityZonesOutput, error)

	// EC2 creation methods (Milestone 3, Task 3.1). These create or mutate
	// real AWS resources.
	CreateVPC(ctx context.Context, in *CreateVPCInput) (*CreateVPCOutput, error)
	CreateSubnet(ctx context.Context, in *CreateSubnetInput) (*CreateSubnetOutput, error)
	CreateRouteTable(ctx context.Context, in *CreateRouteTableInput) (*CreateRouteTableOutput, error)
	AssociateRouteTable(ctx context.Context, in *AssociateRouteTableInput) (*AssociateRouteTableOutput, error)
	CreateSecurityGroup(ctx context.Context, in *CreateSecurityGroupInput) (*CreateSecurityGroupOutput, error)
	RevokeAllSecurityGroupEgress(ctx context.Context, in *RevokeAllSecurityGroupEgressInput) error
	CreateNetworkACL(ctx context.Context, in *CreateNetworkACLInput) (*CreateNetworkACLOutput, error)
	ReplaceNetworkACLAssociation(ctx context.Context, in *ReplaceNetworkACLAssociationInput) (*ReplaceNetworkACLAssociationOutput, error)

	// IAM read-model methods (Milestone 1, Task 1.2), used to validate a
	// user-supplied --egress-operator-role before Whim relies on it. These
	// are read-only: no method here creates, modifies, or deletes an IAM role.
	GetRole(ctx context.Context, in *GetRoleInput) (*GetRoleOutput, error)
	ListAttachedRolePolicies(ctx context.Context, in *ListAttachedRolePoliciesInput) (*ListAttachedRolePoliciesOutput, error)
	ListRolePolicies(ctx context.Context, in *ListRolePoliciesInput) (*ListRolePoliciesOutput, error)
}
