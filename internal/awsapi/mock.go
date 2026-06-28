package awsapi

import (
	"context"
	"fmt"
	"sync"
)

// Mock is an in-memory implementation of API for unit testing.
type Mock struct {
	mu sync.Mutex

	RunMicrovmFn           func(ctx context.Context, in *RunMicrovmInput) (*RunMicrovmOutput, error)
	GetMicrovmFn           func(ctx context.Context, in *GetMicrovmInput) (*GetMicrovmOutput, error)
	TerminateMicrovmFn     func(ctx context.Context, in *TerminateMicrovmInput) error
	SuspendMicrovmFn       func(ctx context.Context, in *SuspendMicrovmInput) error
	ResumeMicrovmFn        func(ctx context.Context, in *ResumeMicrovmInput) error
	CreateShellAuthTokenFn func(ctx context.Context, in *CreateShellAuthTokenInput) (*CreateShellAuthTokenOutput, error)
	ListMicrovmsFn         func(ctx context.Context, in *ListMicrovmsInput) (*ListMicrovmsOutput, error)
	CreateMicrovmImageFn   func(ctx context.Context, in *CreateMicrovmImageInput) (*CreateMicrovmImageOutput, error)
	GetMicrovmImageFn      func(ctx context.Context, in *GetMicrovmImageInput) (*GetMicrovmImageOutput, error)
	DeleteMicrovmImageFn   func(ctx context.Context, in *DeleteMicrovmImageInput) error
	ListMicrovmImagesFn    func(ctx context.Context, in *ListMicrovmImagesInput) (*ListMicrovmImagesOutput, error)

	RunMicrovmCalls           []*RunMicrovmInput
	GetMicrovmCalls           []*GetMicrovmInput
	TerminateMicrovmCalls     []*TerminateMicrovmInput
	SuspendMicrovmCalls       []*SuspendMicrovmInput
	ResumeMicrovmCalls        []*ResumeMicrovmInput
	CreateShellAuthTokenCalls []*CreateShellAuthTokenInput
	ListMicrovmsCalls         []*ListMicrovmsInput
	CreateMicrovmImageCalls   []*CreateMicrovmImageInput
	GetMicrovmImageCalls      []*GetMicrovmImageInput
	DeleteMicrovmImageCalls   []*DeleteMicrovmImageInput
	ListMicrovmImagesCalls    []*ListMicrovmImagesInput
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
