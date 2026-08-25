package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeBuildJSON(t *testing.T, b []byte) buildJSON {
	t.Helper()
	var out buildJSON
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func TestBuildJSON_Shape(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderBuildJSON(&buf,
		"whim-app",
		"arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-app",
		"github.com/org/repo@abc", "public", "", true, nil))

	got := decodeBuildJSON(t, buf.Bytes())
	assert.Equal(t, "whim-app", got.Name)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-app", got.ARN)
	assert.Equal(t, "github.com/org/repo@abc", got.Source)
	assert.True(t, got.Cached)
	assert.Equal(t, "public", got.Egress)
	assert.Nil(t, got.EgressResourceGroup)
}

func TestBuildJSON_RedactsSourceQueryToken(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderBuildJSON(&buf, "n", "arn:x",
		"https://host/app.zip?token=SUPERSECRET&ref=main", "none", "arn:connector", false, nil))
	assert.NotContains(t, buf.String(), "SUPERSECRET", "source query token must be redacted")
	assert.Contains(t, buf.String(), "ref=main")
}

// TestBuildJSON_IncludesResourceGroup checks Task 4.3's acceptance criterion
// that --json exposes managed resource group metadata when auto-provisioning
// produced one.
func TestBuildJSON_IncludesResourceGroup(t *testing.T) {
	var buf bytes.Buffer
	rg := &EgressResourceGroup{
		VPCID: "vpc-1", SubnetIDs: []string{"subnet-1"}, RouteTableID: "rtb-1",
		SecurityGroupID: "sg-1", ResourceGroup: "whim-no-public-egress",
	}
	require.NoError(t, renderBuildJSON(&buf, "whim-app",
		"arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-app",
		"./app", "none", "arn:aws:lambda:us-east-1:123456789012:network-connector:whim-no-public-egress", false, rg))

	got := decodeBuildJSON(t, buf.Bytes())
	require.NotNil(t, got.EgressResourceGroup)
	assert.Equal(t, "vpc-1", got.EgressResourceGroup.VPCID)
	assert.Equal(t, []string{"subnet-1"}, got.EgressResourceGroup.SubnetIDs)
	assert.Equal(t, "rtb-1", got.EgressResourceGroup.RouteTableID)
	assert.Equal(t, "sg-1", got.EgressResourceGroup.SecurityGroupID)
	assert.Equal(t, "whim-no-public-egress", got.EgressResourceGroup.ResourceGroup)
}

func TestBuildRedact_SourceUserinfo(t *testing.T) {
	got := redactSourceCreds("https://user:p4ss@host/app.zip")
	assert.NotContains(t, got, "p4ss")
	assert.NotContains(t, got, "user:")
}

func TestBuildRedact_LocalAndS3Unchanged(t *testing.T) {
	assert.Equal(t, "./build/dir", redactSourceCreds("./build/dir"))
	assert.Equal(t, "s3://bucket/app.zip", redactSourceCreds("s3://bucket/app.zip"))
}

func TestBuildRedact_AccountIDInJSON(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")
	var buf bytes.Buffer
	require.NoError(t, renderBuildJSON(&buf, "n",
		"arn:aws:lambda:us-east-1:123456789012:microvm-image:n",
		"s3://whim-artifacts-123456789012-us-east-1/app.zip", "public", "", false, nil))
	assert.NotContains(t, buf.String(), "123456789012", "account IDs must be redacted when enabled")
}
