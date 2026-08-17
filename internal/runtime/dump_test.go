package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestDumpSeparaArquivos(t *testing.T) {
	f := novoFake("local").responde("nginx -T", resposta{stdout: saidaDump, stderr: saidaTesteOK})

	d, err := New(f).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, d.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", d.ConfigFile)
	require.Len(t, d.Files, 2)

	assert.Equal(t, "/etc/nginx/nginx.conf", d.Files[0].Path)
	assert.Contains(t, d.Files[0].Content, "worker_processes auto;")
	assert.NotContains(t, d.Files[0].Content, "# configuration file")

	assert.Equal(t, "/etc/nginx/conf.d/site.conf", d.Files[1].Path)
	assert.Contains(t, d.Files[1].Content, "server_name exemplo.com;")
	// O ultimo arquivo nao ganha uma linha em branco a mais que os outros.
	assert.True(t, strings.HasSuffix(d.Files[1].Content, "}\n"))
}

func TestDumpIdenticoLocalERemoto(t *testing.T) {
	local := novoFake("local").responde("nginx -T", resposta{stdout: saidaDump, stderr: saidaTesteOK})
	remoto := novoFake("ssh://opc@10.0.0.7:22").responde("nginx -T", resposta{stdout: saidaDump, stderr: saidaTesteOK})

	a, err := New(local).DumpConfig(context.Background())
	require.NoError(t, err)
	b, err := New(remoto).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

// Configuracao invalida faz o `-T` sair diferente de zero e nao despejar
// nada. Continua sendo resultado.
func TestDumpConfiguracaoInvalidaNaoEErro(t *testing.T) {
	f := novoFake("local").responde("nginx -T", resposta{stderr: saidaTesteFalhou, exit: 1})

	d, err := New(f).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.False(t, d.OK)
	assert.Empty(t, d.Files)
	require.Len(t, d.Diagnostics, 1)
	assert.Equal(t, output.SeverityError, d.Diagnostics[0].Severity)

	bruto, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(bruto), `"files":[]`)
}

// DR5, o caso medido no host real: `nginx -T` falha para o usuario comum.
// Sem --sudo o ngx reporta a exigencia e diz qual e o comando — e nao tenta
// de novo.
func TestDumpSemPrivilegioReportaENaoEscala(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("nginx -T", resposta{
		stderr: saidaSemPrivilegio,
		exit:   1,
	})

	_, err := New(f).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoPrivilegioNecessario, e.Diag.Code)
	assert.Equal(t, output.SeverityError, e.Diag.Severity)
	assert.Contains(t, e.Diag.Message, "--sudo")
	assert.Contains(t, e.Diag.Message, "sudo -n nginx -T")

	// O ponto da DR5: uma unica chamada, sem sudo. Escalar em silencio e o
	// defeito que a decisao existe para impedir.
	chamadas := f.chamadas()
	require.Len(t, chamadas, 1)
	assert.Equal(t, []string{"nginx", "-T"}, chamadas[0])
}

// O outro caminho: com --sudo explicito, o comando roda escalado e devolve a
// configuracao.
func TestDumpComSudoExecutaEscalado(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("sudo -n nginx -T", resposta{
		stdout: saidaDump,
		stderr: saidaTesteOK,
	})

	d, err := New(f, ComSudo(true)).DumpConfig(context.Background())
	require.NoError(t, err)
	assert.True(t, d.OK)
	assert.Len(t, d.Files, 2)

	chamadas := f.chamadas()
	require.Len(t, chamadas, 1)
	assert.Equal(t, []string{"sudo", "-n", "nginx", "-T"}, chamadas[0])
}

// Mesma gravacao, mesmos arquivos: o resultado com --sudo e o resultado sem
// privilegio necessario nao podem diferir em nada alem de terem sido obtidos.
func TestDumpComSudoIgualAoSemSudo(t *testing.T) {
	semSudo := novoFake("local").responde("nginx -T", resposta{stdout: saidaDump, stderr: saidaTesteOK})
	comSudo := novoFake("local").responde("sudo -n nginx -T", resposta{stdout: saidaDump, stderr: saidaTesteOK})

	a, err := New(semSudo).DumpConfig(context.Background())
	require.NoError(t, err)
	b, err := New(comSudo, ComSudo(true)).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

// --sudo pedido num alvo onde o sudo quer senha: o ngx nao tem TTY e nao tem
// para onde mandar a senha, entao isso e um desfecho proprio, nao um
// "precisa de privilegio" generico.
func TestDumpSudoExigeSenha(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("sudo -n nginx -T", resposta{
		stderr: "sudo: a password is required\n",
		exit:   1,
	})

	_, err := New(f, ComSudo(true)).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoSudoIndisponivel, e.Diag.Code)
}

// Com --sudo e ainda assim sem permissao, a mensagem nao pode mandar usar
// --sudo de novo.
func TestDumpComSudoAindaSemPermissao(t *testing.T) {
	f := novoFake("local").responde("sudo -n nginx -T", resposta{
		stderr: saidaSemPrivilegio,
		exit:   1,
	})

	_, err := New(f, ComSudo(true)).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoPrivilegioNecessario, e.Diag.Code)
	assert.Contains(t, e.Diag.Message, "com --sudo")
}

// Erro de sintaxe nao pode ser confundido com falta de privilegio: a
// deteccao e conservadora de proposito.
func TestTestConfigErroDeSintaxeNaoViraPrivilegio(t *testing.T) {
	f := novoFake("local").responde("nginx -t", resposta{stderr: saidaTesteFalhou, exit: 1})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)
	assert.False(t, res.OK)
}

func TestDividirDumpIgnoraConteudoAntesDoPrimeiroMarcador(t *testing.T) {
	arquivos := DividirDump("lixo solto\n# configuration file /a.conf:\nfoo;\n")
	require.Len(t, arquivos, 1)
	assert.Equal(t, "/a.conf", arquivos[0].Path)
	assert.Equal(t, "foo;\n", arquivos[0].Content)
}

// Um comentario dentro de uma configuracao nao pode partir o arquivo em dois:
// o marcador so vale no inicio da linha e com o dois-pontos final.
func TestDividirDumpNaoConfundeComentario(t *testing.T) {
	texto := "# configuration file /a.conf:\n    # configuration file /falso.conf:\nfoo;\n"
	arquivos := DividirDump(texto)
	require.Len(t, arquivos, 1)
	assert.Contains(t, arquivos[0].Content, "/falso.conf")
}

func TestDividirDumpVazio(t *testing.T) {
	arquivos := DividirDump("")
	require.NotNil(t, arquivos)
	assert.Empty(t, arquivos)
}
