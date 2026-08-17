package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// ParseOptions controla a leitura. Open existe para permitir testes com
// filesystem em memoria, sem tocar disco.
type ParseOptions struct {
	Path string
	Open func(path string) (io.ReadCloser, error)
}

func (o ParseOptions) abrir(path string) (io.ReadCloser, error) {
	if o.Open != nil {
		return o.Open(path)
	}
	return os.Open(path)
}

// Parse le a configuracao e devolve a arvore canonica. Cada arquivo e
// parseado separadamente, preservando sua fonte: a resolucao de include e
// uma view construida sobre esta arvore, nao uma concatenacao previa, para
// que os spans continuem apontando para offsets reais de arquivos reais.
//
// Um parse com Status != "ok" nao vira erro no crossplane por si so: ele
// registra o problema em payload.Errors/cfg.Errors e segue adiante, o que
// deixaria a arvore com Source completo e zero Nodes numa config quebrada.
// Aqui isso e tratado como falha, preservando arquivo e linha via
// ParseErrors, para que a saida possa apontar o lugar exato do problema.
func Parse(opts ParseOptions) (*Tree, error) {
	cache := novaCacheFonte()
	abrirEspelhado := cache.decora(opts.abrir)

	payload, err := crossplane.Parse(opts.Path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
		Open:                      abrirEspelhado,
	})
	if err != nil {
		return nil, fmt.Errorf("ao parsear %s: %w", opts.Path, err)
	}

	if payload.Status != "ok" {
		return nil, coletarErros(payload)
	}

	tree := &Tree{}
	for _, cfg := range payload.Config {
		src, err := lerFonte(opts, cache, cfg.File)
		if err != nil {
			return nil, err
		}
		tree.Files = append(tree.Files, &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  converterDirectives(cfg.Parsed, cfg.File),
		})
	}
	return tree, nil
}

// coletarErros converte os problemas relatados pelo crossplane num unico
// erro localizado. Os problemas aparecem tanto em payload.Errors quanto no
// Errors de cada Config afetado -- o crossplane grava a mesma ocorrencia
// nos dois lugares --, entao aqui eles sao deduplicados por arquivo, linha
// e mensagem antes de virar ParseErrors.
func coletarErros(payload *crossplane.Payload) error {
	var problemas ParseErrors
	visto := map[string]bool{}

	adicionar := func(arquivo string, linha *int, causa error) {
		if causa == nil {
			return
		}
		l := 0
		if linha != nil {
			l = *linha
		}
		chave := fmt.Sprintf("%s:%d:%s", arquivo, l, causa.Error())
		if visto[chave] {
			return
		}
		visto[chave] = true
		problemas = append(problemas, ParseError{File: arquivo, Line: l, Message: causa.Error()})
	}

	for _, pe := range payload.Errors {
		adicionar(pe.File, pe.Line, pe.Error)
	}
	for _, cfg := range payload.Config {
		for _, ce := range cfg.Errors {
			adicionar(cfg.File, ce.Line, ce.Error)
		}
	}

	if len(problemas) == 0 {
		problemas = append(problemas, ParseError{Message: "parse falhou sem detalhar o erro"})
	}
	return problemas
}

// cacheFonte guarda, por caminho de arquivo, os bytes que o crossplane
// efetivamente leu durante o parse. Sem isso, Source viria de uma segunda
// leitura de disco independente da que o crossplane tokenizou: as duas
// poderiam divergir (arquivo alterado entre as leituras, Open de uso
// unico, etc.), e a Task 9 casaria os spans com um conteudo que nao e o
// que foi de fato parseado.
type cacheFonte struct {
	mu    sync.Mutex
	dados map[string][]byte
}

func novaCacheFonte() *cacheFonte {
	return &cacheFonte{dados: map[string][]byte{}}
}

// decora envolve a funcao de abertura original interceptando os bytes
// lidos, sem alterar o comportamento observado pelo crossplane.
func (c *cacheFonte) decora(abrirOriginal func(string) (io.ReadCloser, error)) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		rc, err := abrirOriginal(path)
		if err != nil {
			return nil, err
		}
		return &leituraEspelhada{rc: rc, path: path, cache: c}, nil
	}
}

