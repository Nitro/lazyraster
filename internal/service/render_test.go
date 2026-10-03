package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/nitro/lazyraster/v2/internal/domain"
)

// fakePageCache records what the worker asks of the cache and serves a canned entry.
type fakePageCache struct {
	mutex sync.Mutex

	entry   []byte
	getErr  error
	getKeys []string
	puts    map[string][]byte
}

func (f *fakePageCache) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.getKeys = append(f.getKeys, key)
	if f.getErr != nil {
		return nil, 0, f.getErr
	}
	if f.entry == nil {
		return nil, 0, nil
	}
	return io.NopCloser(bytes.NewReader(f.entry)), int64(len(f.entry)), nil
}

func (f *fakePageCache) Put(key string, payload []byte) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if f.puts == nil {
		f.puts = make(map[string][]byte)
	}
	f.puts[key] = payload
}

func (f *fakePageCache) putCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return len(f.puts)
}

// countingS3 counts the source document fetches so a test can prove a cached render performs none.
type countingS3 struct {
	mutex   sync.Mutex
	payload []byte
	calls   int
}

func (c *countingS3) GetObject(
	context.Context, *s3.GetObjectInput, ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.calls++
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(c.payload))}, nil
}

func (c *countingS3) callCount() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.calls
}

func newRenderWorker(t *testing.T, s3Client workerS3API, cache workerPageCache) *Worker {
	t.Helper()

	w := &Worker{
		HTTPClient:          http.DefaultClient,
		URLSigningSecret:    "secret",
		TraceExtractor:      traceExtractor,
		StorageBucketRegion: map[string]string{"bucket-1": "eu-central-1"},
		PageCache:           cache,
		getS3Client:         func(string) (workerS3API, error) { return s3Client, nil },
	}
	require.NoError(t, w.Init())
	return w
}

func samplePDF(t *testing.T) []byte {
	t.Helper()

	payload, err := os.ReadFile("testdata/sample.pdf")
	require.NoError(t, err)
	return payload
}

// testVersion has the shape of SWS's `v`: the document's last-modified stamp and a digest of its field
// values.
const testVersion = "1754400000000-9f2b1c4d5e6a7b8c9d0e1f2a3b4c5d6e"

func renderRequest() RenderRequest {
	return RenderRequest{
		Path:    "bucket-1/file.pdf",
		Page:    1,
		DPI:     72,
		Format:  formatPNG,
		Version: testVersion,
	}
}

// TestWorkerRenderServesFromPageCache is the point of the whole cache: a cached page is returned without
// fetching the source document and without entering lazypdf.
func TestWorkerRenderServesFromPageCache(t *testing.T) {
	t.Parallel()

	s3Client := &countingS3{payload: samplePDF(t)}
	cache := &fakePageCache{entry: []byte("CACHEDPNG")}
	w := newRenderWorker(t, s3Client, cache)

	result, err := w.Render(context.Background(), renderRequest())
	require.NoError(t, err)
	defer result.Body.Close()

	payload, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.Equal(t, "CACHEDPNG", string(payload))
	require.True(t, result.Cached)
	require.EqualValues(t, len(payload), result.Size)
	require.Zero(t, s3Client.callCount(), "a cache hit must not fetch the source document")
	require.Zero(t, cache.putCount(), "a cache hit must not write back")
}

// TestWorkerRenderPopulatesPageCache verifies a miss renders and writes the result back under the same
// key it looked up.
func TestWorkerRenderPopulatesPageCache(t *testing.T) {
	t.Parallel()

	cache := &fakePageCache{}
	w := newRenderWorker(t, &countingS3{payload: samplePDF(t)}, cache)

	req := renderRequest()
	result, err := w.Render(context.Background(), req)
	require.NoError(t, err)
	defer result.Body.Close()

	payload, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.NotEmpty(t, payload)
	require.False(t, result.Cached)

	key, err := req.cacheKey()
	require.NoError(t, err)
	require.Len(t, cache.getKeys, 1)
	require.Equal(t, key, cache.getKeys[0])
	require.Equal(t, payload, cache.puts[key])
}

// TestWorkerRenderWithoutUsableVersionSkipsCache covers the safety valve: with no usable version the
// cache is bypassed entirely rather than risk serving the render of superseded content, and a version
// that could reshape the object key is never allowed into one.
func TestWorkerRenderWithoutUsableVersionSkipsCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message string
		version string
	}{
		{message: "absent", version: ""},
		{message: "with a path separator", version: "../../etc/passwd"},
		{message: "with a key separator", version: "1754400000000/deadbeef"},
		{message: "too long", version: strings.Repeat("a", maxVersionLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			t.Parallel()

			cache := &fakePageCache{entry: []byte("CACHEDPNG")}
			w := newRenderWorker(t, &countingS3{payload: samplePDF(t)}, cache)

			req := renderRequest()
			req.Version = tt.version
			result, err := w.Render(context.Background(), req)
			require.NoError(t, err)
			defer result.Body.Close()

			payload, err := io.ReadAll(result.Body)
			require.NoError(t, err)
			require.NotEqual(t, "CACHEDPNG", string(payload))
			require.Empty(t, cache.getKeys)
			require.Zero(t, cache.putCount())
		})
	}
}

