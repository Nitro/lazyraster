package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	awstrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go/aws"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

// Cache used to avoid re-processing files that were already processed.
type Cache struct {
	HTTPClient *http.Client
	Bucket     string
	svc        s3iface.S3API
}

// Init cache internal state.
func (c *Cache) Init() error {
	if c.HTTPClient == nil {
		return errors.New("internal/service/Cache.HTTPClient can't be nil")
	}
	if c.Bucket == "" {
		return errors.New("internal/service/Cache.Bucket can't be empty")
	}
	sess, err := session.NewSession(&aws.Config{HTTPClient: c.HTTPClient})
	if err != nil {
		return fmt.Errorf("fail to start a session: %w", err)
	}
	sess = awstrace.WrapSession(sess)
	c.svc = s3.New(sess, &aws.Config{HTTPClient: c.HTTPClient})
	return nil
}

// Get object.
func (c Cache) Get(ctx context.Context, key string) (_ io.ReadCloser, err error) {
	span, ctx := c.startSpan(ctx, "Cache.Get")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	object, err := c.svc.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: &c.Bucket,
		Key:    &key,
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && (awsErr.Code() == s3.ErrCodeNoSuchKey) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to fetch the object at the key '%s': %w", key, err)
	}
	return object.Body, nil
}

// Put a object at the cache layer.
func (c Cache) Put(ctx context.Context, key string, rawPayload io.Reader) (err error) {
	span, ctx := c.startSpan(ctx, "Cache.Put")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	payload, err := io.ReadAll(rawPayload)
	if err != nil {
		return fmt.Errorf("fail to read the payload: %w", err)
	}

	_, err = c.svc.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: &c.Bucket,
		Key:    &key,
		Body:   bytes.NewReader(payload),
	})
	if err != nil {
		return fmt.Errorf("fail to put the object at the key '%s': %w", key, err)
	}
	return nil
}

func (c Cache) startSpan(ctx context.Context, operation string) (ddtrace.Span, context.Context) {
	return startSpan(ctx, operation, ddTracer.SpanType(ext.AppTypeCache))
}
