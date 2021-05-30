package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

type handlerDocumentService interface {
	Process(context.Context, string, string, string, int, int, float32, io.Writer) error
	Metadata(context.Context, string, string, string) (string, int, error)
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
	h.writer.response(r.Context(), w, map[string]interface{}{"status": "healthy"}, http.StatusOK)
}

func (h handler) document(w http.ResponseWriter, r *http.Request) {
	rawPage := r.URL.Query().Get("page")
	if rawPage == "" {
		h.metadata(w, r)
		return
	}

	page, err := strconv.Atoi(rawPage)
	if err != nil {
		h.writer.error(r.Context(), w, "Invalid 'page' parameter", err, http.StatusBadRequest)
		return
	}

	var width int
	rawWidth := r.URL.Query().Get("width")
	if rawWidth != "" {
		width, err = strconv.Atoi(rawWidth)
		if err != nil {
			h.writer.error(r.Context(), w, "Invalid 'width' parameter", err, http.StatusBadRequest)
			return
		}
	}

	var scale float64
	rawScale := r.URL.Query().Get("scale")
	if rawScale != "" {
		scale, err = strconv.ParseFloat(rawScale, 32)
		if err != nil {
			h.writer.error(r.Context(), w, "Invalid 'scale' parameter", err, http.StatusBadRequest)
			return
		}
	}

	token := r.URL.Query().Get("token")
	path := strings.TrimPrefix(r.URL.Path, "/documents/")
	err = h.documentService.Process(r.Context(), r.URL.String(), token, path, page, width, float32(scale), w)
	if ctxErr := r.Context().Err(); ctxErr != nil {
		if ctxErr == context.Canceled {
			return
		}
		h.writer.error(r.Context(), w, "Timeout processing the request", nil, http.StatusInternalServerError)
		return
	}
	if err != nil {
		reqID := chiMiddleware.GetReqID(r.Context())
		h.logger.Err(err).Str("requestID", reqID).Msg("Internal server error")
		h.writer.error(
			r.Context(), w, "Fail to process the document", fmt.Errorf("errorID: %s", reqID), http.StatusInternalServerError,
		)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h handler) metadata(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	path := strings.TrimPrefix(r.URL.Path, "/documents/")
	fileName, pageCount, err := h.documentService.Metadata(r.Context(), r.URL.String(), token, path)
	if ctxErr := r.Context().Err(); ctxErr != nil {
		if ctxErr == context.Canceled {
			return
		}
		h.writer.error(r.Context(), w, "Timeout processing the request", nil, http.StatusInternalServerError)
		return
	}
	if err != nil {
		reqID := chiMiddleware.GetReqID(r.Context())
		h.logger.Err(err).Str("requestID", reqID).Msg("Internal server error")
		h.writer.error(
			r.Context(), w, "Fail to process the document", fmt.Errorf("errorID: %s", reqID), http.StatusInternalServerError,
		)
		return
	}
	h.writer.response(r.Context(), w, fmt.Sprintf(`{"Filename":"%s","PageCount":%d}`, fileName, pageCount), http.StatusOK)
}
