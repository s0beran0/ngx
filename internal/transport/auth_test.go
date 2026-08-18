package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/s0beran0/ngx/internal/output"
)

// Nenhum teste deste arquivo abre socket, sobe servidor ou espera digitacao.
// O ambienteAuth existe para isso: a montagem dos metodos e uma decisao pura
// sobre o que esta disponivel, e e essa decisao que se verifica aqui.

// ambienteVazio e o pior cenario: sem ssh-agent, sem variaveis de ambiente e
// sem terminal. lerSegredo reprova o teste se for chamado — um prompt aqui
// significaria que o ngx pararia esperando alguem digitar num pipe.
func ambienteVazio(t *testing.T) ambienteAuth {
	t.Helper()
	return ambienteAuth{
		conectarAgente: func() (net.Conn, error) {
			return nil, errors.New("SSH_AUTH_SOCK nao esta definida no ambiente")
		},
		lerEnv:          func(string) string { return "" },
		stdinEhTerminal: func() bool { return false },
		lerSegredo: func(prompt string) (string, error) {
			t.Fatalf("prompt de segredo disparado sem terminal: %q", prompt)
			return "", nil
		},
	}
}

// comSSHAgent devolve um ambiente onde o ssh-agent responde. A conexao e um
// net.Pipe que nunca e usado: agent.NewClient nao faz I/O na construcao e o
// metodo so consulta o agente durante o handshake, que estes testes nao fazem.
func comSSHAgent(t *testing.T, amb ambienteAuth) ambienteAuth {
	t.Helper()
	amb.conectarAgente = func() (net.Conn, error) {
		nossa, deles := net.Pipe()
		t.Cleanup(func() { _ = deles.Close() })
		return nossa, nil
	}
	return amb
}

func comEnv(amb ambienteAuth, pares map[string]string) ambienteAuth {
	amb.lerEnv = func(nome string) string { return pares[nome] }
	return amb
}

// escreverChave grava uma chave ed25519 em disco. Com passphrase vazia a chave
// sai em claro.
func escreverChave(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var bloco *pem.Block
	if passphrase == "" {
		bloco, err = ssh.MarshalPrivateKey(priv, "ngx-test")
	} else {
		bloco, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "ngx-test", []byte(passphrase))
	}
	require.NoError(t, err)

	caminho := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(caminho, pem.EncodeToMemory(bloco), 0o600))
	return caminho
}

func diagnosticoComCodigo(diags []output.Diagnostic, codigo string) *output.Diagnostic {
	for i := range diags {
		if diags[i].Code == codigo {
			return &diags[i]
		}
	}
	return nil
}

// A ordem e o contrato central da DR2, com a excecao da chave nomeada.
//
// Quando o usuario passa --key, ela vem ANTES do ssh-agent. O motivo foi
// medido contra um sshd real: o MaxAuthTries padrao e 6, cada chave do
// ssh-agent gasta uma tentativa, e um desenvolvedor costuma ter varias
// carregadas -- entao a chave explicitamente pedida nunca chegava a ser
// oferecida e a conexao morria com "no supported methods remain", mensagem
// que nao aponta para a causa. E o mesmo problema que IdentitiesOnly=yes
// resolve no ssh.
//
// Sem --key, o ssh-agent continua na frente, que e o preferivel: com ele a
// chave privada nunca e lida pelo ngx.
func TestMontarAutenticacaoChaveNomeadaVemAntesDoAgente(t *testing.T) {
	amb := comSSHAgent(t, comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH: "s3nha",
	}))

	auth, diags, err := montarAutenticacao(SSHOptions{
		Host:    "web1",
		User:    "deploy",
		KeyPath: escreverChave(t, ""),
	}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoChave, MetodoSSHAgent, MetodoSenha}, auth.Nomes,
		"chave nomeada em --key precede o ssh-agent para nao esbarrar no MaxAuthTries")
	require.Len(t, auth.Metodos, len(auth.Nomes))
	require.NotNil(t, diags)
	require.Empty(t, diags)
}

func TestMontarAutenticacaoSemChaveNomeadaMantemAgenteNaFrente(t *testing.T) {
	amb := comSSHAgent(t, comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH: "s3nha",
	}))

	auth, _, err := montarAutenticacao(SSHOptions{
		Host: "web1",
		User: "deploy",
	}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSSHAgent, MetodoSenha}, auth.Nomes,
		"sem --key o ssh-agent vem primeiro: a chave privada nunca e lida pelo ngx")
}

// Ausencia de ssh-agent nao e erro: e um metodo a menos, com um diagnostico
// informativo. Confundir os dois faria o ngx falhar em qualquer maquina que
// nao esteja com o agente rodando.
func TestSSHAgentAusenteNaoEErro(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoSSHAgentAusente)
	require.NotNil(t, d, "a ausencia do ssh-agent precisa ser informada, nao silenciosa")
	require.Equal(t, output.SeverityInfo, d.Severity,
		"nao ha ssh-agent e uma situacao normal; severidade de erro faria parecer defeito")
	require.Contains(t, d.Message, "ssh-add")
}

