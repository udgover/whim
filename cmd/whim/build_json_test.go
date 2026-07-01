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
		"github.com/org/repo@abc", "public", true))

	got := decodeBuildJSON(t, buf.Bytes())
	assert.Equal(t, "whim-app", got.Name)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:microvm-image:whim-app", got.ARN)
	assert.Equal(t, "github.com/org/repo@abc", got.Source)
	assert.True(t, got.Cached)
	assert.Equal(t, "public", got.Egress)
}

func TestBuildJSON_RedactsSourceQueryToken(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderBuildJSON(&buf, "n", "arn:x",
		"https://host/app.zip?token=SUPERSECRET&ref=main", "none", false))
	assert.NotContains(t, buf.String(), "SUPERSECRET", "source query token must be redacted")
	assert.Contains(t, buf.String(), "ref=main")
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
		"s3://whim-artifacts-123456789012-us-east-1/app.zip", "public", false))
	assert.NotContains(t, buf.String(), "123456789012", "account IDs must be redacted when enabled")
}
