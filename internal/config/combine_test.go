package config_test

import (
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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

	require.Len(t, tree.Files, 2, "nginx.conf e conf.d/api.conf")
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
