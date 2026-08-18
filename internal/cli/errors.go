package cli

import "github.com/s0beran0/ngx/internal/output"

// commandOf returns the name of the command that was running, so that the
// error envelope identifies the operation that failed. Before cobra resolves
// the command — an invalid global flag, for example — there is no name, and
// the fallback is the binary itself.
func commandOf(ctx *Context) string {
	if ctx == nil || ctx.Command == "" {
		return "ngx"
	}
	return ctx.Command
}

// alreadyRenderedError carries the exit code of a command that already wrote its
// own envelope.
//
// It exists because of a case that is not an error: `nginx -t` rejecting the
// configuration is the answer to the question that was asked, and that answer
// is the complete envelope, with the located diagnostics. All that is missing
// is exit code 3. Without this wrapper, execute would render a second
// envelope — the error one — and stdout would stop being a single JSON
// document, which is the contract with whoever consumes it.
//
// Unwrap returns the inner *output.Error so that errors.As and output.CodeOf
// keep seeing the code; the field is explicit, and not embedded, because an
// embedded *output.Error would promote its Unwrap and the chain would skip
// precisely the typed error.
type alreadyRenderedError struct {
	err *output.Error
}

func (e *alreadyRenderedError) Error() string { return e.err.Error() }

func (e *alreadyRenderedError) Unwrap() error { return e.err }

// withoutRerender marks a typed error as already shown to the user.
func withoutRerender(err *output.Error) error {
	return &alreadyRenderedError{err: err}
}
