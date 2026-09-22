// Package errs carries errors that know what the user should do about
// them. Every package may create one; only internal/ui renders them.
package errs

import (
	"errors"
	"fmt"
)

// Error is a failure with a message and, where one exists, a concrete
// next step for the user. An error without a hint is fine — an invented
// hint is worse than none.
type Error struct {
	// Msg states what went wrong, lowercase and unpunctuated so it
	// reads correctly when wrapped.
	Msg string
	// Hint states what to do about it. It may be empty.
	Hint string
	// Err is the underlying cause, if any.
	Err error
}

// Error implements the error interface. An empty Msg means the error
// exists only to carry a hint, so the cause speaks for itself.
func (e *Error) Error() string {
	switch {
	case e.Err == nil:
		return e.Msg
	case e.Msg == "":
		return e.Err.Error()
	default:
		return e.Msg + ": " + e.Err.Error()
	}
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// WithHint attaches the next step the user should take and returns the
// same error, so it can be chained onto a constructor.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// New builds an error from a formatted message.
func New(format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// Wrap adds context to an existing error. It returns nil when err is
// nil, so callers can wrap unconditionally.
func Wrap(err error, format string, args ...any) *Error {
	if err == nil {
		return nil
	}
	return &Error{Msg: fmt.Sprintf(format, args...), Err: err}
}

// Hinted attaches a hint to an existing error without changing what it
// says. Use it when the message is already right and only the next step
// is missing.
func Hinted(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &Error{Err: err, Hint: fmt.Sprintf(format, args...)}
}

// Hint returns the hint of the outermost error in the chain that has
// one, or an empty string. The outermost wins because it was written
// with the most context about what the user was trying to do.
func Hint(err error) string {
	var e *Error
	for errors.As(err, &e) {
		if e.Hint != "" {
			return e.Hint
		}
		// Step past this one and keep looking further down.
		err = errors.Unwrap(e)
		e = nil
	}
	return ""
}
