package runtime

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"
)

// resposta e uma saida gravada de um comando: o que um nginx real escreveu,
// congelado num teste.
type resposta struct {
	stdout string
	stderr string
	exit   int
	err    error
}

// fakeTransport devolve saidas gravadas. Nenhum teste deste pacote abre
// conexao, executa nginx ou toca o disco fora do que ele mesmo cria: o
// proposito e provar que os parsers nao sabem de onde os bytes vieram, e um
// teste que dependesse de um nginx instalado provaria o contrario.
type fakeTransport struct {
	descricao string

	// respostas e indexada pelo argv inteiro, juntado por espaco.
	respostas map[string]resposta

	// padrao responde qualquer argv nao mapeado.
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
			return nil, []byte("fake: argv nao gravado: " + chave), 127, nil
		}
		r = *f.padrao
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, r.err
}

func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) Describe() string { return f.descricao }

// chamadas devolve o argv de cada execucao, na ordem. E o que permite afirmar
// que o ngx nao repetiu um comando com sudo por conta propria.
func (f *fakeTransport) chamadas() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.executados...)
}
