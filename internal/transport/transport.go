// Package transport abstracts where the configuration comes from and where
// commands run. The rest of ngx does not know whether it operates on the local
// machine or on a remote server: it always talks to a Transport.
package transport

import (
	"context"
	"io"
)

// Transport is the access to a target — the local machine or a remote host.
//
// The distinction between exit code and error in Run is the central rule of
// this interface: a command that runs to completion and exits with a non-zero
// code returns that code with a nil err, because that is a result. A transport
// error is the binary not existing, the connection dropping, or the context
// being cancelled — then err is non-nil and the exitCode means nothing.
//
// Confusing the two makes an `nginx -t` that rejects the configuration look
// like an infrastructure failure.
// The interface has no write operation, and that is a v0.1 property rather
// than a permanent guarantee: today no command changes anything on the
// target, so offering a way to write would be surface without a caller. v0.2
// brings mutation, and it will need one -- adding it is expected, not a
// violation. What must not happen is a write path appearing before the parts
// that make writing safe (byte spans, stable IDs) are proven.
type Transport interface {
	// Open opens a file for reading. The caller closes it.
	Open(path string) (io.ReadCloser, error)

	// Glob expands a path pattern. With no match it returns an empty
	// list and a nil err, never nil.
	Glob(pattern string) ([]string, error)

	// Run executes argv without a shell: argv[0] is the binary and the
	// rest are the arguments, already separated.
	Run(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)

	// Close releases the transport resources. Calling it twice is safe.
	Close() error

	// Describe identifies the target in one line, for the meta section of
	// the JSON envelope: whoever consumes the output needs to know what
	// the tool operated against. "local" for the local machine,
	// "ssh://user@host:port" for a remote host.
	Describe() string
}

// InputRunner is a Transport that can feed a command's standard input.
//
// It is a SEPARATE interface rather than a method on Transport, and that is the
// point: adding it to Transport would force every implementation and every test
// fake to grow a method most of them have no use for, and a transport that
// cannot do this should say so by NOT implementing it rather than by returning
// an error at the bottom of a call stack.
//
// Only one thing needs it: writing a file with privilege. `sudo` cannot be
// given a file to write, so the content has to arrive on stdin -- `sudo -n tee`
// -- and there is no shell to redirect with, because this project does not use
// one.
//
// A caller type-asserts and refuses clearly when the assertion fails. That is
// how "remote privileged writing is not in v0.2" is expressed: the SSH
// transport does not implement this, so the refusal names the reason instead of
// failing somewhere deeper.
type InputRunner interface {
	Transport

	// RunWithInput executes argv with data on its standard input.
	RunWithInput(ctx context.Context, argv []string, stdin []byte) (stdout, stderr []byte, exitCode int, err error)
}
