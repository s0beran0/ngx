package output_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

type dadoRedigivel struct {
	Valor string `json:"valor"`
}

func (d dadoRedigivel) Redacted(rs output.RedactSet) any {
	if rs.Matches("ssl_certificate_key", []string{d.Valor}) {
		return dadoRedigivel{Valor: output.RedactedValue}
	}
	return d
}

type dadoHumano struct{}

func (dadoHumano) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "saida humana\n")
	return err
}

// Formato auto sem TTY tem que virar JSON: e o caso do agente lendo um pipe.
func TestFormatAutoSemTTYProduzJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: false}

	require.NoError(t, r.Render(output.New("status")))

	var env output.Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "status", env.Command)
}

// Com TTY e sem renderer humano no dado, cai para JSON indentado em vez de
// imprimir a struct crua do Go.
func TestFormatAutoComTTYUsaRenderHumanQuandoDisponivel(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = dadoHumano{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "saida humana")
}

// O portao que a redacao existe para fechar: um humano no terminal pode ver o
// segredo, um agente lendo o pipe nao consegue nem pedir.
func TestNoRedactEhRecusadoSemTTY(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

func TestNoRedactEhAceitoComTTY(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{
		Out: &buf, Format: output.FormatJSON, IsTTY: true,
		Redact: set, NoRedact: true,
	}

	env := output.New("get")
	env.Data = dadoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "/etc/ssl/priv.key")
}

// Sem --no-redact, o dado passa pela redacao antes de ser serializado.
func TestRenderAplicaRedacaoNoDado(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	env.Data = dadoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), "/etc/ssl/priv.key")
	require.Contains(t, buf.String(), output.RedactedValue)
}

// Quiet suprime a saida de sucesso mas nunca a de erro: um agente precisa
// saber o que deu errado.
func TestQuietSuprimeSucessoMasNaoErro(t *testing.T) {
	var sucesso bytes.Buffer
	r := &output.Renderer{Out: &sucesso, Format: output.FormatJSON, Quiet: true}
	require.NoError(t, r.Render(output.New("status")))
	require.Empty(t, sucesso.String())

	var falha bytes.Buffer
	r2 := &output.Renderer{Out: &falha, Format: output.FormatJSON, Quiet: true}
	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.NoError(t, r2.Render(env))
	require.Contains(t, falha.String(), "falhou")
}
