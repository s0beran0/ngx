package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

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

// leitorLento entrega os bytes de dados em pedacos pequenos com uma pausa
// entre cada leitura. Um arquivo pequeno lido do disco real costuma vir
// inteiro (ou quase) numa unica chamada de Read do bufio.Scanner, entao o
// timing organico nao garante que a goroutine do lexer ainda esteja lendo
// no instante em que o parser bate no erro e fecha o arquivo -- a pausa
// artificial torna essa sobreposicao praticamente garantida, em vez de
// depender de sorte de agendamento do scheduler.
type leitorLento struct {
	dados []byte
	pos   int
}

func (l *leitorLento) Read(p []byte) (int, error) {
	if l.pos >= len(l.dados) {
		return 0, io.EOF
	}
	time.Sleep(200 * time.Microsecond)

	fim := l.pos + 32
	if fim > len(l.dados) {
		fim = len(l.dados)
	}
	n := copy(p, l.dados[l.pos:fim])
	l.pos += n
	return n, nil
}

func (l *leitorLento) Close() error { return nil }

// Um "include;" sem argumento faz o parser do crossplane retornar
// imediatamente, abandonando a goroutine do lexer daquele arquivo -- que
// continua lendo do mesmo reader (o fixture tem bem mais de 2048 tokens
// depois do include quebrado, a capacidade do canal de tokens). O Close()
// do arquivo roda no meio disso. Antes do round 2 do fix, leituraEspelhada
// nao tinha mutex proprio no buffer, e essa concorrencia real dava data
// race sob -race -- reproduzido manualmente revertendo leituraEspelhada
// para a versao do round 1 e rodando este mesmo teste. Este teste so trava
// a regressao quando rodado com go test -race.
func TestParseComIncludeSemArgumentosNaoTemDataRace(t *testing.T) {
	dados, err := os.ReadFile(filepath.Join("testdata", "include_sem_args.conf"))
	require.NoError(t, err)

	abrir := func(path string) (io.ReadCloser, error) {
		return &leitorLento{dados: dados}, nil
	}

	_, err = config.Parse(config.ParseOptions{
		Path: "include_sem_args.conf",
		Open: abrir,
	})

	// O include sem argumento e, por si so, um problema que o crossplane
	// reporta via Status/Errors -- entao um erro aqui e esperado. O que
	// este teste trava e a ausencia de data race, nao o valor do erro.
	require.Error(t, err)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas))
}

// leitorComFalha devolve um numero fixo de bytes e depois passa a
// devolver um erro de I/O real (nao io.EOF) em toda chamada seguinte,
// simulando uma falha no meio da leitura de um arquivo -- ex.: um FS de
// rede que cai depois de entregar as primeiras linhas.
type leitorComFalha struct {
	restante []byte
}

func (l *leitorComFalha) Read(p []byte) (int, error) {
	if len(l.restante) == 0 {
		return 0, errors.New("falha de i/o simulada no meio do arquivo")
	}
	n := copy(p, l.restante)
	l.restante = l.restante[n:]
	return n, nil
}

func (l *leitorComFalha) Close() error { return nil }

// Antes do round 2 do fix, leituraEspelhada.Close gravava incondicionalmente
// o que houvesse no buffer, mesmo que a leitura subjacente tivesse
// terminado num erro real em vez de io.EOF. O crossplane, por sua vez, nao
// consulta scanner.Err(), entao esse erro de I/O terminava a tokenizacao em
// silencio -- e config.Parse devolveria uma Tree com Source truncado e
// err == nil. Um Source truncado e mais perigoso que o defeito original do
// round 1: os spans da Task 9 ficariam coerentes com esse Source, e uma
// escrita de volta por substituicao de bytes truncaria o arquivo real do
// usuario.
func TestParseFalhaDeIONaoTruncaSourceSilenciosamente(t *testing.T) {
	primeiraLinha := "worker_processes auto;\n"

	abrir := func(path string) (io.ReadCloser, error) {
		return &leitorComFalha{restante: []byte(primeiraLinha)}, nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "qualquer/nginx.conf",
		Open: abrir,
	})

	require.Error(t, err, "uma falha de i/o no meio do arquivo precisa propagar como erro, nao produzir uma Tree com Source truncado e err nil")
	require.Nil(t, tree)
}

// Antes do round 3 do fix, uma leitura que falhasse no meio mas cuja
// releitura do fallback tivesse sucesso -- uma falha de I/O transitoria --
// produzia err == nil, Source completo (da releitura bem-sucedida) e Nodes
// contendo so o prefixo que o lexer alcancou antes do erro original: uma
// arvore parcial com sucesso silencioso. Esse teste usa um Open que falha
// na primeira leitura mas teria sucesso numa segunda, para provar que
// lerFonte propaga o erro registrado em vez de reler o arquivo.
func TestParseFalhaDeIOTransitoriaNaoViraArvoreParcial(t *testing.T) {
	completo := []byte("worker_processes auto;\nevents {\n    worker_connections 1024;\n}\n")
	primeiraLinha := completo[:len("worker_processes auto;\n")]

	var chamadas int
	abrir := func(path string) (io.ReadCloser, error) {
		chamadas++
		if chamadas == 1 {
			// primeira leitura: falha depois da primeira linha.
			return &leitorComFalha{restante: append([]byte{}, primeiraLinha...)}, nil
		}
		// se lerFonte relesse o arquivo, essa segunda leitura teria
		// sucesso total -- e e exatamente isso que nao pode acontecer.
		return io.NopCloser(bytes.NewReader(completo)), nil
	}

	tree, err := config.Parse(config.ParseOptions{
		Path: "qualquer/nginx.conf",
		Open: abrir,
	})

	require.Error(t, err, "uma falha de i/o transitoria precisa propagar como erro, nao produzir uma arvore parcial com sucesso silencioso numa releitura")
	require.Nil(t, tree)
	require.Equal(t, 1, chamadas, "lerFonte nao deve reler o arquivo quando ja ha um erro registrado para aquele caminho")
}
