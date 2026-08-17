package output_test

import (
	"encoding/json"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// Um agente consumindo a saida faz `.diagnostics.length`. Uma lista nula
// quebra esse acesso, então lista vazia precisa serializar como [].
func TestEnvelopeSerializaDiagnosticsVaziosComoArray(t *testing.T) {
	env := output.New("status")

	b, err := json.Marshal(env)
	require.NoError(t, err)

	require.Contains(t, string(b), `"diagnostics":[]`)
	require.NotContains(t, string(b), `"diagnostics":null`)
}

func TestEnvelopeNasceOK(t *testing.T) {
	env := output.New("status")

	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.Equal(t, output.Version, env.NgxVersion)
}

// Teste golden: trava as tags JSON do envelope, do diagnostico e do meta
// contra renomeacao futura. Tarefas seguintes consomem esses nomes
// verbatim; sem este teste, qualquer campo pode ser renomeado sem quebrar
// nada aqui.
func TestEnvelopeSerializaTodosOsCamposComAsTagsEsperadas(t *testing.T) {
	env := output.New("lint")
	env.Data = map[string]string{"resultado": "ok"}
	env.Meta = output.Meta{
		DurationMS:   42,
		NginxVersion: "1.25.3",
		ConfigHash:   "abc123",
	}
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "seletor malformado",
		File:     "nginx.conf",
		Line:     10,
		Column:   4,
		Selector: "http.server[0]",
		ID:       "diag-1",
		Docs:     "https://example.com/NGX-0002",
	})

	b, err := json.Marshal(env)
	require.NoError(t, err)

	expected := `{
		"ok": false,
		"command": "lint",
		"ngx_version": "0.1.0-dev",
		"data": {"resultado": "ok"},
		"diagnostics": [
			{
				"severity": "error",
				"code": "NGX-0002",
				"message": "seletor malformado",
				"file": "nginx.conf",
				"line": 10,
				"column": 4,
				"selector": "http.server[0]",
				"id": "diag-1",
				"docs": "https://example.com/NGX-0002"
			}
		],
		"meta": {
			"duration_ms": 42,
			"nginx_version": "1.25.3",
			"config_hash": "abc123"
		}
	}`

	require.JSONEq(t, expected, string(b))
}

// A severidade error é o que derruba o ok do envelope. Warning e info não.
func TestAddDiagnosticErrorDerrubaOK(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "cuidado"})
	require.True(t, env.OK, "warning nao deve derrubar ok")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityInfo, Message: "so um aviso"})
	require.True(t, env.OK, "info nao deve derrubar ok")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.False(t, env.OK, "error deve derrubar ok")

	require.Len(t, env.Diagnostics, 3)
}

// Campos opcionais ausentes nao devem poluir a saida do agente.
func TestDiagnosticOmiteCamposVazios(t *testing.T) {
	b, err := json.Marshal(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "seletor malformado",
	})
	require.NoError(t, err)

	s := string(b)
	require.NotContains(t, s, `"file"`, "file vazio deve ser omitido")
	require.NotContains(t, s, `"line"`, "line zero deve ser omitido")
	require.NotContains(t, s, `"column"`, "column zero deve ser omitido")
	require.NotContains(t, s, `"selector"`, "selector vazio deve ser omitido")
	require.NotContains(t, s, `"id"`, "id vazio deve ser omitido")
	require.NotContains(t, s, `"docs"`, "docs vazio deve ser omitido")
}
