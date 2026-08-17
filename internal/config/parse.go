package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// ParseOptions controla a leitura. Open existe para permitir testes com
// filesystem em memoria, sem tocar disco.
//
// Glob acompanha Open e nao e opcional na pratica: quem injeta um filesystem
// -- teste em memoria ou host remoto -- precisa injetar os dois. Sem Glob, o
// crossplane cai em filepath.Glob e resolve "include conf.d/*.conf" contra o
// disco LOCAL, entao a arvore misturaria arquivos da maquina de quem roda o
// comando com a configuracao que se pediu para ler.
type ParseOptions struct {
	Path string
	Open func(path string) (io.ReadCloser, error)
	Glob func(pattern string) ([]string, error)
}

func (o ParseOptions) abrir(path string) (io.ReadCloser, error) {
	if o.Open != nil {
		return o.Open(path)
	}
	return os.Open(path)
}

func (o ParseOptions) expandir(pattern string) ([]string, error) {
	if o.Glob != nil {
		return o.Glob(pattern)
	}
	return filepath.Glob(pattern)
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

	payload, err := parseComBarreira(opts.Path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
		Open:                      abrirEspelhado,
		Glob:                      opts.expandir,
	})

	// A recusa da validacao previa vem antes de qualquer erro do crossplane:
	// quando ela dispara, o Open devolve erro de proposito para que o parser
	// nunca chegue no statement quebrado, e o erro que o crossplane relata em
	// seguida e so o eco disso. Quem explica o problema e a recusa.
	if problemas := cache.recusas(); len(problemas) > 0 {
		return nil, problemas
	}

	// Falha de I/O na leitura tem precedencia sobre o que o crossplane
	// relatar em seguida, porque o que ele relata e consequencia dela e
	// aponta o arquivo errado. Sao dois desfechos: se o arquivo truncado era
	// o de topo (ou veio de um glob), o crossplane devolve o erro cru e a
	// mensagem sai com a string do runtime; se era alvo de um include
	// explicito, ele converte o erro do Open num ParseError localizado no
	// arquivo QUE FAZ o include, na linha do include -- e o consumidor
	// recebe "erro na linha N" para um .conf intacto e vai depurar o arquivo
	// errado. Quem sabe o que aconteceu, e em qual arquivo, e a leitura que
	// falhou; ela ja esta registrada no cache.
	if problemas := cache.errosDeLeitura(); len(problemas) > 0 {
		return nil, problemas
	}

	if err != nil {
		var problemas ParseErrors
		if errors.As(err, &problemas) {
			return nil, problemas
		}
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
		if err := alinhar(arquivo); err != nil {
			return nil, err
		}
		AtribuirIDs(arquivo.Nodes, "")
		tree.Files = append(tree.Files, arquivo)
	}
	tree.Hash = Hash(tree)
	return tree, nil
}

// parseComBarreira roda o parser do crossplane com uma rede de seguranca
// contra panic. Uma CLI cujo consumidor e um agente de IA nao pode emitir
// stack trace: isso nao e JSON, nao e legivel, e nao tem exit code util. O
// panic vira ParseErrors, que a camada de CLI ja traduz para o exit code de
// configuracao invalida (3, ver internal/cli/inspect.go).
//
// Cobre a goroutine do parser, que e esta; um panic dentro da goroutine do
// lexer do crossplane continuaria escapando, e nao ha como recupera-lo daqui.
// O caso conhecido -- prepareIfArgs (util.go:71-86) -- e do parser, e alem
// disso ja e barrado antes por validarExpressoesIf.
func parseComBarreira(path string, opts *crossplane.ParseOptions) (payload *crossplane.Payload, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		payload = nil
		err = ParseErrors{{
			File:    path,
			Message: fmt.Sprintf("o parser da dependencia entrou em panico nesta configuracao: %v", r),
			Classe:  RecusaPanicoDoCrossplane,
		}}
	}()
	return crossplane.Parse(path, opts)
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
	mu       sync.Mutex
	dados    map[string][]byte
	erros    map[string]error
	recusasV ParseErrors
}

func novaCacheFonte() *cacheFonte {
	return &cacheFonte{dados: map[string][]byte{}, erros: map[string]error{}}
}

