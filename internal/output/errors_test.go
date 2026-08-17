package output_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestCodeOfNilEhSucesso(t *testing.T) {
	require.Equal(t, output.ExitOK, output.CodeOf(nil))
}

// Um erro que nao carrega codigo e um erro interno, nao um sucesso.
func TestCodeOfErroDesconhecidoEhInterno(t *testing.T) {
	require.Equal(t, output.ExitInternal, output.CodeOf(errors.New("boom")))
}

func TestConstrutoresCarregamSeuCodigo(t *testing.T) {
	casos := []struct {
		nome string
		err  error
		want output.ExitCode
	}{
		{"usage", output.Usage("seletor malformado: %q", "http..server"), output.ExitUsage},
		{"config invalida", output.InvalidConfig("nginx -t falhou"), output.ExitInvalidConfig},
		{"drift", output.Drift("config em disco mudou apos o ultimo reload"), output.ExitDrift},
		{"hash", output.HashMismatch("sha256:aa", "sha256:bb"), output.ExitHashMismatch},
		{"interno", output.Internal(errors.New("io"), "falha ao ler"), output.ExitInternal},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			require.Equal(t, c.want, output.CodeOf(c.err))
		})
	}
}

// O codigo precisa sobreviver ao wrapping, senao um erro embrulhado por uma
// camada intermediaria vira exit 1 silenciosamente.
func TestCodeOfAtravessaWrapping(t *testing.T) {
	err := fmt.Errorf("ao carregar configuracao: %w", output.Usage("flag invalida"))

	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

// Todo erro tipado precisa render um diagnostico exibivel ao agente.
func TestErroExpoeDiagnostico(t *testing.T) {
	err := output.Usage("seletor malformado: %q", "http..server")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	require.Equal(t, output.SeverityError, e.Diag.Severity)
	require.Equal(t, "NGX-0002", e.Diag.Code)
	require.Contains(t, e.Diag.Message, "http..server")
}

// HashMismatch e o erro que impede o agente de agir sobre um ID envelhecido.
// A mensagem precisa mostrar os dois hashes para ele saber o que aconteceu.
func TestHashMismatchMostraOsDoisHashes(t *testing.T) {
	err := output.HashMismatch("sha256:esperado", "sha256:atual")

	require.Contains(t, err.Error(), "sha256:esperado")
	require.Contains(t, err.Error(), "sha256:atual")
}
