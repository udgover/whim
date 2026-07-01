package microvm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// artifactStore is the narrow, internal S3 seam that source staging uses to
// read sources and place build artifacts in the caller-owned artifact bucket.
// It is deliberately unexported: BuildFromSource remains the only public build
// surface, and tests inject a fake via withArtifactStore rather than through a
// user-facing option.
//
// The two operations cover every staging path: PutObject uploads a staged zip
// (local/HTTPS/S3, including re-rooted archives and repackaged raw sources), and
// GetObject reads an s3:// source (ranged to sniff archive magic, or whole to
// download a zip/raw Dockerfile for validation and re-rooting). Every source is
// validated before it reaches the artifact bucket, so there is no server-side
// pass-through copy.
type artifactStore interface {
	// PutObject uploads body to bucket/key, consuming the reader fully.
	PutObject(ctx context.Context, bucket, key string, body io.Reader) error
	// GetObject opens bucket/key for reading. When maxBytes > 0 only the first
	// maxBytes are requested (a ranged read used to sniff archive magic).
	GetObject(ctx context.Context, bucket, key string, maxBytes int64) (io.ReadCloser, error)
}

// s3ArtifactStore is the production artifactStore backed by the AWS S3 client.
type s3ArtifactStore struct {
	client *s3.Client
}

// newS3ArtifactStore builds the production artifact store from the
// caller-supplied aws.Config. The library never resolves ambient credentials;
// cfg already carries the caller's credentials and region.
func newS3ArtifactStore(cfg aws.Config) *s3ArtifactStore {
	return &s3ArtifactStore{client: s3.NewFromConfig(cfg)}
}

// PutObject uploads body to s3://bucket/key.
func (s *s3ArtifactStore) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("put object s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

// GetObject opens s3://bucket/key, optionally requesting only the first
// maxBytes via a Range header (used to sniff archive magic cheaply).
func (s *s3ArtifactStore) GetObject(ctx context.Context, bucket, key string, maxBytes int64) (io.ReadCloser, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if maxBytes > 0 {
		in.Range = aws.String(fmt.Sprintf("bytes=0-%d", maxBytes-1))
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("get object s3://%s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}

// Ensure the production store satisfies the seam at compile time.
var _ artifactStore = (*s3ArtifactStore)(nil)

// withArtifactStore injects an artifactStore implementation. It is unexported
// so it stays a package-internal test seam and never widens the public API.
func withArtifactStore(store artifactStore) Option {
	return func(m *Manager) { m.artifacts = store }
}

// artifactKey is the S3 key a staged build context uploads to within the
// caller's artifact bucket: "<prefix>/<name>.zip". The image name keys reuse,
// so a same-named rebuild overwrites its prior artifact.
func artifactKey(opts BuildFromSourceOptions) string {
	key := opts.Name + ".zip"
	if prefix := strings.Trim(opts.ArtifactPrefix, "/"); prefix != "" {
		key = prefix + "/" + key
	}
	return key
}

// uploadArtifact uploads a staged build-context zip to the caller's artifact
// bucket and returns the resulting s3:// URI for ImageSpec.CodeArtifactURI.
func (m *Manager) uploadArtifact(ctx context.Context, opts BuildFromSourceOptions, zip []byte) (string, error) {
	key := artifactKey(opts)
	if err := m.artifacts.PutObject(ctx, opts.ArtifactBucket, key, bytes.NewReader(zip)); err != nil {
		return "", fmt.Errorf("upload staged artifact: %w", err)
	}
	return "s3://" + opts.ArtifactBucket + "/" + key, nil
}
