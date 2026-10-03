package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/nitro/lazyraster/v2/internal/domain"
	"github.com/nitro/lazyraster/v2/internal/service"
)

type handlerDocumentService interface {
	Process(context.Context, string, string, int, int, float32, int, io.Writer, string) error
	Metadata(context.Context, string, string) (string, int, error)
	Render(context.Context, service.RenderRequest) (service.RenderResult, error)
}

type handler struct {
	writer          writer
	logger          zerolog.Logger
	traceExtractor  traceExtractor
	documentService handlerDocumentService
}

func (h handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writer.error(r.Context(), w, "Endpoint not found", nil, http.StatusNotFound)
}

func (h handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writer.error(r.Context(), w, "Method not allowed", nil, http.StatusMethodNotAllowed)
}

func (h handler) health(w http.ResponseWriter, r *http.Request) {
	h.writer.response(r.Context(), w, map[string]interface{}{"status": "healthy"}, http.StatusOK, "application/json")
}

func (h handler) document(w http.ResponseWriter, r *http.Request) {
	reqID := chiMiddleware.GetReqID(r.Context())
	logger, err := h.traceExtractor(r.Context(), h.logger)
	if err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Could not extract tracing id")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusInternalServerError)
		return
	}

	rawPage := r.URL.Query().Get("page")
	if rawPage == "" {
		h.metadata(w, r)
		return
	}

	page, err := strconv.Atoi(rawPage)
	if err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Invalid 'page' parameter")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}

	var width int
	rawWidth := r.URL.Query().Get("width")
	if rawWidth != "" {
		width, err = strconv.Atoi(rawWidth)
		if err != nil {
			logger.Err(err).Str("requestID", reqID).Msg("Invalid 'width' parameter")
			h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
			return
		}
	}

	var dpi int
	rawDPI := r.URL.Query().Get("dpi")
	if rawDPI != "" {
		dpi, err = strconv.Atoi(rawDPI)
		if err != nil {
			logger.Err(err).Str("requestID", reqID).Msg("Invalid 'dpi' parameter")
			h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
			return
		}
	}

	var scale float64
	rawScale := r.URL.Query().Get("scale")
	if rawScale != "" {
		scale, err = strconv.ParseFloat(rawScale, 32)
		if err != nil {
			logger.Err(err).Str("requestID", reqID).Msg("Invalid 'scale' parameter")
			h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
			return
		}
	}

	if rawTokenTTL := r.URL.Query().Get("token-ttl"); rawTokenTTL != "" {
		parsedTokenTTL, err := strconv.ParseInt(rawTokenTTL, 10, 64)
		if err != nil {
			logger.Err(err).Str("requestID", reqID).Msg("Invalid 'token-ttl' parameter")
			h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
			return
		}
		if time.Now().After(time.Unix(parsedTokenTTL, 0)) {
			logger.Debug().Str("requestID", reqID).Msg("Token has expired")
			h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusUnauthorized)
			return
		}
	}

	var contentType string
	format := r.URL.Query().Get("format")
	switch format {
	case formatPNG:
		contentType = contentTypePNG
	case formatHTML:
		contentType = contentTypeHTML
	case "":
		contentType = contentTypePNG
		format = formatPNG
	default:
		logger.Err(err).Str("requestID", reqID).Msg("Invalid 'format' parameter")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/documents/")
	buf := bytes.NewBuffer([]byte{})
	err = h.documentService.Process(r.Context(), h.urlToVerify(r), path, page, width, float32(scale), dpi, buf, format)
	if ctxErr := r.Context().Err(); ctxErr != nil {
		logger.Err(ctxErr).Str("requestID", reqID).Msg("Context error")
		if ctxErr == context.Canceled {
			return
		}
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusRequestTimeout)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrClient) {
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrNotFound) {
			status = http.StatusNotFound
		}
		logger.Err(err).Str("requestID", reqID).Msg("Error")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, status)
		return
	}

	w.Header().Set("content-length", strconv.Itoa(len(buf.Bytes())))
	w.Header().Set("content-type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Fail to write the response back to the client")
	}
}

