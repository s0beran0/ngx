package runtime

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// TestResult is the verdict of `nginx -t`, already structured.
//
// OK comes from the exit code, not from the text: nginx is the one that knows
// whether it approved. A rejected configuration is a TestResult with OK false
// and a nil error -- it is not an infrastructure failure, it is the answer to
// the question that was asked.
type TestResult struct {
	OK bool `json:"ok"`

	// ConfigFile is the top-level file nginx tested, when it says which one
	// it was. Omitted when it does not say.
	ConfigFile string `json:"config_file,omitempty"`

	// Diagnostics carries one entry per diagnostic line from nginx. Never
	// nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`

	// Raw is the original output, preserved for debugging cases the parser
	// did not recognize.
	Raw string `json:"raw,omitempty"`
}

// TestConfig runs `nginx -t` on the target and translates the output into
// diagnostics.
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
	// An nginx diagnostic line: "nginx: [emerg] message".
	reDiagnostico = regexp.MustCompile(`^nginx: \[([a-z]+)\] (.*)$`)

	// Location suffix: "... in /etc/nginx/conf.d/a.conf:3".
	// The "in" is greedy on purpose -- messages have "in" in the middle
	// ("invalid number of arguments in \"listen\" directive in /f.conf:2")
	// and the one that matters is always the last.
	reLocalizacao = regexp.MustCompile(`^(.*) in (\S+):(\d+)$`)

	// Summary lines, which name the top-level file that was tested.
	reArquivoTestado = regexp.MustCompile(
		`configuration file (\S+) (?:syntax is ok|test (?:is successful|failed))`)
)

// ParseDiagnosticos translates the textual output of nginx into structured
// diagnostics.
//
// The function is exported and takes nothing beyond the text: it is the heart
// of the rule that the parser does not know where the bytes came from. The
// same function serves `nginx -t`, `nginx -T` and any output recorded in a
// test.
//
// The nginx level becomes severity; the code is always the same, because
// severity never goes into the code.
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

// severidadeDoNivel maps the nginx level to the envelope severity. An unknown
// level becomes an error, not info: underrating a level that is not
// recognized hides exactly the new case.
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
