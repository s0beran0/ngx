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

	require.Len(t, tree.Files, 3, "nginx.conf, conf.d/api.conf and snippets/proxy.conf")
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
		"the include is gone and became the server of the included file")
}

// Origin is what lets the agent know which real file to edit after seeing the
// resolved configuration.
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

// The IDs of the combined tree are renumbered over the resolved structure:
// that is the structure the agent sees and the one it operates on.
func TestCombineRenumeraIDsSobreAEstruturaResolvida(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	api := config.FindByID(combinado, "h.s0")
	require.NotNil(t, api)
	require.Equal(t, "server", api.Directive)
	require.Contains(t, api.Origin.File, "api.conf",
		"the first server of the resolved tree comes from the include")

	legado := config.FindByID(combinado, "h.s1")
	require.NotNil(t, legado)
	require.Contains(t, legado.Origin.File, "nginx.conf")
}

// The hash of the combined tree differs from the uncombined one: they are
// different views, and conflating them would invalidate IDs for no reason.
func TestCombineRecalculaOHash(t *testing.T) {
	original := parseCombine(t)
	combinado, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combinado.Hash)
	require.NotEqual(t, original.Hash, combinado.Hash)
}

// Include nested two levels deep: nginx.conf includes conf.d/api.conf, which
// in turn includes snippets/proxy.conf. The relative pattern declared inside
// conf.d/api.conf resolves against the directory of the top-level file
// (nginx.conf), not against the directory of whoever declared the include --
// the same rule crossplane uses (p.configDir, fixed for the whole parse).
// Standard Debian layout: /etc/nginx/conf.d/*.conf including something in
// /etc/nginx/snippets/, not in /etc/nginx/conf.d/snippets/.
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
		"the content of the third file (nested include) has to show up in the combined tree")

	require.NotNil(t, proxy.Origin)
	require.Contains(t, proxy.Origin.File, "proxy.conf")
}

// A literal include (no *, ? or [) that matches no file of the tree means a
// bug in our path comparison: Parse already fails loudly when crossplane
// cannot open a literal include, so this case should never survive silently
// all the way to Combine.
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

// Args is cloned in the copy: mutating the combined tree must not change the
// original tree, because both stay alive at the same time (the original holds
// the real spans used for editing).
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
		"mutating Args of the combined tree must not affect the original tree")
}
