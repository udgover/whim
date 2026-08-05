package awsapi

import (
	"context"
	"fmt"
	"sync"
)

// Mock is an in-memory implementation of API for unit testing.
type Mock struct {
	mu sync.Mutex

	RunMicrovmFn             func(ctx context.Context, in *RunMicrovmInput) (*RunMicrovmOutput, error)
	GetMicrovmFn             func(ctx context.Context, in *GetMicrovmInput) (*GetMicrovmOutput, error)
	TerminateMicrovmFn       func(ctx context.Context, in *TerminateMicrovmInput) error
	SuspendMicrovmFn         func(ctx context.Context, in *SuspendMicrovmInput) error
	ResumeMicrovmFn          func(ctx context.Context, in *ResumeMicrovmInput) error
	CreateShellAuthTokenFn   func(ctx context.Context, in *CreateShellAuthTokenInput) (*CreateShellAuthTokenOutput, error)
	ListMicrovmsFn           func(ctx context.Context, in *ListMicrovmsInput) (*ListMicrovmsOutput, error)
	CreateMicrovmImageFn     func(ctx context.Context, in *CreateMicrovmImageInput) (*CreateMicrovmImageOutput, error)
	GetMicrovmImageFn        func(ctx context.Context, in *GetMicrovmImageInput) (*GetMicrovmImageOutput, error)
	GetMicrovmImageVersionFn func(ctx context.Context, in *GetMicrovmImageVersionInput) (*GetMicrovmImageVersionOutput, error)
	ListNetworkConnectorsFn  func(ctx context.Context, in *ListNetworkConnectorsInput) (*ListNetworkConnectorsOutput, error)
	GetNetworkConnectorFn    func(ctx context.Context, in *GetNetworkConnectorInput) (*GetNetworkConnectorOutput, error)
	CreateNetworkConnectorFn func(ctx context.Context, in *CreateNetworkConnectorInput) (*CreateNetworkConnectorOutput, error)
	DeleteMicrovmImageFn     func(ctx context.Context, in *DeleteMicrovmImageInput) error
	ListMicrovmImagesFn      func(ctx context.Context, in *ListMicrovmImagesInput) (*ListMicrovmImagesOutput, error)

	GetVPCFn                    func(ctx context.Context, in *GetVPCInput) (*GetVPCOutput, error)
	DescribeSubnetsFn           func(ctx context.Context, in *DescribeSubnetsInput) (*DescribeSubnetsOutput, error)
	DescribeRouteTablesFn       func(ctx context.Context, in *DescribeRouteTablesInput) (*DescribeRouteTablesOutput, error)
	DescribeSecurityGroupsFn    func(ctx context.Context, in *DescribeSecurityGroupsInput) (*DescribeSecurityGroupsOutput, error)
	DescribeNetworkACLsFn       func(ctx context.Context, in *DescribeNetworkACLsInput) (*DescribeNetworkACLsOutput, error)
	DescribeAvailabilityZonesFn func(ctx context.Context) (*DescribeAvailabilityZonesOutput, error)

	CreateVPCFn                    func(ctx context.Context, in *CreateVPCInput) (*CreateVPCOutput, error)
	CreateSubnetFn                 func(ctx context.Context, in *CreateSubnetInput) (*CreateSubnetOutput, error)
	CreateRouteTableFn             func(ctx context.Context, in *CreateRouteTableInput) (*CreateRouteTableOutput, error)
	AssociateRouteTableFn          func(ctx context.Context, in *AssociateRouteTableInput) (*AssociateRouteTableOutput, error)
	CreateSecurityGroupFn          func(ctx context.Context, in *CreateSecurityGroupInput) (*CreateSecurityGroupOutput, error)
	RevokeAllSecurityGroupEgressFn func(ctx context.Context, in *RevokeAllSecurityGroupEgressInput) error
	CreateNetworkACLFn             func(ctx context.Context, in *CreateNetworkACLInput) (*CreateNetworkACLOutput, error)
	ReplaceNetworkACLAssociationFn func(ctx context.Context, in *ReplaceNetworkACLAssociationInput) (*ReplaceNetworkACLAssociationOutput, error)

	GetRoleFn                  func(ctx context.Context, in *GetRoleInput) (*GetRoleOutput, error)
	ListAttachedRolePoliciesFn func(ctx context.Context, in *ListAttachedRolePoliciesInput) (*ListAttachedRolePoliciesOutput, error)
	ListRolePoliciesFn         func(ctx context.Context, in *ListRolePoliciesInput) (*ListRolePoliciesOutput, error)

	RunMicrovmCalls             []*RunMicrovmInput
	GetMicrovmCalls             []*GetMicrovmInput
	TerminateMicrovmCalls       []*TerminateMicrovmInput
	SuspendMicrovmCalls         []*SuspendMicrovmInput
	ResumeMicrovmCalls          []*ResumeMicrovmInput
	CreateShellAuthTokenCalls   []*CreateShellAuthTokenInput
	ListMicrovmsCalls           []*ListMicrovmsInput
	CreateMicrovmImageCalls     []*CreateMicrovmImageInput
	GetMicrovmImageCalls        []*GetMicrovmImageInput
	GetMicrovmImageVersionCalls []*GetMicrovmImageVersionInput
	ListNetworkConnectorsCalls  []*ListNetworkConnectorsInput
	GetNetworkConnectorCalls    []*GetNetworkConnectorInput
	CreateNetworkConnectorCalls []*CreateNetworkConnectorInput
	DeleteMicrovmImageCalls     []*DeleteMicrovmImageInput
	ListMicrovmImagesCalls      []*ListMicrovmImagesInput

	GetVPCCalls                    []*GetVPCInput
	DescribeSubnetsCalls           []*DescribeSubnetsInput
	DescribeRouteTablesCalls       []*DescribeRouteTablesInput
	DescribeSecurityGroupsCalls    []*DescribeSecurityGroupsInput
	DescribeNetworkACLsCalls       []*DescribeNetworkACLsInput
	DescribeAvailabilityZonesCalls int

	CreateVPCCalls                    []*CreateVPCInput
	CreateSubnetCalls                 []*CreateSubnetInput
	CreateRouteTableCalls             []*CreateRouteTableInput
	AssociateRouteTableCalls          []*AssociateRouteTableInput
	CreateSecurityGroupCalls          []*CreateSecurityGroupInput
	RevokeAllSecurityGroupEgressCalls []*RevokeAllSecurityGroupEgressInput
	CreateNetworkACLCalls             []*CreateNetworkACLInput
	ReplaceNetworkACLAssociationCalls []*ReplaceNetworkACLAssociationInput

	GetRoleCalls                  []*GetRoleInput
	ListAttachedRolePoliciesCalls []*ListAttachedRolePoliciesInput
	ListRolePoliciesCalls         []*ListRolePoliciesInput
}

