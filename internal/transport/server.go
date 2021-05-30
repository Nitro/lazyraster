package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	chitrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/go-chi/chi.v5"
)

// Server is responsible for the transport layer of the API.
type Server struct {
	Logger            zerolog.Logger
	AsyncErrorHandler func(error)
	TraceExtractor    traceExtractor
	DocumentService   handlerDocumentService

	writer writer
	server http.Server
	router chi.Mux
}

// Init the server internal state.
func (s *Server) Init() error {
	if s.AsyncErrorHandler == nil {
		return errors.New("missing 'AsyncErrorHandler'")
	}
	if s.TraceExtractor == nil {
		return errors.New("missing TraceExtractor")
	}
	if s.DocumentService == nil {
		return errors.New("missing DocumentService")
	}
	return nil
}

// Start the server.
func (s *Server) Start() {
	s.router = *chi.NewRouter()
	s.writer.logger = s.Logger
	s.writer.traceExtractor = s.TraceExtractor
	s.initMiddleware()
	s.initHandler()

	// The HTTP server uses a static configuration. In the case that we need to change this setting in the future, we
	// could consider moving it to a configuration file.
	s.server = http.Server{
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 20 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxBodySize,
		Addr:              ":8080",
		Handler:           &s.router,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.AsyncErrorHandler(fmt.Errorf("fail to start the http server: %w", err))
		}
	}()
}

// Stop the server.
func (s *Server) Stop(ctx context.Context) error {
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("fail to close the http server: %w", err)
	}
	return nil
}

func (s *Server) initMiddleware() {
	m := middleware{log: s.Logger, writer: s.writer, traceExtractor: s.TraceExtractor}
	s.router.Use(m.recoverer)
	s.router.Use(chitrace.Middleware())
	s.router.Use(chiMiddleware.NoCache)
	s.router.Use(chiMiddleware.RealIP)
	s.router.Use(chiMiddleware.RequestID)
	s.router.Use(chiMiddleware.StripSlashes)
	s.router.Use(chiMiddleware.NewCompressor(5).Handler)
	s.router.Use(m.logger)
	s.router.Use(m.limitReader(maxBodySize))
}

func (s *Server) initHandler() {
	h := handler{
		writer:          s.writer,
		logger:          s.Logger,
		traceExtractor:  s.TraceExtractor,
		documentService: s.DocumentService,
	}

	s.router.MethodNotAllowed(h.methodNotAllowed)
	s.router.NotFound(h.notFound)
	s.router.Get("/health", h.health)
	s.router.Get("/documents/*", h.document)
}
