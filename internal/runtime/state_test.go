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

// "Nao consegui ler" e diferente de "nao existe" (DR5). Sem permissao, o
// campo sai do JSON e um diagnostico diz por que — nunca um false silencioso.
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
	assert.Contains(t, s.Diagnostics[0].Message, "obsoleto")
}

// Processo de outro usuario: afirmar que nao roda seria falso, afirmar que
// roda seria adivinhar. O campo some e o motivo aparece.
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
	f.arquivos["/run/nginx.pid"] = "nao e um pid\n"

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

// O estado nunca traz workers nem horario de carga da configuracao: os dois
// exigem inspecao de processo e nao tem fonte confiavel por SSH. Este teste
// existe para que acrescentar um deles sem fonte quebre alguma coisa.
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

// O estado tambem nao escala privilegio: perguntar se um pid existe nao exige
// sudo, e escalar aqui contrariaria a DR5 sem ganho nenhum.
func TestStateNaoUsaSudoNoKill(t *testing.T) {
	f := novoFake("local").responde("kill -0 7", resposta{})
	f.arquivos["/run/nginx.pid"] = "7"

	_, err := New(f, ComSudo(true)).State(context.Background(), "/run/nginx.pid")
	require.NoError(t, err)

	chamadas := f.chamadas()
	require.Len(t, chamadas, 1)
	assert.Equal(t, []string{"kill", "-0", "7"}, chamadas[0])
}

// Com --sudo o operador ja autorizou privilegio, e a DR5 exige que ele seja
// EXPLICITO, nao que seja recusado. O master do nginx roda como root, entao
// sem esta segunda tentativa o campo `running` ficaria indisponivel no caso
// mais comum que existe -- verificado contra um nginx de producao real.
//
// O par de casos e o que prova a regra: sem a flag nada e escalado.
func TestStateComSudoConfirmaProcessoDeOutroUsuario(t *testing.T) {
	novo := func() *fakeTransport {
		f := novoFake("ssh://opc@10.0.0.7:22").responde("kill -0 4242", resposta{
			stderr: "kill: (4242): Operation not permitted\n",
			exit:   1,
		}).responde("sudo -n kill -0 4242", resposta{})
		f.arquivos["/run/nginx.pid"] = "4242"
		return f
	}

	t.Run("com sudo o campo fica disponivel", func(t *testing.T) {
		f := novo()
		s, err := New(f, ComSudo(true)).State(context.Background(), "/run/nginx.pid")
		require.NoError(t, err)

		require.NotNil(t, s.Running, "com privilegio autorizado o estado e conhecido")
		assert.True(t, *s.Running)
		assert.Empty(t, s.Diagnostics, "nada a avisar quando a resposta e inequivoca")
	})

	t.Run("sem sudo nada e escalado", func(t *testing.T) {
		f := novo()
		s, err := New(f).State(context.Background(), "/run/nginx.pid")
		require.NoError(t, err)

		assert.Nil(t, s.Running, "sem a flag o campo some, em vez de virar palpite")
		for _, argv := range f.executados {
			assert.NotEqual(t, "sudo", argv[0], "escalar sem --sudo contraria a DR5")
		}
	})
}
