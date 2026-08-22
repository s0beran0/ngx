package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// localTransport operates on the machine where ngx runs. It keeps no state:
// it is a thin wrapper over os/exec/filepath.
type localTransport struct{}

// Local returns the transport for the local machine.
func Local() Transport {
	return &localTransport{}
}

func (t *localTransport) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (t *localTransport) Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if matches == nil {
		// Empty list, never nil: consumers call len() without checking.
		return []string{}, nil
	}
	return matches, nil
}

func (t *localTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: empty argv")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// The context wins over anything else: a process killed by
	// cancellation also returns an ExitError, but that is a transport
	// failure, not the command's verdict.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, ctxErr
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The command ran and rejected. That is a result, not an error.
			return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
		}
		// Missing binary, no execute permission, and the like.
		return stdout.Bytes(), stderr.Bytes(), 0, err
	}

	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// RunWithInput is Run with data on the command's standard input.
//
// It exists for one caller: writing a file with privilege, where the content
// reaches `sudo -n tee` this way because there is no shell to redirect with.
//
// The pipe is closed by exec once the command exits, and the data is handed
// over as a reader rather than written by hand, so a command that ignores its
// input cannot deadlock this.
func (t *localTransport) RunWithInput(ctx context.Context, argv []string, stdin []byte) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: empty argv")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), stderr.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

func (t *localTransport) Close() error {
	return nil
}

func (t *localTransport) Describe() string {
	return "local"
}
