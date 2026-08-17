package config_test

import (
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseCombine(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "combine", "nginx.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseSemCombineMantemArquivosSeparados(t *testing.T) {
	tree := parseCombine(t)

	require.Len(t, tree.Files, 3, "nginx.conf, conf.d/api.conf e snippets/proxy.conf")
}

func TestCombineProduzUmUnicoArquivo(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	require.Len(t, combinado.Files, 1)
}

func TestCombineSubstituiIncludePelosNosIncluidos(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var http *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})
	require.NotNil(t, http)

	var nomes []string
	for _, filho := range http.Block {
		nomes = append(nomes, filho.Directive)
	}
	require.Equal(t, []string{"server", "server"}, nomes,
		"o include sumiu e virou o server do arquivo incluido")
}

// Origin e o que permite ao agente saber em qual arquivo real editar depois
// de ver a configuracao resolvida.
func TestCombinePreencheOriginComOArquivoReal(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var api *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "api.exemplo.com" {
			api = n
			return false
		}
		return true
	})
	require.NotNil(t, api)

	require.NotNil(t, api.Origin)
	require.Contains(t, api.Origin.File, "api.conf")
	require.Greater(t, api.Origin.Line, 0)
}

func TestCombineMantemOriginDoArquivoPrincipal(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var legado *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legado.exemplo.com" {
			legado = n
			return false
		}
		return true
	})
	require.NotNil(t, legado)

	require.NotNil(t, legado.Origin)
	require.Contains(t, legado.Origin.File, "nginx.conf")
}

// Os IDs da arvore combinada sao renumerados sobre a estrutura resolvida:
// e essa a estrutura que o agente enxerga e sobre a qual ele opera.
func TestCombineRenumeraIDsSobreAEstruturaResolvida(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	api := config.FindByID(combinado, "h.s0")
	require.NotNil(t, api)
	require.Equal(t, "server", api.Directive)
	require.Contains(t, api.Origin.File, "api.conf",
		"o primeiro server da arvore resolvida vem do include")

	legado := config.FindByID(combinado, "h.s1")
	require.NotNil(t, legado)
	require.Contains(t, legado.Origin.File, "nginx.conf")
}

// O hash da arvore combinada difere do da nao-combinada: sao visoes
// diferentes, e confundi-las invalidaria IDs sem motivo.
func TestCombineRecalculaOHash(t *testing.T) {
	original := parseCombine(t)
	combinado, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combinado.Hash)
	require.NotEqual(t, original.Hash, combinado.Hash)
}

// Include aninhado em dois niveis: nginx.conf inclui conf.d/api.conf, que
// por sua vez inclui snippets/proxy.conf. O padrao relativo declarado dentro
// de conf.d/api.conf resolve contra o diretorio do arquivo de topo
// (nginx.conf), nao contra o diretorio de quem declarou o include -- e a
// mesma regra que o crossplane usa (p.configDir, fixo para o parse inteiro).
// Layout padrao Debian: /etc/nginx/conf.d/*.conf incluindo algo em
// /etc/nginx/snippets/, nao em /etc/nginx/conf.d/snippets/.
func TestCombineResolveIncludeAninhadoDoisNiveis(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var proxy *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "proxy_pass" {
			proxy = n
			return false
		}
		return true
	})
	require.NotNil(t, proxy,
		"o conteudo do terceiro arquivo (include aninhado) deve aparecer na arvore combinada")

	require.NotNil(t, proxy.Origin)
	require.Contains(t, proxy.Origin.File, "proxy.conf")
}

// Um include literal (sem *, ? ou [) que nao casa nenhum arquivo da arvore
// significa bug na nossa comparacao de caminhos: o Parse ja falha alto
// quando o crossplane nao consegue abrir um include literal, entao esse
// caso nunca deveria sobreviver em silencio ate o Combine.
func TestCombineIncludeLiteralSemArquivoCorrespondenteFalha(t *testing.T) {
	arquivoTopo := filepath.Join("testdata", "combine", "nginx.conf")
	tree := &config.Tree{
		Files: []*config.File{
			{
				Path: arquivoTopo,
				Nodes: []*config.Node{
					{
						Directive: "include",
						Args:      []string{"nao-existe.conf"},
						File:      arquivoTopo,
						Line:      3,
					},
				},
			},
		},
	}

	_, err := config.Combine(tree)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nao-existe.conf")
}

// Args e clonado na copia: mutar a arvore combinada nao pode alterar a
// arvore original, porque as duas continuam vivas ao mesmo tempo (a
// original guarda os spans reais para edicao).
func TestCombineNaoCompartilhaArgsComAArvoreOriginal(t *testing.T) {
	original := parseCombine(t)
	combinado, err := config.Combine(original)
	require.NoError(t, err)

	acharLegado := func(t *config.Tree) *config.Node {
		var achado *config.Node
		t.Walk(func(n *config.Node) bool {
			if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legado.exemplo.com" {
				achado = n
				return false
			}
			return true
		})
		return achado
	}

	legadoOriginal := acharLegado(original)
	require.NotNil(t, legadoOriginal)

	legadoCombinado := acharLegado(combinado)
	require.NotNil(t, legadoCombinado)

	legadoCombinado.Args[0] = "mutado.invalido"
	require.Equal(t, "legado.exemplo.com", legadoOriginal.Args[0],
		"mutar Args da arvore combinada nao pode afetar a arvore original")
}
