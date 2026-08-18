package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/s0beran0/ngx/internal/output"
)

const (
	// CodigoConexaoSSH marca a falha de estabelecer a sessao SSH: DNS, rede,
	// timeout, ou o servidor recusando todos os metodos de autenticacao.
	CodigoConexaoSSH = "NGX-0206"

	// CodigoSessaoSFTP marca o caso em que a conexao SSH sobe mas o
	// subsistema SFTP nao. Sao problemas diferentes com solucoes diferentes:
	// o primeiro e rede ou credencial, o segundo e configuracao do sshd.
	CodigoSessaoSFTP = "NGX-0207"
)

// TimeoutSSHPadrao limita o handshake quando SSHOptions.Timeout nao diz nada.
// Sem timeout, um host que aceita a conexao TCP e nunca responde ao handshake
// deixa o ngx pendurado para sempre — e quem espera a saida nao tem como saber
// se o comando esta lento ou morto.
const TimeoutSSHPadrao = 30 * time.Second

// metacaracteresGlob sao os caracteres que fazem um padrao ser padrao. A barra
// invertida entra porque path.Match a trata como escape: um padrao que a
// contenha precisa passar pela expansao, nao pelo atalho do Lstat.
const metacaracteresGlob = `*?[\`

// leitorRemoto e o subconjunto de *sftp.Client que a expansao de padrao usa.
//
// Existe como interface por uma razao so: o glob proprio da DR6 e a parte
// desta camada que precisa de teste de verdade, e um teste que precisa de um
// servidor SFTP de pe nao exercita o caso que importa — o erro de I/O no meio
// da listagem. Com a interface, esse erro e injetado direto.
type leitorRemoto interface {
	ReadDir(p string) ([]os.FileInfo, error)
	Lstat(p string) (os.FileInfo, error)
}

// sshTransport opera um host remoto: arquivos por SFTP, comandos por sessao
// exec. Nada e instalado no destino (DR3) — o ngx le o que ja esta la e roda
// o binario que ja existe.
type sshTransport struct {
	cliente *ssh.Client
	arquivo *sftp.Client

	// destino e "user@host:porta", a forma que Describe publica no envelope.
	destino string

	umaVez     sync.Once
	erroFechar error
}

// SSH conecta no host descrito por opts e devolve o transporte remoto.
//
// Descarta os diagnosticos da conexao. Use SSHComDiagnosticos em qualquer
// caminho que monte envelope: o aviso de --insecure-host-key e o de
// ssh-agent ausente explicam para quem le a saida contra o que o ngx operou,
// e perde-los faz o escape da DR1 virar silencioso.
func SSH(opts SSHOptions) (Transport, error) {
	tr, _, err := SSHComDiagnosticos(opts)
	return tr, err
}

// SSHComDiagnosticos conecta e devolve tambem o que a montagem observou pelo
// caminho: host key aceita sem verificacao, ssh-agent indisponivel, chave
// ilegivel. Nenhum desses derruba a conexao, e nenhum pode sumir.
//
// A lista de diagnosticos nunca e nil.
func SSHComDiagnosticos(opts SSHOptions) (Transport, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	host := strings.TrimSpace(opts.Host)
	if host == "" {
		return nil, diags, output.Usage("host de destino nao informado")
	}

	porta := opts.Port
	if porta == 0 {
		porta = PortaSSHPadrao
	}
	usuario := opts.User
	if usuario == "" {
		usuario = usuarioCorrente()
	}

	verificar, diagsHost, err := VerificadorHostKey(opts)
	if len(diagsHost) > 0 {
		diags = append(diags, diagsHost...)
	}
	if err != nil {
		return nil, diags, err
	}

	auth, diagsAuth, err := MontarAutenticacao(opts)
	if len(diagsAuth) > 0 {
		diags = append(diags, diagsAuth...)
	}
	if err != nil {
		return nil, diags, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = TimeoutSSHPadrao
	}

	endereco := net.JoinHostPort(host, strconv.Itoa(porta))
	cliente, err := ssh.Dial("tcp", endereco, &ssh.ClientConfig{
		User:            usuario,
		Auth:            auth.Metodos,
		HostKeyCallback: verificar,
		Timeout:         timeout,
	})

	// A conexao com o ssh-agent so serve durante o handshake; depois dele e
	// um socket aberto sem uso. O erro de fechar nao vira diagnostico porque
	// nao muda nada para quem chamou: o handshake ja aconteceu ou ja falhou.
	_ = auth.Close()

	if err != nil {
		// O erro de host key ja vem tipado do callback (DR1). Reembrulha-lo
		// em CodigoConexaoSSH apagaria a distincao entre primeiro acesso e
		// chave alterada — que e exatamente o que quem consome a saida tem
		// de separar sem interpretar texto —, e transformaria uma recusa de
		// verificacao numa falha generica de rede ou credencial.
		var tipado *output.Error
		if errors.As(err, &tipado) {
			return nil, diags, tipado
		}
		return nil, diags, erroConexaoSSH(usuario, endereco, auth.Nomes, err)
	}

	arquivo, err := sftp.NewClient(cliente)
	if err != nil {
		_ = cliente.Close()
		return nil, diags, erroSessaoSFTP(usuario, endereco, err)
	}

	return &sshTransport{
		cliente: cliente,
		arquivo: arquivo,
		destino: fmt.Sprintf("%s@%s", usuario, endereco),
	}, diags, nil
}

func (t *sshTransport) Open(caminho string) (io.ReadCloser, error) {
	// Sem `return t.arquivo.Open(...)` direto: no caminho de erro isso
	// devolveria uma interface nao-nula guardando um *sftp.File nulo, e quem
	// checa `rc != nil` antes de `err` levaria panico.
	f, err := t.arquivo.Open(caminho)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (t *sshTransport) Glob(padrao string) ([]string, error) {
	return globRemoto(t.arquivo, padrao)
}

func (t *sshTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: argv vazio")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}

	sessao, err := t.cliente.NewSession()
	if err != nil {
		// Abrir canal falha quando a conexao ja caiu: transporte, nao
		// veredito do comando.
		return nil, nil, 0, err
	}
	defer func() { _ = sessao.Close() }()

	var stdout, stderr bytes.Buffer
	sessao.Stdout = &stdout
	sessao.Stderr = &stderr

	comando := montarLinhaDeComando(argv)

	fim := make(chan error, 1)
	go func() { fim <- sessao.Run(comando) }()

	select {
	case err := <-fim:
		return classificarSaidaSSH(stdout.Bytes(), stderr.Bytes(), err)
	case <-ctx.Done():
		// Melhor esforco: pede ao servidor para matar o processo e derruba o
		// canal, que e o que solta o Run.
		_ = sessao.Signal(ssh.SIGKILL)
		_ = sessao.Close()
		// Espera a goroutine antes de tocar nos buffers. Le-los enquanto a
		// copia da sessao ainda escreve seria disputa de dados.
		<-fim
		return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
}

func (t *sshTransport) Close() error {
	t.umaVez.Do(func() {
		var erros []error
		if t.arquivo != nil {
			if err := t.arquivo.Close(); err != nil {
				erros = append(erros, err)
			}
		}
		if t.cliente != nil {
			if err := t.cliente.Close(); err != nil {
				erros = append(erros, err)
			}
		}
		t.erroFechar = errors.Join(erros...)
	})
	return t.erroFechar
}

func (t *sshTransport) Describe() string {
	return "ssh://" + t.destino
}

// classificarSaidaSSH aplica a regra central do Transport ao desfecho de uma
// sessao remota.
//
// *ssh.ExitError e o servidor reportando o codigo de saida do comando: ele
// rodou ate o fim e reprovou, o que e resultado e nao erro. Um `nginx -t` que
// reprova a configuracao vem por aqui.
//
// *ssh.ExitMissingError e o oposto: a sessao terminou sem que o servidor
// dissesse como. E o que acontece quando a conexao cai no meio, e nesse caso
// nao existe codigo de saida — devolver zero com err nil faria um comando
// interrompido parecer sucesso. Erro de I/O tem a mesma leitura.
func classificarSaidaSSH(stdout, stderr []byte, err error) ([]byte, []byte, int, error) {
	if err == nil {
		return stdout, stderr, 0, nil
	}

	var saida *ssh.ExitError
	if errors.As(err, &saida) {
		return stdout, stderr, saida.ExitStatus(), nil
	}

	return stdout, stderr, 0, err
}

// montarLinhaDeComando transforma argv na string que o canal exec do SSH
// aceita.
//
// O protocolo SSH nao tem forma de mandar argv: o pedido "exec" carrega uma
// string, e o servidor a entrega ao shell de login do usuario. Como a string e
// inevitavel, o que impede injecao e o escape por argumento — cada elemento de
// argv vira um token que o shell nao pode reinterpretar. Concatenar argv com
// espacos, sem aspas, seria o mesmo que rodar shell com entrada do usuario.
func montarLinhaDeComando(argv []string) string {
	partes := make([]string, 0, len(argv))
	for _, arg := range argv {
		partes = append(partes, escaparArgumento(arg))
	}
	return strings.Join(partes, " ")
}

// escaparArgumento envolve o argumento em aspas simples, a unica citacao do
// shell POSIX que nao interpreta absolutamente nada por dentro — nem $, nem
// crase, nem barra invertida.
//
// A propria aspa simples e o unico caractere que nao pode aparecer dentro
// dela: ela e fechada, escapada fora das aspas com barra invertida, e a
// citacao e reaberta. Argumento vazio vira um par de aspas vazio e continua
// sendo um argumento, em vez de desaparecer da linha.
func escaparArgumento(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// globRemoto expande um padrao de caminho no host remoto sobre ReadDir e
// path.Match, propagando erro de I/O como erro (DR6).
//
// O sftp.Client tem um Glob, e ele nao serve: o proprio comentario do fonte
// diz que ignora erro de sistema de arquivos e que o unico erro possivel e
// ErrBadPattern. No caminho sem metacaractere ele e literal — se o Lstat
// falhar, devolve (nil, nil). Num link instavel, `include conf.d/*.conf`
// devolveria zero arquivos sem nenhum sinal, e o ngx apresentaria a
// configuracao do servidor sem os arquivos que ela tem. Uma ferramenta lida
// por agente de IA nao pode ser confiantemente incompleta: o consumidor nao
// tem como desconfiar.
//
// A unica falha que nao vira erro e a ausencia: um diretorio que nao existe
// significa que o padrao nao casa com nada, e isso e resposta legitima. Falta
// de permissao vira erro, porque "nao consegui ler" nao e "nao existe".
//
// Caminho remoto e sempre POSIX: usa path, nunca filepath. Com filepath, o
// ngx rodando em Windows expandiria conf.d\*.conf contra um servidor Linux.
//
// A estrutura acompanha a do filepath.Glob da stdlib de proposito — mesma
// ordem de resolucao, mesma protecao contra recursao infinita, mesma
// semantica de casamento. O que muda e o tratamento do erro.
//
// Sem correspondencia devolve lista vazia e err nil, nunca nil.
func globRemoto(remoto leitorRemoto, padrao string) ([]string, error) {
	// Valida a sintaxe antes de tocar na rede: padrao malformado e erro de
	// uso, nao motivo para uma viagem ate o servidor.
	if _, err := path.Match(padrao, ""); err != nil {
		return nil, err
	}

	if !temMetacaractereGlob(padrao) {
		if _, err := remoto.Lstat(padrao); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []string{}, nil
			}
			return nil, err
		}
		return []string{padrao}, nil
	}

	dir, arquivo := path.Split(padrao)
	dir = limparDirGlob(dir)

	if !temMetacaractereGlob(dir) {
		return globNoDiretorio(remoto, dir, arquivo, []string{})
	}

	// Protege contra recursao infinita: um padrao que se reduz a si mesmo
	// (o que a barra invertida solta consegue produzir) nunca convergiria.
	if dir == padrao {
		return nil, path.ErrBadPattern
	}

	diretorios, err := globRemoto(remoto, dir)
	if err != nil {
		return nil, err
	}

	achados := []string{}
	for _, d := range diretorios {
		achados, err = globNoDiretorio(remoto, d, arquivo, achados)
		if err != nil {
			return nil, err
		}
	}
	return achados, nil
}

// globNoDiretorio acrescenta a achados as entradas de dir que casam o padrao.
//
// A ordenacao e deliberada: o ReadDir do SFTP entrega o que o servidor mandar,
// na ordem que ele quiser, e a lista de arquivos de include alimenta o hash
// canonico da configuracao. Ordem instavel viraria hash instavel.
func globNoDiretorio(remoto leitorRemoto, dir, padrao string, achados []string) ([]string, error) {
	entradas, err := remoto.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Diretorio inexistente e ausencia de correspondencia, nao
			// falha: o padrao simplesmente nao casa com nada.
			return achados, nil
		}
		return nil, err
	}

	nomes := make([]string, 0, len(entradas))
	for _, e := range entradas {
		nomes = append(nomes, e.Name())
	}
	sort.Strings(nomes)

	for _, nome := range nomes {
		casa, err := path.Match(padrao, nome)
		if err != nil {
			return nil, err
		}
		if casa {
			achados = append(achados, path.Join(dir, nome))
		}
	}
	return achados, nil
}

func temMetacaractereGlob(p string) bool {
	return strings.ContainsAny(p, metacaracteresGlob)
}

// limparDirGlob normaliza a metade de diretorio devolvida por path.Split, que
// sempre vem com a barra final. Padrao relativo tem diretorio vazio e vira ".",
// que e o que o ReadDir espera.
func limparDirGlob(dir string) string {
	switch dir {
	case "":
		return "."
	case "/":
		return "/"
	default:
		return strings.TrimSuffix(dir, "/")
	}
}

// erroConexaoSSH descreve a falha de handshake nomeando os metodos de
// autenticacao oferecidos.
//
// A lista de metodos e o que separa "a rede nao chega la" de "a rede chega e
// o servidor nao aceitou nada do que eu tinha". Sem ela, uma recusa por falta
// de chave no ssh-agent fica indistinguivel de host fora do ar, e quem le a
// saida nao tem como escolher o que corrigir.
func erroConexaoSSH(usuario, endereco string, metodos []string, causa error) error {
	oferecidos := "nenhum"
	if len(metodos) > 0 {
		oferecidos = strings.Join(metodos, ", ")
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoConexaoSSH,
			Message: fmt.Sprintf(
				"nao foi possivel conectar em %s@%s: %v. Metodos de autenticacao "+
					"oferecidos: %s",
				usuario, endereco, causa, oferecidos),
		},
		Err: causa,
	}
}

// erroSessaoSFTP descreve o caso em que o SSH sobe e o SFTP nao. A distincao
// importa porque a correcao mora no sshd do servidor, nao na credencial de
// quem chamou.
func erroSessaoSFTP(usuario, endereco string, causa error) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSessaoSFTP,
			Message: fmt.Sprintf(
				"a conexao com %s@%s foi estabelecida, mas o subsistema SFTP nao "+
					"respondeu: %v. O ngx le a configuracao por SFTP; verifique se o "+
					"sshd do servidor tem o subsistema habilitado",
				usuario, endereco, causa),
		},
		Err: causa,
	}
}
