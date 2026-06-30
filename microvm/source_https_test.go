package microvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udgover/whim/internal/awsapi"
)

// httpsManager builds a Manager wired to the test server's TLS client and a
// fake artifact store, returning both for assertions.
func httpsManager(t *testing.T, client *http.Client) (*Manager, *fakeArtifactStore) {
	t.Helper()
	fake := &fakeArtifactStore{}
	m := NewWithAPI(&awsapi.Mock{}, WithRegion("us-east-1"), withArtifactStore(fake), withHTTPClient(client))
	return m, fake
}

func stageHTTPS(t *testing.T, m *Manager, url string, opts BuildFromSourceOptions) (string, error) {
	t.Helper()
	src, err := classifySource(url)
	require.NoError(t, err)
	return m.stageHTTPSSource(context.Background(), src, opts)
}

func httpsOpts() BuildFromSourceOptions {
	return BuildFromSourceOptions{
		Name:           "whim-https",
		ArtifactBucket: "whim-artifacts",
		BaseImageARN:   "arn:base",
		BuildRoleARN:   "arn:role",
		Egress:         EgressPublic,
	}
}

func TestStageHTTPSRawDockerfile(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "FROM scratch\nCMD [\"true\"]\n")
	}))
	defer ts.Close()

	m, fake := httpsManager(t, ts.Client())
	uri, err := stageHTTPS(t, m, ts.URL+"/Dockerfile", httpsOpts())
	require.NoError(t, err)
	assert.Equal(t, "s3://whim-artifacts/whim-https.zip", uri)

	require.Len(t, fake.puts, 1)
	entries := unzipEntries(t, fake.puts[0].body)
	assert.Equal(t, "FROM scratch\nCMD [\"true\"]\n", entries["Dockerfile"])
}

func TestStageHTTPSZipArchive(t *testing.T) {
	zipBody := makeZip(t,
		ztEntry{name: "repo-1/", dir: true},
		ztEntry{name: "repo-1/Dockerfile", content: "FROM scratch\n"},
		ztEntry{name: "repo-1/app/main.go", content: "package main\n"},
	)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipBody)
	}))
	defer ts.Close()

	m, fake := httpsManager(t, ts.Client())
	_, err := stageHTTPS(t, m, ts.URL+"/archive.zip", httpsOpts())
	require.NoError(t, err)

	require.Len(t, fake.puts, 1)
	entries := unzipEntries(t, fake.puts[0].body)
	assert.Contains(t, entries, "Dockerfile", "single top-level dir should be stripped")
	assert.Contains(t, entries, "app/main.go")
}

func TestStageHTTPSSendsHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		fmt.Fprint(w, "FROM scratch\n")
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	opts := httpsOpts()
	opts.HTTPSHeaders = map[string]string{"Authorization": "Bearer tok", "X-Custom": "yes"}
	_, err := stageHTTPS(t, m, ts.URL+"/Dockerfile", opts)
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "yes", gotCustom)
}

func TestStageHTTPSMaxBytes(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 4096))
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	opts := httpsOpts()
	opts.MaxCompressedBytes = 16
	_, err := stageHTTPS(t, m, ts.URL+"/big", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "got %v", err)
}

func TestStageHTTPSRawDockerfileUncompressedCap(t *testing.T) {
	// Highly compressible raw body: small compressed, but inflates past the
	// uncompressed cap. Only an uncompressed-length check on the raw branch
	// rejects it (the compressed cap and download bound both pass).
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("A"), 4096))
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	opts := httpsOpts()
	opts.MaxCompressedBytes = 1 << 20 // 1 MiB: lets the 4 KiB body download
	opts.MaxUncompressedBytes = 1024  // but the body exceeds this
	_, err := stageHTTPS(t, m, ts.URL+"/Dockerfile", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceTooLarge), "raw download must enforce the uncompressed cap, got %v", err)
}

func TestStageHTTPSStatusError(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	_, err := stageHTTPS(t, m, ts.URL+"/missing", httpsOpts())
	require.Error(t, err)
}

func TestStageHTTPSRedirectLimit(t *testing.T) {
	var ts *httptest.Server
	hops := 0
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, ts.URL+fmt.Sprintf("/hop%d", hops), http.StatusFound)
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	_, err := stageHTTPS(t, m, ts.URL+"/start", httpsOpts())
	require.Error(t, err, "infinite redirects must be stopped")
}

func TestStageHTTPSRefusesRedirectToHTTP(t *testing.T) {
	// A plain-HTTP origin that must never be fetched, even via redirect.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "FROM evil\n")
	}))
	defer plain.Close()

	// An https source that 302-redirects down to the http origin.
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/Dockerfile", http.StatusFound)
	}))
	defer tls.Close()

	// Drive the shared client's redirect policy through the test transport, so
	// the scheme check is exercised (not httptest's default policy).
	client := tls.Client()
	client.CheckRedirect = sharedHTTPClient.CheckRedirect

	m, fake := httpsManager(t, client)
	_, err := stageHTTPS(t, m, tls.URL+"/start", httpsOpts())
	require.Error(t, err, "must refuse an https->http redirect")
	assert.Empty(t, fake.puts, "must not stage bytes from a plain-HTTP origin")
}

func TestHTTPRedactToken(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	m, _ := httpsManager(t, ts.Client())
	opts := httpsOpts()
	opts.HTTPSHeaders = map[string]string{"Authorization": "Bearer SUPERSECRET"}
	_, err := stageHTTPS(t, m, ts.URL+"/x?token=QUERYSECRET", opts)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUPERSECRET", "auth header must not leak into errors")
	assert.NotContains(t, err.Error(), "QUERYSECRET", "query token must not leak into errors")
}
