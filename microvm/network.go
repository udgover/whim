package microvm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/udgover/whim/internal/awsapi"
)

func isInternetEgressConnector(identifier string) bool {
	identifier = strings.TrimSpace(identifier)
	return identifier == "INTERNET_EGRESS" || strings.HasSuffix(identifier, ":INTERNET_EGRESS")
}

func sameStringSet(a, b []string) bool {
	set := func(items []string) map[string]struct{} {
		s := make(map[string]struct{}, len(items))
		for _, item := range items {
			s[item] = struct{}{}
		}
		return s
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for item := range sa {
		if _, ok := sb[item]; !ok {
			return false
		}
	}
	return true
}

// NetworkConnector describes a Lambda Core network connector that can be used
// as a MicroVM egress connector.
type NetworkConnector struct {
	ARN   string
	Name  string
	State string
	Type  string
}

// NetworkConnectorSpec is the caller-supplied VPC egress connector
// configuration. The referenced VPC routing and security controls determine
// whether the connector allows or denies public internet access.
type NetworkConnectorSpec struct {
	Name             string
	SubnetIDs        []string
	SecurityGroupIDs []string
	OperatorRoleARN  string
	Tags             map[string]string
}

// ListNetworkConnectors returns Lambda Core network connectors in the account
// and region. Pass an empty state to list all states.
func (m *Manager) ListNetworkConnectors(ctx context.Context, state string) ([]NetworkConnector, error) {
	out, err := m.api.ListNetworkConnectors(ctx, &awsapi.ListNetworkConnectorsInput{State: state})
	if err != nil {
		return nil, fmt.Errorf("list network connectors: %w", err)
	}
	items := make([]NetworkConnector, 0, len(out.Items))
	for _, item := range out.Items {
		items = append(items, NetworkConnector{
			ARN:   item.ARN,
			Name:  item.Name,
			State: item.State,
			Type:  item.Type,
		})
	}
	return items, nil
}

// GetNetworkConnector retrieves a connector by name, ID, or ARN.
func (m *Manager) GetNetworkConnector(ctx context.Context, identifier string) (*NetworkConnector, error) {
	out, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: identifier})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return nil, fmt.Errorf("get network connector %q: %w", identifier, awsapi.ErrNotFound)
		}
		return nil, fmt.Errorf("get network connector %q: %w", identifier, err)
	}
	return &NetworkConnector{
		ARN:   out.ARN,
		Name:  out.Name,
		State: out.State,
		Type:  "VPC_EGRESS",
	}, nil
}

// EnsureNetworkConnector returns an ACTIVE connector matching spec.Name,
// creating it only when it does not already exist and spec carries the VPC
// inputs required by Lambda Core.
func (m *Manager) EnsureNetworkConnector(ctx context.Context, spec NetworkConnectorSpec) (string, error) {
	if spec.Name == "" {
		return "", fmt.Errorf("%w: network connector name is required", ErrInvalidOption)
	}
	existing, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: spec.Name})
	if err == nil {
		if len(spec.SubnetIDs) > 0 && !sameStringSet(existing.SubnetIDs, spec.SubnetIDs) {
			return "", topologyMismatch(existing.Name, "subnets", existing.SubnetIDs, spec.SubnetIDs)
		}
		if len(spec.SecurityGroupIDs) > 0 && !sameStringSet(existing.SecurityGroupIDs, spec.SecurityGroupIDs) {
			return "", topologyMismatch(existing.Name, "security groups", existing.SecurityGroupIDs, spec.SecurityGroupIDs)
		}
		return m.resolveExistingNetworkConnector(ctx, existing)
	}
	if err != nil && !errors.Is(err, awsapi.ErrNotFound) {
		return "", fmt.Errorf("get network connector %q: %w", spec.Name, err)
	}

	if len(spec.SubnetIDs) == 0 || len(spec.SecurityGroupIDs) == 0 {
		return "", fmt.Errorf("%w: network connector %q does not exist; provide subnet IDs and security group IDs to create it",
			ErrInvalidOption, spec.Name)
	}
	created, err := m.api.CreateNetworkConnector(ctx, &awsapi.CreateNetworkConnectorInput{
		Name:             spec.Name,
		SubnetIDs:        append([]string(nil), spec.SubnetIDs...),
		SecurityGroupIDs: append([]string(nil), spec.SecurityGroupIDs...),
		OperatorRoleARN:  spec.OperatorRoleARN,
		Tags:             spec.Tags,
	})
	if err != nil {
		return "", fmt.Errorf("create network connector %q: %w", spec.Name, err)
	}
	return m.resolveExistingNetworkConnector(ctx, &awsapi.GetNetworkConnectorOutput{
		ARN:   created.ARN,
		Name:  created.Name,
		State: created.State,
	})
}

