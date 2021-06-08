package transport

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type middleware struct {
	log            zerolog.Logger
	writer         writer
	traceExtractor traceExtractor
}

func (m middleware) recoverer(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil && rvr != http.ErrAbortHandler {
				m.writer.error(r.Context(), w, "Internal server error", nil, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

// limitReader don't prevent all cases of a way to big payload. The way this function works is by looking to the
// content-length header. This header may not be available or even wrong, this function it's just a initial layer of
// protection.
func (m middleware) limitReader(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			rawContentLength := r.Header.Get("Content-Length")
			if rawContentLength != "" {
				contentLength, err := strconv.ParseInt(rawContentLength, 10, 64)
				if err != nil {
					m.writer.error(r.Context(), w, "Fail to parse the header content-length", err, http.StatusBadRequest)
					return
				}
				if contentLength > limit {
					m.writer.error(r.Context(), w, "Request payload too large", nil, http.StatusRequestEntityTooLarge)
					return
				}
			}
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}

func (m middleware) logger(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		requestURI := r.RequestURI
		if token := r.URL.Query().Get("token"); token != "" {
			requestURI = strings.ReplaceAll(requestURI, token, "[REDACTED]")
		}
		if strings.HasPrefix(requestURI, "/documents/dropbox/") {
			requestURI = "/documents/dropbox/[REDACTED]"
		}

		log, err := m.traceExtractor(r.Context(), m.log)
		if err != nil {
			m.writer.error(r.Context(), w, "Could not extract tracing id", nil, http.StatusInternalServerError)
			return
		}

		t1 := time.Now()
		reqID := chiMiddleware.GetReqID(r.Context())
		entry := log.Info().
			Str("requestID", reqID).
			Str("method", r.Method).
			Str("endpoint", requestURI).
			Str("protocol", r.Proto)
		if r.RemoteAddr != "" {
			entry = entry.Str("ip", r.RemoteAddr)
		}
		entry.Msg("Request started")

		defer func() {
			if err := recover(); err != nil {
				log.Error().
					Str("requestID", reqID).
					Dur("duration", time.Since(t1)).
					Int("status", 500).
					Str("stacktrace", string(debug.Stack())).
					Msg("Request finished with panic")
				panic(err)
			}
		}()

		ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		responseBody := bytes.NewBuffer([]byte{})
		ww.Tee(responseBody)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		entry = log.Info().
			Err(r.Context().Err()).
			Str("requestID", reqID).
			Dur("duration", time.Since(t1)).
			Int("contentLength", ww.BytesWritten()).
			Int("status", status)

		if status < 200 || status >= 300 {
			entry = entry.Str("body", responseBody.String())
		}

		if status == http.StatusInternalServerError {
			entry.Str("stacktrace", string(debug.Stack())).Msg("Internal error during request")
		} else {
			entry.Msg("Request finished")
		}
	}

	return http.HandlerFunc(fn)
}

func (m middleware) datadogTracer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/documents/dropbox/") {
			path = "/documents/dropbox/[REDACTED]"
		}

		opts := []ddtrace.StartSpanOption{
			tracer.SpanType(ext.SpanTypeWeb),
			tracer.Tag(ext.HTTPMethod, r.Method),
			tracer.Tag(ext.HTTPURL, path),
			tracer.Measured(),
		}
		if spanctx, err := tracer.Extract(tracer.HTTPHeadersCarrier(r.Header)); err == nil {
			opts = append(opts, tracer.ChildOf(spanctx))
		}
		span, ctx := tracer.StartSpanFromContext(r.Context(), "http.request", opts...)
		defer span.Finish()

		ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		resourceName := chi.RouteContext(r.Context()).RoutePattern()
		if resourceName == "" {
			resourceName = "unknown"
		}
		resourceName = r.Method + " " + resourceName
		span.SetTag(ext.ResourceName, resourceName)

		status := ww.Status()
		if ww.Status() == 0 {
			status = http.StatusOK
		}
		span.SetTag(ext.HTTPCode, strconv.Itoa(status))

		if status >= 500 {
			span.SetTag(ext.Error, true)
			span.SetTag(ext.ErrorMsg, fmt.Errorf("%d: %s", status, http.StatusText(status)))
			span.SetTag(ext.ErrorType, "internal/service/transport")
			span.SetTag(ext.ErrorStack, string(debug.Stack()))
		}
	})
}
