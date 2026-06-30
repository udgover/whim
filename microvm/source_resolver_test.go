package microvm

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceClassify(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		wantKind   sourceKind
		wantBucket string
		wantKey    string
		wantHost   string
		wantErr    bool
	}{
		{name: "relative dir", source: "./build", wantKind: sourceLocal},
		{name: "absolute path", source: "/srv/app", wantKind: sourceLocal},
		{name: "bare dockerfile", source: "Dockerfile", wantKind: sourceLocal},
		{name: "github shorthand is local to the library", source: "github.com/org/repo", wantKind: sourceLocal},
		{name: "s3 object", source: "s3://my-bucket/app.zip", wantKind: sourceS3, wantBucket: "my-bucket", wantKey: "app.zip"},
		{name: "s3 nested key", source: "s3://my-bucket/path/to/app.zip", wantKind: sourceS3, wantBucket: "my-bucket", wantKey: "path/to/app.zip"},
		{name: "https url", source: "https://example.com/app.zip", wantKind: sourceHTTPS, wantHost: "example.com"},
		{name: "https uppercase scheme", source: "HTTPS://example.com/app.zip", wantKind: sourceHTTPS, wantHost: "example.com"},

		{name: "http rejected", source: "http://example.com/app.zip", wantErr: true},
		{name: "file rejected", source: "file:///etc/passwd", wantErr: true},
		{name: "ssh rejected", source: "ssh://git@host/repo.git", wantErr: true},
		{name: "ftp rejected", source: "ftp://host/file", wantErr: true},
		{name: "git+https rejected", source: "git+https://github.com/org/repo", wantErr: true},
		{name: "s3 userinfo rejected", source: "s3://key:secret@bucket/app.zip", wantErr: true},
		{name: "https userinfo rejected", source: "https://user:token@example.com/app.zip", wantErr: true},
		{name: "s3 missing key", source: "s3://bucket", wantErr: true},
		{name: "s3 empty key trailing slash", source: "s3://bucket/", wantErr: true},
		{name: "https missing host", source: "https:///path", wantErr: true},
		{name: "empty source", source: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifySource(tt.source)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrInvalidSource), "want ErrInvalidSource, got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, got.kind)
			if tt.wantBucket != "" {
				assert.Equal(t, tt.wantBucket, got.bucket)
				assert.Equal(t, tt.wantKey, got.key)
			}
			if tt.wantHost != "" {
				require.NotNil(t, got.url)
				assert.Equal(t, tt.wantHost, got.url.Host)
			}
		})
	}
}
