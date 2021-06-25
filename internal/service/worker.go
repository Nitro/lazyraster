package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nitro/urlsign"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/google/uuid"
	"github.com/nitro/lazypdf/v2"
	"github.com/rs/zerolog"
	awstrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go/aws"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type workerStorage interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, payload io.Reader) error
}

// Worker used to fetch and process PDF files.
type Worker struct {
	HTTPClient          *http.Client
	URLSigningSecret    string
	Storage             workerStorage
	Logger              zerolog.Logger
	TraceExtractor      func(context.Context, zerolog.Logger) (zerolog.Logger, error)
	StorageBucketRegion map[string]string

	getS3Client func(string) (s3iface.S3API, error)
	s3Clients   map[string]s3iface.S3API
	mutex       sync.Mutex
}

// Init worker internal state.
func (w *Worker) Init() error {
	if w.HTTPClient == nil {
		return errors.New("internal/service/Worker.HTTPClient can't be nil")
	}
	if w.URLSigningSecret == "" {
		return errors.New("internal/service/Worker.URLSigningSecret can't be empty")
	}
	if w.Storage == nil {
		return errors.New("internal/service/Worker.Storage can't be nil")
	}
	if w.TraceExtractor == nil {
		return errors.New("internal/service/Worker.TraceExtractor can't be nil")
	}
	if len(w.StorageBucketRegion) == 0 {
		return errors.New("internal/service/Worker.StorageBucketRegion can't be empty")
	}
	if w.getS3Client == nil {
		w.getS3Client = w.getBucketS3Client
	}
	w.s3Clients = make(map[string]s3iface.S3API)
	return nil
}

func (w *Worker) Process(
	ctx context.Context, url, path string, page int, width int, scale float32, output io.Writer,
) (err error) {
	span, ctx := w.startSpan(ctx, "Worker.Process")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	// This change is required because of historical reasons. The first page for the frontend is 1 and not zero.
	page--

	if page < 0 {
		return newClientError(errors.New("invalid page"))
	}

	if width < 0 {
		return newClientError(errors.New("invalid width"))
	} else if width > 4096 {
		return newClientError(errors.New("invalid width, can't be bigger than 4096"))
	}

	if scale < 0 {
		return newClientError(errors.New("invalid scale"))
	} else if scale > 3 {
		return newClientError(errors.New("invalid scale, can't be bigger than 3"))
	}

	if !urlsign.IsValidSignature(w.URLSigningSecret, 8*time.Hour, time.Now(), url) {
		return newClientError(errors.New("invalid token"))
	}

	payload, err := w.fetchFile(ctx, path)
	if err != nil {
		return fmt.Errorf("fail to fetch the file: %w", err)
	}

	hash, err := w.generateHash(payload, []string{
		strconv.Itoa(page),
		strconv.Itoa(width),
		strconv.FormatFloat(float64(scale), 'f', 5, 32),
	})
	if err != nil {
		return fmt.Errorf("fail to generate the hash: %w", err)
	}

	result, err := w.Storage.Get(ctx, hash)
	if err != nil {
		return fmt.Errorf("fail to fetch the object from the storage: %w", err)
	}
	if result == nil {
		storage := bytes.NewBuffer([]byte{})
		err := lazypdf.SaveToPNG(ctx, uint16(page), uint16(width), scale, bytes.NewBuffer(payload), storage)
		if err != nil {
			return fmt.Errorf("fail to extract the PNG from the PDF: %w", err)
		}
		storageBytes := storage.Bytes()
		result = io.NopCloser(storage)

		baseSpan, ok := ddTracer.SpanFromContext(ctx)
		if !ok {
			return fmt.Errorf("fail to get span from context: %w", err)
		}

		storageSpan, storageCtx := ddTracer.StartSpanFromContext(
			context.Background(), "Worker.Storage.Put", ddTracer.ChildOf(baseSpan.Context()),
		)
		go func() {
			var err error
			defer func() { storageSpan.Finish(ddTracer.WithError(err)) }()

			if err = w.Storage.Put(storageCtx, hash, bytes.NewBuffer(storageBytes)); err != nil {
				logger, nerr := w.TraceExtractor(storageCtx, w.Logger)
				if nerr != nil {
					logger.Err(nerr).Msg("Fail to extract the trace ID from the context")
				}

				err = fmt.Errorf("fail to put the object into the storage: %w", err)
				logger.Err(err).Msg("Storage put error")
			}
		}()
	}
	defer result.Close()

	if _, err := io.Copy(output, result); err != nil {
		return fmt.Errorf("fail write the result to the output: %w", err)
	}
	return nil
}