// A senha vem do ambiente, e vir do ambiente dispensa qualquer prompt: o
// lerSegredo de ambienteVazio reprova o teste se for chamado.
func TestSenhaVemDoAmbiente(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// Rodando sob pipe — que e como um agente de IA usa o ngx — nao ha como
// perguntar nada. O comando tem de falhar com uma instrucao acionavel, e nao
// travar. O teste completa: se travasse, ele estouraria o timeout do go test.
func TestSemTerminalESemAmbienteFalhaComMensagemClara(t *testing.T) {
	auth, diags, err := montarAutenticacao(
		SSHOptions{Host: "web1", User: "deploy"}, ambienteVazio(t))
	require.Error(t, err)
	require.Nil(t, auth)
	require.NotNil(t, diags)

	var saida *output.Error
	require.ErrorAs(t, err, &saida)
	require.Equal(t, CodigoSemMetodoAuth, saida.Diag.Code)
	require.Equal(t, output.SeverityError, saida.Diag.Severity)
	require.Contains(t, saida.Diag.Message, EnvSenhaSSH,
		"a mensagem precisa dizer qual variavel de ambiente definir")
	require.Contains(t, saida.Diag.Message, "deploy@web1")
}

// Com terminal existe metodo de senha, mas o prompt e adiado para o handshake:
// se o servidor aceitar a chave, ninguem e perguntado. Montar a lista nunca
// pode ler segredo.
func TestComTerminalNaoPerguntaNaMontagem(t *testing.T) {
	amb := ambienteVazio(t)
	amb.stdinEhTerminal = func() bool { return true }
	amb.lerSegredo = func(prompt string) (string, error) {
		t.Fatalf("prompt disparado durante a montagem, antes de qualquer handshake: %q", prompt)
		return "", nil
	}

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// Chave cifrada com a passphrase no ambiente e aberta na montagem, sem prompt.
func TestChaveCifradaComPassphraseNoAmbiente(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvPassphraseChaveSSH: "abre-te"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoChave}, auth.Nomes)
	require.Nil(t, diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel))
}

// Chave cifrada, sem passphrase no ambiente e sem terminal: o metodo sai da
// lista com um aviso que nomeia a variavel. Nada de prompt sob pipe.
func TestChaveCifradaSemTerminalSaiDaListaComAviso(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Equal(t, output.SeverityWarning, d.Severity)
	require.Contains(t, d.Message, EnvPassphraseChaveSSH)
	require.Equal(t, caminho, d.File)
}

// Chave apontada que nao existe nao derruba a conexao: vira aviso e a ordem
// segue. Mas nao pode ser silenciosa — cair calado para a senha faria um
// caminho errado parecer certo.
func TestChaveInexistenteViraAvisoENaoErro(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "nao-existe")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Equal(t, output.SeverityWarning, d.Severity)
}

// Arquivo que nao e chave nenhuma tambem sai com aviso, e nao com erro.
func TestChaveMalformadaViraAviso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "lixo")
	require.NoError(t, os.WriteFile(caminho, []byte("isto nao e uma chave"), 0o600))
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
	require.NotNil(t, diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel))
}

// Passphrase errada no ambiente nao pode virar uma tentativa muda: sai aviso
// dizendo que a variavel nao abre a chave.
func TestPassphraseErradaNoAmbienteViraAviso(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{
		EnvPassphraseChaveSSH: "errada",
		EnvSenhaSSH:           "s3nha",
	})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Contains(t, d.Message, EnvPassphraseChaveSSH)
}

// Nenhum segredo pode escapar por diagnostico ou mensagem de erro. Quem le a
// saida do ngx nao e necessariamente quem tem direito ao segredo.
func TestSegredoNaoVazaEmDiagnostico(t *testing.T) {
	const senha = "s3nha-supersecreta"
	const passphrase = "passphrase-supersecreta"

	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH:           senha,
		EnvPassphraseChaveSSH: passphrase,
	})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	for _, d := range diags {
		require.NotContains(t, d.Message, senha)
		require.NotContains(t, d.Message, passphrase)
	}
}

// O metodo do ssh-agent segura a conexao aberta ate o fim do handshake; Close
// e quem a devolve, e chamar duas vezes precisa ser seguro.
func TestCloseEIdempotente(t *testing.T) {
	amb := comSSHAgent(t, ambienteVazio(t))

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1"}, amb)
	require.NoError(t, err)

	require.NoError(t, auth.Close())
	require.NoError(t, auth.Close())

	var nulo *Autenticacao
	require.NoError(t, nulo.Close())
}

// A montagem nunca devolve lista ou nomes nulos: um agente que faz .length
// numa lista nula quebra.
func TestListasNuncaSaoNulas(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.NotNil(t, auth.Metodos)
	require.NotNil(t, auth.Nomes)
	require.NotNil(t, diags)
}

// Uma senha ja resolvida pelo chamador vence o ambiente. O campo existe para
// receber o que veio do ambiente, nunca de flag.
func TestSenhaDasOpcoesVenceOAmbiente(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "do-ambiente"})

	auth, _, err := montarAutenticacao(
		SSHOptions{Host: "web1", Password: "das-opcoes"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// Nenhuma flag de senha pode existir na superficie do pacote: flag aparece em
// `ps`, no historico do shell e no log de CI. O teste le o proprio fonte
// porque o defeito que ele previne e alguem acrescentar a flag depois.
func TestNenhumaFlagDeSenhaNoPacote(t *testing.T) {
	entradas, err := os.ReadDir(".")
	require.NoError(t, err)

	proibidos := []string{"--password", "--senha", "--passphrase", "--key-passphrase"}
	for _, e := range entradas {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		conteudo, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		for _, p := range proibidos {
			require.NotContains(t, string(conteudo), p,
				"%s menciona %s: segredo nunca vem de flag", e.Name(), p)
		}
	}
}
