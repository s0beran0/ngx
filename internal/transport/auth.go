package transport

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"

	"github.com/s0beran0/ngx/internal/output"
)

// Codigos de diagnostico da autenticacao (DR2).
//
// So o primeiro e erro: nao ter ssh-agent, ou nao conseguir usar a chave
// apontada, e apenas um metodo a menos na lista. O comando so para quando a
// lista fica vazia — ai nao ha o que tentar contra o servidor.
const (
	// CodigoSemMetodoAuth: nenhum metodo de autenticacao pode ser montado.
	CodigoSemMetodoAuth = "NGX-0205"

	// CodigoAvisoSSHAgentAusente: nao ha ssh-agent alcancavel. Situacao
	// normal, informada porque muda o que sera tentado.
	CodigoAvisoSSHAgentAusente = "NGX-0212"

	// CodigoAvisoChaveIndisponivel: a chave apontada existe na configuracao
	// mas nao pode ser usada — arquivo ausente, formato invalido, ou
	// passphrase que nao ha como obter.
	CodigoAvisoChaveIndisponivel = "NGX-0213"
)

// Nomes dos metodos de autenticacao, na ordem em que sao tentados. Aparecem em
// Autenticacao.Nomes para que quem consome a saida saiba o que foi oferecido ao
// servidor sem ter que inferir a partir de uma falha.
const (
	MetodoSSHAgent = "ssh-agent"
	MetodoChave    = "chave"
	MetodoSenha    = "senha"
)

// Variaveis de ambiente de onde os segredos podem vir.
//
// Segredo nunca vem de flag. Flag aparece em `ps`, no historico do shell e no
// log de qualquer CI: quem passa uma senha por flag ja a vazou. As duas
// entradas aceitas sao o ambiente e o prompt em terminal — nesta ordem — e
// qualquer flag de senha adicionada aqui deve ser reprovada em review.
const (
	// EnvSenhaSSH carrega a senha do usuario no host remoto.
	EnvSenhaSSH = "NGX_SSH_PASSWORD"

	// EnvPassphraseChaveSSH carrega a passphrase que abre a chave privada.
	EnvPassphraseChaveSSH = "NGX_SSH_KEY_PASSPHRASE"

	// EnvSocketSSHAgent e a variavel padrao do OpenSSH que aponta o canal do
	// ssh-agent. E honrada em toda plataforma; no Windows, quando vazia, ha
	// um named pipe padrao (ver agent_windows.go).
	EnvSocketSSHAgent = "SSH_AUTH_SOCK"
)

// errSSHAgentAusente marca as falhas de conexao com o ssh-agent que nao sao
// erro do ngx: nao ha agente, ou ele nao responde. Existir como sentinela
// deixa explicito, em agent_unix.go e agent_windows.go, que o caminho de
// insucesso ali e esperado.
var errSSHAgentAusente = errors.New("ssh-agent indisponivel")

// Autenticacao e a lista de metodos que o ngx oferece ao servidor, na ordem
// da DR2: ssh-agent, chave em arquivo, senha.
//
// A ordem e o produto principal deste tipo. O ssh-agent vem primeiro porque
// com ele a chave privada nunca e lida pelo ngx — ele manda o desafio e recebe
// a assinatura —, e menos codigo nosso tocando material de chave e menos
// superficie para errar.
//
// Metodos e Nomes sao paralelos: Nomes[i] descreve Metodos[i]. Nenhum dos dois
// e nil.
type Autenticacao struct {
	Metodos []ssh.AuthMethod
	Nomes   []string

	fechar []func() error
}

// Close libera os recursos abertos na montagem — hoje, a conexao com o
// ssh-agent. Chamar depois do handshake, e nunca antes: o metodo do ssh-agent
// consulta as chaves durante a autenticacao. Chamar duas vezes e seguro.
func (a *Autenticacao) Close() error {
	if a == nil {
		return nil
	}
	var erros []error
	for _, f := range a.fechar {
		if err := f(); err != nil {
			erros = append(erros, err)
		}
	}
	a.fechar = nil
	return errors.Join(erros...)
}

// ambienteAuth reune as bordas do sistema que a montagem toca: o ssh-agent, o
// ambiente, e o terminal. Ficam atras de campos para que os testes exercitem a
// ordem dos metodos sem socket, sem variavel de ambiente real e — o que mais
// importa — sem nenhum caminho que possa parar esperando alguem digitar.
type ambienteAuth struct {
	conectarAgente  func() (net.Conn, error)
	lerEnv          func(string) string
	stdinEhTerminal func() bool
	lerSegredo      func(prompt string) (string, error)
}

func ambienteAuthPadrao() ambienteAuth {
	return ambienteAuth{
		conectarAgente:  conectarSSHAgent,
		lerEnv:          os.Getenv,
		stdinEhTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		lerSegredo:      lerSegredoDoTerminal,
	}
}

