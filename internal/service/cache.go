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
		return errors.New("Worker.HTTPClient can't be nil")
	}
	if c.Bucket == "" {
		return errors.New("Cache.Bucket can't be empty")
	}
	sess, err := session.NewSession()
	if err != nil {
		return fmt.Errorf("fail to start a session: %w", err)
	}
	sess = awstrace.WrapSession(sess)
	c.svc = s3.New(sess, &aws.Config{HTTPClient: c.HTTPClient})
	return nil
}

// Get object.
func (c Cache) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Cache.Get")
	defer span.Finish()

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
func (c Cache) Put(ctx context.Context, key string, rawPayload io.Reader) error {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Cache.Put")
	defer span.Finish()

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
