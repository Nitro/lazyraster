package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nitro/urlsign"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// nolint: goconst
func TestWorkerProcess(t *testing.T) {
	t.Parallel()

	urlSecret := "secret"
	expiredToken := urlsign.GenerateToken("secret", 8*time.Hour, time.Now().Add(-24*time.Hour), "documents")
	validToken := urlsign.GenerateToken(urlSecret, 8*time.Hour, time.Now().Add(time.Hour), "documents")

	tests := []struct {
		message       string
		url           string
		path          string
		page          int
		width         int
		scale         float32
		s3Client      func(*testing.T) *mockS3
		storageClient func(*testing.T) *mockStorage
		bypassClient  func(*testing.T) *mockBypass
		expectedError string
	}{
		{
			message:       "have an invalid page #1",
			page:          -1,
			expectedError: "invalid page",
		},
		{
			message:       "have an invalid page #2",
			page:          0,
			expectedError: "invalid page",
		},
		{
			message:       "have an invalid width #1",
			page:          1,
			width:         -1,
			expectedError: "invalid width",
		},
		{
			message:       "have an invalid width #2",
			page:          1,
			width:         4097,
			expectedError: "invalid width, can't be bigger than 4096",
		},
		{
			message:       "have an invalid scale #1",
			page:          1,
			scale:         -1,
			expectedError: "invalid scale",
		},
		{
			message:       "have an invalid scale #2",
			page:          1,
			scale:         4,
			expectedError: "invalid scale, can't be bigger than 3",
		},
		{
			message:       "have an invalid token #1",
			page:          1,
			expectedError: "invalid token",
		},
		{
			message:       "have an invalid token #2",
			page:          1,
			url:           fmt.Sprintf("/another-endpoint?token=%s", validToken),
			expectedError: "invalid token",
		},
		{
			message:       "have an invalid token #3",
			page:          1,
			url:           fmt.Sprintf("documents?token=%s", expiredToken),
			expectedError: "invalid token",
		},
		{
			message:       "have an error fetching the file #1",
			page:          1,
			url:           fmt.Sprintf("documents?token=%s", validToken),
			expectedError: "fail to fetch the file: invalid path",
		},
		{
			message:       "have an error fetching the file #2",
			page:          1,
			url:           fmt.Sprintf("documents?token=%s", validToken),
			path:          "documents",
			expectedError: "fail to fetch the file: invalid path",
		},
		{
			message:       "have an error fetching the file #3",
			page:          1,
			url:           fmt.Sprintf("documents?token=%s", validToken),
			path:          "random-bucket/file.pdf",
			expectedError: "fail to fetch the file: fail to get the s3 bucket client: can't find the bucket 'random-bucket' region", // nolint: lll
		},
		{
			message: "have an error fetching the file #4",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(*testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				client.On("GetObjectWithContext", mock.Anything, &input).Return((*s3.GetObjectOutput)(nil), errors.New("s3 error"))
				return &client
			},
			expectedError: "fail to fetch the file: fail to get object: s3 error",
		},
		{
			message: "have an error fetching the file from the storage",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(*testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				output := s3.GetObjectOutput{Body: io.NopCloser(bytes.NewBuffer([]byte{}))}
				client.On("GetObjectWithContext", mock.Anything, &input).Return(&output, nil)
				return &client
			},
			storageClient: func(t *testing.T) *mockStorage {
				var client mockStorage
				hash := "d21271bf44b3c6c0addb3bb52761d5f96f5bbb71b29b1a58e317406997f21160"
				client.On("Get", mock.Anything, hash).Return(nil, errors.New("failed at the storage"))
				return &client
			},
			expectedError: "fail to fetch the object from the storage: failed at the storage",
		},
		{
			message: "have an error processing the file",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(*testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				output := s3.GetObjectOutput{Body: io.NopCloser(bytes.NewBuffer([]byte{}))}
				client.On("GetObjectWithContext", mock.Anything, &input).Return(&output, nil)
				return &client
			},
			storageClient: func(t *testing.T) *mockStorage {
				var client mockStorage
				hash := "d21271bf44b3c6c0addb3bb52761d5f96f5bbb71b29b1a58e317406997f21160"
				client.On("Get", mock.Anything, hash).Return(nil, nil)
				return &client
			},
			expectedError: "fail to extract the PNG from the PDF: failure at the C/MuPDF layer: cannot tell in file",
		},
		{
			message: "have an error processing the file",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(*testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				output := s3.GetObjectOutput{Body: io.NopCloser(bytes.NewBuffer([]byte{}))}
				client.On("GetObjectWithContext", mock.Anything, &input).Return(&output, nil)
				return &client
			},
			storageClient: func(t *testing.T) *mockStorage {
				var client mockStorage
				hash := "d21271bf44b3c6c0addb3bb52761d5f96f5bbb71b29b1a58e317406997f21160"
				client.On("Get", mock.Anything, hash).Return(nil, nil)
				return &client
			},
			expectedError: "fail to extract the PNG from the PDF: failure at the C/MuPDF layer: cannot tell in file",
		},
		{
			message: "return a page from the storage",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(*testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				output := s3.GetObjectOutput{Body: io.NopCloser(bytes.NewBuffer([]byte{}))}
				client.On("GetObjectWithContext", mock.Anything, &input).Return(&output, nil)
				return &client
			},
			storageClient: func(t *testing.T) *mockStorage {
				var client mockStorage
				hash := "d21271bf44b3c6c0addb3bb52761d5f96f5bbb71b29b1a58e317406997f21160"
				output := io.NopCloser(bytes.NewReader([]byte{}))
				client.On("Get", mock.Anything, hash).Return(output, nil)
				return &client
			},
		},
		{
			message: "process and return a page",
			page:    1,
			url:     fmt.Sprintf("documents?token=%s", validToken),
			path:    "bucket-1/file.pdf",
			s3Client: func(t *testing.T) *mockS3 {
				var client mockS3
				input := s3.GetObjectInput{
					Bucket: aws.String("bucket-1"),
					Key:    aws.String("file.pdf"),
				}
				payload, err := os.ReadFile("testdata/sample.pdf")
				require.NoError(t, err)
				output := s3.GetObjectOutput{Body: io.NopCloser(bytes.NewBuffer(payload))}
				client.On("GetObjectWithContext", mock.Anything, &input).Return(&output, nil)
				return &client
			},
			storageClient: func(*testing.T) *mockStorage {
				var client mockStorage
				hash := "3235688e7c74c75e72d6c6e6ea2333522fa0ea1bf25ac035999d97aac3a250ba"
				client.On("Get", mock.Anything, hash).Return(nil, nil)
				return &client
			},
			bypassClient: func(*testing.T) *mockBypass {
				var client mockBypass
				client.On("Bypass", mock.Anything).Return(false)
				return &client
			},
		},
	}
	for _, tt := range tests {
		t.Run("Should "+tt.message, func(t *testing.T) {
			t.Parallel()

			var (
				s3Client      *mockS3
				storageClient *mockStorage = &mockStorage{}
				bypassClient  *mockBypass  = &mockBypass{}
				getS3Client   func(string) (s3iface.S3API, error)
			)
			if tt.s3Client != nil {
				s3Client = tt.s3Client(t)
				defer s3Client.AssertExpectations(t)
				getS3Client = func(string) (s3iface.S3API, error) {
					return s3Client, nil
				}
			}
			if tt.storageClient != nil {
				storageClient = tt.storageClient(t)
				defer storageClient.AssertExpectations(t)
			}
			if tt.bypassClient != nil {
				bypassClient = tt.bypassClient(t)
				defer bypassClient.AssertExpectations(t)
			}

			w := Worker{
				HTTPClient:          http.DefaultClient,
				URLSigningSecret:    urlSecret,
				Storage:             storageClient,
				BypassStoragePut:    bypassClient,
				TraceExtractor:      traceExtractor,
				StorageBucketRegion: map[string]string{"eu-central-1": "bucket-1"},
				getS3Client:         getS3Client,
			}
			require.NoError(t, w.Init())
			err := w.Process(context.Background(), tt.url, tt.path, tt.page, tt.width, tt.scale, bytes.NewBuffer([]byte{}))
			require.Equal(t, tt.expectedError == "", err == nil)
			if tt.expectedError != "" {
				require.Equal(t, tt.expectedError, err.Error())
			}
		})
	}
}

type mockS3 struct {
	s3iface.S3API
	mock.Mock
}

func (m *mockS3) GetObjectWithContext(
	ctx context.Context, input *s3.GetObjectInput, options ...request.Option,
) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, input)
	return args.Get(0).(*s3.GetObjectOutput), args.Error(1)
}

type mockStorage struct {
	mock.Mock
}

func (m *mockStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	args := m.Called(ctx, key)
	err := args.Error(1)
	if reader := args.Get(0); reader == nil {
		return nil, err
	} else {
		return reader.(io.ReadCloser), err
	}
}

func (*mockStorage) Put(ctx context.Context, key string, payload io.Reader) error {
	return nil
}

type mockBypass struct {
	mock.Mock
}

func (m *mockBypass) Bypass(ctx context.Context) bool {
	args := m.Called(ctx)
	return args.Bool(0)
}

func traceExtractor(context.Context, zerolog.Logger) (zerolog.Logger, error) {
	return zerolog.Nop(), nil
}
