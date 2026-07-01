package microvm

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

// stageS3Source stages an s3:// source into the caller's artifact bucket. The
// object is sniffed, then downloaded and validated like any other source: a zip
// is bounded by the compressed cap and run through rerootZip (traversal,
// symlink, duplicate, file-count, and uncompressed checks, plus single-top-dir
// strip and ContextSubdir), while a raw Dockerfile is bounded by the
// uncompressed cap and packaged. The returned URI always points at the artifact
// bucket. Source/destination access failures are wrapped plainly and never
// suggest changing the build role, which only reads the staged artifact.
//
// There is deliberately no server-side pass-through copy: every source must be
// validated before it reaches the artifact bucket and any CreateMicrovmImage.
func (m *Manager) stageS3Source(ctx context.Context, src classifiedSource, opts BuildFromSourceOptions) (string, error) {
	caps := opts.caps()

	head, err := m.readS3Head(ctx, src.bucket, src.key, int64(len(zipMagic)))
	if err != nil {
		return "", fmt.Errorf("read source object: %w", err)
	}

	if bytes.HasPrefix(head, zipMagic) {
		data, err := m.readS3Object(ctx, src.bucket, src.key, caps.maxCompressedBytes)
		if err != nil {
			return "", err
		}
		zip, err := rerootZip(data, opts.ContextSubdir, caps)
		if err != nil {
			return "", err
		}
		return m.uploadArtifact(ctx, opts, zip)
	}

	data, err := m.readS3Object(ctx, src.bucket, src.key, caps.maxUncompressedBytes)
	if err != nil {
		return "", err
	}
	zip, err := buildContextZip([]zipEntry{{name: dockerfileName, content: data}}, caps)
	if err != nil {
		return "", err
	}
	return m.uploadArtifact(ctx, opts, zip)
}

// readS3Head reads up to n bytes from the head of an S3 object to sniff its
// type without downloading the whole object.
func (m *Manager) readS3Head(ctx context.Context, bucket, key string, n int64) ([]byte, error) {
	rc, err := m.artifacts.GetObject(ctx, bucket, key, n)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, n))
}

// readS3Object downloads an S3 object, enforcing limit on the bytes read so a
// large source is rejected with ErrSourceTooLarge rather than buffered whole.
func (m *Manager) readS3Object(ctx context.Context, bucket, key string, limit int64) ([]byte, error) {
	rc, err := m.artifacts.GetObject(ctx, bucket, key, 0)
	if err != nil {
		return nil, fmt.Errorf("read source object: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read source object: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: source object exceeds cap of %d bytes", ErrSourceTooLarge, limit)
	}
	return data, nil
}
