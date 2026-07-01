package microvm

import (
	"fmt"
	"net/url"
	"strings"
)

// sourceKind identifies which transport a build source resolves to. GitHub
// shorthand is a CLI concern lowered to https:// before it reaches the library,
// so the library only ever sees these three kinds.
type sourceKind int

const (
	sourceLocal sourceKind = iota
	sourceS3
	sourceHTTPS
)

// classifiedSource is the validated, parsed form of a build source string.
type classifiedSource struct {
	kind sourceKind
	raw  string

	// bucket/key are set for sourceS3.
	bucket string
	key    string

	// url is set for sourceS3 and sourceHTTPS.
	url *url.URL

	// path is set for sourceLocal.
	path string
}

// classifySource resolves source to exactly one supported transport. It rejects
// unsupported schemes (http, file, ssh, …), credential-bearing URL userinfo,
// and malformed s3/https URLs with ErrInvalidSource. Anything without a URL
// scheme is treated as a local filesystem path; existence is checked later.
func classifySource(source string) (classifiedSource, error) {
	if source == "" {
		return classifiedSource{}, fmt.Errorf("%w: empty source", ErrInvalidSource)
	}
	switch s := schemeOf(source); s {
	case "":
		return classifiedSource{kind: sourceLocal, raw: source, path: source}, nil
	case "s3":
		return classifyURLSource(source, sourceS3)
	case "https":
		return classifyURLSource(source, sourceHTTPS)
	default:
		return classifiedSource{}, fmt.Errorf("%w: unsupported scheme %q (use a local path, s3://bucket/key, or https://host/path)", ErrInvalidSource, s)
	}
}

// classifyURLSource parses an s3:// or https:// source and validates its parts.
func classifyURLSource(source string, kind sourceKind) (classifiedSource, error) {
	u, err := url.Parse(source)
	if err != nil {
		return classifiedSource{}, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	if u.User != nil {
		return classifiedSource{}, fmt.Errorf("%w: credentials in URL userinfo are not allowed", ErrInvalidSource)
	}
	switch kind {
	case sourceS3:
		key := strings.TrimPrefix(u.Path, "/")
		if u.Host == "" || key == "" {
			return classifiedSource{}, fmt.Errorf("%w: s3 source must be s3://bucket/key", ErrInvalidSource)
		}
		return classifiedSource{kind: sourceS3, raw: source, bucket: u.Host, key: key, url: u}, nil
	case sourceHTTPS:
		if u.Host == "" {
			return classifiedSource{}, fmt.Errorf("%w: https source must include a host", ErrInvalidSource)
		}
		return classifiedSource{kind: sourceHTTPS, raw: source, url: u}, nil
	default:
		return classifiedSource{}, fmt.Errorf("%w: %q", ErrInvalidSource, source)
	}
}

// schemeOf returns the lowercased URI scheme of source (the token before
// "://"), or "" when source has no parseable scheme and is therefore treated
// as a local filesystem path. A token that is not a syntactically valid scheme
// (e.g. a relative path that happens to contain "://") also yields "".
func schemeOf(source string) string {
	idx := strings.Index(source, "://")
	if idx <= 0 {
		return ""
	}
	raw := source[:idx]
	for i, r := range raw {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i == 0 {
			if !isAlpha {
				return "" // scheme must start with a letter
			}
			continue
		}
		if !isAlpha && (r < '0' || r > '9') && r != '+' && r != '.' && r != '-' {
			return "" // not a valid scheme token → treat as local path
		}
	}
	return strings.ToLower(raw)
}
