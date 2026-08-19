package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/update"
)

// Num binario empacotado, `ngx update` recusa e ensina o comando certo (DC2).
// O teste passa pelo comando inteiro, e nao so por update.Run, porque e aqui
// que se ve o que o consumidor recebe: envelope de erro em stdout, com o codigo
// tipado e o comando nomeado na mensagem. Nenhuma requisicao sai — a recusa
// acontece antes de a rede ser tocada, entao este teste nao depende do GitHub.
func TestUpdateRefusesOnPackagedInstallChannel(t *testing.T) {
	casos := []struct {
		canal   string
		comando string
	}{
		{"homebrew", "brew upgrade ngx"},
		{"deb", "apt upgrade ngx"},
		{"rpm", "dnf upgrade ngx"},
		{"aur", "pacman -Syu ngx"},
		{"scoop", "scoop update ngx"},
		{"winget", "winget upgrade ngx"},
	}

	for _, caso := range casos {
		t.Run(caso.canal, func(t *testing.T) {
			comCanalDeInstalacao(t, caso.canal)

			var out, errBuf bytes.Buffer
			code := cli.Execute([]string{"update"}, &out, &errBuf, false)

			require.Equal(t, output.ExitInternal, code)

			var env output.Envelope
			require.NoError(t, json.Unmarshal(out.Bytes(), &env))
			assert.False(t, env.OK)
			assert.Equal(t, "update", env.Command)
			require.NotEmpty(t, env.Diagnostics)
			assert.Equal(t, "NGX-0316", env.Diagnostics[0].Code)
			assert.Contains(t, env.Diagnostics[0].Message, caso.comando)
		})
	}
}

// Canal escrito errado tambem recusa. Um `-X ...InstallChannel=homebrwe` num
// pipeline de empacotamento nao pode devolver o auto-update em silencio.
func TestUpdateRefusesOnUnknownInstallChannel(t *testing.T) {
	comCanalDeInstalacao(t, "homebrwe")

	var out, errBuf bytes.Buffer
	code := cli.Execute([]string{"update"}, &out, &errBuf, false)

	require.Equal(t, output.ExitInternal, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	assert.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	assert.Equal(t, "NGX-0317", env.Diagnostics[0].Code)
	assert.Contains(t, env.Diagnostics[0].Message, "homebrwe")
}

// comCanalDeInstalacao troca a variavel injetada por -ldflags e a restaura.
func comCanalDeInstalacao(t *testing.T, canal string) {
	t.Helper()
	anterior := update.InstallChannel
	update.InstallChannel = canal
	t.Cleanup(func() { update.InstallChannel = anterior })
}
