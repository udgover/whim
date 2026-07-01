package microvm

import (
	"net/url"
	"strings"
)

// redactedValue replaces any credential-bearing field surfaced to logs, errors,
// JSON, or config.
const redactedValue = "REDACTED"

// sensitiveQueryKeys are URL query parameter names whose values can carry
// credentials or presigned-request signatures. Compared case-insensitively.
//
// The list errs toward over-redaction on purpose: masking a benign param
// (e.g. a "code" that is really a country code) only costs display clarity,
// whereas missing a real secret leaks it. Keep broad entries like "key" and
// "code" unless a concrete false-positive case justifies narrowing them.
var sensitiveQueryKeys = map[string]struct{}{
	"token":                {},
	"access_token":         {},
	"api_key":              {},
	"apikey":               {},
	"key":                  {},
	"sig":                  {},
	"signature":            {},
	"code":                 {},
	"x-amz-signature":      {},
	"x-amz-credential":     {},
	"x-amz-security-token": {},
}

// sensitiveHeaderKeys are request header names whose values must never be
// surfaced. Compared case-insensitively.
var sensitiveHeaderKeys = map[string]struct{}{
	"authorization":        {},
	"proxy-authorization":  {},
	"cookie":               {},
	"x-api-key":            {},
	"x-auth-token":         {},
	"x-amz-security-token": {},
}

// redactSource returns a display-safe form of a build source: URL userinfo is
// removed and known auth-sensitive query values are masked. Local paths and
// unparseable strings are returned unchanged. Always route source strings
// through this before logging, wrapping in errors, or emitting JSON.
func redactSource(source string) string {
	switch schemeOf(source) {
	case "s3", "https", "http":
		// URL-shaped: parse and scrub. (http never classifies, but may still
		// reach here inside an error message, so handle it too.)
	default:
		return source
	}
	u, err := url.Parse(source)
	if err != nil {
		return source
	}
	if u.User != nil {
		u.User = url.User(redactedValue)
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if _, ok := sensitiveQueryKeys[strings.ToLower(k)]; ok {
				q.Set(k, redactedValue)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// redactHeaders returns a copy of h with the values of known auth-sensitive
// headers masked. The input map is never mutated; nil in yields nil out.
func redactHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if _, ok := sensitiveHeaderKeys[strings.ToLower(k)]; ok {
			out[k] = redactedValue
		} else {
			out[k] = v
		}
	}
	return out
}
