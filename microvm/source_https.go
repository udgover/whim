package microvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// maxHTTPRedirects bounds redirect chains during HTTPS source staging.
const maxHTTPRedirects = 10

// zipMagic is the local-file-header signature that marks a non-empty zip. It is
// used to distinguish zip archives from raw Dockerfiles for both HTTPS and S3
// sources.
var zipMagic = []byte("PK\x03\x04")

// sharedHTTPClient is the default client for HTTPS staging: a bounded timeout,
// a redirect cap, and an https-only redirect policy so a source can never be
// downgraded to http:// (or another scheme) via a redirect. It is safe for
// concurrent use; tests inject their own via withHTTPClient.
var sharedHTTPClient = &http.Client{
	Timeout:       60 * time.Second,
	CheckRedirect: httpsOnlyRedirect,
}

// httpsOnlyRedirect bounds redirect chains and refuses any redirect target
// whose scheme is not https, upholding the "never accept http://" rule across
// redirects. The error never embeds the target URL, so tokens cannot leak.
func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-https scheme %q", req.URL.Scheme)
	}
	if len(via) >= maxHTTPRedirects {
		return fmt.Errorf("stopped after %d redirects", maxHTTPRedirects)
	}
	return nil
}

// withHTTPClient injects the HTTP client used for HTTPS staging. Unexported: a
// package-internal test seam (e.g. httptest's TLS client), not a public option.
func withHTTPClient(c *http.Client) Option {
	return func(m *Manager) { m.httpClient = c }
}

// http returns the Manager's HTTP client, or the shared default.
func (m *Manager) http() *http.Client {
	if m.httpClient != nil {
		return m.httpClient
	}
	return sharedHTTPClient
}

// stageHTTPSSource downloads an https:// source under bounded redirects, time,
// and size, then stages it into the artifact bucket: a zip response is
// validated and re-rooted, a raw response is packaged as a Dockerfile. All
// surfaced errors route the source through redactSource and never carry request
// headers, so auth tokens cannot leak.
func (m *Manager) stageHTTPSSource(ctx context.Context, src classifiedSource, opts BuildFromSourceOptions) (string, error) {
	caps := opts.caps()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.raw, nil)
	if err != nil {
		return "", fmt.Errorf("%w: build request for %s", ErrInvalidSource, redactSource(src.raw))
	}
	for k, v := range opts.HTTPSHeaders {
		req.Header.Set(k, v)
	}

	resp, err := m.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("https request %s: %w", redactSource(src.raw), scrubURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s returned HTTP %d", ErrInvalidSource, redactSource(src.raw), resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, caps.maxCompressedBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", redactSource(src.raw), scrubURLError(err))
	}
	if int64(len(data)) > caps.maxCompressedBytes {
		return "", fmt.Errorf("%w: downloaded source exceeds compressed cap of %d bytes", ErrSourceTooLarge, caps.maxCompressedBytes)
	}

	var zip []byte
	if bytes.HasPrefix(data, zipMagic) {
		zip, err = rerootZip(data, opts.ContextSubdir, caps)
	} else {
		// rerootZip enforces the uncompressed cap for archives; the raw branch
		// must do so itself, since buildContextZip only checks compressed size.
		if int64(len(data)) > caps.maxUncompressedBytes {
			return "", fmt.Errorf("%w: downloaded dockerfile exceeds uncompressed cap of %d bytes", ErrSourceTooLarge, caps.maxUncompressedBytes)
		}
		zip, err = buildContextZip([]zipEntry{{name: dockerfileName, content: data}}, caps)
	}
	if err != nil {
		return "", err
	}
	return m.uploadArtifact(ctx, opts, zip)
}

// scrubURLError unwraps a *url.Error to its underlying cause so the request URL
// — which may carry a token in its query — never reaches an error string.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
