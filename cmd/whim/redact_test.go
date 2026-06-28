package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/microvm"
)

const testARN = "arn:aws:lambda:us-east-1:445478487199:microvm-image:whim-default"

// --- chokepoint wiring tests (redaction is actually applied, not just the pure fn) ---

func TestPrintOut_RedactsWhenEnabled(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	printOut(cmd, "Image ready: %s\n", testARN)
	assert.Contains(t, buf.String(), accountMask)
	assert.NotContains(t, buf.String(), "445478487199")
}

func TestPrintOut_VerbatimByDefault(t *testing.T) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	printOut(cmd, "account 445478487199\n")
	assert.Contains(t, buf.String(), "445478487199")
}

func TestRenderImageList_RedactsWhenEnabled(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")
	imgs := []microvm.ImageSummary{{Name: "whim-default", ARN: testARN, State: "CREATED"}}
	var buf bytes.Buffer
	require.NoError(t, renderImageList(&buf, imgs, outputMode{json: true}))
	assert.Contains(t, buf.String(), accountMask)
	assert.NotContains(t, buf.String(), "445478487199")
}

func TestRenderRmResults_RedactsWhenEnabled(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")
	results := []rmResult{{Name: "whim-default", ARN: testARN, Status: "deleted"}}
	var buf bytes.Buffer
	require.NoError(t, renderRmResults(&buf, results))

	assert.Contains(t, buf.String(), accountMask)
	assert.NotContains(t, buf.String(), "445478487199", "image rm --json must not leak the account")
	var arr []rmResult // still valid JSON after redaction
	require.NoError(t, json.Unmarshal(buf.Bytes(), &arr))
	require.Len(t, arr, 1)
}

func TestRenderRmResults_VerbatimByDefault(t *testing.T) {
	results := []rmResult{{ARN: testARN, Status: "deleted"}}
	var buf bytes.Buffer
	require.NoError(t, renderRmResults(&buf, results))
	assert.Contains(t, buf.String(), "445478487199")
}

func TestRedactAccountID_Disabled(t *testing.T) {
	// Env unset → identity, even for a bare 12-digit account.
	assert.Equal(t, "445478487199", redactAccountID("445478487199"))
	assert.Equal(t, "arn:aws:lambda:us-east-1:445478487199:microvm-image:x",
		redactAccountID("arn:aws:lambda:us-east-1:445478487199:microvm-image:x"))
}

func TestRedactAccountID_StrictlyOne(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "2") // anything other than "1" must NOT enable
	assert.Equal(t, "445478487199", redactAccountID("445478487199"))
	t.Setenv("WHIM_REDACT_ACCOUNT", "true")
	assert.Equal(t, "445478487199", redactAccountID("445478487199"))
}

func TestRedactAccountID_Enabled(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")

	for _, c := range []struct{ name, in string }{
		{"arn", "arn:aws:lambda:us-east-1:445478487199:microvm-image:whim-default"},
		{"bucket", "whim-artifacts-445478487199-us-east-1"},
		{"prose", "Initialising whim in account 445478487199 / region us-east-1"},
	} {
		got := redactAccountID(c.in)
		assert.Containsf(t, got, accountMask, "%s: should contain the mask", c.name)
		assert.NotContainsf(t, got, "445478487199", "%s: raw account must be gone", c.name)
	}

	// Must not touch non-account numbers.
	assert.Equal(t, "ttl=28800s", redactAccountID("ttl=28800s"), "5-digit TTL untouched")
	assert.Equal(t, "1234567890123", redactAccountID("1234567890123"), "13-digit run untouched (not exactly 12)")

	// Mask is the same width as a 12-digit account so tables stay aligned.
	assert.Len(t, accountMask, 12)
}

func TestRenderVMList_RedactsAccountWhenEnabled(t *testing.T) {
	t.Setenv("WHIM_REDACT_ACCOUNT", "1")
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, sampleVMs(), outputMode{json: true}))

	out := buf.String()
	assert.NotContains(t, out, "123456789012", "raw account must be redacted from --json")
	assert.Contains(t, out, accountMask)

	// Still valid JSON after redaction.
	var arr []vmJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &arr))
	require.NotEmpty(t, arr)
	assert.Contains(t, arr[0].ImageARN, accountMask)
}

func TestRenderVMList_NoRedactionByDefault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderVMList(&buf, sampleVMs(), outputMode{json: true}))
	assert.Contains(t, buf.String(), "123456789012", "default: account shown verbatim")
	assert.NotContains(t, buf.String(), strings.ToLower(accountMask))
}