func (c *cacheFonte) obter(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.dados[path]
	return b, ok
}

func (c *cacheFonte) guardar(path string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dados[path] = b
}

// leituraEspelhada copia cada byte lido de um io.ReadCloser para um buffer
// interno, e grava esse buffer na cache quando o arquivo e fechado -- mas
// so se a leitura tiver terminado limpa (io.EOF sem nenhum outro erro).
//
// Duas coisas exigem isso:
//
//  1. O crossplane lexa cada arquivo numa goroutine separada e a abandona
//     em varios retornos antecipados do parser (include sem argumentos,
//     erro de Glob, erro de parse aninhado). Nesses caminhos o Close() do
//     arquivo roda enquanto a goroutine do lexer ainda pode estar lendo
//     do mesmo reader, entao o buffer precisa do seu proprio mutex -- o
//     mutex de cacheFonte so protege o mapa, nunca protegeu este buffer.
//  2. Para include de caminho explicito, o crossplane abre cada arquivo
//     duas vezes: uma sonda de legibilidade que nunca chama Read, e a
//     leitura real. A sonda nunca atinge eofLimpo, entao nunca grava nada
//     na cache -- sem isso, a sonda escreveria []byte{} para aquele
//     caminho e o fallback em lerFonte nunca disparia, porque a chave ja
//     existiria no mapa (mesmo vazia). Pelo mesmo motivo, uma leitura que
//     falha no meio do arquivo (erro de I/O real) tambem nunca grava nada
//     -- ela cai no fallback de lerFonte, que propaga o erro em vez de
//     devolver um Source truncado com err == nil.
type leituraEspelhada struct {
	rc    io.ReadCloser
	path  string
	cache *cacheFonte

	mu        sync.Mutex
	buf       bytes.Buffer
	eofLimpo  bool
	houveErro bool
}

func (l *leituraEspelhada) Read(p []byte) (int, error) {
	n, err := l.rc.Read(p)

	l.mu.Lock()
	if n > 0 {
		l.buf.Write(p[:n])
	}
	switch {
	case err == io.EOF:
		l.eofLimpo = true
	case err != nil:
		l.houveErro = true
	}
	l.mu.Unlock()

	return n, err
}

func (l *leituraEspelhada) Close() error {
	l.mu.Lock()
	if l.eofLimpo && !l.houveErro {
		l.cache.guardar(l.path, append([]byte(nil), l.buf.Bytes()...))
	}
	l.mu.Unlock()
	return l.rc.Close()
}

// lerFonte devolve os bytes que o crossplane leu para path durante o
// parse. Se o cache nao tiver esse arquivo -- por exemplo, um Config
// presente no payload sem leitura correspondente registrada -- cai de
// volta para uma leitura direta via opts.abrir, que ainda respeita
// ParseOptions.Open.
func lerFonte(opts ParseOptions, cache *cacheFonte, path string) ([]byte, error) {
	if b, ok := cache.obter(path); ok {
		return b, nil
	}

	rc, err := opts.abrir(path)
	if err != nil {
		return nil, fmt.Errorf("ao ler %s: %w", path, err)
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("ao ler %s: %w", path, err)
	}
	return b, nil
}

func converterDirectives(ds crossplane.Directives, file string) []*Node {
	nodes := make([]*Node, 0, len(ds))
	for _, d := range ds {
		n := &Node{
			Directive: d.Directive,
			Args:      clonarArgs(d.Args),
			File:      file,
			Line:      d.Line,
			Comment:   d.Comment,
			temBloco:  d.Block != nil,
		}
		if d.Block != nil {
			n.Block = converterDirectives(d.Block, file)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// clonarArgs copia os argumentos da diretiva para que nos construidos por
// tarefas futuras (a Task 12 monta nos novos a partir destes) nao
// compartilhem o array de backing com a arvore do crossplane: um append
// num Args copiado poderia sobrescrever o vizinho.
func clonarArgs(args []string) []string {
	if args == nil {
		return []string{}
	}
	return slices.Clone(args)
}
