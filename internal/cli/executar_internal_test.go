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

// caminhosIsolados devolve dois caminhos dentro de um t.TempDir() para uso
// em Context.GlobalSettingsPath/LocalSettingsPath, para que preparar rode
// settings.Load sem tocar no filesystem real (/etc/ngx/ngx.yaml) nem
// depender do cwd do processo de teste.
func caminhosIsolados(t *testing.T) (global, local string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml")
}

// Um comando pode devolver um erro tipado embrulhado com %w — o padrao
// idiomatico para anexar contexto (ex.: fmt.Errorf("ao ler %s: %w", caminho,
// output.InvalidConfig(...))), e o que comandos futuros vao escrever.
// executar precisa preservar o exit code e o diagnostico originais em vez de
// substituir por um Usage generico so porque uma type assertion direta nao
// atravessa o wrapping.
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
			return fmt.Errorf("ao ler config: %w", output.InvalidConfig("configuracao invalida"))
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

// Um output.format invalido no arquivo de configuracao e erro de uso mesmo
// com --quiet: a validacao roda em preparar, antes do portao de --quiet do
// Renderer, entao o erro nunca fica em silencio so porque o usuario pediu
// saida minima. Os caminhos de settings sao isolados via Context.*SettingsPath,
// sem precisar trocar o cwd do processo de teste.
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