// Mock must always satisfy API; this guards against silent interface drift.
var _ API = (*Mock)(nil)

// RunMicrovm records a copy of the call and delegates to RunMicrovmFn if set.
func (m *Mock) RunMicrovm(ctx context.Context, in *RunMicrovmInput) (*RunMicrovmOutput, error) {
	m.mu.Lock()
	cp := *in
	m.RunMicrovmCalls = append(m.RunMicrovmCalls, &cp)
	fn := m.RunMicrovmFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: RunMicrovmFn not set")
}

// GetMicrovm records a copy of the call and delegates to GetMicrovmFn if set.
func (m *Mock) GetMicrovm(ctx context.Context, in *GetMicrovmInput) (*GetMicrovmOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetMicrovmCalls = append(m.GetMicrovmCalls, &cp)
	fn := m.GetMicrovmFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: GetMicrovmFn not set")
}

// TerminateMicrovm records a copy of the call and delegates to TerminateMicrovmFn if set.
func (m *Mock) TerminateMicrovm(ctx context.Context, in *TerminateMicrovmInput) error {
	m.mu.Lock()
	cp := *in
	m.TerminateMicrovmCalls = append(m.TerminateMicrovmCalls, &cp)
	fn := m.TerminateMicrovmFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil
}

// SuspendMicrovm records a copy of the call and delegates to SuspendMicrovmFn if set.
func (m *Mock) SuspendMicrovm(ctx context.Context, in *SuspendMicrovmInput) error {
	m.mu.Lock()
	cp := *in
	m.SuspendMicrovmCalls = append(m.SuspendMicrovmCalls, &cp)
	fn := m.SuspendMicrovmFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil
}

