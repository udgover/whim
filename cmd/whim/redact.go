package main

import (
	"os"
	"regexp"
)

// accountMask replaces an AWS account ID in whim's own output. It is 12
// characters — the same width as a 12-digit account — so tables stay aligned
// when redaction is applied after rendering.
const accountMask = "<ACCOUNT_ID>"

// accountIDRe matches exactly 12 consecutive digits. In whim's own (chrome)
// output the only 12-digit runs are AWS account IDs (in ARNs, the
// whim-artifacts-<acct>-<region> bucket, and the "in account …" line) — TTLs are
// ≤5 digits, exit codes 1–3, VM ids are hex, timestamps are colon/dash-separated.
// So this is fail-closed: it cannot miss an account occurrence.
var accountIDRe = regexp.MustCompile(`\b\d{12}\b`)

// redactEnabled reports whether WHIM_REDACT_ACCOUNT=1 requests account masking.
func redactEnabled() bool {
	return os.Getenv("WHIM_REDACT_ACCOUNT") == "1"
}

// redactAccountID masks AWS account IDs in whim's OWN (chrome) output —
// status lines, error messages, and the image/VM list renderers — when
// WHIM_REDACT_ACCOUNT=1. It is deliberately NOT applied to streamed remote
// command output (run/exec/shell/get), which is user data, nor anywhere in the
// microvm library. When disabled it is the identity function.
func redactAccountID(s string) string {
	if !redactEnabled() {
		return s
	}
	return accountIDRe.ReplaceAllString(s, accountMask)
}
