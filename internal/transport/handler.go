package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/nitro/lazyraster/v2/internal/service"
)

type handlerDocumentService interface {
	Process(context.Context, string, string, int, int, float32, io.Writer) error
	Metadata(context.Context, string, string) (string, int, error)
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

	path := strings.TrimPrefix(r.URL.Path, "/documents/")
	buf := bytes.NewBuffer([]byte{})
	err = h.documentService.Process(r.Context(), r.URL.String(), path, page, width, float32(scale), buf)
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
		}
		logger.Err(err).Str("requestID", reqID).Msg("Error")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, status)
		return
	}

	w.Header().Set("content-length", strconv.Itoa(len(buf.Bytes())))
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
	fileName, pageCount, err := h.documentService.Metadata(r.Context(), r.URL.String(), path)
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
		}
		logger.Err(err).Str("requestID", reqID).Msg("Error")
		h.writer.error(r.Context(), w, fmt.Sprintf("Request ID '%s'", reqID), nil, status)
		return
	}
	result := map[string]interface{}{
		"Filename":  fileName,
		"PageCount": pageCount,
	}
	h.writer.response(r.Context(), w, result, http.StatusOK)
}
