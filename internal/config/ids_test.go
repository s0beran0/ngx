package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseTexto(t *testing.T, conteudo string) *config.Tree {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.conf")
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	return tree
}

// Root-level context blocks carry no index: they occur at most once.
func TestBlocosRaizNaoLevamIndice(t *testing.T) {
	tree := parseTexto(t, "events {}\nhttp {}\n")

	require.Equal(t, "e", tree.Files[0].Nodes[0].ID)
	require.Equal(t, "h", tree.Files[0].Nodes[1].ID)
}

func TestServersSaoNumeradosEntreSi(t *testing.T) {
	tree := parseTexto(t, `http {
  server { listen 80; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.s0", http.Block[0].ID)
	require.Equal(t, "h.s1", http.Block[1].ID)
}

// The rule that reduces brittleness: the index counts among siblings of the
// same kind, not by absolute position. Inserting a location does not renumber
// the servers.
func TestIndiceContaEntreIrmaosDoMesmoTipo(t *testing.T) {
	tree := parseTexto(t, `http {
  upstream a { server 10.0.0.1; }
  server { listen 80; }
  upstream b { server 10.0.0.2; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.u0", http.Block[0].ID)
	require.Equal(t, "h.s0", http.Block[1].ID)
	require.Equal(t, "h.u1", http.Block[2].ID)
	require.Equal(t, "h.s1", http.Block[3].ID, "the second server is still s1")
}

func TestDiretivasSimplesUsamPrefixoD(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    listen 443 ssl;
    server_name api.exemplo.com;
    location / { proxy_pass http://a; }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.d0", server.Block[0].ID)
	require.Equal(t, "h.s0.d1", server.Block[1].ID)
	require.Equal(t, "h.s0.l0", server.Block[2].ID, "location has an abbreviation of its own")
}

// Comments get no ID and do not count towards the index: if they did, adding
// a comment would renumber the directives around it.
func TestComentariosNaoRecebemIDNemDeslocamIndices(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    # explica o listen
    listen 443 ssl;
    # explica o nome
    server_name api.exemplo.com;
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]

	require.Empty(t, server.Block[0].ID, "a comment has no ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "the comment in between did not shift the index")
}

func TestLocationsAninhadasEncadeiamOID(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    location /a {
      location /a/b { proxy_pass http://x; }
    }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.l0", server.Block[0].ID)
	require.Equal(t, "h.s0.l0.l0", server.Block[0].Block[0].ID)
}

// Directives with no abbreviation in the table use the full name, which keeps
// the ID readable and avoids a collision between server and stream.
func TestDiretivaSemAbreviacaoUsaNomeCompleto(t *testing.T) {
	tree := parseTexto(t, `http {
  map $a $b { default 0; }
  stream { }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.mp0", http.Block[0].ID)
	require.Equal(t, "h.st0", http.Block[1].ID)
}

func TestFindByIDEncontraONo(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    location /api { proxy_pass http://backend; }
  }
}`)

	n := config.FindByID(tree, "h.s0.l0")

	require.NotNil(t, n)
	require.Equal(t, "location", n.Directive)
	require.Equal(t, []string{"/api"}, n.Args)
}

func TestFindByIDDevolveNilQuandoNaoAcha(t *testing.T) {
	tree := parseTexto(t, "http { server { listen 80; } }")

	require.Nil(t, config.FindByID(tree, "h.s9"))
}
