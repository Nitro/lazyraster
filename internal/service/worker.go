package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Nitro/urlsign"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/google/uuid"
	"github.com/nitro/lazypdf/v2"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
	awstrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go/aws"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"github.com/nitro/lazyraster/v2/internal/domain"
)

type workerAnnotationStorage interface {
	FetchAnnotation(context.Context, string) ([]any, error)
}

// Worker used to fetch and process PDF files.
type Worker struct {
	HTTPClient          *http.Client
	URLSigningSecret    string
	Logger              zerolog.Logger
	TraceExtractor      func(context.Context, zerolog.Logger) (zerolog.Logger, error)
	StorageBucketRegion map[string]string
	AnnotationStorage   workerAnnotationStorage

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
	ctx context.Context, url, path string, page int, width int, scale float32, dpi int, output io.Writer, format string,
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

	if dpi > 600 {
		return newClientError(errors.New("invalid dpi, can't  be bigger than 600"))
	}

	if !urlsign.IsValidSignature(w.URLSigningSecret, 8*time.Hour, time.Now(), url) {
		return newClientError(errors.New("invalid token"))
	}

	// Fetch the file in a goroutine to allow the annotations to be processed while the payload is being fetch.
	chanPayload := make(chan []byte)
	chanError := make(chan error)
	go func() {
		payload, err := w.fetchFile(ctx, path)
		if err != nil {
			chanError <- fmt.Errorf("fail to fetch the file: %w", err)
			return
		}

		if len(payload) == 0 {
			chanError <- fmt.Errorf("empty payload")
			return
		}

		chanPayload <- payload
	}()

	storage := bytes.NewBuffer([]byte{})
	switch format {
	case "png":
		token, err := w.extractToken(url)
		if err != nil {
			return fmt.Errorf("failed to extract the token: %w", err)
		}

		annotations, annotationsCleanup, err := w.fetchAnnotations(ctx, token, page)
		if err != nil {
			return fmt.Errorf("failed to fetch the annotations: %w", err)
		}
		defer annotationsCleanup()

		var rawPayload []byte
		select {
		case err := <-chanError:
			return err
		case rawPayload = <-chanPayload:
		}

		if len(annotations) > 0 {
			//nolint:gosec,G115
			err := w.SaveToPNGWithAnnotations(
				ctx, uint16(page), uint16(width), scale, dpi,
				bytes.NewBuffer(rawPayload), storage, annotations,
			)
			if err != nil {
				return fmt.Errorf("failed to process annotations and generate PNG: %w", err)
			}
		} else {
			//nolint:gosec,G115
			err = lazypdf.SaveToPNG(ctx, uint16(page), uint16(width), scale, dpi, bytes.NewBuffer(rawPayload), storage)
			if err != nil {
				return fmt.Errorf("fail to extract the PNG from the PDF: %w", err)
			}
		}
	case "html":
		var rawPayload []byte
		select {
		case err := <-chanError:
			return err
		case rawPayload = <-chanPayload:
		}
		//nolint:gosec,G115
		err = lazypdf.SaveToHTML(ctx, uint16(page), uint16(width), scale, dpi, bytes.NewBuffer(rawPayload), storage)
		if err != nil {
			return fmt.Errorf("fail to render the PDF page to HTML: %w", err)
		}
	default:
		return fmt.Errorf("unknown format '%s'", format)
	}
	result := io.NopCloser(storage)
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

	var bucket, filePath string
	switch {
	case strings.HasPrefix(path, "s3://"):
		path = strings.TrimPrefix(path, "s3://")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid S3 path '%s'", path)
		}
		bucket = parts[0]
		filePath = parts[1]
	case strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://"):
		return w.fetchFileFromInternet(ctx, path)
	default:
		fragments := strings.Split(path, "/")
		if len(fragments) < 2 {
			return nil, newClientError(errors.New("invalid path"))
		}
		bucket = fragments[0]
		filePath = strings.Join(fragments[1:], "/")
	}

	s3Client, err := w.getS3Client(bucket)
	if err != nil {
		return nil, fmt.Errorf("fail to get the s3 bucket client: %w", err)
	}

	output, err := s3Client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &filePath,
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && (awsErr.Code() == s3.ErrCodeNoSuchKey) {
			return nil, newNotFoundError(err)
		}
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

	if resp.StatusCode == http.StatusNotFound {
		return nil, newNotFoundError(errors.New("dropbox returned 404"))
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("invalid status code '%d'", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fail to read the body response: %w", err)
	}

	return payload, nil
}

func (w *Worker) fetchFileFromInternet(ctx context.Context, uri string) (_ []byte, err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.fetchFileFromInternet")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create a HTTP request: %w", err)
	}

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fail to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, newNotFoundError(errors.New("server returned 404"))
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("invalid status code '%d'", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fail to read the body response: %w", err)
	}

	return payload, nil
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

func (w *Worker) extractToken(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse the endpoint: %w", err)
	}

	token := u.Query().Get("token")
	if token == "" {
		return "", errors.New("token not found")
	}

	return token, nil
}

