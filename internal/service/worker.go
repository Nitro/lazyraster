package service

// nolint: gosec
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type workerStorage interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, payload io.Reader) error
}

// Worker used to fetch and process PDF files.
type Worker struct {
	HTTPClient       *http.Client
	URLSigningSecret string
	Storage          workerStorage
	Logger           zerolog.Logger
	svc              s3iface.S3API
}

// Init worker internal state.
func (w *Worker) Init() error {
	if w.HTTPClient == nil {
		return errors.New("Worker.HTTPClient can't be nil")
	}
	if w.URLSigningSecret == "" {
		return errors.New("Worker.URLSigningSecret can't be empty")
	}
	if w.Storage == nil {
		return errors.New("Worker.Storage can't be nil")
	}

	sess, err := session.NewSession()
	if err != nil {
		return fmt.Errorf("fail to start a session: %w", err)
	}
	sess = awstrace.WrapSession(sess)
	w.svc = s3.New(sess, &aws.Config{HTTPClient: w.HTTPClient})
	return nil
}

func (w Worker) Process(
	ctx context.Context, url, token, path string, page int, width int, scale float32, output io.Writer,
) error {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.Process")
	defer span.Finish()

	// This change is required because of historical reasons. The first page for the frontend is 1 and not zero.
	page--

	if width < 0 {
		return errors.New("invalid width")
	} else if width > 4096 {
		return errors.New("invalid width, can't be bigger than 4096")
	}

	if scale < 0 {
		return errors.New("invalid scale")
	} else if scale > 3 {
		return errors.New("invalid scale, can't be bigger than 3")
	}

	if !urlsign.IsValidSignature(w.URLSigningSecret, 8*time.Hour, time.Now(), url) {
		return errors.New("invalid token")
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
		result = io.NopCloser(storage)

		if err := w.Storage.Put(ctx, hash, bytes.NewBuffer(storage.Bytes())); err != nil {
			w.Logger.Err(err).Msg("Fail to put the object into the storage")
		}
	}
	defer result.Close()

	if _, err := io.Copy(output, result); err != nil {
		return fmt.Errorf("fail write the result to the output: %w", err)
	}
	return nil
}

// Metadata is used to fetch the document metadata.
func (w Worker) Metadata(ctx context.Context, url, token, path string) (string, int, error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.Metadata")
	defer span.Finish()

	if !urlsign.IsValidSignature(w.URLSigningSecret, 8*time.Hour, time.Now(), url) {
		return "", 0, errors.New("invalid token")
	}

	// fetch file need to return the document name too.
	payload, err := w.fetchFile(ctx, path)
	if err != nil {
		return "", 0, fmt.Errorf("fail to fetch the file: %w", err)
	}

	pageCount, err := lazypdf.PageCount(ctx, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("fail to count the file pages: %w", err)
	}

	filename, err := w.generateFilename(path)
	if err != nil {
		return "", 0, fmt.Errorf("fail to generate the filename: %w", err)
	}

	return filename, pageCount, nil
}

func (w Worker) fetchFile(ctx context.Context, path string) ([]byte, error) {
	span, ctx := ddTracer.StartSpanFromContext(ctx, "Worker.fetchFile")
	defer span.Finish()

	fragments := strings.Split(path, "/")
	if len(fragments) < 2 {
		return nil, errors.New("invalid path")
	}

	output, err := w.svc.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: &fragments[0],
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

func (Worker) generateHash(payload []byte, parameters []string) (string, error) {
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

func (Worker) generateFilename(fpath string) (string, error) {
	id := uuid.New()
	return id.String(), nil
	// hashedFilename := md5.Sum([]byte(fpath)) // nolint: gosec
	// fnvHasher := fnv.New32()
	// // The current implementation of fnv.New32().Write never returns a non-nil error
	// if _, err := fnvHasher.Write([]byte(fpath)); err != nil {
	// 	return "", fmt.Errorf("fail write to the fnv hash: %w", err)
	// }
	// hashedDir := fnvHasher.Sum(nil)

	// // If we don't find an original file extension, we'll default to this one
	// extension := ".pdf"

	// // Look in the last 5 characters for a . and extension
	// lastDot := strings.LastIndexByte(fpath, '.')
	// if lastDot > len(fpath)-6 {
	// 	extension = fpath[lastDot:]
	// }

	// fileName := fmt.Sprintf("%x%s", hashedFilename, extension)
	// dir := fmt.Sprintf("%x", hashedDir[:1])
	// return filepath.Join(dir, filepath.FromSlash(path.Clean("/"+fileName))), nil
}
