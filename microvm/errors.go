package microvm

import "errors"

// Typed sentinel errors. Callers branch on these with errors.Is; never match
// on string content. Add detail by wrapping: fmt.Errorf("detail: %w", sentinel).
var (
	// ErrInvalidOption is returned when a functional option receives an invalid value.
	ErrInvalidOption = errors.New("microvm: invalid option")
	// ErrInvalidSource is returned when a build source cannot be classified as a
	// supported transport (local path, s3://, https://) or carries credentials.
	ErrInvalidSource = errors.New("microvm: invalid source")
	// ErrSourceTooLarge is returned when a staged build context exceeds a
	// configured size or file-count cap.
	ErrSourceTooLarge = errors.New("microvm: source exceeds size cap")
	// ErrTokenExpired is reserved but not currently returned by any Manager or
	// Sandbox method: live testing confirmed an established shell connection is
	// never re-validated against its token (see ShellTokenLifetime), so there is
	// no expiry condition to detect on that path. Kept for API compatibility.
	ErrTokenExpired = errors.New("microvm: shell token expired")
	// ErrVMProvisionFailed is returned when a MicroVM fails to reach RUNNING.
	ErrVMProvisionFailed = errors.New("microvm: vm failed to reach running")
	// ErrImageBuildFailed is returned when a MicroVM image build fails or times out.
	ErrImageBuildFailed = errors.New("microvm: image build failed")
	// ErrImageNotFound is returned when a requested MicroVM image does not exist.
	ErrImageNotFound = errors.New("microvm: image not found")
	// ErrCapabilityMismatch is returned when an existing image is reused but its
	// OS capabilities differ from those requested, so reuse would silently grant
	// or drop privilege. Rebuild (Force) to resolve.
	ErrCapabilityMismatch = errors.New("microvm: image capabilities differ from request")
	// ErrEgressMismatch is returned when an existing image is reused but its
	// egress connectors differ from those requested, so reuse would silently
	// change network isolation. Rebuild (Force) to resolve.
	ErrEgressMismatch = errors.New("microvm: image egress differs from request")
	// ErrConnClosed is returned when the WebSocket connection is closed unexpectedly.
	ErrConnClosed = errors.New("microvm: connection closed")
	// ErrTimeout is returned when an operation exceeds its deadline.
	ErrTimeout = errors.New("microvm: operation timed out")
	// ErrTerminated is returned when an operation is attempted on a terminated sandbox.
	ErrTerminated = errors.New("microvm: sandbox terminated")
	// ErrPublicRoute is returned when a managed route table contains a public
	// or default egress route: 0.0.0.0/0, ::/0, or any route whose target can
	// reach outside the VPC (internet gateway, NAT gateway, transit gateway,
	// peering, egress-only/carrier gateway, or any other non-local target).
	ErrPublicRoute = errors.New("microvm: route table has a public or default egress route")
	// ErrSecurityGroupEgress is returned when a managed security group has
	// any outbound rule; the no-public-egress MVP requires zero.
	ErrSecurityGroupEgress = errors.New("microvm: security group has an outbound rule")
	// ErrResourceNotOwned is returned when a resource Whim expects to manage
	// is missing the ManagedBy=whim/Purpose=no-public-egress tags, so Whim
	// will not treat it as safely reusable.
	ErrResourceNotOwned = errors.New("microvm: resource is not tagged as Whim-managed")
	// ErrConnectorMissing is returned when the expected no-public-egress
	// network connector does not exist yet.
	ErrConnectorMissing = errors.New("microvm: no-public-egress connector not found")
	// ErrTopologyMismatch is returned when a Whim-owned resource exists but
	// is associated with, or points at, different resources than Whim
	// expects — e.g. a Whim-managed NACL not associated with the managed
	// subnet, or a connector whose subnets/security groups don't match the
	// managed resource group.
	ErrTopologyMismatch = errors.New("microvm: resource topology does not match expected no-public-egress shape")
	// ErrConnectorNotActive is returned when the expected no-public-egress
	// network connector exists but is not ACTIVE (PENDING, INACTIVE, FAILED,
	// DELETING, or DELETE_FAILED).
	ErrConnectorNotActive = errors.New("microvm: no-public-egress connector exists but is not ACTIVE")
	// ErrOperatorRoleInvalid is returned when a user-supplied
	// --egress-operator-role does not exist, does not trust
	// lambda.amazonaws.com to assume it, or has no attached or inline
	// policies at all. Whim never creates this role automatically; the
	// caller must create it once and pass its ARN.
	ErrOperatorRoleInvalid = errors.New("microvm: operator role is not usable by Lambda Core network connectors")
)