func (m *Manager) resolveExistingNetworkConnector(ctx context.Context, connector *awsapi.GetNetworkConnectorOutput) (string, error) {
	switch connector.State {
	case awsapi.NetworkConnectorStateActive:
		return connector.ARN, nil
	case awsapi.NetworkConnectorStatePending:
		return m.pollNetworkConnectorActive(ctx, connector.Name)
	case awsapi.NetworkConnectorStateFailed, awsapi.NetworkConnectorStateDeleteFailed:
		detail := connector.StateReason
		if connector.StateReasonCode != "" {
			detail = connector.StateReasonCode + ": " + detail
		}
		if detail == "" {
			detail = connector.State
		}
		return "", fmt.Errorf("%w: network connector %q is %s (%s)", ErrInvalidOption, connector.Name, connector.State, detail)
	case awsapi.NetworkConnectorStateInactive, awsapi.NetworkConnectorStateDeleting:
		return "", fmt.Errorf("%w: network connector %q is %s, not ACTIVE", ErrInvalidOption, connector.Name, connector.State)
	default:
		return "", fmt.Errorf("%w: network connector %q is in unexpected state %q", ErrInvalidOption, connector.Name, connector.State)
	}
}

// findNoPublicEgressConnector looks up the connector named by spec's derived
// connector name and validates it is ACTIVE and points at exactly
// expectedSubnetIDs/expectedSecurityGroupID (see
// validateNoPublicEgressConnector). A connector that does not exist yet is
// reported as ErrConnectorMissing — the trigger for a caller's creation
// path (Milestone 3), not a validation failure in itself. This is the
// read-only discovery half of what will become EnsureNoPublicEgressConnector;
// it never creates, updates, or polls a pending connector to completion.
func (m *Manager) findNoPublicEgressConnector(ctx context.Context, spec NoPublicEgressSpec, expectedSubnetIDs []string, expectedSecurityGroupID string) (*NoPublicEgressResources, error) {
	connectorName := spec.names().Connector
	out, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: connectorName})
	if err != nil {
		if errors.Is(err, awsapi.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrConnectorMissing, connectorName)
		}
		return nil, fmt.Errorf("get network connector %q: %w", connectorName, err)
	}
	connector := NoPublicEgressConnector{
		ARN:              out.ARN,
		Name:             out.Name,
		State:            out.State,
		SubnetIDs:        out.SubnetIDs,
		SecurityGroupIDs: out.SecurityGroupIDs,
	}
	if err := validateNoPublicEgressConnector(connector, expectedSubnetIDs, []string{expectedSecurityGroupID}); err != nil {
		return nil, err
	}
	return &NoPublicEgressResources{
		SubnetIDs:       connector.SubnetIDs,
		SecurityGroupID: expectedSecurityGroupID,
		ConnectorARN:    connector.ARN,
		ResourceGroup:   spec.ResourceGroup,
	}, nil
}

func (m *Manager) pollNetworkConnectorActive(ctx context.Context, name string) (string, error) {
	var arn string
	err := m.poll(ctx, func(ctx context.Context) (bool, error) {
		out, err := m.api.GetNetworkConnector(ctx, &awsapi.GetNetworkConnectorInput{Identifier: name})
		if err != nil {
			return false, fmt.Errorf("poll network connector %q: %w", name, err)
		}
		switch out.State {
		case awsapi.NetworkConnectorStateActive:
			arn = out.ARN
			return true, nil
		case awsapi.NetworkConnectorStatePending:
			m.log().Debug("network connector pending", "name", name)
			return false, nil
		case awsapi.NetworkConnectorStateFailed, awsapi.NetworkConnectorStateDeleteFailed:
			return false, fmt.Errorf("%w: network connector %q is %s", ErrInvalidOption, name, out.State)
		default:
			return false, fmt.Errorf("%w: network connector %q is %s, not ACTIVE", ErrInvalidOption, name, out.State)
		}
	})
	if err != nil {
		return "", err
	}
	return arn, nil
}
