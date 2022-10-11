package service

import (
	"errors"
)

// Sentinel errors.
var (
	ErrClient   = ServiceError{origin: "client"}
	ErrNotFound = ServiceError{origin: "notFound"}
)

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
