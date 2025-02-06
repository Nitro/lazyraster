package transport

import (
	"fmt"
	"net/http"
	"testing"

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
