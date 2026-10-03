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

	"github.com/nitro/lazyraster/v2/internal/repository"
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
	RedisURL            string
	RedisUsername       string
	RedisPassword       string
	redisDisabled       bool
	// PageCacheBucket enables the rendered-page cache when set; the other two are only read with it.
	PageCacheBucket string
	PageCacheRegion string
	PageCachePrefix string

	server        transport.Server
	serviceWorker service.Worker
	pageCache     *repository.S3PageCache
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

	if !c.redisDisabled {
		redisClient, err := repository.NewRedisClient(c.RedisURL, c.RedisUsername, c.RedisPassword)
		if err != nil {
			return fmt.Errorf("failed to create a redis client: %w", err)
		}
		c.serviceWorker.AnnotationStorage = redisClient
	}

	if c.PageCacheBucket != "" {
		pageCache, err := repository.NewS3PageCache(context.Background(), repository.PageCacheConfig{
			Bucket:     c.PageCacheBucket,
			Region:     c.PageCacheRegion,
			Prefix:     c.PageCachePrefix,
			Logger:     c.Logger,
			HTTPClient: httpClient,
		})
		if err != nil {
			return fmt.Errorf("failed to create the page cache: %w", err)
		}
		c.pageCache = pageCache
		c.serviceWorker.PageCache = pageCache
		c.Logger.Info().Str("bucket", c.PageCacheBucket).Msg("Rendered page cache enabled")
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
	// After the server, never before: draining the upload queue is only safe once no request can still be
	// producing page cache writes. A failure to drain loses cache entries and nothing else, so it is
	// logged rather than propagated into a non-zero exit.
	if c.pageCache != nil {
		if err := c.pageCache.Close(ctx); err != nil {
			c.Logger.Warn().Err(err).Msg("Failed to drain the page cache")
		}
	}
	return nil
}
