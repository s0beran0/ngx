package transport_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

// fake responde leitura e execucao gravadas, e registra o que foi executado.
// Provar que algo NAO foi escalado exige olhar os comandos, nao so o
// resultado.
type fake struct {
	arquivos   map[string]string
	negados    map[string]bool
	saidas     map[string]string
	falhas     map[string]bool
	executados [][]string
}

func novoFake() *fake {
	return &fake{
		arquivos: map[string]string{}, negados: map[string]bool{},
		saidas: map[string]string{}, falhas: map[string]bool{},
	}
}

func (f *fake) Open(p string) (io.ReadCloser, error) {
	if f.negados[p] {
		return nil, fs.ErrPermission
	}
	c, ok := f.arquivos[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(c)), nil
}

func (f *fake) Glob(padrao string) ([]string, error) {
	if f.negados[padrao] {
		return nil, fs.ErrPermission
	}
	return []string{}, nil
}

func (f *fake) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.executados = append(f.executados, argv)
	chave := ""
	for i, a := range argv {
		if i > 0 {
			chave += " "
		}
		chave += a
	}
	if f.falhas[chave] {
		return nil, []byte("sudo: a password is required"), 1, nil
	}
	return []byte(f.saidas[chave]), nil, 0, nil
}

func (f *fake) Close() error     { return nil }
func (f *fake) Describe() string { return "fake" }

// TestSemSudoNadaEEscalado e a metade da regra que a DR5 exige: sem a flag, o
// transporte tem de ficar exatamente como estava, e nenhum comando pode ser
// executado. Um teste que so verificasse o caminho COM sudo deixaria a
// escalada silenciosa passar despercebida.
func TestSemSudoNadaEEscalado(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, false)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Empty(t, f.executados, "sem --sudo nenhum comando pode ser executado")
	assert.Same(t, transport.Transport(f), tr, "sem a flag o transporte volta intocado")
}

// A escalada e MINIMA: so o arquivo recusado e relido com privilegio. Numa
// configuracao de 132 arquivos onde um e restrito, os outros 131 nao podem
// passar por sudo nenhum.
func TestComSudoSoORecusadoEElevado(t *testing.T) {
	f := novoFake()
	f.arquivos["/etc/nginx/aberto.conf"] = "worker_processes 1;\n"
	f.negados["/etc/nginx/restrito.conf"] = true
	f.saidas["sudo -n cat /etc/nginx/restrito.conf"] = "server { listen 80; }\n"

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)

	rc, err := tr.Open("/etc/nginx/aberto.conf")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Empty(t, f.executados, "arquivo legivel nao pode disparar sudo")

	rc, err = tr.Open("/etc/nginx/restrito.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "server { listen 80; }\n", string(b))
	require.Len(t, f.executados, 1)
	assert.Equal(t,
		[]string{"sudo", "-n", "cat", "/etc/nginx/restrito.conf"}, f.executados[0],
		"argv explicito, sem shell: nome de arquivo nao pode virar injecao")

	diags := transport.Diagnosticos(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityInfo, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "/etc/nginx/restrito.conf",
		"ler config de servidor com privilegio nao pode acontecer calado")
}

// Servidor endurecido libera no sudoers comandos especificos -- tipicamente o
// nginx -- e recusa `cat`. Ali o dump e o unico caminho, e sem ele a leitura
// privilegiada seria inutil justamente onde o sudo esta bem configurado.
func TestQuandoOSudoNaoPermiteCatODumpResolve(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true
	f.falhas["sudo -n cat /etc/nginx/nginx.conf"] = true

	dump := func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"/etc/nginx/nginx.conf": []byte("worker_processes 4;\n")}, nil
	}
	tr := transport.ComLeituraPrivilegiadaEDump(context.Background(), f, true, dump)

	rc, err := tr.Open("/etc/nginx/nginx.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()

	assert.Equal(t, "worker_processes 4;\n", string(b))
	diags := transport.Diagnosticos(tr)
	require.NotEmpty(t, diags)
	assert.Equal(t, transport.CodigoLeituraPeloDump, diags[0].Code,
		"a origem do conteudo tem de aparecer: veio do nginx -T, nao do arquivo")
}

// Sem dump e sem `cat`, o desfecho e recusa com o motivo — nunca uma arvore
// parcial apresentada como completa.
func TestSemCaminhoNenhumRecusaEmVezDeApresentarParcial(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true
	f.falhas["sudo -n cat /etc/nginx/nginx.conf"] = true

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission, "a causa continua sendo permissao")
	diags := transport.Diagnosticos(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "sudo")
}
