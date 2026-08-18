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
	f.saidas["sudo -n cat -- /etc/nginx/restrito.conf"] = "server { listen 80; }\n"

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
		[]string{"sudo", "-n", "cat", "--", "/etc/nginx/restrito.conf"}, f.executados[0],
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
	f.falhas["sudo -n cat -- /etc/nginx/nginx.conf"] = true

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
	f.falhas["sudo -n cat -- /etc/nginx/nginx.conf"] = true

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission, "a causa continua sendo permissao")
	diags := transport.Diagnosticos(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "sudo")
}

// Caminho que comeca com `-` e o caso de injecao de ARGUMENTO: sem o `--`
// separando as opcoes, o cat leria "-rf" como flag em vez de como arquivo.
// Argv explicito resolve injecao de shell e nao resolve esta -- sao defeitos
// diferentes, e o comando aqui roda com privilegio.
//
// O caminho vem de diretiva `include` da configuracao do alvo, que nao e
// entrada confiavel.
func TestCaminhoComTracoNaoViraFlag(t *testing.T) {
	f := novoFake()
	suspeito := "/etc/nginx/-rf"
	f.negados[suspeito] = true
	f.saidas["sudo -n cat -- "+suspeito] = "worker_processes 1;\n"

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
	rc, err := tr.Open(suspeito)
	require.NoError(t, err)
	_ = rc.Close()

	require.Len(t, f.executados, 1)
	argv := f.executados[0]
	require.Contains(t, argv, "--", "o separador de fim de opcoes tem de estar presente")
	assert.Less(t, indiceDe(argv, "--"), indiceDe(argv, suspeito),
		"o separador precisa vir ANTES do caminho para valer de alguma coisa")
}

func indiceDe(lista []string, alvo string) int {
	for i, v := range lista {
		if v == alvo {
			return i
		}
	}
	return -1
}

// A arvore de confianca e DERIVADA da configuracao, nunca uma lista fixa de
// caminhos: lista fixa quebraria instalacao fora do padrao, e um servidor
// real medido inclui de /etc/letsencrypt, fora de /etc/nginx.
//
// O par de casos e o ponto: elevar um irmao de arquivo ja alcancado e
// rotina e sai como info; elevar num diretorio que a configuracao nunca
// tinha tocado e novidade, e novidade envolvendo sudo sai como aviso.
func TestElevacaoForaDaArvoreViraAviso(t *testing.T) {
	severidadeDe := func(diags []output.Diagnostic, codigo string) output.Severity {
		for _, d := range diags {
			if d.Code == codigo {
				return d.Severity
			}
		}
		return ""
	}

	t.Run("irmao de arquivo ja lido sai como info", func(t *testing.T) {
		f := novoFake()
		f.arquivos["/etc/nginx/nginx.conf"] = "include conf.d/*.conf;\n"
		f.negados["/etc/nginx/conf.d/restrito.conf"] = true
		f.saidas["sudo -n cat -- /etc/nginx/conf.d/restrito.conf"] = "server {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/etc/nginx/conf.d/restrito.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Equal(t, output.SeverityInfo,
			severidadeDe(diags, transport.CodigoLeituraPrivilegiada))
		assert.Empty(t, severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"conf.d fica abaixo de /etc/nginx, que a configuracao ja alcancava")
	})

	t.Run("diretorio nunca tocado sai como aviso", func(t *testing.T) {
		f := novoFake()
		f.arquivos["/etc/nginx/nginx.conf"] = "include /opt/segredos/x.conf;\n"
		f.negados["/opt/segredos/x.conf"] = true
		f.saidas["sudo -n cat -- /opt/segredos/x.conf"] = "server {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/opt/segredos/x.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Equal(t, output.SeverityWarning,
			severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"elevar em diretorio novo e a anomalia que o aviso existe para mostrar")
	})

	t.Run("o proprio arquivo de topo nunca e anomalia", func(t *testing.T) {
		f := novoFake()
		f.negados["/opt/nginx-custom/nginx.conf"] = true
		f.saidas["sudo -n cat -- /opt/nginx-custom/nginx.conf"] = "events {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/opt/nginx-custom/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Empty(t, severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"a configuracao que o operador nomeou nao e novidade, esteja onde estiver")
	})
}