// MontarAutenticacao monta os metodos de autenticacao para as opcoes dadas.
//
// Devolve tres coisas pelo mesmo motivo que VerificadorHostKey: a lista, os
// diagnosticos do que ficou de fora, e o erro de quando nada sobrou. Um metodo
// que nao pode ser montado nao derruba a conexao — ele so nao entra na lista,
// com um diagnostico dizendo por que. Sem ssh-agent nao e falha; sem nenhum
// metodo e.
//
// A lista de diagnosticos nunca e nil.
func MontarAutenticacao(opts SSHOptions) (*Autenticacao, []output.Diagnostic, error) {
	return montarAutenticacao(opts, ambienteAuthPadrao())
}

func montarAutenticacao(opts SSHOptions, amb ambienteAuth) (*Autenticacao, []output.Diagnostic, error) {
	auth := &Autenticacao{Metodos: []ssh.AuthMethod{}, Nomes: []string{}}
	diags := []output.Diagnostic{}

	adicionar := func(nome string, metodo ssh.AuthMethod, diag *output.Diagnostic) {
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			auth.Metodos = append(auth.Metodos, metodo)
			auth.Nomes = append(auth.Nomes, nome)
		}
	}

	metodo, fechar, diag := metodoSSHAgent(amb)
	if fechar != nil {
		auth.fechar = append(auth.fechar, fechar)
	}
	adicionar(MetodoSSHAgent, metodo, diag)

	metodo, diag = metodoChave(opts, amb)
	adicionar(MetodoChave, metodo, diag)

	adicionar(MetodoSenha, metodoSenha(opts, amb), nil)

	if len(auth.Metodos) == 0 {
		_ = auth.Close()
		return nil, diags, erroSemMetodoAuth(opts)
	}

	return auth, diags, nil
}

// metodoSSHAgent conecta no ssh-agent do sistema e transforma o cliente num
// metodo de autenticacao.
//
// Usa PublicKeysCallback, e nao PublicKeys: com o callback a lista de chaves e
// pedida ao agente no momento da autenticacao, entao uma chave adicionada com
// `ssh-add` depois que o ngx comecou ainda e vista.
//
// Nao alcancar o ssh-agent devolve (nil, nil, aviso). E o caso mais comum em
// maquina sem agente rodando e nao tem nada de errado.
func metodoSSHAgent(amb ambienteAuth) (ssh.AuthMethod, func() error, *output.Diagnostic) {
	conn, err := amb.conectarAgente()
	if err != nil {
		d := avisoSSHAgentAusente(err)
		return nil, nil, &d
	}
	cliente := agent.NewClient(conn)
	return ssh.PublicKeysCallback(cliente.Signers), conn.Close, nil
}

// metodoChave le a chave privada apontada por opts.KeyPath.
//
// Chave cifrada tem tres desfechos, nesta ordem: a passphrase esta no
// ambiente, e a chave e aberta agora; a entrada padrao e um terminal, e o
// prompt fica adiado para o momento da autenticacao — assim quem ja autenticou
// pelo ssh-agent nunca chega a ser perguntado; ou nao ha de onde tirar a
// passphrase, e o metodo sai da lista com um aviso que nomeia a variavel de
// ambiente.
//
// O terceiro caso e o que mantem o ngx utilizavel por um agente de IA: rodando
// sob pipe, ele falha rapido em vez de parar esperando uma digitacao que nunca
// vira.
func metodoChave(opts SSHOptions, amb ambienteAuth) (ssh.AuthMethod, *output.Diagnostic) {
	if opts.KeyPath == "" {
		return nil, nil
	}

	pem, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf("nao foi possivel ler o arquivo (%v)", err))
		return nil, &d
	}

	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return ssh.PublicKeys(signer), nil
	}

	var faltaPassphrase *ssh.PassphraseMissingError
	if !errors.As(err, &faltaPassphrase) {
		d := avisoChaveIndisponivel(opts.KeyPath,
			fmt.Sprintf("o arquivo nao e uma chave privada valida (%v)", err))
		return nil, &d
	}

	if passphrase := amb.lerEnv(EnvPassphraseChaveSSH); passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			d := avisoChaveIndisponivel(opts.KeyPath,
				fmt.Sprintf("a passphrase de %s nao abre a chave (%v)", EnvPassphraseChaveSSH, err))
			return nil, &d
		}
		return ssh.PublicKeys(signer), nil
	}

	if !amb.stdinEhTerminal() {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf(
			"a chave esta protegida por passphrase e a entrada padrao nao e um terminal, "+
				"entao nao ha como perguntar; defina %s no ambiente para usar esta chave",
			EnvPassphraseChaveSSH))
		return nil, &d
	}

	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		passphrase, err := amb.lerSegredo(fmt.Sprintf("passphrase da chave %s: ", opts.KeyPath))
		if err != nil {
			return nil, fmt.Errorf("nao foi possivel ler a passphrase de %s: %w", opts.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("a passphrase informada nao abre a chave %s: %w", opts.KeyPath, err)
		}
		return []ssh.Signer{signer}, nil
	}), nil
}