// ResumeMicrovm records a copy of the call and delegates to ResumeMicrovmFn if set.
func (m *Mock) ResumeMicrovm(ctx context.Context, in *ResumeMicrovmInput) error {
	m.mu.Lock()
	cp := *in
	m.ResumeMicrovmCalls = append(m.ResumeMicrovmCalls, &cp)
	fn := m.ResumeMicrovmFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil
}

// CreateShellAuthToken records a copy of the call and delegates to CreateShellAuthTokenFn if set.
func (m *Mock) CreateShellAuthToken(ctx context.Context, in *CreateShellAuthTokenInput) (*CreateShellAuthTokenOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateShellAuthTokenCalls = append(m.CreateShellAuthTokenCalls, &cp)
	fn := m.CreateShellAuthTokenFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateShellAuthTokenFn not set")
}

// ListMicrovms records a copy of the call and delegates to ListMicrovmsFn if set.
func (m *Mock) ListMicrovms(ctx context.Context, in *ListMicrovmsInput) (*ListMicrovmsOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ListMicrovmsCalls = append(m.ListMicrovmsCalls, &cp)
	fn := m.ListMicrovmsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &ListMicrovmsOutput{}, nil
}

// CreateMicrovmImage records a copy of the call and delegates to CreateMicrovmImageFn if set.
func (m *Mock) CreateMicrovmImage(ctx context.Context, in *CreateMicrovmImageInput) (*CreateMicrovmImageOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateMicrovmImageCalls = append(m.CreateMicrovmImageCalls, &cp)
	fn := m.CreateMicrovmImageFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateMicrovmImageFn not set")
}

// GetMicrovmImage records a copy of the call and delegates to GetMicrovmImageFn if set.
func (m *Mock) GetMicrovmImage(ctx context.Context, in *GetMicrovmImageInput) (*GetMicrovmImageOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetMicrovmImageCalls = append(m.GetMicrovmImageCalls, &cp)
	fn := m.GetMicrovmImageFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: GetMicrovmImageFn not set")
}

// GetMicrovmImageVersion records a copy of the call and delegates to GetMicrovmImageVersionFn if set.
func (m *Mock) GetMicrovmImageVersion(ctx context.Context, in *GetMicrovmImageVersionInput) (*GetMicrovmImageVersionOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetMicrovmImageVersionCalls = append(m.GetMicrovmImageVersionCalls, &cp)
	fn := m.GetMicrovmImageVersionFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	// Benign default (like ListMicrovms): a version with no elevated capabilities.
	return &GetMicrovmImageVersionOutput{}, nil
}

// ListNetworkConnectors records a copy of the call and delegates to ListNetworkConnectorsFn if set.
func (m *Mock) ListNetworkConnectors(ctx context.Context, in *ListNetworkConnectorsInput) (*ListNetworkConnectorsOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ListNetworkConnectorsCalls = append(m.ListNetworkConnectorsCalls, &cp)
	fn := m.ListNetworkConnectorsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &ListNetworkConnectorsOutput{}, nil
}

// GetNetworkConnector records a copy of the call and delegates to GetNetworkConnectorFn if set.
func (m *Mock) GetNetworkConnector(ctx context.Context, in *GetNetworkConnectorInput) (*GetNetworkConnectorOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetNetworkConnectorCalls = append(m.GetNetworkConnectorCalls, &cp)
	fn := m.GetNetworkConnectorFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: GetNetworkConnectorFn not set")
}

// CreateNetworkConnector records a copy of the call and delegates to CreateNetworkConnectorFn if set.
func (m *Mock) CreateNetworkConnector(ctx context.Context, in *CreateNetworkConnectorInput) (*CreateNetworkConnectorOutput, error) {
	m.mu.Lock()
	cp := *in
	cp.SubnetIDs = append([]string(nil), in.SubnetIDs...)
	cp.SecurityGroupIDs = append([]string(nil), in.SecurityGroupIDs...)
	if in.Tags != nil {
		cp.Tags = make(map[string]string, len(in.Tags))
		for k, v := range in.Tags {
			cp.Tags[k] = v
		}
	}
	m.CreateNetworkConnectorCalls = append(m.CreateNetworkConnectorCalls, &cp)
	fn := m.CreateNetworkConnectorFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateNetworkConnectorFn not set")
}

