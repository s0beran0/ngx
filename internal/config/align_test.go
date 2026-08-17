package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHeadSpanCobreDiretivaEArgumentos(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})
	require.NotNil(t, listen)

	require.Equal(t, "listen 443 ssl", string(src[listen.HeadSpan.Start:listen.HeadSpan.End]))
}

func TestSpanDeDiretivaSimplesTerminaNoPontoEVirgula(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.Equal(t, "listen 443 ssl;", string(src[listen.Span.Start:listen.Span.End]))
}

func TestSpanDeBlocoTerminaNaChaveDeFechamento(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	var upstream *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "upstream" {
			upstream = n
			return false
		}
		return true
	})
	require.NotNil(t, upstream)

	texto := string(src[upstream.Span.Start:upstream.Span.End])
	require.True(t, strings.HasPrefix(texto, "upstream backend_v1"))
	require.True(t, strings.HasSuffix(texto, "}"))
	require.Contains(t, texto, "server 10.0.0.1:8080;")

	require.Equal(t, "upstream backend_v1", string(src[upstream.HeadSpan.Start:upstream.HeadSpan.End]),
		"o head nao inclui o bloco")
}

func TestLinhaEColunaVemDoTokenizador(t *testing.T) {
	tree := parseSimples(t)

	var serverName *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" {
			serverName = n
			return false
		}
		return true
	})
	require.NotNil(t, serverName)

	require.Greater(t, serverName.Line, 0)
	require.Greater(t, serverName.Column, 0)

	linhas := strings.Split(string(tree.Files[0].Source), "\n")
	require.Contains(t, linhas[serverName.Line-1], "server_name")
}

// Aspas contendo ponto e virgula sao o caso que quebra um alinhamento ingenuo.
func TestAlinhamentoSobreviveAAspasComPontoEVirgula(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	var addHeader *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "add_header" {
			addHeader = n
			return false
		}
		return true
	})
	require.NotNil(t, addHeader)

	require.Equal(t, `add_header X-A "b; c";`, string(src[addHeader.Span.Start:addHeader.Span.End]))
}

// Invariante de contencao: o span de um filho vive dentro do span do pai.
func TestSpansDeFilhosEstaoContidosNoPai(t *testing.T) {
	tree := parseSimples(t)

	var verificar func(nodes []*config.Node, pai *config.Node)
	verificar = func(nodes []*config.Node, pai *config.Node) {
		anteriorFim := -1
		for _, n := range nodes {
			if pai != nil {
				require.GreaterOrEqual(t, n.Span.Start, pai.Span.Start,
					"%s comeca antes do pai %s", n.Directive, pai.Directive)
				require.LessOrEqual(t, n.Span.End, pai.Span.End,
					"%s termina depois do pai %s", n.Directive, pai.Directive)
			}
			require.GreaterOrEqual(t, n.Span.Start, anteriorFim,
				"%s sobrepoe o irmao anterior", n.Directive)
			anteriorFim = n.Span.End
			verificar(n.Block, n)
		}
	}

	for _, f := range tree.Files {
		verificar(f.Nodes, nil)
	}
}

// Cobertura: todo byte significativo do arquivo pertence ao span de algum no
// de nivel raiz. E a formulacao concreta da propriedade que sustenta a
// arquitetura: se ela vale, o casamento token-arvore esta correto.
func TestSpansRaizCobremTodoByteSignificativo(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	coberto := make([]bool, len(src))
	for _, n := range tree.Files[0].Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			coberto[i] = true
		}
	}

	for i, b := range src {
		if coberto[i] {
			continue
		}
		require.True(t, b == ' ' || b == '\t' || b == '\n' || b == '\r',
			"byte %d (%q) na linha nao coberta nao e espaco", i, string(b))
	}
}

func TestBlocoVazioEhReconhecido(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vazio.conf")
	require.NoError(t, os.WriteFile(p, []byte("events {}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	events := tree.Files[0].Nodes[0]
	require.Equal(t, "events", events.Directive)
	require.True(t, events.HasBlock(), "events {} abre um bloco, mesmo vazio")
	require.Equal(t, "events {}", string(tree.Files[0].Source[events.Span.Start:events.Span.End]))
}
