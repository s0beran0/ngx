package config

import (
	"bytes"
	"errors"
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
		arquivo := &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  converterDirectives(cfg.Parsed, cfg.File),
		}
		AtribuirIDs(arquivo.Nodes, "")
		tree.Files = append(tree.Files, arquivo)
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
	erros map[string]error
}

func novaCacheFonte() *cacheFonte {
	return &cacheFonte{dados: map[string][]byte{}, erros: map[string]error{}}
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

func (c *cacheFonte) obterErro(path string) (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.erros[path]
	return e, ok
}

func (c *cacheFonte) guardar(path string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dados[path] = b
}

func (c *cacheFonte) guardarErro(path string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.erros[path] = err
}

// leituraEspelhada copia cada byte lido de um io.ReadCloser para um buffer
// interno, e grava o resultado na cache quando o arquivo e fechado: os
// bytes lidos se a leitura terminou limpa (io.EOF sem nenhum outro erro),
// ou o erro em si caso contrario. lerFonte usa esse resultado para nunca
// reler o arquivo -- nem para servir Source, nem para tentar recuperar de
// um erro -- porque uma segunda leitura poderia devolver algo diferente
// do que o crossplane realmente tokenizou.
//
// O gate eofLimpo && !houveErro existe para nao cachear uma leitura
// incompleta como se fosse o conteudo inteiro do arquivo. Sem ele, uma
// leitura interrompida no meio (erro de I/O real, ou o Close() de um
// arquivo cuja goroutine de lexer foi abandonada antes do fim) gravaria
// um prefixo do arquivo como se fosse o todo.
//
// O mutex protege so o buf/eofLimpo/houveErro/erro deste tipo -- Read e
// Close rodam em goroutines diferentes (o crossplane lexa cada arquivo
// numa goroutine separada e a abandona em varios retornos antecipados do
// parser: include sem argumentos, erro de Glob, erro de parse aninhado),
// entao esses campos precisam de protecao propria; o mutex de cacheFonte
// so protege os mapas dele, nunca protegeu este buffer. O mutex NAO
// protege rc.Read nem rc.Close: essas chamadas continuam acontecendo sem
// sincronizacao entre as duas goroutines. Para os.File isso e seguro
// porque o runtime faz o proprio refcounting do descritor internamente.
// Para um ParseOptions.Open fornecido por quem usa este pacote, cujo
// Close feche algo com estado (por exemplo um buffer compartilhado), a
// mesma classe de race pode continuar existindo do lado do reader
// delegado -- este tipo nao cobre isso.
type leituraEspelhada struct {
	rc    io.ReadCloser
	path  string
	cache *cacheFonte

	mu        sync.Mutex
	buf       bytes.Buffer
	eofLimpo  bool
	houveErro bool
	erro      error
}

func (l *leituraEspelhada) Read(p []byte) (int, error) {
	n, err := l.rc.Read(p)

	l.mu.Lock()
	if n > 0 {
		l.buf.Write(p[:n])
	}
	switch {
	case errors.Is(err, io.EOF):
		l.eofLimpo = true
	case err != nil:
		l.houveErro = true
		l.erro = err
	}
	l.mu.Unlock()

	return n, err
}

func (l *leituraEspelhada) Close() error {
	l.mu.Lock()
	switch {
	case l.eofLimpo && !l.houveErro:
		l.cache.guardar(l.path, append([]byte(nil), l.buf.Bytes()...))
	case l.houveErro:
		l.cache.guardarErro(l.path, l.erro)
	}
	l.mu.Unlock()
	return l.rc.Close()
}

// lerFonte devolve os bytes que o crossplane leu para path durante o
// parse. Se aquela leitura registrou um erro, lerFonte propaga esse erro
// em vez de reler o arquivo: uma releitura poderia ter sucesso mesmo
// quando a leitura original que o crossplane de fato tokenizou falhou --
// uma falha de I/O transitoria, por exemplo --, o que produziria uma
// Tree com Source completo e Nodes correspondendo so ao prefixo que o
// lexer alcancou antes do erro, com err == nil escondendo o problema.
//
// So cai para uma leitura direta via opts.abrir (que ainda respeita
// ParseOptions.Open) quando o cache nao tem nem bytes nem erro para esse
// caminho -- um Config presente no payload sem leitura correspondente
// registrada, o que nao deveria acontecer no caminho normal do
// crossplane, mas serve de rede de seguranca.
func lerFonte(opts ParseOptions, cache *cacheFonte, path string) ([]byte, error) {
	if erroLeitura, ok := cache.obterErro(path); ok {
		return nil, fmt.Errorf("ao ler %s: %w", path, erroLeitura)
	}

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
