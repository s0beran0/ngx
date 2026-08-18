//go:build !windows

package transport

import (
	"fmt"
	"net"
	"os"
)

// conectarSSHAgent opens the connection to the system ssh-agent.
//
// Outside Windows the channel is a Unix socket whose path the ssh-agent itself
// publishes in SSH_AUTH_SOCK — the same variable ssh(1) reads in
// ssh_get_authentication_socket. An empty variable means "there is no agent":
// there is no default path to try instead.
//
// Neither failure is an ngx error. Both come wrapped in errSSHAgentAusente and
// amount to one less method in the list.
//
// The signature is identical to the one in agent_windows.go: the build tags
// pick which of the two goes into the binary and the rest of the package does
// not know the difference.
func conectarSSHAgent() (net.Conn, error) {
	caminho := os.Getenv(EnvSocketSSHAgent)
	if caminho == "" {
		return nil, fmt.Errorf("%w: %s is not set in the environment", errSSHAgentAusente, EnvSocketSSHAgent)
	}

	conn, err := net.Dial("unix", caminho)
	if err != nil {
		return nil, fmt.Errorf("%w: could not talk to the socket %s: %w",
			errSSHAgentAusente, caminho, err)
	}
	return conn, nil
}