func (h handler) metadata(w http.ResponseWriter, r *http.Request) {
	reqID := chiMiddleware.GetReqID(r.Context())
	logger, err := h.traceExtractor(r.Context(), h.logger)
	if err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Could not extract tracing id")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/documents/")
	fileName, pageCount, err := h.documentService.Metadata(r.Context(), h.urlToVerify(r), path)
	if ctxErr := r.Context().Err(); ctxErr != nil {
		logger.Err(ctxErr).Str("requestID", reqID).Msg("Context error")
		if ctxErr == context.Canceled {
			return
		}
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusRequestTimeout)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrClient) {
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrNotFound) {
			status = http.StatusNotFound
		}
		logger.Err(err).Str("requestID", reqID).Msg("Error")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, status)
		return
	}
	result := map[string]interface{}{
		"Filename":  fileName,
		"PageCount": pageCount,
	}
	h.writer.response(r.Context(), w, result, http.StatusOK, "application/json")
}

// render backs the internal POST /render endpoint used by the SWS-direct envelopes flow. Unlike the
// signed GET /documents/* path, it takes the S3 location, render params and annotations from the
// JSON request body — no URL signature and no Redis lookup. It is intended to be reachable only
// in-cluster (not via the public keyless Tyk raster route); that exposure boundary is enforced by
// infra (Tyk route + NetworkPolicy), not here.
func (h handler) render(w http.ResponseWriter, r *http.Request) {
	reqID := chiMiddleware.GetReqID(r.Context())
	logger, err := h.traceExtractor(r.Context(), h.logger)
	if err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Could not extract tracing id")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusInternalServerError)
		return
	}

	var req struct {
		Path        string          `json:"path"`
		Page        int             `json:"page"`
		Width       int             `json:"width"`
		DPI         int             `json:"dpi"`
		Scale       float64         `json:"scale"`
		Format      string          `json:"format"`
		Version     string          `json:"version"`
		Annotations json.RawMessage `json:"annotations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Invalid render request body")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		logger.Error().Str("requestID", reqID).Msg("Missing 'path' in render request")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}

	annotations, err := domain.ParseAnnotations(req.Annotations)
	if err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Invalid annotations")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}

	var contentType string
	switch req.Format {
	case formatPNG:
		contentType = contentTypePNG
	case formatHTML:
		contentType = contentTypeHTML
	case "":
		contentType = contentTypePNG
		req.Format = formatPNG
	default:
		logger.Error().Str("requestID", reqID).Msg("Invalid 'format' parameter")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusBadRequest)
		return
	}

	result, err := h.documentService.Render(r.Context(), service.RenderRequest{
		Path:        req.Path,
		Page:        req.Page,
		Width:       req.Width,
		Scale:       float32(req.Scale),
		DPI:         req.DPI,
		Format:      req.Format,
		Version:     req.Version,
		Annotations: annotations,
	})
	if ctxErr := r.Context().Err(); ctxErr != nil {
		logger.Err(ctxErr).Str("requestID", reqID).Msg("Context error")
		if ctxErr == context.Canceled {
			return
		}
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, http.StatusRequestTimeout)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrClient) {
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrNotFound) {
			status = http.StatusNotFound
		}
		logger.Err(err).Str("requestID", reqID).Msg("Error")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, status)
		return
	}
	defer result.Body.Close()

	// The page is copied straight through: on a cache hit that streams it from S3 to the caller without
	// the render ever being buffered here. The status is committed only once the render has succeeded, so
	// a failure still answers with an error rather than a truncated image.
	if result.Size >= 0 {
		w.Header().Set("content-length", strconv.FormatInt(result.Size, 10))
	}
	w.Header().Set("content-type", contentType)
	w.Header().Set(headerPageCache, pageCacheStatus(result.Cached))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, result.Body); err != nil {
		logger.Err(err).Str("requestID", reqID).Msg("Fail to write the response back to the client")
	}
}

func pageCacheStatus(cached bool) string {
	if cached {
		return pageCacheHit
	}
	return pageCacheMiss
}

// Remove all the parameters, but the token and page, from the path. Other parameters can then be passed to the service
// without making the url signature invalid.
func (handler) urlToVerify(r *http.Request) string {
	q := r.URL.Query()
	for key := range q {
		if slices.Contains([]string{"page", "token", "token-ttl"}, key) {
			continue
		}
		q.Del(key)
	}
	r.URL.RawQuery = q.Encode()
	return r.URL.String()
}
