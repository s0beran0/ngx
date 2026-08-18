package transport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

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
	// home existe injetado para o teste nao depender do HOME de quem roda a
	// suite: a busca por chave padrao le ~/.ssh, e um teste que enxergasse a
	// chave real do desenvolvedor passaria na maquina dele e falharia no CI.
	home func() (string, error)
}

func ambienteAuthPadrao() ambienteAuth {
	return ambienteAuth{
		conectarAgente:  conectarSSHAgent,
		lerEnv:          os.Getenv,
		stdinEhTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		lerSegredo:      lerSegredoDoTerminal,
		home:            os.UserHomeDir,
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

	// Ordem da DR2 -- ssh-agent antes de arquivo de chave -- com uma
	// excecao medida contra servidor real: quando o usuario NOMEIA a chave
	// em --key, ela vem primeiro.
	//
	// O motivo e o MaxAuthTries do sshd, 6 por padrao. Cada chave do
	// ssh-agent gasta uma tentativa, e um desenvolvedor costuma ter varias
	// carregadas. Com o agente na frente, a chave explicitamente pedida
	// simplesmente nunca chega a ser oferecida, e o servidor derruba a
	// conexao com "no supported methods remain" -- mensagem que nao aponta
	// para a causa. E o mesmo problema que o IdentitiesOnly=yes do ssh
	// resolve.
	//
	// Sem --key a ordem original vale: o agente e preferivel justamente
	// porque a chave privada nunca e lida pelo ngx.
	chaveExplicita := opts.KeyPath != ""

	// TODAS as chaves entram num UNICO metodo de chave publica, e a ordem
	// dentro dele e que decide a preferencia.
	//
	// Medido contra um servidor real: com o ssh-agent carregado, oferecer
	// agente e arquivo como metodos SEPARADOS falhava, enquanto o `ssh`
	// conectava com as mesmas chaves. Assim que o primeiro metodo de chave
	// publica se esgota sem autenticar, o seguinte nao salva. O OpenSSH nao
	// sofre disso porque oferece tudo num metodo so -- e agora o ngx tambem.
	//
	// A ordem: chave nomeada em --key primeiro, porque o usuario a nomeou;
	// depois o ssh-agent, preferivel porque a chave privada nunca e lida por
	// nos; e por fim as chaves padrao do ~/.ssh, que sao o que faz
	// `ngx --host web1` funcionar para quem ja tem `ssh web1`.
	assinantes := []func() ([]ssh.Signer, error){}

	if chaveExplicita {
		metodo, diag := metodoChave(opts, amb)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			assinantes = append(assinantes, metodo)
			auth.Nomes = append(auth.Nomes, MetodoChave)
		}
	}

	if fonte, fechar, diag := assinantesDoAgente(amb); diag != nil || fonte != nil {
		if diag != nil {
			diags = append(diags, *diag)
		}
		if fechar != nil {
			auth.fechar = append(auth.fechar, fechar)
		}
		if fonte != nil {
			assinantes = append(assinantes, fonte)
			auth.Nomes = append(auth.Nomes, MetodoSSHAgent)
		}
	}

	if !chaveExplicita {
		metodo, diag := metodoChave(opts, amb)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			assinantes = append(assinantes, metodo)
			auth.Nomes = append(auth.Nomes, MetodoChave)
		}
	}

	if len(assinantes) > 0 {
		auth.Metodos = append(auth.Metodos, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			todos := []ssh.Signer{}
			for _, fonte := range assinantes {
				ss, err := fonte()
				if err != nil {
					// Uma fonte que falha nao derruba as outras: um
					// ssh-agent que morreu no meio nao pode impedir a
					// chave do disco de ser oferecida.
					continue
				}
				todos = append(todos, ss...)
			}
			return todos, nil
		}))
	}

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
func assinantesDoAgente(amb ambienteAuth) (fonteAssinantes, func() error, *output.Diagnostic) {
	conn, err := amb.conectarAgente()
	if err != nil {
		d := avisoSSHAgentAusente(err)
		return nil, nil, &d
	}
	cliente := agent.NewClient(conn)
	return cliente.Signers, conn.Close, nil
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
// ChavesPadrao sao os arquivos de identidade que o OpenSSH tenta quando
// ninguem indica um. A ordem e a dele. O `ssh` procurar por conta propria e
// justamente o que faz `ssh web1` funcionar sem configuracao, e a DR2 promete
// que `ngx --host web1` funcione para quem ja tem isso — entao o ngx procura
// tambem.
//
// Medido contra um servidor real: a chave que autenticava era ~/.ssh/id_rsa,
// fora do ~/.ssh/config e fora do ssh-agent, que so tinha certificados de
// outro sistema. Sem esta busca o ngx falhava onde o ssh conectava.
//
// Nao entra id_dsa: o OpenSSH desabilitou DSA por padrao, e oferecer uma
// chave que o servidor recusa so gasta uma das poucas tentativas do
// MaxAuthTries.
var ChavesPadrao = []string{"id_rsa", "id_ecdsa", "id_ed25519"}

func metodoChave(opts SSHOptions, amb ambienteAuth) (fonteAssinantes, *output.Diagnostic) {
	if opts.KeyPath == "" {
		return metodoChavesPadrao(amb)
	}

	pem, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf("nao foi possivel ler o arquivo (%v)", err))
		return nil, &d
	}

	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return assinantesFixos(signer), nil
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
		return assinantesFixos(signer), nil
	}

	if !amb.stdinEhTerminal() {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf(
			"a chave esta protegida por passphrase e a entrada padrao nao e um terminal, "+
				"entao nao ha como perguntar; defina %s no ambiente para usar esta chave",
			EnvPassphraseChaveSSH))
		return nil, &d
	}

	return func() ([]ssh.Signer, error) {
		passphrase, err := amb.lerSegredo(fmt.Sprintf("passphrase da chave %s: ", opts.KeyPath))
		if err != nil {
			return nil, fmt.Errorf("nao foi possivel ler a passphrase de %s: %w", opts.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("a passphrase informada nao abre a chave %s: %w", opts.KeyPath, err)
		}
		return []ssh.Signer{signer}, nil
	}, nil
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

// metodoChavesPadrao monta um metodo com as chaves padrao que existem no
// disco e abrem sem passphrase.
//
// Sem passphrase de proposito: aqui o usuario nao pediu chave nenhuma, entao
// perguntar senha de um arquivo que ele nem citou seria intrusivo, e sob pipe
// — que e como um agente de IA roda isto — nao ha a quem perguntar. Chave
// protegida por passphrase continua acessivel pelo ssh-agent, que e o
// caminho recomendado, ou por --key explicito.
func metodoChavesPadrao(amb ambienteAuth) (fonteAssinantes, *output.Diagnostic) {
	if amb.home == nil {
		return nil, nil
	}
	home, err := amb.home()
	if err != nil {
		return nil, nil
	}

	signers := []ssh.Signer{}
	for _, nome := range ChavesPadrao {
		pem, err := os.ReadFile(filepath.Join(home, ".ssh", nome))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}

	if len(signers) == 0 {
		return nil, nil
	}
	return assinantesFixos(signers...), nil
}

// fonteAssinantes entrega chaves para o handshake. E uma funcao, e nao uma
// lista pronta, porque o ssh-agent pode ganhar chaves depois de o ngx comecar
// e porque uma chave com passphrase so deve pedir a senha se realmente chegar
// a vez dela.
type fonteAssinantes func() ([]ssh.Signer, error)

func assinantesFixos(ss ...ssh.Signer) fonteAssinantes {
	return func() ([]ssh.Signer, error) { return ss, nil }
}
