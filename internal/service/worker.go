package service

import (
	"bytes"
	"context"
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
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/nitro/lazypdf/v2"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	awsv2trace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go-v2/aws"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"github.com/nitro/lazyraster/v2/internal/domain"
)

type workerS3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type workerAnnotationStorage interface {
	FetchAnnotation(context.Context, string) ([]any, error)
}

// workerPageCache is the rendered-page cache behind the SWS-direct render path. A miss is reported as a
// nil reader and a nil error, mirroring workerAnnotationStorage.
//
// Put is asynchronous and bounded by the implementation: it takes no context because it must outlive the
// request whose render produced the payload, and it must never block the response on an upload. The
// payload is not copied, so implementations must treat it as read-only.
type workerPageCache interface {
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Put(key string, payload []byte)
}

// Worker used to fetch and process PDF files.
type Worker struct {
	HTTPClient          *http.Client
	URLSigningSecret    string
	Logger              zerolog.Logger
	TraceExtractor      func(context.Context, zerolog.Logger) (zerolog.Logger, error)
	StorageBucketRegion map[string]string
	AnnotationStorage   workerAnnotationStorage
	// PageCache is optional: nil disables the rendered-page cache and every render is performed.
	PageCache workerPageCache

	getS3Client func(string) (workerS3API, error)
	s3Clients   map[string]workerS3API
	mutex       sync.Mutex
	renderGroup singleflight.Group
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
	w.s3Clients = make(map[string]workerS3API)
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

	// Fetch the file in a goroutine to allow the annotations to be processed while the payload is being fetch. The
	// channels are buffered so the goroutine can always complete its single send and exit, even when Process returns
	// early (e.g. on a token or annotation error) before reaching the select that drains them. With unbuffered
	// channels the goroutine would block forever on the send, leaking both the goroutine and the payload it holds.
	chanPayload := make(chan []byte, 1)
	chanError := make(chan error, 1)
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
	case formatPNG:
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
	case formatHTML:
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

// Render renders a single PDF page using annotations supplied directly by the caller, without a URL
// signature and without consulting Redis. It backs the internal /render endpoint used by the new
// SWS-direct envelopes flow: annotations arrive in the request body instead of via the annotation
// store, so this path has no build-time/render-time coupling and no dependency on Redis.
//
// The render is served from the page cache when it can be: the request hashes to a content-addressed
// key, so a page already rendered for one viewer is returned to the next without fetching the document
// or entering lazypdf at all. A cache failure in either direction is logged and ignored -- a cache must
// never be able to fail a render, which is the lesson of the 2026-07-01 Redis incident.
func (w *Worker) Render(ctx context.Context, req RenderRequest) (_ RenderResult, err error) {
	span, ctx := w.startSpan(ctx, "Worker.Render")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	if err := req.validate(); err != nil {
		return RenderResult{}, err
	}

	key, err := req.cacheKey()
	if err != nil {
		return RenderResult{}, fmt.Errorf("failed to derive the page cache key: %w", err)
	}
	// Tagged even when the cache is disabled: comparing the cardinality of this tag against the request
	// count is how the achievable hit rate gets measured.
	span.SetTag("pageCache.key", key)

	// No usable version means no safe key: see RenderRequest.Version.
	cacheable := w.PageCache != nil && keySafeVersion(req.Version)
	span.SetTag("pageCache.enabled", cacheable)

	if cacheable {
		body, size, err := w.PageCache.Get(ctx, key)
		switch {
		case err != nil:
			w.Logger.Warn().Err(err).Str("pageCacheKey", key).Msg("Failed to read from the page cache")
		case body != nil:
			span.SetTag("pageCache.hit", true)
			return RenderResult{Body: body, Size: size, Cached: true}, nil
		}
		span.SetTag("pageCache.hit", false)
	}

	// Identical concurrent renders collapse onto one. That matters most on a cold document: the viewer
	// asks for every page tile at once and several of those requests can be for the same tile, and each
	// in-flight render holds the whole source document in memory while it runs.
	//
	// The shared render runs under the first caller's context, so if that request is cancelled the
	// followers fail with it rather than silently inheriting a cancelled render. They are retried by the
	// caller, which is the same disposition as any other transient render failure.
	shared, err, _ := w.renderGroup.Do(key, func() (any, error) {
		return w.renderPage(ctx, req)
	})
	if err != nil {
		return RenderResult{}, err
	}
	payload, ok := shared.([]byte)
	if !ok {
		return RenderResult{}, fmt.Errorf("unexpected render result type '%T'", shared)
	}

	if cacheable {
		// Handed over without copying: the payload is never mutated after this point, and a copy per
		// render is exactly the extra heap this cache exists to avoid.
		w.PageCache.Put(key, payload)
	}

	// Several callers can share one payload through the single-flight group, so each gets its own reader
	// over those immutable bytes rather than a shared, drainable one.
	return RenderResult{Body: io.NopCloser(bytes.NewReader(payload)), Size: int64(len(payload)), Cached: false}, nil
}

// renderPage performs the actual render: fetch the source document and rasterise the requested page.
// It returns the encoded page so the caller can both answer the request and populate the cache from a
// single render.
func (w *Worker) renderPage(ctx context.Context, req RenderRequest) (_ []byte, err error) {
	span, ctx := w.startSpan(ctx, "Worker.renderPage")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	// The frontend's first page is 1; lazypdf is 0-based.
	page := req.Page - 1

	payload, err := w.fetchFile(ctx, req.Path)
	if err != nil {
		return nil, fmt.Errorf("fail to fetch the file: %w", err)
	}
	if len(payload) == 0 {
		return nil, errors.New("empty payload")
	}

	storage := bytes.NewBuffer([]byte{})
	switch req.Format {
	case formatPNG:
		processed, cleanup, err := w.preprocessAnnotations(ctx, req.Annotations, page)
		if err != nil {
			return nil, fmt.Errorf("failed to preprocess the annotations: %w", err)
		}
		defer cleanup()

		if len(processed) > 0 {
			//nolint:gosec,G115
			if err := w.SaveToPNGWithAnnotations(
				ctx, uint16(page), uint16(req.Width), req.Scale, req.DPI, bytes.NewBuffer(payload), storage, processed,
			); err != nil {
				return nil, fmt.Errorf("failed to process annotations and generate PNG: %w", err)
			}
		} else {
			//nolint:gosec,G115
			if err := lazypdf.SaveToPNG(
				ctx, uint16(page), uint16(req.Width), req.Scale, req.DPI, bytes.NewBuffer(payload), storage,
			); err != nil {
				return nil, fmt.Errorf("fail to extract the PNG from the PDF: %w", err)
			}
		}
	case formatHTML:
		//nolint:gosec,G115
		if err := lazypdf.SaveToHTML(
			ctx, uint16(page), uint16(req.Width), req.Scale, req.DPI, bytes.NewBuffer(payload), storage,
		); err != nil {
			return nil, fmt.Errorf("fail to render the PDF page to HTML: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown format '%s'", req.Format)
	}

	return storage.Bytes(), nil
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

	output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(filePath),
	})
	if err != nil {
		var notFound *types.NoSuchKey
		if errors.As(err, &notFound) {
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

func (w *Worker) getBucketS3Client(bucket string) (workerS3API, error) {
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

	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion(region),
		config.WithHTTPClient(w.HTTPClient),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to load configuration for region '%s': %w", region, err)
	}
	awsv2trace.AppendMiddleware(&cfg)

	client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.HTTPClient = w.HTTPClient
	})
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

	return w.preprocessAnnotations(ctx, originalAnnotations, page)
}

// preprocessAnnotations filters annotations down to the requested page (0-based) and prepares them
// for lazypdf: image annotations are downloaded to temporary files so the C layer can consume them.
// The returned cleanup function removes those temporary files and must always be executed once the
// annotations are no longer needed; it is only valid when err is nil.
func (w *Worker) preprocessAnnotations(
	ctx context.Context, originalAnnotations []any, page int,
) (annotations []any, cleanup func(), err error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.preprocessAnnotations")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	annotations = make([]any, 0)
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

	deadline, hasDeadline := ctx.Deadline()

	for _, annotation := range annotations {
		if hasDeadline && time.Now().After(deadline) {
			return context.DeadlineExceeded
		}

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
