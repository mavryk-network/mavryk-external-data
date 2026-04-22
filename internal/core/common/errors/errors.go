package coreerrors

import "fmt"

// Code is a stable machine-readable error identifier for API clients.
type Code string

const (
	CodeInvalidArgument Code = "INVALID_ARGUMENT"
	CodeNotFound        Code = "NOT_FOUND"
	CodeInternal        Code = "INTERNAL"
	CodeUnavailable     Code = "UNAVAILABLE"
)

// Error is an application error safe to expose to HTTP clients (Message + Code only via responder).
type Error struct {
	Code    Code
	Message string
	Cause   error // optional; never serialized to JSON; use for logs / Unwrap
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap returns the underlying cause for errors.Is / errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// InvalidArgument marks a client mistake (HTTP 400).
func InvalidArgument(message string) *Error {
	return &Error{Code: CodeInvalidArgument, Message: message}
}

// NotFound marks a missing resource (HTTP 404).
func NotFound(message string) *Error {
	return &Error{Code: CodeNotFound, Message: message}
}

// Internal marks a server-side failure. message must be safe for clients; cause is for logging only.
func Internal(message string, cause error) *Error {
	return &Error{Code: CodeInternal, Message: message, Cause: cause}
}

// Unavailable marks dependency outage (HTTP 503).
func Unavailable(message string) *Error {
	return &Error{Code: CodeUnavailable, Message: message}
}

// Wrap builds an error with an explicit code (use when none of the helpers fit).
func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}
