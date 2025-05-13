package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestHandlerURLToVerify(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{
			path:     "/path",
			expected: "/path",
		},
		{
			path:     "/path?page=1",
			expected: "/path?page=1",
		},
		{
			path:     "/path?token=2",
			expected: "/path?token=2",
		},
		{
			path:     "/path?page=1&token=2",
			expected: "/path?page=1&token=2",
		},
		{
			path:     "/path?page=1&token=2&scale=3&width=4",
			expected: "/path?page=1&token=2",
		},
		{
			path:     "/path?page=1&token=2&scale=3&width=4&token-ttl=5",
			expected: "/path?page=1&token=2&token-ttl=5",
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("Scenario %d", i), func(t *testing.T) {
			t.Parallel()
			var h handler
			req, err := http.NewRequest(http.MethodGet, tt.path, nil)
			require.NoError(t, err)
			require.Equal(t, tt.expected, h.urlToVerify(req))
		})
	}
}

func TestHandlerDocumentTokenTTLExpired(t *testing.T) {
	ttl := time.Now().UTC().Add(-1 * time.Hour).Unix()
	strconv.FormatInt(ttl, 10)
	path := fmt.Sprintf("/documents?page=1&token-ttl=%s", strconv.FormatInt(ttl, 10))
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	traceExtractor := func(context.Context, zerolog.Logger) (zerolog.Logger, error) {
		return zerolog.Nop(), nil
	}
	h := handler{
		traceExtractor: traceExtractor,
		writer: writer{
			logger:         zerolog.Nop(),
			traceExtractor: traceExtractor,
		},
	}
	h.document(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
