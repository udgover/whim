package microvm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/udgover/whim/internal/awsapi"
)

// Whim-managed no-public-egress resources are tagged with these key/value
// pairs so Whim can prove ownership before reusing them. WhimResourceGroup
// additionally carries a stable id, so a resource group survives a rename
// (the name prefix is a display convenience, not the identity).
const (
	noPublicEgressManagedByTag   = "ManagedBy"
	noPublicEgressManagedByValue = "whim"
	noPublicEgressPurposeTag     = "Purpose"
	noPublicEgressPurposeValue   = "no-public-egress"
	noPublicEgressGroupTag       = "WhimResourceGroup"
)

// defaultNoPublicEgressPrefix is the --egress-resource-prefix default.
const defaultNoPublicEgressPrefix = "whim"

// NoPublicEgressTags returns the tag set Whim stamps on every resource it
// creates for a no-public-egress resource group.
func NoPublicEgressTags(resourceGroup string) map[string]string {
	return map[string]string{
		noPublicEgressManagedByTag: noPublicEgressManagedByValue,
		noPublicEgressPurposeTag:   noPublicEgressPurposeValue,
		noPublicEgressGroupTag:     resourceGroup,
	}
}

// isNoPublicEgressOwned reports whether tags carry the ManagedBy/Purpose
// pair Whim requires before treating a resource as safely reusable. It does
// not check WhimResourceGroup: callers that need to match one specific
// resource group compare that tag themselves.
func isNoPublicEgressOwned(tags map[string]string) bool {
	return tags[noPublicEgressManagedByTag] == noPublicEgressManagedByValue &&
		tags[noPublicEgressPurposeTag] == noPublicEgressPurposeValue
}

// requireNoPublicEgressOwnership returns a *NoPublicEgressViolation wrapping
// ErrResourceNotOwned if tags lack Whim's no-public-egress ownership tags,
// and nil otherwise.
func requireNoPublicEgressOwnership(resourceID string, tags map[string]string) error {
	if isNoPublicEgressOwned(tags) {
		return nil
	}
	return &NoPublicEgressViolation{
		Err:        ErrResourceNotOwned,
		ResourceID: resourceID,
		Detail:     "missing ManagedBy=whim and/or Purpose=no-public-egress tags",
	}
}

func noPublicEgressResourceGroup(resourceID string, tags map[string]string) (string, error) {
	if err := requireNoPublicEgressOwnership(resourceID, tags); err != nil {
		return "", err
	}
	resourceGroup := tags[noPublicEgressGroupTag]
	if resourceGroup == "" {
		return "", &NoPublicEgressViolation{
			Err:        ErrResourceNotOwned,
			ResourceID: resourceID,
			Detail:     "missing WhimResourceGroup identity tag",
		}
	}
	return resourceGroup, nil
}

// NoPublicEgressNames is the deterministic resource name set Whim derives
// from a name prefix, matching the naming scheme in the source spec's
// Resource Model section (dev/docs/no-public-egress-connector-spec.md).
type NoPublicEgressNames struct {
	VPC           string
	SubnetA       string
	SubnetB       string
	RouteTable    string
	SecurityGroup string
	NetworkACL    string
	OperatorRole  string
	Connector     string
}

// newNoPublicEgressNames builds the deterministic name set for prefix. An
// empty prefix uses defaultNoPublicEgressPrefix.
func newNoPublicEgressNames(prefix string) NoPublicEgressNames {
	if prefix == "" {
		prefix = defaultNoPublicEgressPrefix
	}
	base := prefix + "-no-public-egress"
	return NoPublicEgressNames{
		VPC:           base + "-vpc",
		SubnetA:       base + "-subnet-a",
		SubnetB:       base + "-subnet-b",
		RouteTable:    base + "-rt",
		SecurityGroup: base + "-sg",
		NetworkACL:    base + "-acl",
		OperatorRole:  base + "-operator-role",
		Connector:     base,
	}
}

// NoPublicEgressSpec configures no-public-egress resource discovery,
// validation, and (Milestone 3) creation.
type NoPublicEgressSpec struct {
	// NamePrefix defaults to "whim" (defaultNoPublicEgressPrefix) when empty.
	NamePrefix string
	// ConnectorName overrides the derived connector name; empty uses the
	// name prefix's default connector name.
	ConnectorName string
	// ResourceGroup is the stable id stamped as the WhimResourceGroup tag.
	// Generated at creation time (Milestone 3); required to validate reuse
	// unambiguously when a name alone could collide.
	ResourceGroup string

	// The fields below are required only when EnsureNoPublicEgressConnector
	// finds nothing existing under the expected connector name and needs to
	// create a fresh resource group. They are unused on the reuse path.

	// OperatorRoleARN is an existing IAM role's ARN or name, passed to
	// CreateNetworkConnector as OperatorRole. Whim never creates this role
	// itself (Task 3.2 decision); creation fails closed if this is empty.
	OperatorRoleARN string
	// VPCCIDRBlock is the CIDR block for a freshly created VPC. Required for
	// creation; there is no default, since any hardcoded CIDR risks
	// colliding with the caller's existing networks.
	VPCCIDRBlock string
	// SubnetCIDRBlock is the CIDR block for a freshly created subnet; must
	// fall within VPCCIDRBlock. Required for creation.
	SubnetCIDRBlock string
	// AvailabilityZone is the AZ for a freshly created subnet. Empty picks
	// the first zone DescribeAvailabilityZones reports as "available".
	AvailabilityZone string
}

