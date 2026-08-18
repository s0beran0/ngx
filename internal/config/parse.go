package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// ParseOptions controls the reading. Open is there so that tests can run
// against an in-memory filesystem, without touching disk.
//
// Glob comes along with Open and is not optional in practice: whoever injects
// a filesystem -- an in-memory test or a remote host -- has to inject both.
// Without Glob, crossplane falls back to filepath.Glob and resolves "include
// conf.d/*.conf" against the LOCAL disk, so the tree would mix files from the
// machine running the command with the configuration it was asked to read.
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

// Parse reads the configuration and returns the canonical tree. Each file is
// parsed separately, with its source preserved: include resolution is a view
// built on top of this tree, not an up-front concatenation, so that the spans
// keep pointing at real offsets of real files.
//
// A parse with Status != "ok" does not become an error in crossplane by
// itself: it records the problem in payload.Errors/cfg.Errors and carries on,
// which would leave the tree with a complete Source and zero Nodes for a
// broken config. Here that is treated as a failure, keeping file and line in
// ParseErrors so that the output can point at the exact spot of the problem.
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

	// The refusal from the up-front validation comes before any crossplane
	// error: when it fires, Open returns an error on purpose so that the
	// parser never reaches the broken statement, and the error crossplane
	// reports next is only an echo of that. What explains the problem is the
	// refusal.
	if problemas := cache.recusas(); len(problemas) > 0 {
		return nil, problemas
	}

	// An I/O failure while reading takes precedence over whatever crossplane
	// reports next, because what it reports is a consequence of it and points
	// at the wrong file. There are two outcomes: if the truncated file was
	// the top-level one (or came from a glob), crossplane returns the raw
	// error and the message goes out with the runtime string; if it was the
	// target of an explicit include, it turns the Open error into a
	// ParseError located in the file THAT DOES the include, on the include
	// line -- and the consumer gets "error on line N" for an intact .conf and
	// goes off debugging the wrong file. What knows what happened, and in
	// which file, is the read that failed; it is already recorded in the cache.
	if problemas := cache.errosDeLeitura(); len(problemas) > 0 {
		return nil, problemas
	}

	if err != nil {
		var problemas ParseErrors
		if errors.As(err, &problemas) {
			return nil, problemas
		}
		return nil, fmt.Errorf("while parsing %s: %w", opts.Path, err)
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

// parseComBarreira runs crossplane's parser behind a safety net against
// panics. A CLI whose consumer is an AI agent cannot emit a stack trace: that
// is not JSON, is not readable, and carries no useful exit code. The panic
// becomes ParseErrors, which the CLI layer already translates into the exit
// code for an invalid configuration (3, see internal/cli/inspect.go).
//
// It covers the parser goroutine, which is this one; a panic inside
// crossplane's lexer goroutine would still escape, and there is no way to
// recover it from here. The known case -- prepareIfArgs (util.go:71-86) -- is
// in the parser, and besides is already blocked earlier by validarExpressoesIf.
func parseComBarreira(path string, opts *crossplane.ParseOptions) (payload *crossplane.Payload, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		payload = nil
		err = ParseErrors{{
			File:    path,
			Message: fmt.Sprintf("the dependency parser panicked on this configuration: %v", r),
			Classe:  RecusaPanicoDoCrossplane,
		}}
	}()
	return crossplane.Parse(path, opts)
}

// coletarErros turns the problems crossplane reported into a single located
// error. The problems show up both in payload.Errors and in the Errors of
// each affected Config -- crossplane records the same occurrence in both
// places --, so here they are deduplicated by file, line and message before
// becoming ParseErrors.
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
		problemas = append(problemas, ParseError{Message: "parse failed without detailing the error"})
	}
	return problemas
}

// cacheFonte keeps, per file path, the bytes crossplane actually read during
// the parse. Without it, Source would come from a second disk read
// independent of the one crossplane tokenized: the two could diverge (file
// changed between the reads, single-use Open, and so on), and Task 9 would
// match the spans against content that is not what was in fact parsed.
type cacheFonte struct {
	mu       sync.Mutex
	dados    map[string][]byte
	erros    map[string]error
	recusasV ParseErrors
}

func novaCacheFonte() *cacheFonte {
	return &cacheFonte{dados: map[string][]byte{}, erros: map[string]error{}}
}

