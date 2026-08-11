package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/nitro/lazyraster/v2/internal/service"
)

// fakeDocumentService records the arguments passed to Render and returns a canned body, so the
// render handler can be tested in isolation from lazypdf/S3.
type fakeDocumentService struct {
	renderRequest service.RenderRequest
	renderCalled  bool
	renderOutput  []byte
	renderCached  bool
	renderSize    int64
	renderErr     error
}

func (f *fakeDocumentService) Process(
	context.Context, string, string, int, int, float32, int, io.Writer, string,
) error {
	return nil
}

func (f *fakeDocumentService) Metadata(context.Context, string, string) (string, int, error) {
	return "", 0, nil
}

func (f *fakeDocumentService) Render(
	_ context.Context, request service.RenderRequest,
) (service.RenderResult, error) {
	f.renderCalled = true
	f.renderRequest = request
	if f.renderErr != nil {
		return service.RenderResult{}, f.renderErr
	}
	return service.RenderResult{
		Body:   io.NopCloser(bytes.NewReader(f.renderOutput)),
		Size:   f.renderSize,
		Cached: f.renderCached,
	}, nil
}

func newTestHandler(ds handlerDocumentService) handler {
	traceExtractor := func(context.Context, zerolog.Logger) (zerolog.Logger, error) {
		return zerolog.Nop(), nil
	}
	return handler{
		traceExtractor:  traceExtractor,
		logger:          zerolog.Nop(),
		writer:          writer{logger: zerolog.Nop(), traceExtractor: traceExtractor},
		documentService: ds,
	}
}

func TestHandlerRender(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderOutput: []byte("PNGDATA"), renderSize: int64(len("PNGDATA"))}
	h := newTestHandler(ds)

	body := `{"path":"bucket/key.pdf","page":2,"width":800,"dpi":150,"scale":1.5,"format":"png",` +
		`"version":"1754400000000-9f2b1c4d5e6a7b8c9d0e1f2a3b4c5d6e",` +
		`"annotations":[{"type":"text","value":"hi","page":2,"location":{"x":1,"y":2},` +
		`"font":{"family":"f","size":10},"size":{"height":3,"width":4}}]}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "image/png", rr.Header().Get("content-type"))
	require.Equal(t, strconv.Itoa(len("PNGDATA")), rr.Header().Get("content-length"))
	require.Equal(t, "PNGDATA", rr.Body.String())
	require.Equal(t, "bucket/key.pdf", ds.renderRequest.Path)
	require.Equal(t, 2, ds.renderRequest.Page)
	require.Equal(t, 800, ds.renderRequest.Width)
	require.Equal(t, 150, ds.renderRequest.DPI)
	require.EqualValues(t, 1.5, ds.renderRequest.Scale)
	require.Equal(t, "png", ds.renderRequest.Format)
	require.Equal(t, "1754400000000-9f2b1c4d5e6a7b8c9d0e1f2a3b4c5d6e", ds.renderRequest.Version)
	require.Len(t, ds.renderRequest.Annotations, 1)
}

// TestHandlerRenderReportsCacheStatus keeps the hit rate observable from the caller and from a canary.
func TestHandlerRenderReportsCacheStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message  string
		cached   bool
		expected string
	}{
		{message: "hit", cached: true, expected: "hit"},
		{message: "miss", cached: false, expected: "miss"},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			t.Parallel()

			ds := &fakeDocumentService{renderOutput: []byte("PNGDATA"), renderSize: 7, renderCached: tt.cached}
			h := newTestHandler(ds)
			req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"path":"b/k","page":1}`))
			rr := httptest.NewRecorder()

			h.render(rr, req)

			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, tt.expected, rr.Header().Get("x-lazyraster-page-cache"))
		})
	}
}

// TestHandlerRenderOmitsUnknownContentLength covers the streamed case: a cached page whose length S3 did
// not report must still be written, without a bogus content-length.
func TestHandlerRenderOmitsUnknownContentLength(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderOutput: []byte("PNGDATA"), renderSize: -1, renderCached: true}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"path":"b/k","page":1}`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, rr.Header().Get("content-length"))
	require.Equal(t, "PNGDATA", rr.Body.String())
}

// TestHandlerRenderFailure verifies a failed render still answers with an error status rather than an
// empty 200: the status is only committed once the render has succeeded.
func TestHandlerRenderFailure(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderErr: errors.New("render exploded")}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"path":"b/k","page":1}`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.NotContains(t, rr.Body.String(), "PNGDATA")
}

func TestHandlerRenderDefaultsFormatToPNG(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderOutput: []byte("X"), renderSize: 1}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"path":"b/k","page":1}`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "png", ds.renderRequest.Format)
	require.Equal(t, "image/png", rr.Header().Get("content-type"))
}

// TestHandlerRenderNullVersion pins the wire contract with SWS: it sends `"version": null` when it has
// no version for the content, and that must decode to "no version" rather than rejecting the render.
func TestHandlerRenderNullVersion(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderOutput: []byte("PNGDATA"), renderSize: 7}
	h := newTestHandler(ds)
	body := `{"path":"b/k","page":1,"width":null,"dpi":null,"scale":null,"version":null,"annotations":null}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, ds.renderCalled)
	require.Empty(t, ds.renderRequest.Version)
	require.Empty(t, ds.renderRequest.Annotations)
}

func TestHandlerRenderMissingPath(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"page":1}`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.False(t, ds.renderCalled)
}

func TestHandlerRenderInvalidBody(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{not json`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.False(t, ds.renderCalled)
}