// TestWorkerRenderSurvivesPageCacheFailure is the 2026-07-01 lesson as a test: a broken cache degrades
// to a render, it never fails the request.
func TestWorkerRenderSurvivesPageCacheFailure(t *testing.T) {
	t.Parallel()

	cache := &fakePageCache{getErr: errors.New("s3 is unhappy")}
	w := newRenderWorker(t, &countingS3{payload: samplePDF(t)}, cache)

	result, err := w.Render(context.Background(), renderRequest())
	require.NoError(t, err)
	defer result.Body.Close()

	payload, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.NotEmpty(t, payload)
}

func TestWorkerRenderInvalidFormat(t *testing.T) {
	t.Parallel()

	w := newRenderWorker(t, &countingS3{}, nil)

	req := renderRequest()
	req.Format = "jpeg"
	_, err := w.Render(context.Background(), req)
	require.ErrorIs(t, err, ErrClient)
}

// TestRenderRequestCacheKeyIsStableAndVersionScoped pins the layout: the version is a path component in
// the clear, so everything cached for one document version can be listed and dropped as a unit.
func TestRenderRequestCacheKeyIsStableAndVersionScoped(t *testing.T) {
	t.Parallel()

	first, err := renderRequest().cacheKey()
	require.NoError(t, err)
	second, err := renderRequest().cacheKey()
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Regexp(t, `^v1/`+testVersion+`/[0-9a-f]{64}\.png$`, first)

	// Every page of a document version shares the prefix, which is what makes that listing possible.
	other := renderRequest()
	other.Page = 7
	otherKey, err := other.cacheKey()
	require.NoError(t, err)
	require.NotEqual(t, first, otherKey)
	require.True(t, strings.HasPrefix(otherKey, "v1/"+testVersion+"/"))
}

// TestRenderRequestCacheKeyIgnoresAnnotationOrder is what keeps the hit rate from silently collapsing:
// the annotations come from a query with no explicit ordering, so the same page can arrive ordered
// differently on two requests.
func TestRenderRequestCacheKeyIgnoresAnnotationOrder(t *testing.T) {
	t.Parallel()

	first := domain.AnnotationText{Value: "first", Page: 1}
	second := domain.AnnotationCheckbox{Value: true, Page: 1}

	forward := renderRequest()
	forward.Annotations = []any{first, second}
	reverse := renderRequest()
	reverse.Annotations = []any{second, first}

	forwardKey, err := forward.cacheKey()
	require.NoError(t, err)
	reverseKey, err := reverse.cacheKey()
	require.NoError(t, err)

	require.Equal(t, forwardKey, reverseKey)
}

func TestRenderRequestCacheKeyChangesWithInputs(t *testing.T) {
	t.Parallel()

	base, err := renderRequest().cacheKey()
	require.NoError(t, err)

	tests := []struct {
		message string
		mutate  func(*RenderRequest)
	}{
		{message: "path", mutate: func(r *RenderRequest) { r.Path = "bucket-1/other.pdf" }},
		{message: "version", mutate: func(r *RenderRequest) { r.Version = testVersion + "0" }},
		{message: "page", mutate: func(r *RenderRequest) { r.Page = 2 }},
		{message: "width", mutate: func(r *RenderRequest) { r.Width = 200 }},
		{message: "dpi", mutate: func(r *RenderRequest) { r.DPI = 150 }},
		{message: "scale", mutate: func(r *RenderRequest) { r.Scale = 1.5 }},
		{message: "format", mutate: func(r *RenderRequest) { r.Format = formatHTML }},
		{
			message: "annotation added",
			mutate:  func(r *RenderRequest) { r.Annotations = []any{domain.AnnotationText{Value: "a", Page: 1}} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			t.Parallel()

			req := renderRequest()
			tt.mutate(&req)
			key, err := req.cacheKey()
			require.NoError(t, err)
			require.NotEqual(t, base, key)
		})
	}
}

// TestRenderRequestCacheKeyDistinguishesAnnotationTypes guards the structural overlap between the
// annotation types: a text and a checkbox annotation differ only in how "value" is typed.
func TestRenderRequestCacheKeyDistinguishesAnnotationTypes(t *testing.T) {
	t.Parallel()

	text := renderRequest()
	text.Annotations = []any{domain.AnnotationText{Page: 1}}
	checkbox := renderRequest()
	checkbox.Annotations = []any{domain.AnnotationCheckbox{Page: 1}}

	textKey, err := text.cacheKey()
	require.NoError(t, err)
	checkboxKey, err := checkbox.cacheKey()
	require.NoError(t, err)

	require.NotEqual(t, textKey, checkboxKey)
}
