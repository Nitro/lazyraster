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

	"github.com/nitro/lazyraster/v2/internal/service"
	"github.com/nitro/lazyraster/v2/internal/transport"
)

// Client holds the logic to bootstrap the application.
type Client struct {
	Logger              zerolog.Logger
	AsyncErrorHandler   func(error)
	CacheBucket         string
	CacheSecret         string
	URLSigningSecret    string
	EnableDatadog       bool
	StorageBucketRegion map[string]string

	server        transport.Server
	serviceCipher service.Cipher
	serviceCache  service.Cache
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
	}

	c.serviceCache.HTTPClient = httpClient
	c.serviceCache.Bucket = c.CacheBucket
	if err := c.serviceCache.Init(); err != nil {
		return fmt.Errorf("fail to initialize service cache: %w", err)
	}

	c.serviceCipher.Key = c.CacheSecret
	c.serviceCipher.Storage = c.serviceCache
	if err := c.serviceCipher.Init(); err != nil {
		return fmt.Errorf("fail to initialize service cipher: %w", err)
	}

	c.serviceWorker.URLSigningSecret = c.URLSigningSecret
	c.serviceWorker.HTTPClient = httpClient
	c.serviceWorker.Storage = c.serviceCipher
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
