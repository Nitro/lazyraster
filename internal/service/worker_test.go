package service

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestWorkerProcess(t *testing.T) {
	t.Parallel()

	// expiredToken := urlsign.GenerateToken("secret", 8*time.Hour, time.Now().Add(-24*time.Hour), "")
	// validToken := urlsign.GenerateToken("secret", 8*time.Hour, time.Now(), "")

	tests := []struct {
		message       string
		token         string
		width         int
		scale         float32
		expectedError string
	}{
		// {
		// 	message:       "have an invalid width #1",
		// 	width:         -1,
		// 	expectedError: "invalid width",
		// },
		// {
		// 	message:       "have an invalid width #2",
		// 	width:         4097,
		// 	expectedError: "invalid width, can't be bigger than 4096",
		// },
		// {
		// 	message:       "have an invalid scale #1",
		// 	scale:         -1,
		// 	expectedError: "invalid scale",
		// },
		// {
		// 	message:       "have an invalid scale #2",
		// 	scale:         4,
		// 	expectedError: "invalid scale, can't be bigger than 3",
		// },
		// {
		// 	message:       "have an invalid token #1",
		// 	token:         "",
		// 	expectedError: "invalid token",
		// },
		// {
		// 	message:       "have an invalid token #2",
		// 	token:         "still a invalid token",
		// 	expectedError: "invalid token",
		// },
		// {
		// 	message:       "have an invalid token #3",
		// 	token:         expiredToken,
		// 	expectedError: "invalid token",
		// },
		// {
		// 	message: "have an error fetching the file",
		// 	token:   "validToken",
		// },
	}
	for _, tt := range tests {
		t.Run("Should "+tt.message, func(t *testing.T) {
			t.Parallel()

			w := Worker{
				HTTPClient:       http.DefaultClient,
				URLSigningSecret: "secret",
				Storage:          &mockStorage{},
			}
			require.NoError(t, w.Init())
			err := w.Process(context.Background(), "", tt.token, "", 0, tt.width, tt.scale, nil)
			require.Equal(t, tt.expectedError == "", err == nil)
			if tt.expectedError != "" {
				require.Equal(t, tt.expectedError, err.Error())
			}
		})
	}
}

type mockStorage struct {
	mock.Mock
}

func (*mockStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, nil
}

func (*mockStorage) Put(ctx context.Context, key string, payload io.Reader) error {
	return nil
}
