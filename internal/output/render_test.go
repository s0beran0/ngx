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

// dadoNaoRedigivel deliberadamente nao implementa Redactable: existe para
// fixar o comportamento de fail-open quando Data nao sabe se redigir.
type dadoNaoRedigivel struct {
	Valor string `json:"valor"`
}

type dadoHumano struct{}

func (dadoHumano) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "saida humana\n")
	return err
}

// dadoHumanoRedigivel implementa as duas interfaces: Redactable e
// HumanRenderable. Existe para provar que a redacao tambem alcanca o
// caminho FormatHuman, nao so o JSON.
type dadoHumanoRedigivel struct {
	Valor string `json:"valor"`
}

func (d dadoHumanoRedigivel) Redacted(rs output.RedactSet) any {
	if rs.Matches("ssl_certificate_key", []string{d.Valor}) {
		return dadoHumanoRedigivel{Valor: output.RedactedValue}
	}
	return d
}

func (d dadoHumanoRedigivel) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, d.Valor+"\n")
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

// Com TTY, quando o dado implementa HumanRenderable, RenderHuman e usado em
// vez de serializar a struct como JSON.
func TestFormatAutoComTTYUsaRenderHumanQuandoDisponivel(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = dadoHumano{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "saida humana")
}

// Com TTY mas sem RenderHuman no dado, o formato humano cai para JSON
// indentado em vez de imprimir a struct crua do Go.
func TestFormatHumanSemHumanRenderableCaiParaJSONIndentado(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("status")
	env.Data = dadoNaoRedigivel{Valor: "abc"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "\"valor\": \"abc\"")
}

// Sem Data, o formato humano nao escreve nada alem dos diagnosticos (o
// early-return de Data == nil).
func TestFormatHumanComDataNilNaoEscreveNada(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	require.NoError(t, r.Render(output.New("status")))

	require.Empty(t, buf.String())
}

// O formato humano imprime cada diagnostico com sua localizacao.
func TestFormatHumanImprimeDiagnosticos(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("lint")
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityWarning,
		Message:  "linha longa",
		File:     "nginx.conf",
		Line:     12,
		Column:   3,
	})
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "warning: linha longa nginx.conf:12:3")
}

// Diagnostico com arquivo mas sem linha nao pode imprimir a coordenada falsa
// ":0:0" - Line e Column sao opcionais por design.
func TestFormatHumanDiagnosticoComArquivoSemLinhaNaoImprimeZeroZero(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("lint")
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityWarning,
		Message:  "arquivo sem linha",
		File:     "nginx.conf",
	})
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), ":0:0")
	require.Contains(t, buf.String(), "nginx.conf")
}

// O portao que a redacao existe para fechar: um humano no terminal pode ver o
// segredo, um agente lendo o pipe nao consegue nem pedir. O ponto do portao e
// que o segredo nunca chegue a saida - se a checagem fosse movida para depois
// do switch, o buffer nao estaria mais vazio aqui.
func TestNoRedactEhRecusadoSemTTY(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
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

// A redacao tambem cobre o caminho FormatHuman, nao so o JSON: os dois
// passam pelo mesmo bloco de codigo em Render, e e exatamente por isso que
// este teste importa - ele e o guarda contra alguem mover a redacao para
// dentro de renderJSON, mudanca que passaria em toda a suite JSON e vazaria
// o segredo na saida humana.
func TestRenderHumanAplicaRedacaoNoDado(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true, Redact: set}

	env := output.New("get")
	env.Data = dadoHumanoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), "/etc/ssl/priv.key")
	require.Contains(t, buf.String(), output.RedactedValue)
}

// Fixa o fail-open documentado no campo Redact: um Data que nao implementa
// Redactable sai integro, sem erro e sem nenhum sinal, mesmo com regras de
// redacao ativas. E o modo de falha mais provavel quando uma arvore real
// (ex.: Task 13) for plugada em Data sem implementar a interface.
func TestRenderNaoRedigeDadoQueNaoImplementaRedactable(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	env.Data = dadoNaoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "/etc/ssl/priv.key")
}

// Render nao muta o envelope do chamador: o Data original permanece intacto
// depois da chamada, mesmo quando a redacao troca o valor serializado.
func TestRenderNaoMutaDataDoChamador(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	original := dadoRedigivel{Valor: "/etc/ssl/priv.key"}
	env.Data = original
	require.NoError(t, r.Render(env))

	require.Equal(t, original, env.Data)
}

// Um Format fora de auto/json/human e erro de uso, nunca cai em JSON
// silenciosamente. Isso importa porque Format costuma vir de output.format
// no arquivo de configuracao YAML, que e string livre.
func TestFormatInvalidoEhRecusado(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.Format("xml"), IsTTY: false}

	err := r.Render(output.New("status"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
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
