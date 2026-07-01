package microvm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactSource(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		wantContains  []string
		wantOmits     []string
		wantUnchanged bool
	}{
		{
			name:         "https userinfo stripped",
			source:       "https://user:s3cr3t@example.com/app.zip",
			wantContains: []string{"example.com", "app.zip"},
			wantOmits:    []string{"s3cr3t", "user:"},
		},
		{
			name:         "sensitive query redacted",
			source:       "https://example.com/app.zip?token=abc123&ref=main",
			wantContains: []string{"ref=main"},
			wantOmits:    []string{"abc123"},
		},
		{
			name:         "s3 presigned signature redacted",
			source:       "s3://bucket/app.zip?X-Amz-Signature=deadbeef&X-Amz-Expires=900",
			wantContains: []string{"bucket", "X-Amz-Expires=900"},
			wantOmits:    []string{"deadbeef"},
		},
		{name: "plain s3 unchanged", source: "s3://bucket/app.zip", wantUnchanged: true},
		{name: "local path unchanged", source: "./build/context", wantUnchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactSource(tt.source)
			if tt.wantUnchanged {
				assert.Equal(t, tt.source, got)
				return
			}
			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
			for _, omit := range tt.wantOmits {
				assert.NotContains(t, got, omit)
			}
		})
	}
}

func TestRedactSourceHeaders(t *testing.T) {
	in := map[string]string{
		"Authorization": "Bearer ghp_secrettoken",
		"X-Api-Key":     "key-secret",
		"Accept":        "application/zip",
	}
	got := redactHeaders(in)

	assert.Equal(t, "application/zip", got["Accept"], "non-sensitive headers are preserved")
	assert.NotContains(t, got["Authorization"], "ghp_secrettoken")
	assert.NotContains(t, got["X-Api-Key"], "key-secret")

	// Redaction must not mutate the caller's map.
	assert.Equal(t, "Bearer ghp_secrettoken", in["Authorization"])

	assert.Nil(t, redactHeaders(nil))
}