// names resolves the deterministic resource names for spec, applying the
// ConnectorName override if set.
func (s NoPublicEgressSpec) names() NoPublicEgressNames {
	n := newNoPublicEgressNames(s.NamePrefix)
	if s.ConnectorName != "" {
		n.Connector = s.ConnectorName
	}
	return n
}

// NoPublicEgressResources identifies a no-public-egress resource group by
// AWS resource ID/ARN rather than by name: names can collide, be reused
// after deletion, or change with the name-prefix flag; IDs cannot.
type NoPublicEgressResources struct {
	VPCID            string
	SubnetIDs        []string
	RouteTableID     string
	RouteTableIDs    []string
	SecurityGroupID  string
	SecurityGroupIDs []string
	// NetworkACLID is empty when no Whim-managed NACL is in use.
	NetworkACLID  string
	ConnectorARN  string
	ResourceGroup string
}

// NoPublicEgressViolation is one specific reason a no-public-egress resource
// group fails validation. It wraps one of the Err* sentinels in errors.go so
// callers can branch with errors.Is, while carrying enough detail (which
// resource, what was found) for an actionable CLI message.
type NoPublicEgressViolation struct {
	// Err is one of ErrPublicRoute, ErrSecurityGroupEgress,
	// ErrResourceNotOwned, or ErrConnectorMissing.
	Err error
	// ResourceID is the offending resource's ID or ARN (route table,
	// security group, subnet, connector, ...).
	ResourceID string
	// Detail is a human-readable specific, e.g. "route to igw-0123 for
	// destination 0.0.0.0/0". May be empty.
	Detail string
}

// Error renders "<sentinel>: <resourceID>[: <detail>]".
func (v *NoPublicEgressViolation) Error() string {
	if v.Detail == "" {
		return fmt.Sprintf("%v: %s", v.Err, v.ResourceID)
	}
	return fmt.Sprintf("%v: %s: %s", v.Err, v.ResourceID, v.Detail)
}

// Unwrap exposes Err so errors.Is/errors.As match the wrapped sentinel.
func (v *NoPublicEgressViolation) Unwrap() error { return v.Err }

// PartialNoPublicEgressCreationError is returned when creating a fresh
// no-public-egress resource group fails partway through. Resources names
// the IDs of everything successfully created before the failure, so a human
// can find and clean them up — Whim never rolls back or deletes them
// automatically (managed-resource cleanup is a deliberately separate,
// explicit, confirmed command; see Milestone 6), and it never falls back to
// public egress.
type PartialNoPublicEgressCreationError struct {
	// Resources holds whichever fields were successfully populated before
	// Step failed. Fields for resources not yet created remain zero-valued.
	Resources *NoPublicEgressResources
	// Step names the creation call that failed, e.g. "CreateSubnet".
	Step string
	Err  error
}

func (e *PartialNoPublicEgressCreationError) Error() string {
	return fmt.Sprintf("no-public-egress creation failed at %s: %v (created so far: %+v)", e.Step, e.Err, e.Resources)
}

func (e *PartialNoPublicEgressCreationError) Unwrap() error { return e.Err }

// validateNoPublicEgressRouteTable returns a *NoPublicEgressViolation if rt
// is not Whim-owned (ErrResourceNotOwned), does not actually govern subnetID
// (ErrTopologyMismatch), or contains any route that is not an exact local
// route for vpcCIDR (ErrPublicRoute), and nil otherwise.
//
// Checks run in order of how fundamental the failure is, so none is masked
// by a later, more superficial one: ownership, then association (a
// local-only table that doesn't actually govern the managed subnet proves
// nothing), then route content.
func validateNoPublicEgressRouteTable(rt awsapi.RouteTable, subnetID, vpcCIDR string) error {
	if err := requireNoPublicEgressOwnership(rt.ID, rt.Tags); err != nil {
		return err
	}
	return validateNoPublicEgressRouteTableShape(rt, subnetID, vpcCIDR)
}

func validateNoPublicEgressRouteTableShape(rt awsapi.RouteTable, subnetID, vpcCIDR string) error {
	if !routeTableAssociatedWithSubnet(rt, subnetID) {
		return &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: rt.ID,
			Detail:     fmt.Sprintf("not associated with expected subnet %s", subnetID),
		}
	}
	for _, r := range rt.Routes {
		if err := validateNoPublicEgressRoute(rt.ID, r, vpcCIDR); err != nil {
			return err
		}
	}
	return nil
}

