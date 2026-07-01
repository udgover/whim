package sdkclient

import (
	"testing"

	sdktypes "github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/stretchr/testify/assert"
)

// TestSdkCapabilities checks the string→SDK Capability lift used when submitting
// a build. An empty input must produce nil so the request omits the field.
func TestSdkCapabilities(t *testing.T) {
	assert.Nil(t, sdkCapabilities(nil), "nil in → nil out (no field sent)")
	assert.Nil(t, sdkCapabilities([]string{}), "empty in → nil out (no field sent)")

	got := sdkCapabilities([]string{"ALL"})
	assert.Equal(t, []sdktypes.Capability{sdktypes.CapabilityAll}, got)
}
