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

// In a packaged binary, `ngx update` refuses and names the command that does
// the job (DC2). The test goes through the whole command rather than through
// update.Run alone, because this is where what the consumer receives is
// visible: an error envelope on stdout, carrying the typed code and naming the
// command in the message. No request leaves -- the refusal happens before the
// network is touched, so this test does not depend on GitHub.
func TestUpdateRefusesOnPackagedInstallChannel(t *testing.T) {
	cases := []struct {
		channel string
		comando string
	}{
		{"homebrew", "brew upgrade ngx"},
		{"deb", "apt upgrade ngx"},
		{"rpm", "dnf upgrade ngx"},
		{"aur", "pacman -Syu ngx"},
		{"scoop", "scoop update ngx"},
		{"winget", "winget upgrade ngx"},
	}

	for _, c := range cases {
		t.Run(c.channel, func(t *testing.T) {
			withInstallChannel(t, c.channel)

			var out, errBuf bytes.Buffer
			code := cli.Execute([]string{"update"}, &out, &errBuf, false)

			require.Equal(t, output.ExitInternal, code)

			var env output.Envelope
			require.NoError(t, json.Unmarshal(out.Bytes(), &env))
			assert.False(t, env.OK)
			assert.Equal(t, "update", env.Command)
			require.NotEmpty(t, env.Diagnostics)
			assert.Equal(t, "NGX-0316", env.Diagnostics[0].Code)
			assert.Contains(t, env.Diagnostics[0].Message, c.comando)
		})
	}
}

// A misspelled channel refuses as well. An `-X ...InstallChannel=homebrwe` in
// a packaging pipeline must not silently hand self-update back.
func TestUpdateRefusesOnUnknownInstallChannel(t *testing.T) {
	withInstallChannel(t, "homebrwe")

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

// withInstallChannel swaps the variable injected by -ldflags and restores it.
func withInstallChannel(t *testing.T, channel string) {
	t.Helper()
	anterior := update.InstallChannel
	update.InstallChannel = channel
	t.Cleanup(func() { update.InstallChannel = anterior })
}
