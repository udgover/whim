package microvm_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/udgover/whim/microvm"
)

func allSentinels() []error {
	return []error{
		microvm.ErrInvalidOption,
		microvm.ErrTokenExpired,
		microvm.ErrVMProvisionFailed,
		microvm.ErrImageBuildFailed,
		microvm.ErrConnClosed,
		microvm.ErrTimeout,
		microvm.ErrTerminated,
	}
}

func TestErrorSentinelsExist(t *testing.T) {
	for _, err := range allSentinels() {
		if err == nil {
			t.Fatalf("sentinel error must not be nil")
		}
	}
}

func TestErrorSentinelsAreDistinct(t *testing.T) {
	sentinels := allSentinels()
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinels[%d] and sentinels[%d] must be distinct", i, j)
			}
		}
	}
}

// Sentinels are meant to be wrapped with the stdlib %w idiom; verify errors.Is
// matches through a wrap and does not match a different sentinel.
func TestSentinelWrappingViaFmtErrorf(t *testing.T) {
	wrapped := fmt.Errorf("token expired after 30m: %w", microvm.ErrTokenExpired)
	if !errors.Is(wrapped, microvm.ErrTokenExpired) {
		t.Error("wrapped error must match its sentinel via errors.Is")
	}
	if errors.Is(wrapped, microvm.ErrTimeout) {
		t.Error("wrapped error must not match a different sentinel")
	}
}
