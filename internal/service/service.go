package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

// Sentinel errors.
var (
	ErrClient   = ServiceError{origin: "client"}
	ErrNotFound = ServiceError{origin: "notFound"}
)

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

func newNotFoundError(err error) error {
	return ServiceError{base: err, origin: "notFound"}
}

func generateHash(parameters ...[]byte) (string, error) {
	h := sha256.New()
	for _, parameter := range parameters {
		if _, err := h.Write(parameter); err != nil {
			return "", fmt.Errorf("fail to write the parameter to the hash function: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
