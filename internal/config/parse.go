package config

import (
	"fmt"
	"io"
	"os"

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
func Parse(opts ParseOptions) (*Tree, error) {
	payload, err := crossplane.Parse(opts.Path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
		Open:                      opts.abrir,
	})
	if err != nil {
		return nil, fmt.Errorf("ao parsear %s: %w", opts.Path, err)
	}

	tree := &Tree{}
	for _, cfg := range payload.Config {
		src, err := lerFonte(opts, cfg.File)
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

func lerFonte(opts ParseOptions, path string) ([]byte, error) {
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
			Args:      d.Args,
			File:      file,
			Line:      d.Line,
			Comment:   d.Comment,
			temBloco:  d.Block != nil,
		}
		if n.Args == nil {
			n.Args = []string{}
		}
		if d.Block != nil {
			n.Block = converterDirectives(d.Block, file)
		}
		nodes = append(nodes, n)
	}
	return nodes
}