// routeTableAssociatedWithSubnet reports whether rt governs subnetID: either
// an explicit association names it, or rt has no explicit subnet
// associations at all and is the VPC's main route table — the implicit
// association every subnet without an explicit one falls back to. A route
// table with explicit associations that simply don't include subnetID is
// never treated as an implicit fallback for it, even if one of those
// associations also marks it as the main table (that Main association
// governs only the subnets with no explicit association of their own,
// which by definition excludes subnetID once something else claims it).
func routeTableAssociatedWithSubnet(rt awsapi.RouteTable, subnetID string) bool {
	hasOtherExplicitSubnetAssociation := false
	for _, a := range rt.Associations {
		if a.SubnetID == subnetID {
			return true
		}
		if a.SubnetID != "" {
			hasOtherExplicitSubnetAssociation = true
		}
	}
	if hasOtherExplicitSubnetAssociation {
		return false
	}
	for _, a := range rt.Associations {
		if a.Main {
			return true
		}
	}
	return false
}

// validateNoPublicEgressRoute rejects any route that is not an exact local
// route for vpcCIDR. The 0.0.0.0/0 and ::/0 checks are explicit and
// independent of isLocalOnlyRoute's target check, so a default route is
// rejected outright even if some future data source ever paired it with
// GatewayID "local" — AWS itself would never produce that combination, but
// this function does not rely on that guarantee holding.
func validateNoPublicEgressRoute(routeTableID string, r awsapi.Route, vpcCIDR string) error {
	if r.DestinationCIDRBlock == "0.0.0.0/0" || r.DestinationIPv6CIDRBlock == "::/0" {
		return &NoPublicEgressViolation{
			Err:        ErrPublicRoute,
			ResourceID: routeTableID,
			Detail:     describeRouteTarget(r),
		}
	}
	if isLocalOnlyRoute(r, vpcCIDR) {
		return nil
	}
	return &NoPublicEgressViolation{
		Err:        ErrPublicRoute,
		ResourceID: routeTableID,
		Detail:     describeRouteTarget(r),
	}
}

// isLocalOnlyRoute reports whether r is exactly the implicit local route AWS
// creates for the VPC's own CIDR block: GatewayID == "local", destination
// equal to vpcCIDR (with no IPv6 destination — Whim's MVP VPC is IPv4-only),
// and every other route-target field empty.
//
// Destination is checked, not just target: a route to "local" for a
// different CIDR than vpcCIDR (e.g. a secondary CIDR block someone
// associated with the VPC out of band) is rejected too, since Whim's own
// resource model creates exactly one CIDR block and anything else is drift.
//
// The target check is deliberately deny-by-default rather than an
// allow-list of "known public-capable" targets (internet gateway, NAT
// gateway, transit gateway, peering, egress-only/carrier gateway, ...):
// EC2's Route type can carry several more target kinds (instance, network
// interface, local gateway, core network, route-server next-hop IP, ODB
// network, prefix list), and any one of those — like the named ones — can
// carry traffic outside the VPC. Rejecting everything except the one shape
// AWS itself creates automatically means a future EC2 route-target type
// this function has never heard of is rejected too, instead of silently
// passing validation.
func isLocalOnlyRoute(r awsapi.Route, vpcCIDR string) bool {
	return r.GatewayID == "local" &&
		r.DestinationCIDRBlock == vpcCIDR &&
		r.DestinationIPv6CIDRBlock == "" &&
		r.NatGatewayID == "" &&
		r.TransitGatewayID == "" &&
		r.VPCPeeringConnectionID == "" &&
		r.EgressOnlyInternetGatewayID == "" &&
		r.CarrierGatewayID == "" &&
		r.InstanceID == "" &&
		r.NetworkInterfaceID == "" &&
		r.LocalGatewayID == "" &&
		r.CoreNetworkARN == "" &&
		r.IPAddress == "" &&
		r.ODBNetworkARN == "" &&
		r.DestinationPrefixListID == ""
}

// describeRouteTarget renders whichever route-target field(s) are set on a
// rejected route, for an actionable error message. It does not need to
// enumerate every field to remain safe: isLocalOnlyRoute already covers all
// of them for the rejection decision itself; this only affects message
// quality, falling back to a generic description for any target it doesn't
// specifically recognize.
func describeRouteTarget(r awsapi.Route) string {
	dest := r.DestinationCIDRBlock
	if dest == "" {
		dest = r.DestinationIPv6CIDRBlock
	}
	for _, target := range []struct {
		label string
		value string
	}{
		{"gateway", r.GatewayID},
		{"NAT gateway", r.NatGatewayID},
		{"transit gateway", r.TransitGatewayID},
		{"VPC peering connection", r.VPCPeeringConnectionID},
		{"egress-only internet gateway", r.EgressOnlyInternetGatewayID},
		{"carrier gateway", r.CarrierGatewayID},
		{"instance", r.InstanceID},
		{"network interface", r.NetworkInterfaceID},
		{"local gateway", r.LocalGatewayID},
		{"core network", r.CoreNetworkARN},
		{"next-hop IP", r.IPAddress},
		{"ODB network", r.ODBNetworkARN},
		{"prefix list", r.DestinationPrefixListID},
	} {
		if target.value != "" && target.value != "local" {
			return fmt.Sprintf("route to %s %s for destination %s", target.label, target.value, dest)
		}
	}
	return fmt.Sprintf("non-local route for destination %s", dest)
}