// decora envolve a funcao de abertura original: le o arquivo inteiro, guarda
// os bytes lidos e devolve ao crossplane um leitor sobre esses MESMOS bytes.
// Duas coisas dependem disso.
//
// A primeira e Source: sem a copia, ele viria de uma segunda leitura de disco
// independente da que o crossplane tokenizou, e as duas poderiam divergir
// (arquivo alterado entre as leituras, Open de uso unico), o que faria os
// spans da Task 9 casarem com um conteudo que nao foi o parseado.
//
// A segunda e a validacao previa: e aqui, antes de o primeiro token chegar ao
// parser, o unico ponto em que da para recusar um "if" mal formado ANTES de
// prepareIfArgs derrubar o processo (ver expressao_if.go). Uma versao
// anterior espelhava a leitura em streaming, e ali nao havia esse ponto: o
// parser ja consome tokens enquanto o lexer ainda le. A leitura de uma vez
// tambem elimina a concorrencia entre Read e Close que o streaming tinha (o
// crossplane lexa cada arquivo numa goroutine e a abandona em varios retornos
// antecipados: include sem argumentos, erro de Glob, erro de parse aninhado).
//
// Erro de leitura nao vira leitura parcial: e registrado e devolvido, para
// que o crossplane pare em vez de tokenizar um prefixo -- um Source truncado
// seria pior que o erro, porque os spans ficariam coerentes com ele e uma
// reescrita da v0.2 truncaria o arquivo do usuario.
func (c *cacheFonte) decora(abrirOriginal func(string) (io.ReadCloser, error)) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		rc, err := abrirOriginal(path)
		if err != nil {
			return nil, err
		}

		if problemas := recusarAlvoNaoRegular(path, rc); len(problemas) > 0 {
			_ = rc.Close()
			c.guardarRecusas(problemas)
			return nil, problemas
		}

		conteudo, err := io.ReadAll(rc)
		if erroFechar := rc.Close(); err == nil {
			err = erroFechar
		}
		if err != nil {
			c.guardarErro(path, err)
			return nil, err
		}

		if problemas := validarExpressoesIf(path, conteudo); len(problemas) > 0 {
			c.guardarRecusas(problemas)
			return nil, problemas
		}

		c.guardar(path, conteudo)
		return io.NopCloser(bytes.NewReader(conteudo)), nil
	}
}

// recusarAlvoNaoRegular recusa um caminho que abriu mas nao e arquivo
// regular -- diretorio, socket, fifo, dispositivo.
//
// O crossplane aceita: para um alvo de include sem caractere de glob,
// parse.go:385-395 so confere que o os.Open funciona ("nginx will check that
// the included file can be opened and read"), e abrir diretorio funciona; o
// alvo entra em fnames, e lexado no laco de parse.go:161-168, e como o lexer
// nao consulta o erro de leitura o payload sai com Status "ok" e zero
// diretiva. O nginx, ao contrario do que o comentario deles diz, LE o alvo, e
// ler diretorio falha -- entao recusar e o comportamento do nginx.
//
// Sem esta checagem a recusa acontecia de qualquer jeito, mas pelo io.ReadAll,
// e o diagnostico saia com a string crua do runtime ("read /tmp/x: is a
// directory"). Numa CLI feita para ser lida por agente, mensagem de erro e
// contrato: ela tem que ser nossa e ter classe.
//
// A checagem depende de o io.ReadCloser saber se descrever (os.File sabe). Um
// ParseOptions.Open que devolva um leitor em memoria nao tem alvo no
// filesystem e simplesmente nao entra aqui.
func recusarAlvoNaoRegular(path string, rc io.ReadCloser) ParseErrors {
	comStat, ok := rc.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return nil
	}
	info, err := comStat.Stat()
	if err != nil || info.Mode().IsRegular() {
		return nil
	}

	tipo := "nao e um arquivo regular"
	if info.IsDir() {
		tipo = "e um diretorio"
	}
	return ParseErrors{{
		File:    path,
		Message: fmt.Sprintf("%s: configuracao precisa ser arquivo regular", tipo),
		Classe:  RecusaAlvoNaoERegular,
	}}
}

func (c *cacheFonte) guardarRecusas(problemas ParseErrors) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recusasV = append(c.recusasV, problemas...)
}

func (c *cacheFonte) recusas() ParseErrors {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recusasV
}

// errosDeLeitura converte as falhas de I/O registradas durante o parse em
// recusas nossas: uma por arquivo, com o caminho de quem de fato falhou e
// mensagem propria. A string crua do runtime ("read tcp ...: connection reset
// by peer") fica de fora do diagnostico pelo mesmo motivo de
// recusarAlvoNaoRegular: numa CLI lida por agente a mensagem e contrato, tem
// que ser nossa e ter classe.
//
// A ordem sai por caminho para que dois parses da mesma configuracao quebrada
// produzam o mesmo diagnostico -- a iteracao de map em Go e aleatoria.
func (c *cacheFonte) errosDeLeitura() ParseErrors {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.erros) == 0 {
		return nil
	}

	caminhos := make([]string, 0, len(c.erros))
	for path := range c.erros {
		caminhos = append(caminhos, path)
	}
	slices.Sort(caminhos)

	problemas := make(ParseErrors, 0, len(caminhos))
	for _, path := range caminhos {
		problemas = append(problemas, ParseError{
			File:    path,
			Message: "a leitura deste arquivo falhou antes do fim: a configuracao nao pode ser lida por inteiro",
			Classe:  RecusaFalhaDeLeitura,
		})
	}
	return problemas
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
