package runtime

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
)

// response is a recorded output of a command: what a real nginx wrote, frozen
// into a test.
type response struct {
	stdout string
	stderr string
	exit   int
	err    error
}

// fakeTransport returns recorded outputs. No test in this package opens a
// connection, executes nginx or touches the disk beyond what it creates
// itself: the purpose is to prove that the parsers do not know where the bytes
// came from, and a test that depended on an installed nginx would prove the
// opposite.
type fakeTransport struct {
	description string

	// responses is indexed by the whole argv, joined by spaces.
	responses map[string]response

	// fallback answers any argv that is not mapped.
	fallback *response

	files      map[string]string
	openErrors map[string]error
	mu         sync.Mutex
	executed   [][]string
}

func newFake(description string) *fakeTransport {
	return &fakeTransport{
		description: description,
		responses:   map[string]response{},
		files:       map[string]string{},
		openErrors:  map[string]error{},
	}
}

func (f *fakeTransport) respond(argv string, r response) *fakeTransport {
	f.responses[argv] = r
	return f
}

func (f *fakeTransport) Open(path string) (io.ReadCloser, error) {
	if err, ok := f.openErrors[path]; ok {
		return nil, err
	}
	if content, ok := f.files[path]; ok {
		return io.NopCloser(strings.NewReader(content)), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeTransport) Glob(pattern string) ([]string, error) {
	return []string{}, nil
}

func (f *fakeTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	cloned := append([]string(nil), argv...)
	f.executed = append(f.executed, cloned)
	f.mu.Unlock()

	key := strings.Join(argv, " ")
	r, ok := f.responses[key]
	if !ok {
		if f.fallback == nil {
			return nil, []byte("fake: unrecorded argv: " + key), 127, nil
		}
		r = *f.fallback
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, r.err
}

func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) Describe() string { return f.description }

// calls returns the argv of each execution, in order. It is what allows
// asserting that ngx did not retry a command with sudo on its own.
func (f *fakeTransport) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.executed...)
}
