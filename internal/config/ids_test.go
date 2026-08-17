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

// Blocos de contexto do nivel raiz nao levam indice: ocorrem no maximo uma vez.
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

// A regra que reduz a fragilidade: o indice conta entre irmaos do mesmo tipo,
// nao por posicao absoluta. Inserir uma location nao renumera os servers.
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
	require.Equal(t, "h.s1", http.Block[3].ID, "o segundo server continua sendo s1")
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
	require.Equal(t, "h.s0.l0", server.Block[2].ID, "location tem abreviacao propria")
}

// Comentarios nao recebem ID e nao contam no indice: se contassem, adicionar
// um comentario renumeraria as diretivas ao redor.
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

	require.Empty(t, server.Block[0].ID, "comentario nao tem ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "o comentario no meio nao deslocou o indice")
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

// Diretivas sem abreviacao na tabela usam o nome completo, o que mantem o ID
// legivel e evita colisao entre server e stream.
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
