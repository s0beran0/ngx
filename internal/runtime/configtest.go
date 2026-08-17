package runtime

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// TestResult e o veredito de `nginx -t` ja estruturado.
//
// OK vem do codigo de saida, nao do texto: o nginx e quem sabe se aprovou.
// Uma configuracao reprovada e um TestResult com OK false e erro nulo — nao
// e falha de infraestrutura, e a resposta a pergunta que se fez.
type TestResult struct {
	OK bool `json:"ok"`

	// ConfigFile e o arquivo de topo que o nginx testou, quando ele diz
	// qual foi. Omitido quando nao diz.
	ConfigFile string `json:"config_file,omitempty"`

	// Diagnostics traz uma entrada por linha de diagnostico do nginx.
	// Nunca e nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`

	// Raw e a saida original, preservada para depuracao de casos que o
	// parser nao reconheceu.
	Raw string `json:"raw,omitempty"`
}

// TestConfig executa `nginx -t` no alvo e traduz a saida em diagnosticos.
func (r *Runtime) TestConfig(ctx context.Context) (*TestResult, error) {
	e, err := r.executar(ctx, "-t")
	if err != nil {
		return nil, err
	}
	return montarTestResult(e), nil
}

func montarTestResult(e *execucao) *TestResult {
	texto := e.saida()
	res := &TestResult{
		OK:          e.exit == 0,
		Diagnostics: ParseDiagnosticos(texto),
		Raw:         strings.TrimRight(texto, "\n"),
	}
	res.ConfigFile = arquivoTestado(texto)
	return res
}

var (
	// Linha de diagnostico do nginx: "nginx: [emerg] mensagem".
	reDiagnostico = regexp.MustCompile(`^nginx: \[([a-z]+)\] (.*)$`)

	// Sufixo de localizacao: "... in /etc/nginx/conf.d/a.conf:3".
	// O "in" e ganancioso de proposito — mensagens tem "in" no meio
	// ("invalid number of arguments in \"listen\" directive in /f.conf:2")
	// e o que interessa e sempre o ultimo.
	reLocalizacao = regexp.MustCompile(`^(.*) in (\S+):(\d+)$`)

	// Linhas de sumario, que nomeiam o arquivo de topo testado.
	reArquivoTestado = regexp.MustCompile(
		`configuration file (\S+) (?:syntax is ok|test (?:is successful|failed))`)
)

// ParseDiagnosticos traduz a saida textual do nginx em diagnosticos
// estruturados.
//
// A funcao e exportada e nao recebe nada alem do texto: e o coracao da regra
// de que o parser nao sabe de onde os bytes vieram. A mesma funcao serve a
// `nginx -t`, a `nginx -T` e a qualquer saida gravada num teste.
//
// O nivel do nginx vira severity; o codigo e sempre o mesmo, porque a
// severidade nunca entra no codigo.
func ParseDiagnosticos(texto string) []output.Diagnostic {
	diags := []output.Diagnostic{}

	for _, linha := range strings.Split(texto, "\n") {
		linha = strings.TrimRight(linha, "\r")
		if strings.TrimSpace(linha) == "" {
			continue
		}
		m := reDiagnostico.FindStringSubmatch(linha)
		if m == nil {
			continue
		}

		d := output.Diagnostic{
			Severity: severidadeDoNivel(m[1]),
			Code:     CodigoTesteConfig,
			Message:  m[2],
		}
		if loc := reLocalizacao.FindStringSubmatch(d.Message); loc != nil {
			if n, err := strconv.Atoi(loc[3]); err == nil {
				d.Message = loc[1]
				d.File = loc[2]
				d.Line = n
			}
		}
		diags = append(diags, d)
	}

	return diags
}

// severidadeDoNivel mapeia o nivel do nginx para a severidade do envelope.
// Nivel desconhecido vira erro, e nao info: subestimar um nivel que nao se
// reconhece esconde exatamente o caso novo.
func severidadeDoNivel(nivel string) output.Severity {
	switch nivel {
	case "warn":
		return output.SeverityWarning
	case "notice", "info", "debug":
		return output.SeverityInfo
	default:
		return output.SeverityError
	}
}

func arquivoTestado(texto string) string {
	if m := reArquivoTestado.FindStringSubmatch(texto); m != nil {
		return m[1]
	}
	return ""
}
