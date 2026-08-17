//go:build windows

package transport

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// PipeSSHAgentWindows e o named pipe que o ssh-agent do OpenSSH para Windows
// cria. O ssh-agent do Windows nao e um socket Unix.
//
// O nome nao foi adivinhado: o proprio ssh-agent.exe o cria com esta string
// literal, em openssh-portable (branch latestw_all),
// contrib/win32/win32compat/ssh-agent/agent.c:50 —
// `#define AGENT_PIPE_ID L"\\\\.\\pipe\\openssh-ssh-agent"` — usada no
// CreateNamedPipeW logo abaixo.
const PipeSSHAgentWindows = `\\.\pipe\openssh-ssh-agent`

// tempoLimiteSSHAgentWindows limita a espera por um pipe ocupado. O
// ERROR_PIPE_BUSY e transitorio e o go-winio faz o retry por nos, mas sem teto
// um agente travado seguraria o comando indefinidamente.
const tempoLimiteSSHAgentWindows = 2 * time.Second

// conectarSSHAgent abre a conexao com o ssh-agent do sistema.
//
// A regra espelha a do OpenSSH no Windows: honrar SSH_AUTH_SOCK quando ela
// estiver definida e, so quando vazia, cair no pipe padrao. E literalmente o
// que o ssh.exe faz — contrib/win32/win32compat/wmain_common.c:53-54 preenche
// SSH_AUTH_SOCK com esse valor no wmain quando a variavel esta vazia, e dali
// em diante o codigo portavel usa a variavel normalmente. Manter a ordem faz o
// ngx funcionar com o ssh-agent nativo sem configuracao alguma e, ao mesmo
// tempo, respeitar quem aponta SSH_AUTH_SOCK para outro agente (1Password,
// gpg-agent, um relay de WSL).
//
// Nao alcancar o agente nao e erro do ngx: vem embrulhado em
// errSSHAgentAusente e vira um metodo a menos na lista.
//
// A assinatura e identica a de agent_unix.go: as build tags escolhem qual dos
// dois entra no binario e o resto do pacote nao sabe a diferenca.
func conectarSSHAgent() (net.Conn, error) {
	caminho := os.Getenv(EnvSocketSSHAgent)
	if caminho == "" {
		caminho = PipeSSHAgentWindows
	}

	limite := tempoLimiteSSHAgentWindows
	conn, err := winio.DialPipe(caminho, &limite)
	if err != nil {
		return nil, fmt.Errorf("%w: nao foi possivel falar com o named pipe %s: %w",
			errSSHAgentAusente, caminho, err)
	}
	return conn, nil
}