// validateNoPublicEgressSecurityGroup returns a *NoPublicEgressViolation if
// sg is not Whim-owned (ErrResourceNotOwned) or has any outbound rule
// (ErrSecurityGroupEgress), and nil otherwise. MVP requires zero egress
// rules outright — there is no partial-allow case (e.g. a narrow rule for
// port 53 is rejected exactly like 0.0.0.0/0 on all ports).
func validateNoPublicEgressSecurityGroup(sg awsapi.SecurityGroup) error {
	if err := requireNoPublicEgressOwnership(sg.ID, sg.Tags); err != nil {
		return err
	}
	return validateNoPublicEgressSecurityGroupShape(sg)
}

func validateNoPublicEgressSecurityGroupShape(sg awsapi.SecurityGroup) error {
	if len(sg.EgressRules) == 0 {
		return nil
	}
	return &NoPublicEgressViolation{
		Err:        ErrSecurityGroupEgress,
		ResourceID: sg.ID,
		Detail:     describeSecurityGroupRule(sg.EgressRules[0]),
	}
}

// describeSecurityGroupRule renders one egress rule for an actionable error
// message.
func describeSecurityGroupRule(r awsapi.SecurityGroupRule) string {
	target := "unknown target"
	switch {
	case r.CIDRIPv4 != "":
		target = "CIDR " + r.CIDRIPv4
	case r.CIDRIPv6 != "":
		target = "CIDR " + r.CIDRIPv6
	case r.PrefixListID != "":
		target = "prefix list " + r.PrefixListID
	case r.ReferencedGroupID != "":
		target = "security group " + r.ReferencedGroupID
	}
	ports := ""
	if r.FromPort != nil || r.ToPort != nil {
		ports = fmt.Sprintf(" ports %d-%d", derefInt32(r.FromPort), derefInt32(r.ToPort))
	}
	return fmt.Sprintf("egress rule protocol %s%s to %s", r.IPProtocol, ports, target)
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// validateNoPublicEgressNetworkACL returns a *NoPublicEgressViolation if acl
// is not Whim-owned (ErrResourceNotOwned) or is not associated with
// subnetID (ErrTopologyMismatch), and nil otherwise.
//
// This checks association only, not rule content: a NACL is not the primary
// no-public-egress enforcement mechanism (that's the route table and
// security group above), and per AWS's VPC documentation, network ACLs
// cannot filter traffic to or from the Amazon-provided DNS server — so this
// check never claims, and must never be read as claiming, that a NACL
// blocks DNS resolution.
func validateNoPublicEgressNetworkACL(acl awsapi.NetworkACL, subnetID string) error {
	if err := requireNoPublicEgressOwnership(acl.ID, acl.Tags); err != nil {
		return err
	}
	for _, a := range acl.Associations {
		if a.SubnetID == subnetID {
			return nil
		}
	}
	return &NoPublicEgressViolation{
		Err:        ErrTopologyMismatch,
		ResourceID: acl.ID,
		Detail:     fmt.Sprintf("not associated with expected subnet %s", subnetID),
	}
}

// NoPublicEgressConnector is the narrow connector read model
// validateNoPublicEgressConnector checks: state and the subnet/security-group
// topology the connector's VpcEgressConfiguration actually points at.
type NoPublicEgressConnector struct {
	ARN              string
	Name             string
	State            string
	SubnetIDs        []string
	SecurityGroupIDs []string
}

// validateNoPublicEgressConnector returns a *NoPublicEgressViolation if
// connector is not ACTIVE (ErrConnectorNotActive) or does not point at
// exactly expectedSubnetIDs/expectedSecurityGroupIDs (ErrTopologyMismatch),
// and nil otherwise.
//
// This does not check connector-level ownership tags: Lambda Core's
// GetNetworkConnector API does not return them (confirmed against the SDK
// response shape; there is no tag field to read). A topology mismatch — a
// connector under Whim's expected name that points at different
// subnets/security groups — doubles as the "unknown ownership" signal for
// the connector itself: Whim doesn't know who created it or why it
// collided with the expected name, so it is rejected exactly like a
// deliberate drift. Ownership of the subnets/security group themselves is
// proven separately by validateNoPublicEgressSecurityGroup and the
// route-table/subnet tag checks.
func validateNoPublicEgressConnector(connector NoPublicEgressConnector, expectedSubnetIDs, expectedSecurityGroupIDs []string) error {
	if connector.State != "ACTIVE" {
		return &NoPublicEgressViolation{
			Err:        ErrConnectorNotActive,
			ResourceID: connector.Name,
			Detail:     fmt.Sprintf("state is %s", connector.State),
		}
	}
	if !stringSetsEqual(connector.SubnetIDs, expectedSubnetIDs) || !stringSetsEqual(connector.SecurityGroupIDs, expectedSecurityGroupIDs) {
		return &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: connector.Name,
			Detail: fmt.Sprintf("connector subnets %v / security groups %v do not match expected subnets %v / security groups %v",
				connector.SubnetIDs, connector.SecurityGroupIDs, expectedSubnetIDs, expectedSecurityGroupIDs),
		}
	}
	return nil
}

