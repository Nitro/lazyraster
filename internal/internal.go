package internal

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type datadogLogger struct {
	logger zerolog.Logger
}

func (dl datadogLogger) Log(msg string) {
	dl.logger.Info().Msg(msg)
}

func traceLogger(enabled bool) func(context.Context, zerolog.Logger) (zerolog.Logger, error) {
	return func(ctx context.Context, logger zerolog.Logger) (zerolog.Logger, error) {
		if !enabled {
			return logger, nil
		}

		span, ok := tracer.SpanFromContext(ctx)
		if !ok {
			return logger, errors.New("could not found a span inside the context")
		}

		traceLogger := logger.With().Fields(map[string]interface{}{
			"dd": map[string]uint64{
				"trace_id": span.Context().TraceID(),
				"span_id":  span.Context().SpanID(),
			},
		}).Logger()
		return traceLogger, nil
	}
}
