package main

import (
	"net/url"
	"os"
	"regexp"
	"strings"
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

// sensitiveSourceQuery lists URL query parameter names whose values can carry
// credentials or presigned-request signatures. Compared case-insensitively.
// Errs toward over-redaction: masking a benign param only costs display clarity.
var sensitiveSourceQuery = map[string]bool{
	"token": true, "access_token": true, "api_key": true, "apikey": true,
	"key": true, "sig": true, "signature": true, "code": true,
	"x-amz-signature": true, "x-amz-credential": true, "x-amz-security-token": true,
}

// redactSourceCreds returns a display-safe build source: URL userinfo is
// stripped and known auth-sensitive query values are masked. Local paths,
// s3:// URIs without a query, and unparseable strings are returned unchanged.
// Always route a source through this before printing it (JSON or chrome).
func redactSourceCreds(source string) string {
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" {
		return source // local path or not URL-shaped → nothing to redact
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if sensitiveSourceQuery[strings.ToLower(k)] {
				q.Set(k, "REDACTED")
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
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