// stringSetsEqual reports whether a and b contain the same strings,
// ignoring order and duplicates. AWS gives no ordering guarantee for
// SubnetIds/SecurityGroupIds.
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}

// NoPublicEgressOperatorRole is the narrow IAM role read model
// validateNoPublicEgressOperatorRole checks: the trust policy document and
// the policies attached/inline on the role. Whim never creates this role
// (see Task 3.2 — a deliberate human decision to keep IAM role creation out
// of Whim's own permissions); this only validates a role the caller already
// created and supplied via --egress-operator-role.
type NoPublicEgressOperatorRole struct {
	ARN                      string
	AssumeRolePolicyDocument string
	AttachedPolicyARNs       []string
	InlinePolicyNames        []string
}

// validateNoPublicEgressOperatorRole returns a *NoPublicEgressViolation
// wrapping ErrOperatorRoleInvalid if role's trust policy does not allow
// lambda.amazonaws.com to assume it, or if it has no attached or inline
// policies at all, and nil otherwise.
//
// This cannot fully verify the role grants Lambda Core's actual required
// permissions (ec2:CreateNetworkInterface, gated by
// AWSLambdaNetworkConnectorOperatorPolicy or an equivalent custom policy):
// inspecting an attached managed policy's document requires
// iam:GetPolicy/GetPolicyVersion, and an inline policy's requires
// iam:GetRolePolicy, neither of which Task 1.2 added (it only lists names
// and ARNs). Presence of at least one attached or inline policy is a
// deliberately conservative floor — a role with the right trust but zero
// policies is certain to fail at connector creation, so failing here first
// gives a clearer message than the resulting AWS AccessDenied would.
func validateNoPublicEgressOperatorRole(role NoPublicEgressOperatorRole) error {
	if !trustsLambdaService(role.AssumeRolePolicyDocument) {
		return &NoPublicEgressViolation{
			Err:        ErrOperatorRoleInvalid,
			ResourceID: role.ARN,
			Detail:     "trust policy does not allow lambda.amazonaws.com to assume this role",
		}
	}
	if len(role.AttachedPolicyARNs) == 0 && len(role.InlinePolicyNames) == 0 {
		return &NoPublicEgressViolation{
			Err:        ErrOperatorRoleInvalid,
			ResourceID: role.ARN,
			Detail:     "role has no attached or inline policies; needs ec2:CreateNetworkInterface permissions (e.g. AWSLambdaNetworkConnectorOperatorPolicy)",
		}
	}
	return nil
}

// iamPolicyDocument, iamStatement, and iamPrincipal model just enough of an
// IAM policy document's JSON shape to check trust: Effect, Principal.Service,
// and Action. Per AWS's IAM JSON policy grammar, Statement, Service, and
// Action may each be either a single JSON value or an array
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_statement.html);
// statementOrStatementSlice and stringOrStringSlice normalize either shape.
type iamPolicyDocument struct {
	Statement statementOrStatementSlice `json:"Statement"`
}

type iamStatement struct {
	Effect    string              `json:"Effect"`
	Principal iamPrincipal        `json:"Principal"`
	Action    stringOrStringSlice `json:"Action"`
}

type iamPrincipal struct {
	Service stringOrStringSlice `json:"Service"`
}

// statementOrStatementSlice unmarshals either a single JSON statement object
// or a JSON array of statement objects into a []iamStatement. A trust policy
// with exactly one statement is valid IAM and commonly written as a bare
// object rather than a single-element array.
type statementOrStatementSlice []iamStatement

func (s *statementOrStatementSlice) UnmarshalJSON(data []byte) error {
	var single iamStatement
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []iamStatement{single}
		return nil
	}
	var multiple []iamStatement
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*s = multiple
	return nil
}

// stringOrStringSlice unmarshals either a JSON string or a JSON array of
// strings into a []string.
type stringOrStringSlice []string

func (s *stringOrStringSlice) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*s = multiple
	return nil
}