// DeleteMicrovmImage records a copy of the call and delegates to DeleteMicrovmImageFn if set.
func (m *Mock) DeleteMicrovmImage(ctx context.Context, in *DeleteMicrovmImageInput) error {
	m.mu.Lock()
	cp := *in
	m.DeleteMicrovmImageCalls = append(m.DeleteMicrovmImageCalls, &cp)
	fn := m.DeleteMicrovmImageFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil
}

// ListMicrovmImages records a copy of the call and delegates to ListMicrovmImagesFn if set.
func (m *Mock) ListMicrovmImages(ctx context.Context, in *ListMicrovmImagesInput) (*ListMicrovmImagesOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ListMicrovmImagesCalls = append(m.ListMicrovmImagesCalls, &cp)
	fn := m.ListMicrovmImagesFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &ListMicrovmImagesOutput{}, nil
}

// GetVPC records a copy of the call and delegates to GetVPCFn if set.
func (m *Mock) GetVPC(ctx context.Context, in *GetVPCInput) (*GetVPCOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetVPCCalls = append(m.GetVPCCalls, &cp)
	fn := m.GetVPCFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: GetVPCFn not set")
}

// DescribeSubnets records a copy of the call and delegates to DescribeSubnetsFn if set.
func (m *Mock) DescribeSubnets(ctx context.Context, in *DescribeSubnetsInput) (*DescribeSubnetsOutput, error) {
	m.mu.Lock()
	cp := *in
	cp.SubnetIDs = append([]string(nil), in.SubnetIDs...)
	m.DescribeSubnetsCalls = append(m.DescribeSubnetsCalls, &cp)
	fn := m.DescribeSubnetsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &DescribeSubnetsOutput{}, nil
}

// DescribeRouteTables records a copy of the call and delegates to DescribeRouteTablesFn if set.
func (m *Mock) DescribeRouteTables(ctx context.Context, in *DescribeRouteTablesInput) (*DescribeRouteTablesOutput, error) {
	m.mu.Lock()
	cp := *in
	m.DescribeRouteTablesCalls = append(m.DescribeRouteTablesCalls, &cp)
	fn := m.DescribeRouteTablesFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &DescribeRouteTablesOutput{}, nil
}

// DescribeSecurityGroups records a copy of the call and delegates to DescribeSecurityGroupsFn if set.
func (m *Mock) DescribeSecurityGroups(ctx context.Context, in *DescribeSecurityGroupsInput) (*DescribeSecurityGroupsOutput, error) {
	m.mu.Lock()
	cp := *in
	cp.GroupIDs = append([]string(nil), in.GroupIDs...)
	m.DescribeSecurityGroupsCalls = append(m.DescribeSecurityGroupsCalls, &cp)
	fn := m.DescribeSecurityGroupsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &DescribeSecurityGroupsOutput{}, nil
}

// DescribeNetworkACLs records a copy of the call and delegates to DescribeNetworkACLsFn if set.
func (m *Mock) DescribeNetworkACLs(ctx context.Context, in *DescribeNetworkACLsInput) (*DescribeNetworkACLsOutput, error) {
	m.mu.Lock()
	cp := *in
	m.DescribeNetworkACLsCalls = append(m.DescribeNetworkACLsCalls, &cp)
	fn := m.DescribeNetworkACLsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &DescribeNetworkACLsOutput{}, nil
}

// DescribeAvailabilityZones records the call and delegates to DescribeAvailabilityZonesFn if set.
func (m *Mock) DescribeAvailabilityZones(ctx context.Context) (*DescribeAvailabilityZonesOutput, error) {
	m.mu.Lock()
	m.DescribeAvailabilityZonesCalls++
	fn := m.DescribeAvailabilityZonesFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return &DescribeAvailabilityZonesOutput{}, nil
}

// CreateVPC records a copy of the call and delegates to CreateVPCFn if set.
func (m *Mock) CreateVPC(ctx context.Context, in *CreateVPCInput) (*CreateVPCOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateVPCCalls = append(m.CreateVPCCalls, &cp)
	fn := m.CreateVPCFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateVPCFn not set")
}

// CreateSubnet records a copy of the call and delegates to CreateSubnetFn if set.
func (m *Mock) CreateSubnet(ctx context.Context, in *CreateSubnetInput) (*CreateSubnetOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateSubnetCalls = append(m.CreateSubnetCalls, &cp)
	fn := m.CreateSubnetFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateSubnetFn not set")
}

