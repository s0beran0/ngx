package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// caminhosIsolados returns two paths inside a t.TempDir() for use in
// Context.GlobalSettingsPath/LocalSettingsPath, so that preparar runs
// settings.Load without touching the real filesystem (/etc/ngx/ngx.yaml) nor
// depending on the cwd of the test process.
func caminhosIsolados(t *testing.T) (global, local string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml")
}

// A command may return a typed error wrapped with %w — the idiomatic pattern
// to attach context (e.g. fmt.Errorf("while reading %s: %w", caminho,
// output.InvalidConfig(...))), and what future commands are going to write.
// executar has to preserve the original exit code and diagnostic instead of
// replacing them with a generic Usage just because a direct type assertion
// does not traverse the wrapping.
func TestExecutarPreservaErroTipadoEmbrulhado(t *testing.T) {
	global, local := caminhosIsolados(t)

	var out bytes.Buffer
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: &out, IsTTY: false},
		GlobalSettingsPath: global,
		LocalSettingsPath:  local,
	}

	root := NewRoot(ctx)
	root.AddCommand(&cobra.Command{
		Use:  "falha-embrulhada",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("while reading config: %w", output.InvalidConfig("invalid configuration"))
		},
	})

	var errBuf bytes.Buffer
	code := executar(root, ctx, []string{"falha-embrulhada"}, &errBuf)

	require.Equal(t, output.ExitInvalidConfig, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0003", env.Diagnostics[0].Code)
}

// An invalid output.format in the configuration file is a usage error even
// with --quiet: the validation runs in preparar, before the Renderer's --quiet
// gate, so the error is never silenced just because the user asked for minimal
// output. The settings paths are isolated via Context.*SettingsPath, with no
// need to change the cwd of the test process.
func TestFormatoInvalidoNaConfigEhErroDeUsoMesmoComQuiet(t *testing.T) {
	global, local := caminhosIsolados(t)
	require.NoError(t, os.WriteFile(local, []byte("output:\n  format: xlm\n"), 0o644))

	var out bytes.Buffer
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: &out, IsTTY: false},
		GlobalSettingsPath: global,
		LocalSettingsPath:  local,
	}

	root := NewRoot(ctx)
	var errBuf bytes.Buffer
	code := executar(root, ctx, []string{"--quiet", "version"}, &errBuf)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "xlm")
}
