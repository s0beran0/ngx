//go:build windows

package transport

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// PipeSSHAgentWindows is the named pipe that the OpenSSH ssh-agent for Windows
// creates. The Windows ssh-agent is not a Unix socket.
//
// The name was not guessed: ssh-agent.exe itself creates it with this literal
// string, in openssh-portable (branch latestw_all),
// contrib/win32/win32compat/ssh-agent/agent.c:50 —
// `#define AGENT_PIPE_ID L"\\\\.\\pipe\\openssh-ssh-agent"` — used in the
// CreateNamedPipeW right below it.
const PipeSSHAgentWindows = `\\.\pipe\openssh-ssh-agent`

// tempoLimiteSSHAgentWindows caps the wait on a busy pipe. ERROR_PIPE_BUSY is
// transient and go-winio does the retry for us, but without a ceiling a stuck
// agent would hold the command indefinitely.
const tempoLimiteSSHAgentWindows = 2 * time.Second

// conectarSSHAgent opens the connection to the system ssh-agent.
//
// The rule mirrors OpenSSH on Windows: honor SSH_AUTH_SOCK when it is set and,
// only when empty, fall back to the default pipe. That is literally what
// ssh.exe does — contrib/win32/win32compat/wmain_common.c:53-54 fills
// SSH_AUTH_SOCK with this value in wmain when the variable is empty, and from
// there on the portable code just uses the variable. Keeping the order makes
// ngx work with the native ssh-agent with no configuration at all and, at the
// same time, respects whoever points SSH_AUTH_SOCK at another agent
// (1Password, gpg-agent, a WSL relay).
//
// Not reaching the agent is not an ngx error: it comes wrapped in
// errSSHAgentAusente and amounts to one less method in the list.
//
// The signature is identical to the one in agent_unix.go: the build tags pick
// which of the two goes into the binary and the rest of the package does not
// know the difference.
func conectarSSHAgent() (net.Conn, error) {
	caminho := os.Getenv(EnvSocketSSHAgent)
	if caminho == "" {
		caminho = PipeSSHAgentWindows
	}

	limite := tempoLimiteSSHAgentWindows
	conn, err := winio.DialPipe(caminho, &limite)
	if err != nil {
		return nil, fmt.Errorf("%w: could not talk to the named pipe %s: %w",
			errSSHAgentAusente, caminho, err)
	}
	return conn, nil
}
