// Package apperr defines the typed domain errors used across services.
//
// Services return these instead of HTTP concerns, and the Fiber error handler
// translates them into status codes. That keeps the service layer free of any
// transport knowledge.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier returned to clients.
type Code string

const (
	CodeBadRequest       Code = "BAD_REQUEST"
	CodeValidationFailed Code = "VALIDATION_FAILED"
	CodeUnauthorized     Code = "UNAUTHORIZED"
	CodeForbidden        Code = "FORBIDDEN"
	CodeNotFound         Code = "NOT_FOUND"
	CodeConflict         Code = "CONFLICT"
	CodeUnprocessable    Code = "UNPROCESSABLE_ENTITY"
	CodeTooLarge         Code = "PAYLOAD_TOO_LARGE"
	CodeInternal         Code = "INTERNAL_ERROR"
	CodeUnavailable      Code = "SERVICE_UNAVAILABLE"
)

// Error is a domain error carrying an HTTP status, a stable code, a
// human-readable message, and optional structured details.
type Error struct {
	Status  int
	Code    Code
	Message string
	Details any
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// WithDetails attaches structured details (e.g. per-field validation errors).
func (e *Error) WithDetails(details any) *Error {
	e.Details = details
	return e
}

// Wrap attaches an underlying cause, kept server-side for logs.
func (e *Error) Wrap(err error) *Error {
	e.Err = err
	return e
}

func newf(status int, code Code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// BadRequest reports malformed input the client can correct.
func BadRequest(format string, args ...any) *Error {
	return newf(http.StatusBadRequest, CodeBadRequest, format, args...)
}

// Validation reports a request body that failed field validation.
func Validation(format string, args ...any) *Error {
	return newf(http.StatusBadRequest, CodeValidationFailed, format, args...)
}

// Unauthorized reports a missing or invalid credential.
func Unauthorized(format string, args ...any) *Error {
	return newf(http.StatusUnauthorized, CodeUnauthorized, format, args...)
}

// Forbidden reports an authenticated caller lacking permission.
func Forbidden(format string, args ...any) *Error {
	return newf(http.StatusForbidden, CodeForbidden, format, args...)
}

// NotFound reports a missing resource.
func NotFound(format string, args ...any) *Error {
	return newf(http.StatusNotFound, CodeNotFound, format, args...)
}

// Conflict reports a uniqueness or state conflict.
func Conflict(format string, args ...any) *Error {
	return newf(http.StatusConflict, CodeConflict, format, args...)
}

// Unprocessable reports semantically invalid input that passed basic parsing.
func Unprocessable(format string, args ...any) *Error {
	return newf(http.StatusUnprocessableEntity, CodeUnprocessable, format, args...)
}

// TooLarge reports an oversized upload.
func TooLarge(format string, args ...any) *Error {
	return newf(http.StatusRequestEntityTooLarge, CodeTooLarge, format, args...)
}

// Internal reports an unexpected server-side failure.
func Internal(format string, args ...any) *Error {
	return newf(http.StatusInternalServerError, CodeInternal, format, args...)
}

// Unavailable reports a dependency (AI service, storage) being unreachable or
// unconfigured.
func Unavailable(format string, args ...any) *Error {
	return newf(http.StatusServiceUnavailable, CodeUnavailable, format, args...)
}

// As extracts an *Error from an error chain, reporting whether one was found.
func As(err error) (*Error, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}
