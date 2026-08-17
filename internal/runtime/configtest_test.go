package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestTestConfigAprovada(t *testing.T) {
	f := novoFake("local").responde("nginx -t", resposta{stderr: saidaTesteOK})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, res.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", res.ConfigFile)
	assert.Empty(t, res.Diagnostics)
	assert.NotNil(t, res.Diagnostics)
}

// A invariante do Transport aplicada ao runtime: uma configuracao reprovada e
// resultado, nao falha de infraestrutura. Se isto virar erro, o agente perde a
// resposta que pediu.
func TestTestConfigReprovadaNaoEErro(t *testing.T) {
	f := novoFake("local").responde("nginx -t", resposta{stderr: saidaTesteFalhou, exit: 1})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.False(t, res.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", res.ConfigFile)
	require.Len(t, res.Diagnostics, 1)

	d := res.Diagnostics[0]
	assert.Equal(t, output.SeverityError, d.Severity)
	assert.Equal(t, CodigoTesteConfig, d.Code)
	assert.Equal(t, `unknown directive "foo"`, d.Message)
	assert.Equal(t, "/etc/nginx/conf.d/a.conf", d.File)
	assert.Equal(t, 3, d.Line)
}

func TestTestConfigAvisoNaoDerrubaOVeredito(t *testing.T) {
	f := novoFake("local").responde("nginx -t", resposta{stderr: saidaTesteComAviso})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, res.OK)
	require.Len(t, res.Diagnostics, 1)
	assert.Equal(t, output.SeverityWarning, res.Diagnostics[0].Severity)
	// A severidade nunca entra no codigo: aviso e erro compartilham o codigo
	// e se distinguem pelo campo severity.
	assert.Equal(t, CodigoTesteConfig, res.Diagnostics[0].Code)
}

// O mesmo texto, vindo de dois transportes diferentes, produz o mesmo
// resultado. E o ponto da tarefa: nao ha caminho de codigo "remoto".
func TestTestConfigIdenticoLocalERemoto(t *testing.T) {
	local := novoFake("local").responde("nginx -t", resposta{stderr: saidaTesteFalhou, exit: 1})
	remoto := novoFake("ssh://opc@10.0.0.7:22").responde("nginx -t", resposta{stderr: saidaTesteFalhou, exit: 1})

	a, err := New(local).TestConfig(context.Background())
	require.NoError(t, err)
	b, err := New(remoto).TestConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

func TestParseDiagnosticosLocalizacaoNoFimDaMensagem(t *testing.T) {
	texto := `nginx: [emerg] invalid number of arguments in "listen" directive in /etc/nginx/nginx.conf:12`

	diags := ParseDiagnosticos(texto)
	require.Len(t, diags, 1)
	assert.Equal(t, `invalid number of arguments in "listen" directive`, diags[0].Message)
	assert.Equal(t, "/etc/nginx/nginx.conf", diags[0].File)
	assert.Equal(t, 12, diags[0].Line)
}

func TestParseDiagnosticosSemLocalizacao(t *testing.T) {
	diags := ParseDiagnosticos(`nginx: [emerg] bind() to 0.0.0.0:80 failed (98: Address already in use)`)
	require.Len(t, diags, 1)
	assert.Empty(t, diags[0].File)
	assert.Zero(t, diags[0].Line)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
}

// Nivel desconhecido nao pode virar info: subestimar o que nao se reconhece
// esconde exatamente o caso novo.
func TestParseDiagnosticosNivelDesconhecidoViraErro(t *testing.T) {
	diags := ParseDiagnosticos("nginx: [xyz] algo inesperado")
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
}

func TestParseDiagnosticosListaNuncaNil(t *testing.T) {
	diags := ParseDiagnosticos("")
	require.NotNil(t, diags)

	bruto, err := json.Marshal(map[string]any{"diagnostics": diags})
	require.NoError(t, err)
	assert.Equal(t, `{"diagnostics":[]}`, string(bruto))
}
