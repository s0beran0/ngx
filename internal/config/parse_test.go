package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseSimples(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simples.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseProduzUmArquivoComFonte(t *testing.T) {
	tree := parseSimples(t)

	require.Len(t, tree.Files, 1)
	require.NotEmpty(t, tree.Files[0].Source, "a fonte original precisa ser guardada para os spans")
	require.Contains(t, tree.Files[0].Path, "simples.conf")
}

func TestParsePreservaComentarios(t *testing.T) {
	tree := parseSimples(t)

	var comentarios int
	tree.Walk(func(n *config.Node) bool {
		if n.IsComment() {
			comentarios++
			require.NotNil(t, n.Comment)
			require.Contains(t, *n.Comment, "configuracao de exemplo")
		}
		return true
	})

	require.Equal(t, 1, comentarios)
}

func TestParseMonstaBlocosAninhados(t *testing.T) {
	tree := parseSimples(t)

	var http *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})

	require.NotNil(t, http)
	require.True(t, http.HasBlock())

	var servers, upstreams int
	for _, filho := range http.Block {
		switch filho.Directive {
		case "server":
			servers++
		case "upstream":
			upstreams++
		}
	}
	require.Equal(t, 1, servers)
	require.Equal(t, 1, upstreams)
}

func TestParseGuardaArgumentosEArquivo(t *testing.T) {
	tree := parseSimples(t)

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.NotNil(t, listen)
	require.Equal(t, []string{"443", "ssl"}, listen.Args)
	require.Contains(t, listen.File, "simples.conf")
}

func TestParseArquivoInexistenteVirarErro(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{Path: "testdata/nao-existe.conf"})

	require.Error(t, err)
}

// A redacao acontece na saida: a arvore em memoria mantem o valor real, senao
// fmt gravaria *** dentro do .conf do usuario.
func TestArvoreEmMemoriaNaoEhRedigida(t *testing.T) {
	tree := parseSimples(t)

	var achou bool
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "ssl_certificate_key" {
			achou = true
			require.Equal(t, []string{"/etc/ssl/private/api.key"}, n.Args)
		}
		return true
	})
	require.True(t, achou)
}

// O crossplane nao aborta num erro de sintaxe: ele registra o problema em
// payload.Errors/cfg.Errors e devolve err == nil. Sem esse tratamento,
// TestParseErroDeSintaxeViraParseErrors falharia porque config.Parse
// devolveria uma *Tree com Source mas zero Nodes, e nenhum erro.
func TestParseErroDeSintaxeViraParseErrors(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "erro_sintaxe.conf"),
	})

	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "o erro devolvido precisa ser (ou envolver) config.ParseErrors")
	require.NotEmpty(t, problemas)
	require.NotEmpty(t, problemas[0].File)
	require.NotZero(t, problemas[0].Line)
}

// Um include apontando para um arquivo inexistente e o mesmo tipo de
// defeito silencioso: o crossplane marca o problema no arquivo que faz o
// include, sem gerar Config nenhum para o arquivo ausente e sem devolver
// erro pela via normal.
func TestParseIncludeQuebradoViraParseErrors(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "include_quebrado.conf"),
	})

	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "o erro devolvido precisa ser (ou envolver) config.ParseErrors")
	require.NotEmpty(t, problemas)
	require.NotEmpty(t, problemas[0].File)
	require.NotZero(t, problemas[0].Line)
}

// ParseOptions.Open e o unico gancho de teste sem disco do pacote: precisa
// ser exercitado com um filesystem em memoria, e precisa ser a unica fonte
// de leitura -- um caminho ausente do FS em memoria tem que falhar mesmo
// que exista de verdade no disco.
func TestParseComFilesystemEmMemoria(t *testing.T) {
	memFS := map[string][]byte{
		"mem/nginx.conf": []byte("worker_processes auto;\n"),
	}
	abrir := func(path string) (io.ReadCloser, error) {
		b, ok := memFS[path]
		if !ok {
			return nil, fmt.Errorf("arquivo nao existe no fs em memoria: %s", path)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "mem/nginx.conf",
		Open: abrir,
	})
	require.NoError(t, err)
	require.Len(t, tree.Files, 1)
	require.Equal(t, memFS["mem/nginx.conf"], tree.Files[0].Source)

	// simples.conf existe de verdade no disco, mas nao esta no FS em
	// memoria: o Open injetado precisa ser a unica fonte, sem fallback.
	_, err = config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simples.conf"),
		Open: abrir,
	})
	require.Error(t, err)
}

// A tag json:"-" em File.Source e o unico anteparo contra os bytes crus do
// .conf -- onde caminhos de chave privada aparecem em texto -- vazarem na
// saida JSON por baixo da redacao, que so age sobre os argumentos. Este
// teste trava a forma serializada de File para que a remocao acidental da
// tag quebre a build.
func TestFileNaoSerializaSource(t *testing.T) {
	f := &config.File{
		Path:   "exemplo.conf",
		Source: []byte("segredo-que-nao-pode-vazar"),
		Nodes:  []*config.Node{},
	}

	b, err := json.Marshal(f)
	require.NoError(t, err)
	require.NotContains(t, string(b), "segredo-que-nao-pode-vazar")

	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	require.Equal(t, []string{"file", "parsed"}, chaves)
}
