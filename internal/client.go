package internal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	ddHTTP "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"

	"github.com/nitro/lazyraster/v2/internal/service"
	"github.com/nitro/lazyraster/v2/internal/transport"
)

// Client holds the logic to bootstrap the application.
type Client struct {
	Logger              zerolog.Logger
	AsyncErrorHandler   func(error)
	URLSigningSecret    string
	EnableDatadog       bool
	StorageBucketRegion map[string]string

	server        transport.Server
	serviceWorker service.Worker
}

// Init the client internal state.
func (c *Client) Init() (err error) {
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	httpClient = ddHTTP.WrapClient(httpClient)

	if c.EnableDatadog {
		tracer.Start(
			tracer.WithHTTPClient(httpClient),
			tracer.WithLogger(datadogLogger{logger: c.Logger}),
			tracer.WithRuntimeMetrics(),
		)
		defer func() {
			if err != nil {
				tracer.Stop()
			}
		}()

		err = profiler.Start(
			profiler.WithProfileTypes(
				profiler.CPUProfile,
				profiler.HeapProfile,
			),
		)
		if err != nil {
			return fmt.Errorf("failed to start datadog profiler: %w", err)
		}
		defer func() {
			if err != nil {
				profiler.Stop()
			}
		}()
	}

	c.serviceWorker.URLSigningSecret = c.URLSigningSecret
	c.serviceWorker.HTTPClient = httpClient
	c.serviceWorker.Logger = c.Logger
	c.serviceWorker.TraceExtractor = traceLogger(c.EnableDatadog)
	c.serviceWorker.StorageBucketRegion = c.StorageBucketRegion
	if err := c.serviceWorker.Init(); err != nil {
		return fmt.Errorf("fail to initialize service worker: %w", err)
	}

	c.server.Logger = c.Logger
	c.server.AsyncErrorHandler = c.AsyncErrorHandler
	c.server.TraceExtractor = traceLogger(c.EnableDatadog)
	c.server.DocumentService = &c.serviceWorker
	if err := c.server.Init(); err != nil {
		return fmt.Errorf("fail to initialize the transport server: %w", err)
	}

	return nil
}

// Start the client.
func (c *Client) Start() {
	c.server.Start()
}

// Stop the client.
func (c *Client) Stop(ctx context.Context) error {
	defer tracer.Stop()
	if err := c.server.Stop(ctx); err != nil {
		return fmt.Errorf("fail to stop the server")
	}
	return nil
}
