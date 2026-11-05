package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
)

// ErrorKind describes a failure independently of a provider's codes and messages.
type ErrorKind string

const (
	ErrorUnknown                     ErrorKind = "unknown"
	ErrorCanceled                    ErrorKind = "canceled"
	ErrorTimeout                     ErrorKind = "timeout"
	ErrorDisconnected                ErrorKind = "disconnected"
	ErrorRateLimited                 ErrorKind = "rate_limited"
	ErrorTemporaryFailure            ErrorKind = "temporary_failure"
	ErrorRequestRejected             ErrorKind = "request_rejected"
	ErrorPreviousResponseUnavailable ErrorKind = "previous_response_unavailable"
)

// CallError carries the source's classification and retry decision for one failure.
type CallError struct {
	// Kind is the internal classification; consumers never interpret provider codes.
	Kind ErrorKind
	// Retryable allows another complete attempt after this attempt's cleanup succeeds.
	Retryable bool
	// Cause preserves the original failure, including structured protocol details.
	Cause error
}

func (e *CallError) Error() string { return e.Cause.Error() }
func (e *CallError) Unwrap() error { return e.Cause }

// MapProviderError maps structured provider fields once at the protocol boundary.
// Recognized codes take precedence over error types, then HTTP status. Messages
// are preserved in cause and never used to decide whether a request can be retried.
func MapProviderError(status int, code, errorType, param string, cause error) *CallError {
	kind := ErrorUnknown
	for _, value := range []string{code, errorType} {
		switch value {
		case "previous_response_not_found", "previous_response_id_not_found", "invalid_previous_response_id":
			if (status == 0 || status == http.StatusBadRequest || status == http.StatusNotFound) && (param == "" || param == "previous_response_id") {
				kind = ErrorPreviousResponseUnavailable
			}
		case "response_not_found", "not_found":
			if (status == 0 || status == http.StatusBadRequest || status == http.StatusNotFound) && param == "previous_response_id" {
				kind = ErrorPreviousResponseUnavailable
			}
		case "request_timeout", "timeout_error":
			kind = ErrorTimeout
		case "rate_limit_exceeded", "rate_limit_error":
			kind = ErrorRateLimited
		case "server_error", "internal_server_error", "service_unavailable", "overloaded_error":
			kind = ErrorTemporaryFailure
		case "invalid_request_error", "invalid_api_key", "authentication_error", "permission_error", "permission_denied", "insufficient_quota", "billing_hard_limit_reached":
			kind = ErrorRequestRejected
		}
		if kind != ErrorUnknown {
			break
		}
	}
	if kind == ErrorUnknown {
		switch {
		case status == http.StatusRequestTimeout:
			kind = ErrorTimeout
		case status == http.StatusTooManyRequests:
			kind = ErrorRateLimited
		case status == http.StatusConflict, status == http.StatusTooEarly, status >= 500 && status < 600:
			kind = ErrorTemporaryFailure
		case status >= 400 && status < 500:
			kind = ErrorRequestRejected
		}
	}
	return &CallError{
		Kind:      kind,
		Retryable: kind == ErrorTimeout || kind == ErrorDisconnected || kind == ErrorRateLimited || kind == ErrorTemporaryFailure,
		Cause:     cause,
	}
}

// MapTransportError classifies Go errors without inspecting their text.
func MapTransportError(cause error) *CallError {
	var networkError net.Error
	var dnsError *net.DNSError
	kind := ErrorUnknown
	switch {
	case errors.Is(cause, context.Canceled):
		kind = ErrorCanceled
	case errors.As(cause, &networkError) && networkError.Timeout():
		kind = ErrorTimeout
	case errors.As(cause, &dnsError) && dnsError.IsTemporary:
		kind = ErrorTemporaryFailure
	case errors.Is(cause, io.EOF), errors.Is(cause, io.ErrUnexpectedEOF),
		errors.Is(cause, syscall.ECONNRESET), errors.Is(cause, syscall.ECONNABORTED),
		errors.Is(cause, syscall.ECONNREFUSED), errors.Is(cause, syscall.EPIPE),
		errors.Is(cause, syscall.ENETUNREACH), errors.Is(cause, syscall.EHOSTUNREACH):
		kind = ErrorDisconnected
	}
	return &CallError{
		Kind:      kind,
		Retryable: kind == ErrorTimeout || kind == ErrorDisconnected || kind == ErrorTemporaryFailure,
		Cause:     cause,
	}
}
