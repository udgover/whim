package microvm_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
	"github.com/udgover/whim/microvm"
)

// mixedFleet returns a ListMicrovms mock with whim-owned, AWS-managed, foreign,
// and terminated VMs — so ownership filtering can be verified.
func mixedFleet() *awsapi.Mock {
	now := time.Now()
	m := &awsapi.Mock{}
	m.ListMicrovmsFn = func(_ context.Context, _ *awsapi.ListMicrovmsInput) (*awsapi.ListMicrovmsOutput, error) {
		return &awsapi.ListMicrovmsOutput{Items: []awsapi.MicrovmSummary{
			{MicrovmID: "vm-own-old", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default", State: "RUNNING", StartedAt: now.Add(-2 * time.Hour)},
			{MicrovmID: "vm-own-new", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-test", State: "PENDING", StartedAt: now.Add(-5 * time.Minute)},
			{MicrovmID: "vm-managed", ImageARN: "arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1", State: "RUNNING", StartedAt: now.Add(-3 * time.Hour)},
			{MicrovmID: "vm-foreign", ImageARN: "arn:aws:lambda:us-east-1:999999999999:microvm-image:other", State: "RUNNING", StartedAt: now.Add(-3 * time.Hour)},
			{MicrovmID: "vm-own-term", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default", State: "TERMINATED", StartedAt: now.Add(-4 * time.Hour)},
			// hostile/edge: same account but NOT a microvm-image; malformed ARN; already terminating
			{MicrovmID: "vm-func", ImageARN: "arn:aws:lambda:us-east-1:123456789012:function:foo", State: "RUNNING", StartedAt: now.Add(-3 * time.Hour)},
			{MicrovmID: "vm-bad", ImageARN: "not-an-arn", State: "RUNNING", StartedAt: now.Add(-3 * time.Hour)},
			{MicrovmID: "vm-own-terming", ImageARN: "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-default", State: "TERMINATING", StartedAt: now.Add(-3 * time.Hour)},
		}}, nil
	}
	return m
}

func ids(infos []microvm.SandboxInfo) []string {
	out := make([]string, len(infos))
	for i, in := range infos {
		out[i] = in.ID
	}
	sort.Strings(out)
	return out
}

func TestList_OnlyAccountOwnedActiveVMs(t *testing.T) {
	infos, err := newTestManager(mixedFleet()).List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-own-new", "vm-own-old"}, ids(infos),
		"only account-owned microvm-image, non-terminated/terminating VMs — "+
			"never AWS-managed, foreign, non-microvm-image (vm-func), malformed (vm-bad), or terminating (vm-own-terming)")
}

func TestList_RequiresAccountID(t *testing.T) {
	mgr := microvm.NewWithAPI(mixedFleet(), microvm.WithRegion("us-east-1")) // no account ID
	_, err := mgr.List(context.Background())
	require.ErrorIs(t, err, microvm.ErrInvalidOption, "ownership can't be determined without an account ID")
}

func TestGC_ReapsOnlyOwned(t *testing.T) {
	mock := mixedFleet()
	reaped, err := newTestManager(mock).GC(context.Background(), microvm.GCFilter{})
	require.NoError(t, err)
	sort.Strings(reaped)
	assert.Equal(t, []string{"vm-own-new", "vm-own-old"}, reaped)

	var terminated []string
	for _, c := range mock.TerminateMicrovmCalls {
		terminated = append(terminated, c.MicrovmIdentifier)
	}
	sort.Strings(terminated)
	assert.Equal(t, []string{"vm-own-new", "vm-own-old"}, terminated,
		"GC must NEVER terminate AWS-managed (vm-managed) or foreign (vm-foreign) VMs")
}

func TestGC_BestEffortOnError(t *testing.T) {
	mock := mixedFleet()
	mock.TerminateMicrovmFn = func(_ context.Context, in *awsapi.TerminateMicrovmInput) error {
		if in.MicrovmIdentifier == "vm-own-old" {
			return fmt.Errorf("throttled")
		}
		return nil
	}
	reaped, err := newTestManager(mock).GC(context.Background(), microvm.GCFilter{})
	require.Error(t, err, "a terminate failure must be reported")
	assert.Contains(t, err.Error(), "vm-own-old")
	assert.Contains(t, reaped, "vm-own-new", "other VMs are still reaped despite one failure")
	assert.NotContains(t, reaped, "vm-own-old")
}

func TestGC_OlderThanFiltersByAge(t *testing.T) {
	mock := mixedFleet()
	reaped, err := newTestManager(mock).GC(context.Background(), microvm.GCFilter{OlderThan: 30 * time.Minute})
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-own-old"}, reaped, "only VMs older than 30m (not the 5m-old one)")
}
