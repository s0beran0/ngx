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

// Os valores numericos dos exit codes sao o contrato de saida do processo.
// Comparar constante simbolica com constante simbolica nao pega uma troca
// acidental de valor (ex.: ExitDrift de 7 para 8); aqui fixamos os numeros.
func TestValoresDosExitCodesSaoContrato(t *testing.T) {
	require.Equal(t, 0, int(output.ExitOK))
	require.Equal(t, 1, int(output.ExitInternal))
	require.Equal(t, 2, int(output.ExitUsage))
	require.Equal(t, 3, int(output.ExitInvalidConfig))
	require.Equal(t, 7, int(output.ExitDrift))
	require.Equal(t, 9, int(output.ExitHashMismatch))
}

func TestConstrutoresCarregamSeuCodigo(t *testing.T) {
	casos := []struct {
		nome     string
		err      error
		want     output.ExitCode
		wantDiag string
	}{
		{"usage", output.Usage("seletor malformado: %q", "http..server"), output.ExitUsage, "NGX-0002"},
		{"config invalida", output.InvalidConfig("nginx -t falhou"), output.ExitInvalidConfig, "NGX-0003"},
		{"drift", output.Drift("config em disco mudou apos o ultimo reload"), output.ExitDrift, "NGX-0007"},
		{"hash", output.HashMismatch("sha256:aa", "sha256:bb"), output.ExitHashMismatch, "NGX-0009"},
		{"interno", output.Internal(errors.New("io"), "falha ao ler"), output.ExitInternal, "NGX-0001"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			require.Equal(t, c.want, output.CodeOf(c.err))

			var e *output.Error
			require.ErrorAs(t, c.err, &e)
			require.Equal(t, c.wantDiag, e.Diag.Code)
			require.Equal(t, output.SeverityError, e.Diag.Severity)
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

// Internal precisa preservar a causa original via Unwrap, mesmo que ela nao
// apareca na mensagem exibida. Sem isso, quem chama errors.Is/errors.As
// contra a causa nunca a encontra.
func TestInternalPreservaACausa(t *testing.T) {
	causa := errors.New("io: disco cheio")
	require.ErrorIs(t, output.Internal(causa, "falha ao ler"), causa)
}
