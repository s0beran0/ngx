package runtime

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestStateMasterVivo(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("kill -0 4242", resposta{})
	f.arquivos["/run/nginx.pid"] = "4242\n"

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	require.NotNil(t, s.Running)
	assert.True(t, *s.Running)
	assert.Equal(t, 4242, s.MasterPID)
	assert.Empty(t, s.Diagnostics)
}

func TestStatePidfileAusenteSignificaParado(t *testing.T) {
	f := novoFake("local")

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	require.NotNil(t, s.Running)
	assert.False(t, *s.Running)
	assert.Zero(t, s.MasterPID)
	require.Len(t, s.Diagnostics, 1)
	assert.Equal(t, output.SeverityInfo, s.Diagnostics[0].Severity)
}

// "I could not read it" is different from "it does not exist" (DR5). Without
// permission, the field drops out of the JSON and a diagnostic says why --
// never a silent false.
func TestStatePidfileIlegivelOmiteRunning(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22")
	f.errosOpen["/run/nginx.pid"] = &fs.PathError{
		Op: "open", Path: "/run/nginx.pid", Err: fs.ErrPermission,
	}

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	assert.Nil(t, s.Running)
	require.Len(t, s.Diagnostics, 1)
	assert.Equal(t, CodigoPrivilegioNecessario, s.Diagnostics[0].Code)

	bruto, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(bruto), "running")
	assert.NotContains(t, string(bruto), "master_pid")
}

func TestStatePidfileObsoleto(t *testing.T) {
	f := novoFake("local").responde("kill -0 4242", resposta{
		stderr: "kill: (4242): No such process\n",
		exit:   1,
	})
	f.arquivos["/run/nginx.pid"] = "4242"

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	require.NotNil(t, s.Running)
	assert.False(t, *s.Running)
	assert.Equal(t, 4242, s.MasterPID)
	require.Len(t, s.Diagnostics, 1)
	assert.Contains(t, s.Diagnostics[0].Message, "stale")
}

// A process of another user: saying it is not running would be false, saying
// it is running would be guessing. The field disappears and the reason shows
// up.
func TestStateProcessoDeOutroUsuario(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("kill -0 4242", resposta{
		stderr: "kill: (4242): Operation not permitted\n",
		exit:   1,
	})
	f.arquivos["/run/nginx.pid"] = "4242"

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	assert.Nil(t, s.Running)
	assert.Equal(t, 4242, s.MasterPID)
	require.Len(t, s.Diagnostics, 1)
	assert.Equal(t, CodigoPrivilegioNecessario, s.Diagnostics[0].Code)
}

func TestStatePidfileComLixo(t *testing.T) {
	f := novoFake("local")
	f.arquivos["/run/nginx.pid"] = "not a pid\n"

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	assert.Nil(t, s.Running)
	assert.Zero(t, s.MasterPID)
	require.Len(t, s.Diagnostics, 1)
	assert.Equal(t, CodigoEstadoProcesso, s.Diagnostics[0].Code)
}

func TestStateSemCaminhoDePidfile(t *testing.T) {
	s, err := New(novoFake("local")).State(context.Background(), "")
	require.NoError(t, err)

	assert.Nil(t, s.Running)
	require.Len(t, s.Diagnostics, 1)
	assert.Equal(t, CodigoEstadoProcesso, s.Diagnostics[0].Code)
}

// The state never carries workers nor the configuration load time: both
// require process inspection and have no trustworthy source over SSH. This
// test exists so that adding one of them without a source breaks something.
func TestStateNaoInventaWorkersNemHorario(t *testing.T) {
	f := novoFake("local").responde("kill -0 7", resposta{})
	f.arquivos["/run/nginx.pid"] = "7"

	s, err := New(f).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	bruto, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(bruto), "workers")
	assert.NotContains(t, string(bruto), "config_loaded_at")
	assert.Contains(t, string(bruto), `"diagnostics":[]`)
}

// The state does not escalate privilege either: asking whether a pid exists
// does not require sudo, and escalating here would go against DR5 with no gain
// at all.
func TestStateNaoUsaSudoNoKill(t *testing.T) {
	f := novoFake("local").responde("kill -0 7", resposta{})
	f.arquivos["/run/nginx.pid"] = "7"

	_, err := New(f, ComSudo(true)).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	chamadas := f.chamadas()
	require.Len(t, chamadas, 1)
	assert.Equal(t, []string{"kill", "-0", "7"}, chamadas[0])
}

// With --sudo the operator has already authorized privilege, and DR5 requires
// it to be EXPLICIT, not to be refused. The nginx master runs as root, so
// without this second attempt the `running` field would stay unavailable in
// the most common case there is -- verified against a real production nginx.
//
// The pair of cases is what proves the rule: without the flag nothing is
// escalated.
func TestStateComSudoConfirmaProcessoDeOutroUsuario(t *testing.T) {
	novo := func() *fakeTransport {
		f := novoFake("ssh://opc@10.0.0.7:22").responde("kill -0 4242", resposta{
			stderr: "kill: (4242): Operation not permitted\n",
			exit:   1,
		}).responde("sudo -n kill -0 4242", resposta{})
		f.arquivos["/run/nginx.pid"] = "4242"
		return f
	}

	t.Run("with sudo the field becomes available", func(t *testing.T) {
		f := novo()
		s, err := New(f, ComSudo(true)).State(context.Background(), "/run/nginx.pid")
		require.NoError(t, err)

		require.NotNil(t, s.Running, "with privilege authorized the state is known")
		assert.True(t, *s.Running)
		assert.Empty(t, s.Diagnostics, "nothing to warn about when the answer is unambiguous")
	})

	t.Run("without sudo nothing is escalated", func(t *testing.T) {
		f := novo()
		s, err := New(f).State(context.Background(), "/run/nginx.pid")
		require.NoError(t, err)

		assert.Nil(t, s.Running, "without the flag the field disappears, instead of becoming a guess")
		for _, argv := range f.executados {
			assert.NotEqual(t, "sudo", argv[0], "escalating without --sudo goes against DR5")
		}
	})
}
