package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nitro/lazyraster/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// lazypdf caps annotation text at 500 characters and rejects anything longer. Both the value and the
// document come from the caller and are identical on a retry, so the failure is deterministic and must
// not reach callers as a server fault: the transport layer turns ErrClient into a 400, which stops SWS
// retrying a request that can never succeed and stops a run of them tripping its circuit breaker for
// unrelated documents.
func TestSaveToPNGWithAnnotationsTextLength(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/sample.pdf")
	require.NoError(t, err)

	// Helvetica is one of the base-14 fonts MuPDF resolves without an external font file.
	textAnnotation := func(length int) []any {
		return []any{domain.AnnotationText{
			Value:    strings.Repeat("a", length),
			Page:     1,
			Location: domain.AnnotationLocation{X: 10, Y: 10},
			Font:     domain.AnnotationTextFont{Family: "Helvetica", Size: 6},
			Size:     domain.AnnotationSize{Width: 400, Height: 400},
		}}
	}

	tests := []struct {
		name      string
		length    int
		clientErr bool
	}{
		{name: "at the limit", length: 500, clientErr: false},
		{name: "over the limit", length: 501, clientErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			worker := Worker{}
			err := worker.SaveToPNGWithAnnotations(
				context.Background(), 0, 1000, 0, 72,
				bytes.NewReader(payload), bytes.NewBuffer(nil), textAnnotation(tt.length),
			)
			if !tt.clientErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			// Compared with errors.Is rather than require.ErrorIs: the latter renders the expected
			// sentinel via Error(), and ErrClient carries no base error, so a failure would panic
			// instead of reporting the mismatch.
			require.Truef(t, errors.Is(err, ErrClient), "expected a client error, got: %v", err)
		})
	}
}