// fetchAnnotations is used to get the annotations based on a token and preprocess them. The second return parameter is
// a cleanup function that always need to be executed once the information is no longer needed. The cleanup function is
// only available in case there is no errors.
func (w *Worker) fetchAnnotations(
	ctx context.Context, token string, page int,
) (annotations []any, cleanup func(), err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.fetchAnnotations")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	annotations = make([]any, 0)
	originalAnnotations, err := w.AnnotationStorage.FetchAnnotation(ctx, token)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch the annotations: %w", err)
	}

	var temporaryAnnotationFilesMutex sync.Mutex
	temporaryAnnotationFiles := make([]string, 0)
	g, gctx := errgroup.WithContext(ctx)
	for _, annotation := range originalAnnotations {
		//nolint:gocritic
		switch v := annotation.(type) {
		case domain.AnnotationText:
			if v.Page != page+1 {
				continue
			}
			annotations = append(annotations, v)
		case domain.AnnotationCheckbox:
			if v.Page != page+1 {
				continue
			}
			annotations = append(annotations, v)
		case domain.AnnotationImage:
			if v.Page != page+1 {
				continue
			}
			imgIdx := len(annotations)
			annotations = append(annotations, v)
			g.Go(func() error {
				// Fetch the file from the internet.
				payload, err := w.fetchFile(gctx, v.ImageLocation)
				if err != nil {
					return fmt.Errorf("failed to fetch the image: %w", err)
				}

				// Once we have the image in memory it needs to be dumped into a file because this is how the C layer at lazypdf
				// can consume it.
				tmpFile, err := os.CreateTemp("", uuid.New().String())
				if err != nil {
					return fmt.Errorf("failed to create a temporary file: %w", err)
				}
				defer tmpFile.Close()

				// Save the temporary file on an array to cleanup later.
				temporaryAnnotationFilesMutex.Lock()
				temporaryAnnotationFiles = append(temporaryAnnotationFiles, tmpFile.Name())
				temporaryAnnotationFilesMutex.Unlock()

				// Get the payload from S3 and send it to the temporary file.
				if _, err := tmpFile.Write(payload); err != nil {
					return fmt.Errorf("failed to write to the temporary file: %w", err)
				}

				// Update the image location to the disk copy.
				v.ImageLocation = tmpFile.Name()
				annotations[imgIdx] = v
				return nil
			})
		}
	}

	cleanup = func() {
		for _, entry := range temporaryAnnotationFiles {
			go func() {
				os.Remove(entry)
			}()
		}
	}

	if err := g.Wait(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to preprocess the annotations: %w", err)
	}

	return annotations, cleanup, nil
}

func (w *Worker) SaveToPNGWithAnnotations(
	ctx context.Context, page uint16, width uint16, scale float32, dpi int,
	payload io.Reader, storage io.Writer, annotations []any,
) (err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.SaveToPNGWithAnnotations")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	ph := lazypdf.NewPdfHandler(ctx, nil)

	doc, err := ph.OpenPDF(payload)
	if err != nil {
		return fmt.Errorf("failed to open the PDF: %w", err)
	}
	defer func() {
		if err := ph.ClosePDF(doc); err != nil {
			w.Logger.Err(err).Msg("Failed to close the PDF")
		}
	}()

	for _, annotation := range annotations {
		var err error
		switch v := annotation.(type) {
		case domain.AnnotationCheckbox:
			params := lazypdf.CheckboxParams{
				Value: v.Value,
				Page:  v.Page - 1,
				Location: lazypdf.Location{
					X: v.Location.X,
					Y: v.Location.Y,
				},
				Size: lazypdf.Size{
					Width:  v.Size.Width,
					Height: v.Size.Height,
				},
			}
			err = ph.AddCheckboxToPage(doc, params)
		case domain.AnnotationImage:
			params := lazypdf.ImageParams{
				Page: v.Page - 1,
				Location: lazypdf.Location{
					X: v.Location.X,
					Y: v.Location.Y,
				},
				Size: lazypdf.Size{
					Width:  v.Size.Width,
					Height: v.Size.Height,
				},
				ImagePath: v.ImageLocation,
			}
			err = ph.AddImageToPage(doc, params)
		case domain.AnnotationText:
			params := lazypdf.TextParams{
				Value: v.Value,
				Page:  v.Page - 1,
				Location: lazypdf.Location{
					X: v.Location.X,
					Y: v.Location.Y,
				},
				Font: struct {
					Family string
					Size   float64
				}{
					Family: v.Font.Family,
					Size:   v.Font.Size,
				},
				Size: lazypdf.Size{
					Width:  v.Size.Width,
					Height: v.Size.Height,
				},
			}
			err = ph.AddTextBoxToPage(doc, params)
		default:
			return fmt.Errorf("annotation type '%T' not supported", annotation)
		}
		if err != nil {
			return fmt.Errorf("failed to add an annotation to the PDF: %w", err)
		}
	}

	err = ph.SaveToPNG(doc, page, width, scale, dpi, storage)
	if err != nil {
		return fmt.Errorf("failed to add an annotation to the PDF: %w", err)
	}
	return nil
}
