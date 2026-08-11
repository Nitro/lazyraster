package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	formatPNG  = "png"
	formatHTML = "html"

	// maxVersionLength bounds the caller-supplied version that becomes a key component.
	maxVersionLength = 128
)

// pageCacheKeyVersion namespaces the cache key layout. Bump it whenever a change to the key inputs or
// to the rendering itself would make previously cached pages wrong: every key changes at once, so the
// old objects are simply never read again and the bucket lifecycle rule reclaims them.
const pageCacheKeyVersion = "v1"

// RenderRequest describes a single page render on the SWS-direct path. Every field is an input to the
// rendered output, which is what makes the request hashable into a cache key: the same request always
// produces the same bytes.
type RenderRequest struct {
	// Path is the source document, either "bucket/key" or "s3://bucket/key".
	Path string
	// Page is 1-based, matching the frontend and the public /documents endpoint.
	Page int
	// Width, Scale and DPI are the rasterisation parameters; zero means "lazypdf default".
	Width int
	Scale float32
	DPI   int
	// Format is "png" or "html".
	Format string
	// Version identifies the content being rendered, opaque to us and supplied by the caller (SWS's
	// `v`: the document's last-modified stamp combined with a digest of its field values). Cache
	// lookups are skipped entirely when it is absent or not key-safe: without a version, content
	// changing under the same source object would keep serving the render of its previous state.
	Version string
	// Annotations are the already-parsed domain annotations for this page.
	Annotations []any
}

// RenderResult carries a rendered page back to the transport layer.
//
// It is a stream rather than a buffer so a cache hit can be piped from S3 straight to the response
// without the bytes ever landing on our heap. Body must always be closed by the caller.
type RenderResult struct {
	Body io.ReadCloser
	// Size is the payload length, or -1 when it is unknown.
	Size int64
	// Cached reports whether this came from the page cache instead of a render.
	Cached bool
}

func (req RenderRequest) validate() error {
	if req.Page < 1 {
		return newClientError(errors.New("invalid page"))
	}
	if req.Width < 0 || req.Width > 4096 {
		return newClientError(fmt.Errorf("invalid width %d, must be between 0 and 4096", req.Width))
	}
	if req.Scale < 0 || req.Scale > 3 {
		return newClientError(fmt.Errorf("invalid scale %v, must be between 0 and 3", req.Scale))
	}
	if req.DPI < 0 || req.DPI > 600 {
		return newClientError(fmt.Errorf("invalid dpi %d, must be between 0 and 600", req.DPI))
	}
	if req.Format != formatPNG && req.Format != formatHTML {
		return newClientError(fmt.Errorf("unknown format '%s'", req.Format))
	}
	return nil
}

// cacheKey derives the page cache object key from every input that affects the rendered bytes. It is
// content-addressed on purpose: no caller identity, package or actor takes part, so a key can never
// grant access to a document the caller could not already render. Authorization stays entirely with
// the caller (SWS), which authorizes before it ever reaches this endpoint.
//
// The version is also the key's first path component, in the clear, so that everything cached for one
// document version can be listed and dropped as a unit -- the reason for keying on the caller's whole
// version rather than on the source object's timestamp alone.
//
// The annotations stay in the hash even though the version already moves whenever a field changes.
// They cost nothing in hit rate (for a given version and page they are fixed) and they keep the key a
// hash of what is actually rasterised, so a version that ever failed to capture a content change
// could not serve a stale page.
func (req RenderRequest) cacheKey() (string, error) {
	annotations, err := canonicalAnnotations(req.Annotations)
	if err != nil {
		return "", err
	}

	hash := sha256.New()
	// Every field is newline-terminated: without separators ("ab", "c") and ("a", "bc") would collide.
	fields := []string{
		pageCacheKeyVersion,
		req.Path,
		req.Version,
		strconv.Itoa(req.Page),
		strconv.Itoa(req.Width),
		strconv.Itoa(req.DPI),
		strconv.FormatFloat(float64(req.Scale), 'g', -1, 32),
		req.Format,
		annotations,
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(hash, "%s\n", field); err != nil {
			return "", fmt.Errorf("failed to hash the render request: %w", err)
		}
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	return fmt.Sprintf("%s/%s/%s.%s", pageCacheKeyVersion, req.Version, sum, req.Format), nil
}

// keySafeVersion reports whether the version can be used as an S3 key component. The caller is
// in-cluster and its versions are digits and hex, but the value still lands verbatim in an object key,
// so anything that could reshape the key layout (a separator, a traversal) is rejected here rather
// than trusted. A rejected version disables caching for the request; it never fails the render.
func keySafeVersion(version string) bool {
	if version == "" || len(version) > maxVersionLength {
		return false
	}
	for _, r := range version {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// canonicalAnnotations renders the annotations into a stable string, independent of the order they
// arrived in. The order genuinely varies: they are built from a database query with no explicit
// ordering, so hashing the array as received would give the same page two different keys and quietly
// destroy the hit rate.
func canonicalAnnotations(annotations []any) (string, error) {
	if len(annotations) == 0 {
		return "", nil
	}

	entries := make([]string, 0, len(annotations))
	for _, annotation := range annotations {
		payload, err := json.Marshal(annotation)
		if err != nil {
			return "", fmt.Errorf("failed to serialize an annotation: %w", err)
		}
		// The Go type prefixes the payload because the annotation types overlap structurally: a text and
		// a checkbox annotation with the same placement differ only in how "value" is typed.
		entries = append(entries, fmt.Sprintf("%T:%s", annotation, payload))
	}
	sort.Strings(entries)

	return strings.Join(entries, "\n"), nil
}
