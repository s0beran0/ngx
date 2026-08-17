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

// O hash protege o significado, nao o texto: duas configuracoes que so diferem
// em formatacao precisam produzir o mesmo hash, senao rodar fmt invalidaria
// todos os IDs que o agente esta segurando.
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
	com := parseTexto(t, `# um comentario
http {
  # outro
  server { listen 80; }
}`)

	require.Equal(t, sem.Hash, com.Hash)
}

func TestMudancaDeArgumentoMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } }")
	b := parseTexto(t, "http { server { listen 443; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Ordem importa: mover um server muda o significado dos IDs, entao precisa
// mudar o hash.
func TestOrdemDeBlocosMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseTexto(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Sem separador entre diretiva e argumentos, "a b" e "ab" colidiriam.
func TestDiretivasDiferentesNaoColidem(t *testing.T) {
	a := parseTexto(t, "ab c;")
	b := parseTexto(t, "a bc;")

	require.NotEqual(t, a.Hash, b.Hash)
}
