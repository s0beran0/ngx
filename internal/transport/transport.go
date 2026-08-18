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
