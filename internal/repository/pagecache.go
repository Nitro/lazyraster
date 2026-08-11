package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rs/zerolog"
	awsv2trace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go-v2/aws"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

const (
	// pageCacheUploadWorkers and pageCacheQueueSize bound what the write-back can cost us. The queue is
	// the only place rendered pages are retained beyond their request, so it is also the cache's entire
	// memory footprint: at a few hundred KB per page this caps out around low tens of MB, unlike an
	// in-process page or document cache whose whole purpose is to hold bytes.
	pageCacheUploadWorkers = 4
	pageCacheQueueSize     = 64

	// pageCacheUploadTimeout bounds one write-back. It runs after the response has been sent, so a slow
	// upload costs nothing but a queue slot.
	pageCacheUploadTimeout = 10 * time.Second

	// pageCacheProbeTimeout bounds the best-effort startup reachability check.
	pageCacheProbeTimeout = 5 * time.Second
)

type pageCacheS3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

// PageCacheConfig configures the S3-backed rendered-page cache.
type PageCacheConfig struct {
	// Bucket holds the rendered pages. Entries are expired by a bucket lifecycle rule rather than by
	// this service, so the bucket MUST have one configured (see the README): nothing here deletes.
	Bucket string
	// Region is where Bucket lives; it should be the region this instance runs in.
	Region string
	// Prefix is prepended to every object key. Optional.
	Prefix string
	Logger zerolog.Logger
	// HTTPClient is shared with the rest of the service so the cache inherits its timeouts and tracing.
	HTTPClient *http.Client
}

// S3PageCache stores rendered pages in S3, keyed by a content-addressed hash of the render inputs.
//
// S3 rather than Redis or process memory: a page is a few hundred KB, the shared ElastiCache cluster is
// a cache.t4g.micro, and this service already runs close to its memory limit. Reads stream straight out
// of S3 to the client, and writes happen off the request path, so a cached page costs the service
// neither a document fetch, a rasterisation, nor any retained heap.
type S3PageCache struct {
	client pageCacheS3API
	bucket string
	prefix string
	logger zerolog.Logger

	queue    chan pageCacheEntry
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

type pageCacheEntry struct {
	key     string
	payload []byte
}

// NewS3PageCache builds the cache and starts its write-back workers.
//
// Startup never fails on a cache problem: an unreachable or misconfigured bucket is reported as a
// warning and the service runs with every render performed, exactly as it did before the cache existed.
func NewS3PageCache(ctx context.Context, config PageCacheConfig) (*S3PageCache, error) {
	if config.Bucket == "" {
		return nil, errors.New("internal/repository.PageCacheConfig.Bucket can't be empty")
	}
	if config.Region == "" {
		return nil, errors.New("internal/repository.PageCacheConfig.Region can't be empty")
	}
	if config.HTTPClient == nil {
		return nil, errors.New("internal/repository.PageCacheConfig.HTTPClient can't be nil")
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(config.Region),
		awsconfig.WithHTTPClient(config.HTTPClient),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to load the AWS configuration for region '%s': %w", config.Region, err)
	}
	awsv2trace.AppendMiddleware(&awsConfig)

	client := s3.NewFromConfig(awsConfig, func(o *s3.Options) {
		o.HTTPClient = config.HTTPClient
	})

	cache := newS3PageCache(client, config)

	probeCtx, probeCancel := context.WithTimeout(ctx, pageCacheProbeTimeout)
	defer probeCancel()
	if _, err := client.HeadBucket(probeCtx, &s3.HeadBucketInput{Bucket: aws.String(config.Bucket)}); err != nil {
		cache.logger.Warn().Err(err).Str("bucket", config.Bucket).
			Msg("Page cache bucket is not reachable, renders will not be cached until it is")
	}

	return cache, nil
}

func newS3PageCache(client pageCacheS3API, config PageCacheConfig) *S3PageCache {
	cache := &S3PageCache{
		client: client,
		bucket: config.Bucket,
		prefix: config.Prefix,
		logger: config.Logger,
		queue:  make(chan pageCacheEntry, pageCacheQueueSize),
		stop:   make(chan struct{}),
	}

	cache.wg.Add(pageCacheUploadWorkers)
	for range pageCacheUploadWorkers {
		go cache.uploadLoop()
	}

	return cache
}

// Get returns the cached page for key. A miss is a nil reader with a nil error; the caller closes the
// reader it gets back.
func (c *S3PageCache) Get(ctx context.Context, key string) (_ io.ReadCloser, _ int64, err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "internal/repository/S3PageCache.Get")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	output, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(c.objectKey(key)),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		var notFound *types.NotFound
		if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
			span.SetTag("pageCache.hit", false)
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("fail to get the cached page '%s': %w", key, err)
	}

	size := int64(-1)
	if output.ContentLength != nil {
		size = *output.ContentLength
	}
	span.SetTag("pageCache.hit", true)
	span.SetTag("pageCache.size", size)

	return output.Body, size, nil
}

// Put queues a rendered page for upload and returns immediately.
//
// The upload is deliberately not part of the request: the response has already been written by the time
// it runs. When the queue is full the page is dropped rather than queued -- shedding a cache write is
// always cheaper than growing an unbounded backlog of retained page bytes.
func (c *S3PageCache) Put(key string, payload []byte) {
	select {
	case <-c.stop:
		return
	default:
	}

	select {
	case c.queue <- pageCacheEntry{key: key, payload: payload}:
	default:
		c.logger.Debug().Str("pageCacheKey", key).Msg("Page cache upload queue is full, dropping the write")
	}
}

// Close stops accepting writes, drains the pages already queued and waits for the in-flight uploads. It
// must be called after the HTTP server has shut down, so no request can still be calling Put.
func (c *S3PageCache) Close(ctx context.Context) error {
	c.stopOnce.Do(func() { close(c.stop) })

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("fail to drain the page cache upload queue: %w", ctx.Err())
	}
}

func (c *S3PageCache) uploadLoop() {
	defer c.wg.Done()

	for {
		select {
		case entry := <-c.queue:
			c.upload(entry)
		case <-c.stop:
			// Drain what is already queued: those pages have been rendered and paid for, and the queue is
			// bounded, so finishing it costs at most a few uploads inside the shutdown budget.
			for {
				select {
				case entry := <-c.queue:
					c.upload(entry)
				default:
					return
				}
			}
		}
	}
}

func (c *S3PageCache) upload(entry pageCacheEntry) {
	// A fresh context, not the request's: the request that produced this page has already been answered,
	// so its context is cancelled and would fail every upload.
	ctx, cancel := context.WithTimeout(context.Background(), pageCacheUploadTimeout)
	defer cancel()

	span, ctx := ddTracer.StartSpanFromContext(ctx, "internal/repository/S3PageCache.upload")

	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(c.objectKey(entry.key)),
		Body:        bytes.NewReader(entry.payload),
		ContentType: aws.String(contentTypeFor(entry.key)),
	})
	span.Finish(ddTracer.WithError(err))
	if err != nil {
		c.logger.Warn().Err(err).Str("pageCacheKey", entry.key).Msg("Failed to write to the page cache")
	}
}

func (c *S3PageCache) objectKey(key string) string {
	if c.prefix == "" {
		return key
	}
	return path.Join(c.prefix, key)
}

// contentTypeFor keeps the stored object browsable in the console; the response content type is set by
// the transport layer from the request, not from here.
func contentTypeFor(key string) string {
	switch path.Ext(key) {
	case ".png":
		return "image/png"
	case ".html":
		return "text/html"
	default:
		return "application/octet-stream"
	}
}
