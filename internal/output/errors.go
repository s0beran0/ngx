package output

import (
	"errors"
	"fmt"
)

// ExitCode is the process exit code. v0.1 emits only the codes below; 4
// (lint), 5 and 6 (apply) and 8 (ambiguous mutation) belong to commands that
// do not exist yet and are not documented as supported until they can be
// emitted.
type ExitCode int

const (
	ExitOK            ExitCode = 0
	ExitInternal      ExitCode = 1
	ExitUsage         ExitCode = 2
	ExitInvalidConfig ExitCode = 3
	ExitDrift         ExitCode = 7
	ExitHashMismatch  ExitCode = 9
)

// Error is an error that carries its own exit code and the corresponding
// diagnostic. Commands never pick an exit code directly: they return one of
// these, and main.go translates it at a single point.
type Error struct {
	Code ExitCode
	Diag Diagnostic
	Err  error

	// Extras are diagnostics that accompany the failure without being its
	// cause -- for example, which files required privilege before the read
	// failed on another one. Without this field they would be lost exactly
	// on the error path, which is where they help the most to understand
	// what happened.
	Extras []Diagnostic
}

func (e *Error) Error() string { return e.Diag.Message }

func (e *Error) Unwrap() error { return e.Err }

func newError(code ExitCode, diagCode, format string, args ...any) *Error {
	return &Error{
		Code: code,
		Diag: Diagnostic{
			Severity: SeverityError,
			Code:     diagCode,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

// Usage signals a usage error: invalid flag, malformed selector, missing
// required argument.
func Usage(format string, args ...any) *Error {
	return newError(ExitUsage, "NGX-0002", format, args...)
}

// InvalidConfig signals that the nginx configuration is not valid.
func InvalidConfig(format string, args ...any) *Error {
	return newError(ExitInvalidConfig, "NGX-0003", format, args...)
}

// Drift signals that the configuration on disk differs from the loaded one.
func Drift(format string, args ...any) *Error {
	return newError(ExitDrift, "NGX-0007", format, args...)
}

// HashMismatch signals that an ID was presented against a version of the
// configuration different from the one it was generated in. The previous IDs
// are invalid and the agent needs to read again before acting.
func HashMismatch(esperado, atual string) *Error {
	return newError(ExitHashMismatch, "NGX-0009",
		"the configuration changed since the read: expected %s, current %s", esperado, atual)
}

// Internal wraps an IO failure or a defect of ngx itself. The original cause
// (err) is kept in the Err field and is only reachable via
// errors.Unwrap/errors.Is/errors.As: Error() and Diag.Message return only the
// format, never the cause. This is deliberate -- whoever renders the
// diagnostic in the JSON envelope must not leak internal details to the
// agent.
func Internal(err error, format string, args ...any) *Error {
	e := newError(ExitInternal, "NGX-0001", format, args...)
	e.Err = err
	return e
}

// CodeOf extracts the exit code from an error, going through wrapping. An
// error without a code is treated as an internal failure, never as a success.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitInternal
}