// Metadata is used to fetch the document metadata.
func (w *Worker) Metadata(ctx context.Context, url, path string) (_ string, _ int, err error) {
	span, ctx := w.startSpan(ctx, "Worker.Metadata")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	if !urlsign.IsValidSignature(w.URLSigningSecret, 8*time.Hour, time.Now(), url) {
		return "", 0, newClientError(errors.New("invalid token"))
	}

	payload, err := w.fetchFile(ctx, path)
	if err != nil {
		return "", 0, fmt.Errorf("fail to fetch the file: %w", err)
	}

	pageCount, err := lazypdf.PageCount(ctx, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("fail to count the file pages: %w", err)
	}

	return w.generateFilename(), pageCount, nil
}

func (w *Worker) fetchFile(ctx context.Context, path string) (_ []byte, err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.fetchFile")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	if strings.HasPrefix(path, "dropbox/") {
		return w.fetchFileFromDropbox(ctx, path)
	}

	fragments := strings.Split(path, "/")
	if len(fragments) < 2 {
		return nil, newClientError(errors.New("invalid path"))
	}
	bucket := fragments[0]

	s3Client, err := w.getS3Client(bucket)
	if err != nil {
		return nil, fmt.Errorf("fail to get the s3 bucket client: %w", err)
	}

	output, err := s3Client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    aws.String(strings.Join(fragments[1:], "/")),
	})
	if err != nil {
		return nil, fmt.Errorf("fail to get object: %w", err)
	}
	defer output.Body.Close()

	payload, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("fail to read the reader: %w", err)
	}
	span.SetTag("fileSize", len(payload))

	return payload, nil
}

func (w *Worker) fetchFileFromDropbox(ctx context.Context, path string) (_ []byte, err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.fetchFileFromDropbox")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	fileURL, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(path, "dropbox/"))
	if err != nil {
		return nil, newClientError(fmt.Errorf("fail to decode base64 path: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(fileURL), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to create the HTTP request: %w", err)
	}

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fail to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("invalid status code '%d'", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fail to read the body response: %w", err)
	}

	return payload, nil
}

func (*Worker) generateHash(payload []byte, parameters []string) (string, error) {
	h := sha256.New()
	if _, err := h.Write(payload); err != nil {
		return "", fmt.Errorf("fail to write the payload to the hash function: %w", err)
	}

	var sb strings.Builder
	for _, parameter := range parameters {
		if _, err := sb.WriteString(parameter); err != nil {
			return "", fmt.Errorf("fail to write the parameters to the string builder: %w", err)
		}
	}

	if _, err := io.WriteString(h, sb.String()); err != nil {
		return "", fmt.Errorf("fail to write the parameters to the hash function: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (*Worker) generateFilename() string {
	id := uuid.New()
	return id.String() + "/document.pdf"
}

func (*Worker) startSpan(ctx context.Context, operation string) (ddtrace.Span, context.Context) {
	return ddTracer.StartSpanFromContext(ctx, "internal/service/"+operation)
}

func (w *Worker) getBucketS3Client(bucket string) (s3iface.S3API, error) {
	region, ok := w.StorageBucketRegion[bucket]
	if !ok {
		return nil, fmt.Errorf("can't find the bucket '%s' region", bucket)
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()

	client, ok := w.s3Clients[region]
	if ok {
		return client, nil
	}

	sess, err := session.NewSession(&aws.Config{HTTPClient: w.HTTPClient, Region: &region})
	if err != nil {
		return nil, fmt.Errorf("fail to start a session on region '%s': %w", region, err)
	}
	sess = awstrace.WrapSession(sess)

	client = s3.New(sess, &aws.Config{HTTPClient: w.HTTPClient})
	w.s3Clients[region] = client
	return client, nil
}