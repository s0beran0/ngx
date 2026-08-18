package output_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestCodeOfNilEhSucesso(t *testing.T) {
	require.Equal(t, output.ExitOK, output.CodeOf(nil))
}

// An error that carries no code is an internal error, not a success.
func TestCodeOfErroDesconhecidoEhInterno(t *testing.T) {
	require.Equal(t, output.ExitInternal, output.CodeOf(errors.New("boom")))
}

// The numeric values of the exit codes are the process output contract.
// Comparing a symbolic constant against a symbolic constant does not catch an
// accidental value swap (e.g. ExitDrift going from 7 to 8); here we pin the
// numbers.
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
		{"usage", output.Usage("malformed selector: %q", "http..server"), output.ExitUsage, "NGX-0002"},
		{"invalid config", output.InvalidConfig("nginx -t failed"), output.ExitInvalidConfig, "NGX-0003"},
		{"drift", output.Drift("the config on disk changed after the last reload"), output.ExitDrift, "NGX-0007"},
		{"hash", output.HashMismatch("sha256:aa", "sha256:bb"), output.ExitHashMismatch, "NGX-0009"},
		{"internal", output.Internal(errors.New("io"), "read failed"), output.ExitInternal, "NGX-0001"},
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

// The code needs to survive wrapping, otherwise an error wrapped by an
// intermediate layer silently becomes exit 1.
func TestCodeOfAtravessaWrapping(t *testing.T) {
	err := fmt.Errorf("while loading the configuration: %w", output.Usage("invalid flag"))

	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

// Every typed error needs to yield a diagnostic that can be shown to the
// agent.
func TestErroExpoeDiagnostico(t *testing.T) {
	err := output.Usage("malformed selector: %q", "http..server")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	require.Equal(t, output.SeverityError, e.Diag.Severity)
	require.Equal(t, "NGX-0002", e.Diag.Code)
	require.Contains(t, e.Diag.Message, "http..server")
}

// HashMismatch is the error that stops the agent from acting on a stale ID.
// The message needs to show both hashes so it knows what happened.
func TestHashMismatchMostraOsDoisHashes(t *testing.T) {
	err := output.HashMismatch("sha256:expected", "sha256:current")

	require.Contains(t, err.Error(), "sha256:expected")
	require.Contains(t, err.Error(), "sha256:current")
}

// Internal needs to preserve the original cause via Unwrap, even if it does
// not show up in the displayed message. Without that, whoever calls
// errors.Is/errors.As against the cause never finds it.
func TestInternalPreservaACausa(t *testing.T) {
	causa := errors.New("io: disk full")
	require.ErrorIs(t, output.Internal(causa, "read failed"), causa)
}