// trustsLambdaService reports whether assumeRolePolicyDocument (plain JSON,
// already URL-decoded — see decodeAssumeRolePolicyDocument in
// internal/awsapi/sdkclient) contains an Allow statement letting
// lambda.amazonaws.com assume the role via sts:AssumeRole. Malformed or
// empty input returns false rather than erroring, since this is used purely
// as a boolean gate.
func trustsLambdaService(assumeRolePolicyDocument string) bool {
	var doc iamPolicyDocument
	if err := json.Unmarshal([]byte(assumeRolePolicyDocument), &doc); err != nil {
		return false
	}
	for _, stmt := range doc.Statement {
		if !strings.EqualFold(stmt.Effect, "Allow") {
			continue
		}
		if !containsFold(stmt.Action, "sts:AssumeRole") {
			continue
		}
		if containsFold(stmt.Principal.Service, "lambda.amazonaws.com") {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// roleNameFromARNOrName extracts the IAM role name from either a bare name
// or a full ARN (arn:aws:iam::<account>:role/<name>, or with a path,
// arn:aws:iam::<account>:role/<path>/<name> — the role name is always the
// final path segment). IAM's GetRole/ListAttachedRolePolicies/
// ListRolePolicies APIs accept only the name, never the ARN.
func roleNameFromARNOrName(s string) string {
	if !strings.HasPrefix(s, "arn:") {
		return s
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// validateNoPublicEgressOperatorRole fetches roleARNOrName (a bare role
// name or a full ARN) and validates it via the package-level function of
// the same name, before Whim relies on it to create a no-public-egress
// connector. A missing role is reported as ErrOperatorRoleInvalid, the same
// sentinel as a misconfigured one: either way, the caller's
// --egress-operator-role value needs fixing.
func (m *Manager) validateNoPublicEgressOperatorRole(ctx context.Context, roleARNOrName string) error {
	roleName := roleNameFromARNOrName(roleARNOrName)
	roleOut, err := m.api.GetRole(ctx, &awsapi.GetRoleInput{RoleName: roleName})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return fmt.Errorf("%w: operator role %q not found", ErrOperatorRoleInvalid, roleARNOrName)
		}
		return fmt.Errorf("get IAM role %q: %w", roleName, err)
	}
	attached, err := m.api.ListAttachedRolePolicies(ctx, &awsapi.ListAttachedRolePoliciesInput{RoleName: roleName})
	if err != nil {
		return fmt.Errorf("list attached policies for role %q: %w", roleName, err)
	}
	inline, err := m.api.ListRolePolicies(ctx, &awsapi.ListRolePoliciesInput{RoleName: roleName})
	if err != nil {
		return fmt.Errorf("list inline policies for role %q: %w", roleName, err)
	}
	attachedARNs := make([]string, len(attached.Items))
	for i, p := range attached.Items {
		attachedARNs[i] = p.PolicyARN
	}
	return validateNoPublicEgressOperatorRole(NoPublicEgressOperatorRole{
		ARN:                      roleOut.ARN,
		AssumeRolePolicyDocument: roleOut.AssumeRolePolicyDocument,
		AttachedPolicyARNs:       attachedARNs,
		InlinePolicyNames:        inline.PolicyNames,
	})
}

// EnsureNoPublicEgressConnector discovers, validates, and — only if nothing
// safe to reuse exists — creates a Whim-managed no-public-egress resource
// group (VPC, subnet, route table, security group, and Lambda Core VPC
// egress connector), returning its resource IDs and connector ARN.
//
// A resource group found under spec's derived connector name is reused only
// after every layer validates: connector ACTIVE and pointing at a single
// subnet/security group, that subnet and its VPC Whim-owned, its route
// table Whim-owned/associated/local-only, and its security group
// Whim-owned with zero egress rules. Any failure at any layer is returned
// immediately — Whim never falls back to creating a second resource group
// when an unsafe or drifted one already occupies the expected name, and
// never falls back to public egress.
//
// Creation requires spec.OperatorRoleARN, spec.VPCCIDRBlock, and
// spec.SubnetCIDRBlock: Whim does not create the IAM operator role itself
// (see Task 3.2), and there is no default CIDR block to fall back to. A
// partial creation failure is returned as *PartialNoPublicEgressCreationError,
// naming exactly what was created so far; Whim never rolls that back
// automatically (see Milestone 6) and never falls back to public egress.
func (m *Manager) EnsureNoPublicEgressConnector(ctx context.Context, spec NoPublicEgressSpec) (*NoPublicEgressResources, error) {
	names := spec.names()
	resources, err := m.findExistingNoPublicEgressResources(ctx, names, spec.ResourceGroup)
	if err == nil {
		return resources, nil
	}
	if !errors.Is(err, ErrConnectorMissing) {
		return nil, err
	}
	if spec.OperatorRoleARN == "" {
		return nil, fmt.Errorf("%w: creating a new no-public-egress connector requires an existing --egress-operator-role (Whim does not create IAM roles automatically)", ErrOperatorRoleInvalid)
	}
	if err := m.validateNoPublicEgressOperatorRole(ctx, spec.OperatorRoleARN); err != nil {
		return nil, err
	}
	if spec.VPCCIDRBlock == "" || spec.SubnetCIDRBlock == "" {
		return nil, fmt.Errorf("%w: creating a new no-public-egress VPC requires VPCCIDRBlock and SubnetCIDRBlock", ErrInvalidOption)
	}
	return m.createNoPublicEgressResources(ctx, spec, names)
}

// findExistingNoPublicEgressResources looks up the connector named by
// names.Connector and, if found, derives and validates the full resource
// group it points at. A missing connector returns ErrConnectorMissing (the
// trigger for EnsureNoPublicEgressConnector's creation path); any other
// error means something exists under the expected name but is unsafe or
// drifted, and must fail closed rather than trigger creation of a second
// resource group.
//
// MVP requires exactly one subnet and one security group — matching the
// one-subnet resource model (Task 0.3) — not merely at least one: a
// connector with more attached is treated as drifted from Whim's own
// managed shape, not validated permissively.
func (m *Manager) findExistingNoPublicEgressResources(ctx context.Context, names NoPublicEgressNames, expectedResourceGroup string) (*NoPublicEgressResources, error) {
	out, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: names.Connector})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrConnectorMissing, names.Connector)
		}
		return nil, fmt.Errorf("get network connector %q: %w", names.Connector, err)
	}

	connectorARN := out.ARN
	if out.State == awsapi.NetworkConnectorStatePending {
		if connectorARN, err = m.pollNetworkConnectorActive(ctx, names.Connector); err != nil {
			return nil, err
		}
	} else if out.State != awsapi.NetworkConnectorStateActive {
		return nil, &NoPublicEgressViolation{
			Err:        ErrConnectorNotActive,
			ResourceID: out.Name,
			Detail:     fmt.Sprintf("state is %s", out.State),
		}
	}

	if len(out.SubnetIDs) != 1 {
		return nil, &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: out.Name,
			Detail:     fmt.Sprintf("expected exactly one subnet for the MVP one-subnet resource model, found %d", len(out.SubnetIDs)),
		}
	}
	if len(out.SecurityGroupIDs) != 1 {
		return nil, &NoPublicEgressViolation{
			Err:        ErrTopologyMismatch,
			ResourceID: out.Name,
			Detail:     fmt.Sprintf("expected exactly one security group, found %d", len(out.SecurityGroupIDs)),
		}
	}
	subnetID, sgID := out.SubnetIDs[0], out.SecurityGroupIDs[0]

	subnetsOut, err := m.api.DescribeSubnets(ctx, &awsapi.DescribeSubnetsInput{SubnetIDs: []string{subnetID}})
	if err != nil {
		return nil, fmt.Errorf("describe subnet %q: %w", subnetID, err)
	}
	if len(subnetsOut.Items) == 0 {
		return nil, fmt.Errorf("%w: subnet %s", awsapi.ErrNotFound, subnetID)
	}
	subnet := subnetsOut.Items[0]
	resourceGroup, err := noPublicEgressResourceGroup(subnet.ID, subnet.Tags)
	if err != nil {
		return nil, err
	}
	if expectedResourceGroup != "" && resourceGroup != expectedResourceGroup {
		return nil, topologyMismatch(subnet.ID, "resource group", []string{resourceGroup}, []string{expectedResourceGroup})
	}

	vpcOut, err := m.api.GetVPC(ctx, &awsapi.GetVPCInput{VPCID: subnet.VPCID})
	if err != nil {
		return nil, fmt.Errorf("get vpc %q: %w", subnet.VPCID, err)
	}
	if err := requireResourceGroup(vpcOut.VPC.ID, vpcOut.VPC.Tags, resourceGroup); err != nil {
		return nil, err
	}

	rtOut, err := m.api.DescribeRouteTables(ctx, &awsapi.DescribeRouteTablesInput{VPCID: subnet.VPCID, SubnetID: subnet.ID})
	if err != nil {
		return nil, fmt.Errorf("describe route tables for subnet %q: %w", subnet.ID, err)
	}
	if len(rtOut.Items) == 0 {
		return nil, fmt.Errorf("%w: no route table associated with subnet %s", awsapi.ErrNotFound, subnet.ID)
	}
	rt := rtOut.Items[0]
	if err := requireResourceGroup(rt.ID, rt.Tags, resourceGroup); err != nil {
		return nil, err
	}
	if err := validateNoPublicEgressRouteTable(rt, subnet.ID, vpcOut.VPC.CIDRBlock); err != nil {
		return nil, err
	}

	sgOut, err := m.api.DescribeSecurityGroups(ctx, &awsapi.DescribeSecurityGroupsInput{GroupIDs: []string{sgID}})
	if err != nil {
		return nil, fmt.Errorf("describe security group %q: %w", sgID, err)
	}
	if len(sgOut.Items) == 0 {
		return nil, fmt.Errorf("%w: security group %s", awsapi.ErrNotFound, sgID)
	}
	if err := requireResourceGroup(sgOut.Items[0].ID, sgOut.Items[0].Tags, resourceGroup); err != nil {
		return nil, err
	}
	if err := validateNoPublicEgressSecurityGroup(sgOut.Items[0]); err != nil {
		return nil, err
	}

	return &NoPublicEgressResources{
		VPCID:            subnet.VPCID,
		SubnetIDs:        []string{subnet.ID},
		RouteTableID:     rt.ID,
		RouteTableIDs:    []string{rt.ID},
		SecurityGroupID:  sgID,
		SecurityGroupIDs: []string{sgID},
		ConnectorARN:     connectorARN,
		ResourceGroup:    resourceGroup,
	}, nil
}