// decora wraps the original open function: it reads the whole file, keeps the
// bytes read and hands crossplane a reader over those SAME bytes. Two things
// depend on that.
//
// The first is Source: without the copy it would come from a second disk read
// independent of the one crossplane tokenized, and the two could diverge
// (file changed between the reads, single-use Open), which would make the
// spans of Task 9 match content that was not the one parsed.
//
// The second is the up-front validation: right here, before the first token
// reaches the parser, is the only point where a malformed "if" can be refused
// BEFORE prepareIfArgs brings the process down (see expressao_if.go). An
// earlier version mirrored the read in streaming fashion, and there was no
// such point there: the parser already consumes tokens while the lexer is
// still reading. Reading it all at once also removes the race between Read
// and Close that the streaming version had (crossplane lexes each file in a
// goroutine and abandons it on several early returns: include with no
// arguments, Glob error, nested parse error).
//
// A read error does not become a partial read: it is recorded and returned,
// so that crossplane stops instead of tokenizing a prefix -- a truncated
// Source would be worse than the error, because the spans would be coherent
// with it and a v0.2 rewrite would truncate the user's file.
func (c *cacheFonte) decora(abrirOriginal func(string) (io.ReadCloser, error)) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		rc, err := abrirOriginal(path)
		if err != nil {
			// Failing to OPEN is an I/O failure too, and has to be recorded
			// like the read ones. Locally opening almost always works, which
			// is why the case slipped through; against a remote target it is
			// routine -- measured against a production nginx, one file out of
			// 128 was not readable by the connection user. Without the
			// record, crossplane returns a generic error and the diagnostic
			// blames the top-level file instead of the one that actually
			// failed, sending the reader off to debug the wrong place.
			//
			// A MISSING file is the exception, and is left out: `include
			// /does/not/exist.conf` is a configuration error, not an
			// environment one, and what helps there is the LINE of the
			// include -- which crossplane reports and which would be lost
			// here, because a file that never opened has no line at all to
			// offer.
			if !errors.Is(err, fs.ErrNotExist) {
				c.guardarErro(path, err)
			}
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

// recusarAlvoNaoRegular refuses a path that opened but is not a regular file
// -- directory, socket, fifo, device.
//
// Crossplane accepts it: for an include target with no glob character,
// parse.go:385-395 only checks that os.Open works ("nginx will check that the
// included file can be opened and read"), and opening a directory works; the
// target goes into fnames, is lexed in the loop of parse.go:161-168, and
// since the lexer never consults the read error the payload comes out with
// Status "ok" and zero directives. nginx, contrary to what their comment
// says, does READ the target, and reading a directory fails -- so refusing is
// nginx's behavior.
//
// Without this check the refusal happened anyway, but via io.ReadAll, and the
// diagnostic went out with the raw runtime string ("read /tmp/x: is a
// directory"). In a CLI meant to be read by an agent, an error message is a
// contract: it has to be ours and it has to carry a class.
//
// The check depends on the io.ReadCloser being able to describe itself
// (os.File can). A ParseOptions.Open that returns an in-memory reader has no
// target on the filesystem and simply never gets here.
func recusarAlvoNaoRegular(path string, rc io.ReadCloser) ParseErrors {
	comStat, ok := rc.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return nil
	}
	info, err := comStat.Stat()
	if err != nil || info.Mode().IsRegular() {
		return nil
	}

	tipo := "not a regular file"
	if info.IsDir() {
		tipo = "is a directory"
	}
	return ParseErrors{{
		File:    path,
		Message: fmt.Sprintf("%s: a configuration has to be a regular file", tipo),
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

// errosDeLeitura turns the I/O failures recorded during the parse into
// refusals of our own: one per file, with the path of whoever actually failed
// and a message of ours. The raw runtime string ("read tcp ...: connection
// reset by peer") is kept out of the diagnostic for the same reason as in
// recusarAlvoNaoRegular: in a CLI read by an agent the message is a contract,
// it has to be ours and it has to carry a class.
//
// The order comes out by path so that two parses of the same broken
// configuration produce the same diagnostic -- map iteration in Go is random.
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
			Message: mensagemDeFalhaDeLeitura(c.erros[path]),
			Classe:  classeDeFalhaDeLeitura(c.erros[path]),
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

// lerFonte returns the bytes crossplane read for path during the parse. If
// that read recorded an error, lerFonte propagates the error instead of
// re-reading the file: a second read could succeed even when the original
// read crossplane actually tokenized had failed -- a transient I/O failure,
// for instance --, which would produce a Tree with a complete Source and
// Nodes matching only the prefix the lexer reached before the error, with err
// == nil hiding the problem.
//
// It only falls back to a direct read through opts.abrir (which still honors
// ParseOptions.Open) when the cache has neither bytes nor an error for that
// path -- a Config present in the payload with no corresponding read
// recorded, which should not happen on crossplane's normal path, but serves
// as a safety net.
func lerFonte(opts ParseOptions, cache *cacheFonte, path string) ([]byte, error) {
	if erroLeitura, ok := cache.obterErro(path); ok {
		return nil, fmt.Errorf("while reading %s: %w", path, erroLeitura)
	}

	if b, ok := cache.obter(path); ok {
		return b, nil
	}

	rc, err := opts.abrir(path)
	if err != nil {
		return nil, fmt.Errorf("while reading %s: %w", path, err)
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("while reading %s: %w", path, err)
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

// clonarArgs copies the directive arguments so that nodes built by future
// tasks (Task 12 builds new nodes out of these ones) do not share the backing
// array with crossplane's tree: an append on a copied Args could overwrite
// the neighbour.
func clonarArgs(args []string) []string {
	if args == nil {
		return []string{}
	}
	return slices.Clone(args)
}

// mensagemDeFalhaDeLeitura classifies the cause instead of forwarding the
// runtime string.
//
// Both things matter. The raw string is unstable -- it changes across systems
// and across library versions -- and an agent branching on it breaks on its
// own; hence the message is ours. But saying only "cannot be read" wastes the
// one piece of actionable information there is: permission denied is fixed
// one way, a dropping connection another. So the cause goes in classified,
// not literal.
//
// The distinction only started to matter with remote access. Verified against
// a real server that the SFTP error matches fs.ErrPermission, so the same
// check serves both the local and the remote target.
// classeDeFalhaDeLeitura separates permission from every other read failure.
// The distinction is not cosmetic: --sudo fixes one and does nothing for the
// other, so a caller needs to tell them apart without reading the message.
func classeDeFalhaDeLeitura(err error) ClasseRecusa {
	if errors.Is(err, fs.ErrPermission) {
		return RecusaPermissaoNegada
	}
	return RecusaFalhaDeLeitura
}

func mensagemDeFalhaDeLeitura(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		// "ngx" and not "the connection user": the same message goes out
		// when the binary runs on the machine itself, where there is no
		// connection at all.
		return "ngx has no permission to read this file, " +
			"so the configuration cannot be presented in full"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "reading this file was interrupted before the end, " +
			"so the configuration cannot be presented in full"
	default:
		return "reading this file failed before the end, " +
			"so the configuration cannot be presented in full"
	}
}
