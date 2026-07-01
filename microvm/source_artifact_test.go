package microvm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

// fakeArtifactStore records uploads and serves seeded objects so
// package-internal tests can assert on staging behavior without reaching S3.
type fakeArtifactStore struct {
	objects map[string][]byte // "bucket/key" -> bytes (seeded sources + results)
	puts    []putCall
	err     error // PutObject failure
	getErr  error // GetObject failure
}

type putCall struct {
	bucket string
	key    string
	body   []byte
}

func (f *fakeArtifactStore) seed(bucket, key string, data []byte) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[bucket+"/"+key] = data
}

func (f *fakeArtifactStore) PutObject(_ context.Context, bucket, key string, body io.Reader) error {
	if f.err != nil {
		return f.err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.puts = append(f.puts, putCall{bucket: bucket, key: key, body: data})
	f.seed(bucket, key, data)
	return nil
}

func (f *fakeArtifactStore) GetObject(_ context.Context, bucket, key string, maxBytes int64) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	data, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, fmt.Errorf("fake: no object %s/%s", bucket, key)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		data = data[:maxBytes]
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Confirm the fake satisfies the seam at compile time.
var _ artifactStore = (*fakeArtifactStore)(nil)

func TestNewFromConfigWiresArtifactStore(t *testing.T) {
	m := NewFromConfig(aws.Config{Region: "us-east-1"})
	require.NotNil(t, m.artifacts, "NewFromConfig should wire a production artifact store")
}

func TestNewWithAPIArtifactStoreNil(t *testing.T) {
	m := NewWithAPI(&awsapi.Mock{})
	assert.Nil(t, m.artifacts, "NewWithAPI must not require an S3 artifact store")
}

func TestArtifactStoreFakeInjection(t *testing.T) {
	fake := &fakeArtifactStore{}
	m := NewWithAPI(&awsapi.Mock{}, withArtifactStore(fake))
	require.Same(t, artifactStore(fake), m.artifacts)

	err := m.artifacts.PutObject(context.Background(), "bucket", "prefix/app.zip", bytes.NewReader([]byte("payload")))
	require.NoError(t, err)
	require.Len(t, fake.puts, 1)
	assert.Equal(t, "bucket", fake.puts[0].bucket)
	assert.Equal(t, "prefix/app.zip", fake.puts[0].key)
	assert.Equal(t, []byte("payload"), fake.puts[0].body)
}