// metodoSenha e o ultimo recurso da ordem.
//
// A senha vem de opts.Password — preenchida pelo ambiente por quem montou as
// opcoes, nunca por flag —, do ambiente, ou de um prompt. O prompt so existe
// quando a entrada padrao e um terminal, e mesmo ai fica adiado para o momento
// da autenticacao: se o servidor aceitar a chave, ninguem e perguntado.
//
// Sem terminal e sem segredo no ambiente o metodo simplesmente nao existe.
// Nunca ha bloqueio esperando digitacao.
func metodoSenha(opts SSHOptions, amb ambienteAuth) ssh.AuthMethod {
	if opts.Password != "" {
		return ssh.Password(opts.Password)
	}
	if senha := amb.lerEnv(EnvSenhaSSH); senha != "" {
		return ssh.Password(senha)
	}
	if !amb.stdinEhTerminal() {
		return nil
	}
	return ssh.PasswordCallback(func() (string, error) {
		return amb.lerSegredo(fmt.Sprintf("senha de %s: ", destinoLegivel(opts)))
	})
}

// lerSegredoDoTerminal pergunta um segredo sem eco.
//
// O prompt sai em stderr porque stdout carrega o envelope JSON: escrever o
// texto do prompt ali corromperia a saida que outro programa esta parseando.
func lerSegredoDoTerminal(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("a entrada padrao nao e um terminal; defina %s no ambiente", EnvSenhaSSH)
	}
	fmt.Fprint(os.Stderr, prompt)
	segredo, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(segredo), nil
}

// avisoSSHAgentAusente informa que o ssh-agent ficou de fora.
//
// Severidade info, e nao warning: nao ha nada a corrigir. O diagnostico existe
// porque a lista de metodos oferecidos mudou, e quem le a saida precisa poder
// explicar uma recusa do servidor sem adivinhar.
func avisoSSHAgentAusente(causa error) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityInfo,
		Code:     CodigoAvisoSSHAgentAusente,
		Message: fmt.Sprintf(
			"ssh-agent nao esta disponivel (%v); a autenticacao por ssh-agent nao sera tentada. "+
				"Isto nao e um erro: se voce quer usa-la, inicie o ssh-agent e registre a chave "+
				"com `ssh-add`",
			causa),
	}
}

// avisoChaveIndisponivel informa que a chave apontada nao entrou na lista.
//
// Severidade warning, e nao info: alguem apontou uma chave — por --key ou pelo
// IdentityFile do ~/.ssh/config — e ela nao esta sendo usada. Cair calado para
// a senha faria um caminho errado parecer certo.
func avisoChaveIndisponivel(caminho, motivo string) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoChaveIndisponivel,
		Message: fmt.Sprintf(
			"a chave %s nao sera usada na autenticacao: %s", caminho, motivo),
		File: caminho,
	}
}

// erroSemMetodoAuth e o unico erro desta etapa: nao sobrou nada para oferecer
// ao servidor.
//
// Chegar aqui implica que a entrada padrao nao e um terminal — com terminal
// sempre existe ao menos o metodo de senha —, entao a mensagem nomeia a
// variavel de ambiente. E exatamente o caso de um agente de IA rodando o ngx
// sob pipe: em vez de parar esperando uma digitacao que nao vem, ele recebe a
// instrucao do que definir.
func erroSemMetodoAuth(opts SSHOptions) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSemMetodoAuth,
			Message: fmt.Sprintf(
				"nenhum metodo de autenticacao disponivel para %s: o ssh-agent nao respondeu, "+
					"nenhuma chave utilizavel foi indicada, e a entrada padrao nao e um terminal, "+
					"entao o ngx nao tem como perguntar a senha. Escolha um: inicie o ssh-agent e "+
					"registre a chave com `ssh-add`; aponte uma chave sem passphrase com --key (ou "+
					"defina %s); ou coloque a senha em %s. A senha nunca e aceita por flag, porque "+
					"flag aparece em `ps`, no historico do shell e no log de CI",
				destinoLegivel(opts), EnvPassphraseChaveSSH, EnvSenhaSSH),
		},
	}
}

// destinoLegivel descreve o alvo como o usuario o reconhece, "user@host".
func destinoLegivel(opts SSHOptions) string {
	switch {
	case opts.User != "" && opts.Host != "":
		return opts.User + "@" + opts.Host
	case opts.Host != "":
		return opts.Host
	default:
		return "o destino"
	}
}
