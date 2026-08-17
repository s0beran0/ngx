//go:build !windows

package transport

import (
	"fmt"
	"net"
	"os"
)

// conectarSSHAgent abre a conexao com o ssh-agent do sistema.
//
// Fora do Windows o canal e um socket Unix cujo caminho o proprio ssh-agent
// publica em SSH_AUTH_SOCK — e a mesma variavel que o ssh(1) le em
// ssh_get_authentication_socket. Variavel vazia significa "nao ha agente":
// nao existe caminho padrao para tentar no lugar dela.
//
// Nenhum dos dois insucessos e erro do ngx. Ambos vem embrulhados em
// errSSHAgentAusente e viram apenas um metodo a menos na lista.
//
// A assinatura e identica a de agent_windows.go: as build tags escolhem qual
// dos dois entra no binario e o resto do pacote nao sabe a diferenca.
func conectarSSHAgent() (net.Conn, error) {
	caminho := os.Getenv(EnvSocketSSHAgent)
	if caminho == "" {
		return nil, fmt.Errorf("%w: %s nao esta definida no ambiente", errSSHAgentAusente, EnvSocketSSHAgent)
	}

	conn, err := net.Dial("unix", caminho)
	if err != nil {
		return nil, fmt.Errorf("%w: nao foi possivel falar com o socket %s: %w",
			errSSHAgentAusente, caminho, err)
	}
	return conn, nil
}
