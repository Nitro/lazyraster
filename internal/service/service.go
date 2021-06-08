package service

import (
	"context"
	"errors"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

// ErrClient is sentinel error to indentify errors originated by the client.
var ErrClient = ServiceError{origin: "client"}

func startSpan(
	ctx context.Context, operation string, opts ...ddTracer.StartSpanOption,
) (ddtrace.Span, context.Context) {
	return ddTracer.StartSpanFromContext(ctx, "internal/service/"+operation, opts...)
}

// ServiceError has detailed information about errors from the service package.
type ServiceError struct {
	base   error
	origin string
}

// Is checks if the given error and the current ServiceError are the same.
func (se ServiceError) Is(target error) bool {
	var err ServiceError
	if !errors.As(target, &err) {
		return false
	}
	return se.origin == err.origin
}

// Error is used to output the error message.
func (se ServiceError) Error() string {
	return se.base.Error()
}

func newClientError(err error) error {
	return ServiceError{base: err, origin: "client"}
}
