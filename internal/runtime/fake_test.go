package runtime

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
)

// resposta is a recorded output of a command: what a real nginx wrote, frozen
// into a test.
type resposta struct {
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
	descricao string

	// respostas is indexed by the whole argv, joined by spaces.
	respostas map[string]resposta

	// padrao answers any argv that is not mapped.
	padrao *resposta

	arquivos   map[string]string
	errosOpen  map[string]error
	mu         sync.Mutex
	executados [][]string
}

func novoFake(descricao string) *fakeTransport {
	return &fakeTransport{
		descricao: descricao,
		respostas: map[string]resposta{},
		arquivos:  map[string]string{},
		errosOpen: map[string]error{},
	}
}

func (f *fakeTransport) responde(argv string, r resposta) *fakeTransport {
	f.respostas[argv] = r
	return f
}

func (f *fakeTransport) Open(path string) (io.ReadCloser, error) {
	if err, ok := f.errosOpen[path]; ok {
		return nil, err
	}
	if conteudo, ok := f.arquivos[path]; ok {
		return io.NopCloser(strings.NewReader(conteudo)), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeTransport) Glob(pattern string) ([]string, error) {
	return []string{}, nil
}

func (f *fakeTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	copia := append([]string(nil), argv...)
	f.executados = append(f.executados, copia)
	f.mu.Unlock()

	chave := strings.Join(argv, " ")
	r, ok := f.respostas[chave]
	if !ok {
		if f.padrao == nil {
			return nil, []byte("fake: unrecorded argv: " + chave), 127, nil
		}
		r = *f.padrao
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, r.err
}

func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) Describe() string { return f.descricao }

// chamadas returns the argv of each execution, in order. It is what allows
// asserting that ngx did not retry a command with sudo on its own.
func (f *fakeTransport) chamadas() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.executados...)
}
