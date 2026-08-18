package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashTemPrefixoSha256(t *testing.T) {
	tree := parseTexto(t, "http { server { listen 80; } }")

	require.True(t, strings.HasPrefix(tree.Hash, "sha256:"))
	require.Len(t, tree.Hash, len("sha256:")+64)
}

func TestHashEhDeterministico(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } }")
	b := parseTexto(t, "http { server { listen 80; } }")

	require.Equal(t, a.Hash, b.Hash)
}

// The hash protects the meaning, not the text: two configurations differing
// only in formatting have to produce the same hash, otherwise running fmt
// would invalidate every ID the agent is holding.
func TestFormatacaoDiferenteProduzMesmoHash(t *testing.T) {
	compacto := parseTexto(t, "http{server{listen 80;}}")
	espacado := parseTexto(t, `
http {
    server {
        listen 80;
    }
}
`)

	require.Equal(t, compacto.Hash, espacado.Hash)
}

func TestComentariosNaoEntramNoHash(t *testing.T) {
	sem := parseTexto(t, "http { server { listen 80; } }")
	com := parseTexto(t, `# a comment
http {
  # another
  server { listen 80; }
}`)

	require.Equal(t, sem.Hash, com.Hash)
}

func TestMudancaDeArgumentoMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } }")
	b := parseTexto(t, "http { server { listen 443; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Order matters: moving a server changes what the IDs mean, so it has to
// change the hash.
func TestOrdemDeBlocosMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseTexto(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// With no separator between directive and arguments, "a b" and "ab" would collide.
func TestDiretivasDiferentesNaoColidem(t *testing.T) {
	a := parseTexto(t, "ab c;")
	b := parseTexto(t, "a bc;")

	require.NotEqual(t, a.Hash, b.Hash)
}
