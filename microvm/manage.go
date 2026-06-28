package microvm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/udgover/whim/internal/awsapi"
)

// SandboxInfo describes a whim-owned MicroVM for List/GC.
type SandboxInfo struct {
	ID        string
	ImageARN  string
	State     string
	StartedAt time.Time
}

// GCFilter narrows which whim-owned VMs GC terminates.
type GCFilter struct {
	// OlderThan, if > 0, reaps only VMs whose age (now - StartedAt) is at least
	// this. VMs with an unknown StartedAt are skipped when OlderThan is set.
	OlderThan time.Duration
}

// List returns the whim-owned, non-terminated MicroVMs in the account. Ownership
// is derived from the image ARN: a VM is whim-owned iff it was launched from an
// account-owned microvm-image (microvms cannot be tagged). AWS-managed base
// images (ARN account "aws") and other accounts' images are never included.
func (m *Manager) List(ctx context.Context) ([]SandboxInfo, error) {
	if m.accountID == "" {
		return nil, fmt.Errorf("%w: account ID required to determine VM ownership", ErrInvalidOption)
	}
	out, err := m.api.ListMicrovms(ctx, &awsapi.ListMicrovmsInput{})
	if err != nil {
		return nil, fmt.Errorf("list microvms: %w", err)
	}
	var infos []SandboxInfo
	for _, vm := range out.Items {
		if !m.ownsImage(vm.ImageARN) || vm.State == "TERMINATED" || vm.State == "TERMINATING" {
			continue
		}
		infos = append(infos, SandboxInfo{
			ID:        vm.MicrovmID,
			ImageARN:  vm.ImageARN,
			State:     vm.State,
			StartedAt: vm.StartedAt,
		})
	}
	return infos, nil
}

// GC terminates whim-owned VMs matching f and returns the IDs it reaped. It only
// ever touches VMs List would return (account-owned images) — never AWS-managed
// or foreign VMs. Confirmation/age policy beyond the filter is the caller's job.
func (m *Manager) GC(ctx context.Context, f GCFilter) ([]string, error) {
	infos, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var reaped []string
	var errs []error
	for _, vm := range infos {
		if f.OlderThan > 0 && (vm.StartedAt.IsZero() || now.Sub(vm.StartedAt) < f.OlderThan) {
			continue // too new, or age unknown — don't reap under an age filter
		}
		err := m.api.TerminateMicrovm(ctx, &awsapi.TerminateMicrovmInput{MicrovmIdentifier: vm.ID})
		if err != nil && !errors.Is(err, awsapi.ErrNotFound) {
			errs = append(errs, fmt.Errorf("terminate %q: %w", vm.ID, err))
			continue // best-effort: one failure must not block reaping the rest
		}
		reaped = append(reaped, vm.ID)
	}
	return reaped, errors.Join(errs...)
}

// ownsImage reports whether imageARN is an account-owned microvm-image (vs an
// AWS-managed base image like arn:aws:lambda:<region>:aws:microvm-image:al2023-1,
// a foreign account, or some other resource type). This is the safety boundary
// for List/GC, so it checks both the account (field 4) AND the resource type
// (field 5) and requires a known account ID.
// ARN form: arn:partition:service:region:account:resource-type:resource-id.
func (m *Manager) ownsImage(imageARN string) bool {
	parts := strings.Split(imageARN, ":")
	return len(parts) >= 6 && m.accountID != "" && parts[4] == m.accountID && parts[5] == "microvm-image"
}