// nameTag returns a copy of tags with an additional "Name" entry, without
// mutating the input map (tags is reused across several creation calls).
func nameTag(tags map[string]string, name string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	for k, v := range tags {
		out[k] = v
	}
	out["Name"] = name
	return out
}

// createNoPublicEgressResources creates a fresh no-public-egress resource
// group in dependency order: VPC, subnet, route table (+ association),
// security group (+ revoke the default egress-all rule), then the Lambda
// Core connector (via the existing EnsureNetworkConnector, which creates and
// polls to ACTIVE). It does not create a custom NACL: that's an optional
// additional layer per the spec, not required for the route-table/
// security-group enforcement MVP relies on.
//
// Every step tags its resource via NoPublicEgressTags(spec.ResourceGroup)
// plus a Name tag, so ownership and identity are provable without relying
// on names alone. If any step fails, the error is
// *PartialNoPublicEgressCreationError naming every resource created so far;
// this function never attempts to roll them back (see Milestone 6) and
// never falls back to public egress.
func (m *Manager) createNoPublicEgressResources(ctx context.Context, spec NoPublicEgressSpec, names NoPublicEgressNames) (*NoPublicEgressResources, error) {
	resourceGroup := spec.ResourceGroup
	if resourceGroup == "" {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, fmt.Errorf("generate no-public-egress resource-group identity: %w", err)
		}
		resourceGroup = fmt.Sprintf("%s-%x", names.Connector, id)
	}
	tags := NoPublicEgressTags(resourceGroup)
	res := &NoPublicEgressResources{ResourceGroup: resourceGroup}

	vpcOut, err := m.api.CreateVPC(ctx, &awsapi.CreateVPCInput{CIDRBlock: spec.VPCCIDRBlock, Tags: nameTag(tags, names.VPC)})
	if err != nil {
		return nil, &PartialNoPublicEgressCreationError{Resources: res, Step: "CreateVPC", Err: err}
	}
	res.VPCID = vpcOut.VPCID

	az := spec.AvailabilityZone
	if az == "" {
		azOut, err := m.api.DescribeAvailabilityZones(ctx)
		if err != nil {
			return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "DescribeAvailabilityZones", Err: err}
		}
		for _, z := range azOut.Items {
			if z.State == "available" {
				az = z.ZoneName
				break
			}
		}
		if az == "" {
			return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "DescribeAvailabilityZones", Err: errors.New("no available availability zone found")}
		}
	}

	subnetOut, err := m.api.CreateSubnet(ctx, &awsapi.CreateSubnetInput{
		VPCID: res.VPCID, CIDRBlock: spec.SubnetCIDRBlock, AvailabilityZone: az, Tags: nameTag(tags, names.SubnetA),
	})
	if err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "CreateSubnet", Err: err}
	}
	res.SubnetIDs = []string{subnetOut.SubnetID}

	rtOut, err := m.api.CreateRouteTable(ctx, &awsapi.CreateRouteTableInput{VPCID: res.VPCID, Tags: nameTag(tags, names.RouteTable)})
	if err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "CreateRouteTable", Err: err}
	}
	res.RouteTableID = rtOut.RouteTableID
	res.RouteTableIDs = []string{rtOut.RouteTableID}

	if _, err := m.api.AssociateRouteTable(ctx, &awsapi.AssociateRouteTableInput{RouteTableID: res.RouteTableID, SubnetID: subnetOut.SubnetID}); err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "AssociateRouteTable", Err: err}
	}

	sgOut, err := m.api.CreateSecurityGroup(ctx, &awsapi.CreateSecurityGroupInput{
		VPCID: res.VPCID, GroupName: names.SecurityGroup,
		Description: "Whim-managed no-public-egress security group (zero outbound rules)",
		Tags:        nameTag(tags, names.SecurityGroup),
	})
	if err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "CreateSecurityGroup", Err: err}
	}
	res.SecurityGroupID = sgOut.SecurityGroupID
	res.SecurityGroupIDs = []string{sgOut.SecurityGroupID}

	if err := m.api.RevokeAllSecurityGroupEgress(ctx, &awsapi.RevokeAllSecurityGroupEgressInput{SecurityGroupID: res.SecurityGroupID}); err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "RevokeAllSecurityGroupEgress", Err: err}
	}

	connectorARN, err := m.EnsureNetworkConnector(ctx, NetworkConnectorSpec{
		Name:             names.Connector,
		SubnetIDs:        res.SubnetIDs,
		SecurityGroupIDs: []string{res.SecurityGroupID},
		OperatorRoleARN:  spec.OperatorRoleARN,
		Tags:             tags,
	})
	if err != nil {
		return res, &PartialNoPublicEgressCreationError{Resources: res, Step: "EnsureNetworkConnector", Err: err}
	}
	res.ConnectorARN = connectorARN
	return res, nil
}
