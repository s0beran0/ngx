package output_test

import (
	"encoding/json"
	"strings"
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
	require.NotEmpty(t, env.NgxVersion)
}

// A severidade error é o que derruba o ok do envelope. Warning e info não.
func TestAddDiagnosticErrorDerrubaOK(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "cuidado"})
	require.True(t, env.OK, "warning nao deve derrubar ok")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.False(t, env.OK, "error deve derrubar ok")

	require.Len(t, env.Diagnostics, 2)
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
	require.False(t, strings.Contains(s, `"file"`), "file vazio deve ser omitido")
	require.False(t, strings.Contains(s, `"line"`), "line zero deve ser omitido")
	require.False(t, strings.Contains(s, `"selector"`), "selector vazio deve ser omitido")
}