// CreateRouteTable records a copy of the call and delegates to CreateRouteTableFn if set.
func (m *Mock) CreateRouteTable(ctx context.Context, in *CreateRouteTableInput) (*CreateRouteTableOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateRouteTableCalls = append(m.CreateRouteTableCalls, &cp)
	fn := m.CreateRouteTableFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateRouteTableFn not set")
}

// AssociateRouteTable records a copy of the call and delegates to AssociateRouteTableFn if set.
func (m *Mock) AssociateRouteTable(ctx context.Context, in *AssociateRouteTableInput) (*AssociateRouteTableOutput, error) {
	m.mu.Lock()
	cp := *in
	m.AssociateRouteTableCalls = append(m.AssociateRouteTableCalls, &cp)
	fn := m.AssociateRouteTableFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: AssociateRouteTableFn not set")
}

// CreateSecurityGroup records a copy of the call and delegates to CreateSecurityGroupFn if set.
func (m *Mock) CreateSecurityGroup(ctx context.Context, in *CreateSecurityGroupInput) (*CreateSecurityGroupOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateSecurityGroupCalls = append(m.CreateSecurityGroupCalls, &cp)
	fn := m.CreateSecurityGroupFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateSecurityGroupFn not set")
}

// RevokeAllSecurityGroupEgress records a copy of the call and delegates to RevokeAllSecurityGroupEgressFn if set.
func (m *Mock) RevokeAllSecurityGroupEgress(ctx context.Context, in *RevokeAllSecurityGroupEgressInput) error {
	m.mu.Lock()
	cp := *in
	m.RevokeAllSecurityGroupEgressCalls = append(m.RevokeAllSecurityGroupEgressCalls, &cp)
	fn := m.RevokeAllSecurityGroupEgressFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil
}

// CreateNetworkACL records a copy of the call and delegates to CreateNetworkACLFn if set.
func (m *Mock) CreateNetworkACL(ctx context.Context, in *CreateNetworkACLInput) (*CreateNetworkACLOutput, error) {
	m.mu.Lock()
	cp := *in
	m.CreateNetworkACLCalls = append(m.CreateNetworkACLCalls, &cp)
	fn := m.CreateNetworkACLFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: CreateNetworkACLFn not set")
}

// ReplaceNetworkACLAssociation records a copy of the call and delegates to ReplaceNetworkACLAssociationFn if set.
func (m *Mock) ReplaceNetworkACLAssociation(ctx context.Context, in *ReplaceNetworkACLAssociationInput) (*ReplaceNetworkACLAssociationOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ReplaceNetworkACLAssociationCalls = append(m.ReplaceNetworkACLAssociationCalls, &cp)
	fn := m.ReplaceNetworkACLAssociationFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: ReplaceNetworkACLAssociationFn not set")
}

// GetRole records a copy of the call and delegates to GetRoleFn if set.
func (m *Mock) GetRole(ctx context.Context, in *GetRoleInput) (*GetRoleOutput, error) {
	m.mu.Lock()
	cp := *in
	m.GetRoleCalls = append(m.GetRoleCalls, &cp)
	fn := m.GetRoleFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return nil, fmt.Errorf("mock: GetRoleFn not set")
}

// ListAttachedRolePolicies records a copy of the call and delegates to ListAttachedRolePoliciesFn if set.
func (m *Mock) ListAttachedRolePolicies(ctx context.Context, in *ListAttachedRolePoliciesInput) (*ListAttachedRolePoliciesOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ListAttachedRolePoliciesCalls = append(m.ListAttachedRolePoliciesCalls, &cp)
	fn := m.ListAttachedRolePoliciesFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &ListAttachedRolePoliciesOutput{}, nil
}

// ListRolePolicies records a copy of the call and delegates to ListRolePoliciesFn if set.
func (m *Mock) ListRolePolicies(ctx context.Context, in *ListRolePoliciesInput) (*ListRolePoliciesOutput, error) {
	m.mu.Lock()
	cp := *in
	m.ListRolePoliciesCalls = append(m.ListRolePoliciesCalls, &cp)
	fn := m.ListRolePoliciesFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return &ListRolePoliciesOutput{}, nil
}
