package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeDocumentService records the arguments passed to Render and returns a canned body, so the
// render handler can be tested in isolation from lazypdf/S3.
type fakeDocumentService struct {
	renderPath        string
	renderPage        int
	renderWidth       int
	renderScale       float32
	renderDPI         int
	renderFormat      string
	renderAnnotations []any
	renderCalled      bool
	renderOutput      []byte
	renderErr         error
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
	_ context.Context, path string, page, width int, scale float32, dpi int, format string,
	annotations []any, output io.Writer,
) error {
	f.renderCalled = true
	f.renderPath = path
	f.renderPage = page
	f.renderWidth = width
	f.renderScale = scale
	f.renderDPI = dpi
	f.renderFormat = format
	f.renderAnnotations = annotations
	if f.renderErr != nil {
		return f.renderErr
	}
	_, err := output.Write(f.renderOutput)
	return err
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

	ds := &fakeDocumentService{renderOutput: []byte("PNGDATA")}
	h := newTestHandler(ds)

	body := `{"path":"bucket/key.pdf","page":2,"width":800,"dpi":150,"scale":1.5,"format":"png",` +
		`"annotations":[{"type":"text","value":"hi","page":2,"location":{"x":1,"y":2},` +
		`"font":{"family":"f","size":10},"size":{"height":3,"width":4}}]}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "image/png", rr.Header().Get("content-type"))
	require.Equal(t, "PNGDATA", rr.Body.String())
	require.Equal(t, "bucket/key.pdf", ds.renderPath)
	require.Equal(t, 2, ds.renderPage)
	require.Equal(t, 800, ds.renderWidth)
	require.Equal(t, 150, ds.renderDPI)
	require.EqualValues(t, 1.5, ds.renderScale)
	require.Equal(t, "png", ds.renderFormat)
	require.Len(t, ds.renderAnnotations, 1)
}

func TestHandlerRenderDefaultsFormatToPNG(t *testing.T) {
	t.Parallel()

	ds := &fakeDocumentService{renderOutput: []byte("X")}
	h := newTestHandler(ds)
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"path":"b/k","page":1}`))
	rr := httptest.NewRecorder()

	h.render(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "png", ds.renderFormat)
	require.Equal(t, "image/png", rr.Header().Get("content-type"))
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
