package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// Glob remoto (DR6) — a parte que mais importa desta camada.
// ---------------------------------------------------------------------------

// entradaFalsa e o minimo de os.FileInfo que o glob consome: so o nome.
type entradaFalsa struct {
	nome string
	dir  bool
}

func (e entradaFalsa) Name() string       { return e.nome }
func (e entradaFalsa) Size() int64        { return 0 }
func (e entradaFalsa) Mode() os.FileMode  { return 0o644 }
func (e entradaFalsa) ModTime() time.Time { return time.Time{} }
func (e entradaFalsa) IsDir() bool        { return e.dir }
func (e entradaFalsa) Sys() any           { return nil }

// remotoFalso e uma arvore em memoria com falha injetavel por caminho. E o
// unico jeito de exercitar o caso que a DR6 existe para cobrir: um erro de
// I/O no meio da listagem, que um servidor de teste saudavel nunca produz.
type remotoFalso struct {
	// dirs mapeia caminho de diretorio para os nomes que ele contem.
	dirs map[string][]string
	// arquivos e o conjunto de caminhos que existem para o Lstat.
	arquivos map[string]bool
	// falhas mapeia caminho para o erro que ReadDir/Lstat devolve ali.
	falhas map[string]error

	chamadas []string
}

func (r *remotoFalso) ReadDir(p string) ([]os.FileInfo, error) {
	r.chamadas = append(r.chamadas, "ReadDir:"+p)
	if err, ok := r.falhas[p]; ok {
		return nil, err
	}
	nomes, ok := r.dirs[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	entradas := make([]os.FileInfo, 0, len(nomes))
	for _, n := range nomes {
		_, ehDir := r.dirs[path.Join(p, n)]
		entradas = append(entradas, entradaFalsa{nome: n, dir: ehDir})
	}
	return entradas, nil
}

func (r *remotoFalso) Lstat(p string) (os.FileInfo, error) {
	r.chamadas = append(r.chamadas, "Lstat:"+p)
	if err, ok := r.falhas[p]; ok {
		return nil, err
	}
	if r.arquivos[p] {
		return entradaFalsa{nome: path.Base(p)}, nil
	}
	if _, ok := r.dirs[p]; ok {
		return entradaFalsa{nome: path.Base(p), dir: true}, nil
	}
	return nil, fs.ErrNotExist
}

func remotoNginx() *remotoFalso {
	return &remotoFalso{
		dirs: map[string][]string{
			// Ordem embaralhada de proposito: o servidor entrega o que quiser
			// e o glob precisa devolver ordenado.
			"/etc/nginx":            {"conf.d", "sites", "nginx.conf", "mime.types"},
			"/etc/nginx/conf.d":     {"zz-ultimo.conf", "gzip.conf", "aa-primeiro.conf", "leiame.txt"},
			"/etc/nginx/sites":      {"a", "b"},
			"/etc/nginx/sites/a":    {"srv.conf"},
			"/etc/nginx/sites/b":    {"srv.conf", "extra.conf"},
			".":                     {"local.conf", "outro.txt"},
			"/etc/nginx/conf.d/sub": {},
		},
		arquivos: map[string]bool{
			"/etc/nginx/nginx.conf":            true,
			"/etc/nginx/conf.d/gzip.conf":      true,
			"/etc/nginx/conf.d/zz-ultimo.conf": true,
		},
		falhas: map[string]error{},
	}
}

func TestGlobRemotoExpandeEOrdena(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/conf.d/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{
		"/etc/nginx/conf.d/aa-primeiro.conf",
		"/etc/nginx/conf.d/gzip.conf",
		"/etc/nginx/conf.d/zz-ultimo.conf",
	}, achados, "a ordem alimenta o hash canonico: precisa ser estavel")
}

func TestGlobRemotoPropagaErroDeIOAoListar(t *testing.T) {
	// Este e o teste que a DR6 existe para permitir. O sftp.Client.Glob
	// devolveria a lista vazia e err nil aqui, e o ngx apresentaria a
	// configuracao do servidor sem os arquivos que ela tem.
	r := remotoNginx()
	quedaDeLink := errors.New("connection lost")
	r.falhas["/etc/nginx/conf.d"] = quedaDeLink

	achados, err := globRemoto(r, "/etc/nginx/conf.d/*.conf")

	require.Error(t, err, "erro de I/O nao pode virar lista vazia")
	assert.ErrorIs(t, err, quedaDeLink)
	assert.Nil(t, achados)
}

func TestGlobRemotoPropagaPermissaoNegada(t *testing.T) {
	// "Nao consegui ler" nao e "nao existe": so a ausencia vira lista vazia.
	r := remotoNginx()
	r.falhas["/etc/nginx/conf.d"] = fs.ErrPermission

	_, err := globRemoto(r, "/etc/nginx/conf.d/*.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrPermission)
}

func TestGlobRemotoDiretorioAusenteNaoEErro(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/nao-existe/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{}, achados)
	assert.NotNil(t, achados, "lista vazia, nunca nil")
}

func TestGlobRemotoSemMetacaractereComArquivoPresente(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/nginx.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{"/etc/nginx/nginx.conf"}, achados)
	assert.Contains(t, r.chamadas, "Lstat:/etc/nginx/nginx.conf")
}

func TestGlobRemotoSemMetacaractereComArquivoAusente(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/sumiu.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{}, achados)
}

func TestGlobRemotoSemMetacaracterePropagaErroDeIO(t *testing.T) {
	// O caminho literal do sftp.Client.Glob e `if err != nil { return nil, nil }`.
	// Aqui ele tem que devolver erro.
	r := remotoNginx()
	quedaDeLink := errors.New("connection lost")
	r.falhas["/etc/nginx/nginx.conf"] = quedaDeLink

	achados, err := globRemoto(r, "/etc/nginx/nginx.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, quedaDeLink)
	assert.Nil(t, achados)
}

func TestGlobRemotoMetacaractereNoDiretorio(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/sites/*/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{
		"/etc/nginx/sites/a/srv.conf",
		"/etc/nginx/sites/b/extra.conf",
		"/etc/nginx/sites/b/srv.conf",
	}, achados)
}

func TestGlobRemotoPropagaErroDuranteExpansaoDoDiretorio(t *testing.T) {
	r := remotoNginx()
	quedaDeLink := errors.New("connection lost")
	r.falhas["/etc/nginx/sites/b"] = quedaDeLink

	_, err := globRemoto(r, "/etc/nginx/sites/*/*.conf")

	require.Error(t, err, "falha num subdiretorio nao pode virar resultado parcial")
	assert.ErrorIs(t, err, quedaDeLink)
}

func TestGlobRemotoPadraoMalformado(t *testing.T) {
	r := remotoNginx()

	_, err := globRemoto(r, "/etc/nginx/[a-.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, path.ErrBadPattern)
	assert.Empty(t, r.chamadas, "padrao invalido nao gasta viagem de rede")
}

func TestGlobRemotoPadraoRelativoListaODiretorioCorrente(t *testing.T) {
	r := remotoNginx()

	achados, err := globRemoto(r, "*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{"local.conf"}, achados)
	assert.Contains(t, r.chamadas, "ReadDir:.")
}

func TestGlobRemotoUsaSeparadorPOSIX(t *testing.T) {
	// A garantia de que o glob nunca passa por filepath: se passasse, num
	// cliente Windows o resultado viria com barra invertida e o servidor
	// Linux nao acharia o arquivo.
	r := remotoNginx()

	achados, err := globRemoto(r, "/etc/nginx/sites/a/*.conf")

	require.NoError(t, err)
	require.Len(t, achados, 1)
	assert.Equal(t, "/etc/nginx/sites/a/srv.conf", achados[0])
	assert.NotContains(t, achados[0], `\`)
}

func TestGlobRemotoNaoRecursaInfinitamente(t *testing.T) {
	r := remotoNginx()

	_, err := globRemoto(r, `\`)

	// O importante e terminar. O desfecho e "nao casa" ou ErrBadPattern,
	// nunca um laco.
	if err != nil {
		assert.ErrorIs(t, err, path.ErrBadPattern)
	}
}

// ---------------------------------------------------------------------------
// Distincao entre codigo de saida e erro de transporte.
// ---------------------------------------------------------------------------

func TestClassificarSaidaSSHComandoOK(t *testing.T) {
	stdout, stderr, codigo, err := classificarSaidaSSH([]byte("ok"), nil, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, codigo)
	assert.Equal(t, []byte("ok"), stdout)
	assert.Nil(t, stderr)
}

func TestClassificarSaidaSSHExitMissingEFalhaDeTransporte(t *testing.T) {
	// A sessao terminou sem o servidor dizer como: a conexao caiu. Devolver
	// codigo zero com err nil faria isso parecer sucesso.
	_, _, codigo, err := classificarSaidaSSH(nil, nil, &ssh.ExitMissingError{})

	require.Error(t, err)
	var faltando *ssh.ExitMissingError
	assert.ErrorAs(t, err, &faltando)
	assert.Equal(t, 0, codigo)
}

func TestClassificarSaidaSSHErroDeIOEFalhaDeTransporte(t *testing.T) {
	_, _, _, err := classificarSaidaSSH(nil, nil, io.ErrUnexpectedEOF)

	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// ---------------------------------------------------------------------------
// Montagem da linha de comando: zero shell.
// ---------------------------------------------------------------------------

func TestEscaparArgumento(t *testing.T) {
	casos := []struct {
		nome, entrada, esperado string
	}{
		{"simples", "nginx", `'nginx'`},
		{"vazio", "", `''`},
		{"espaco", "meu arquivo.conf", `'meu arquivo.conf'`},
		{"cifrao", "$HOME/x", `'$HOME/x'`},
		{"crase", "`id`", "'`id`'"},
		{"ponto e virgula", "a; rm -rf /", `'a; rm -rf /'`},
		{"aspa simples", "o'brien", `'o'\''brien'`},
		{"barra invertida", `c:\x`, `'c:\x'`},
		{"nova linha", "a\nrm -rf /", "'a\nrm -rf /'"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			assert.Equal(t, c.esperado, escaparArgumento(c.entrada))
		})
	}
}

func TestMontarLinhaDeComandoCitaCadaArgumento(t *testing.T) {
	linha := montarLinhaDeComando([]string{"nginx", "-c", "/etc/nginx/nginx.conf"})

	assert.Equal(t, `'nginx' '-c' '/etc/nginx/nginx.conf'`, linha)
}

// ---------------------------------------------------------------------------
// Servidor SSH em memoria: o resto so se prova ponta a ponta.
// ---------------------------------------------------------------------------

// respostaExec e o que o servidor de teste devolve para um comando.
type respostaExec struct {
	stdout string
	stderr string
	codigo uint32
	// semStatus reproduz a conexao caindo: o canal fecha sem exit-status, e
	// o cliente recebe *ssh.ExitMissingError.
	semStatus bool
}

type servidorSSHTeste struct {
	endereco  string
	chavePub  ssh.PublicKey
	ouvinte   net.Listener
	espera    sync.WaitGroup
	mu        sync.Mutex
	recebidos []string
	responder func(comando string) respostaExec
}

const senhaDeTeste = "senha-de-teste"

// subirServidorSSH poe de pe um servidor SSH real em 127.0.0.1:0, com chave de
// host efemera e autenticacao por senha. Sem isso nao ha como provar que um
// codigo de saida diferente de zero chega como resultado e uma queda de
// conexao chega como erro.
func subirServidorSSH(t *testing.T, responder func(string) respostaExec) *servidorSSHTeste {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	assinante, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, senha []byte) (*ssh.Permissions, error) {
			if string(senha) == senhaDeTeste {
				return nil, nil
			}
			return nil, fmt.Errorf("senha invalida")
		},
	}
	cfg.AddHostKey(assinante)

	ouvinte, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &servidorSSHTeste{
		endereco:  ouvinte.Addr().String(),
		chavePub:  assinante.PublicKey(),
		ouvinte:   ouvinte,
		responder: responder,
	}

	s.espera.Add(1)
	go func() {
		defer s.espera.Done()
		for {
			conn, err := ouvinte.Accept()
			if err != nil {
				return
			}
			s.espera.Add(1)
			go func() {
				defer s.espera.Done()
				s.atender(conn, cfg)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = ouvinte.Close()
		s.espera.Wait()
	})
	return s
}

func (s *servidorSSHTeste) atender(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	sc, canaisNovos, pedidosGlobais, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(pedidosGlobais)

	for novo := range canaisNovos {
		if novo.ChannelType() != "session" {
			_ = novo.Reject(ssh.UnknownChannelType, "so session")
			continue
		}
		canal, pedidos, err := novo.Accept()
		if err != nil {
			return
		}
		s.espera.Add(1)
		go func() {
			defer s.espera.Done()
			s.atenderSessao(canal, pedidos)
		}()
	}
}

func (s *servidorSSHTeste) atenderSessao(canal ssh.Channel, pedidos <-chan *ssh.Request) {
	fecharCanal := true
	defer func() {
		if fecharCanal {
			_ = canal.Close()
		}
	}()

	for req := range pedidos {
		switch req.Type {
		case "exec":
			var carga struct{ Comando string }
			if err := ssh.Unmarshal(req.Payload, &carga); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)

			s.mu.Lock()
			s.recebidos = append(s.recebidos, carga.Comando)
			s.mu.Unlock()

			resp := s.responder(carga.Comando)
			_, _ = io.WriteString(canal, resp.stdout)
			_, _ = io.WriteString(canal.Stderr(), resp.stderr)
			if !resp.semStatus {
				_, _ = canal.SendRequest("exit-status", false,
					ssh.Marshal(struct{ Status uint32 }{resp.codigo}))
			}
			return

		case "subsystem":
			var carga struct{ Nome string }
			if err := ssh.Unmarshal(req.Payload, &carga); err != nil || carga.Nome != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			srv, err := sftp.NewServer(canal)
			if err != nil {
				return
			}
			fecharCanal = false
			s.espera.Add(1)
			go func() {
				defer s.espera.Done()
				defer func() { _ = srv.Close() }()
				_ = srv.Serve()
			}()

		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *servidorSSHTeste) comandosRecebidos() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.recebidos...)
}

// opcoesPara monta as SSHOptions que alcancam o servidor de teste, gravando a
// chave de host num known_hosts temporario. Verificacao estrita, como em
// producao: o teste tambem prova que a politica da DR1 nao atrapalha o caminho
// legitimo.
func opcoesPara(t *testing.T, s *servidorSSHTeste) SSHOptions {
	t.Helper()

	host, portaTexto, err := net.SplitHostPort(s.endereco)
	require.NoError(t, err)
	porta := 0
	_, err = fmt.Sscanf(portaTexto, "%d", &porta)
	require.NoError(t, err)

	linha := knownhosts.Line([]string{knownhosts.Normalize(s.endereco)}, s.chavePub)
	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, []byte(linha+"\n"), 0o600))

	return SSHOptions{
		Host:           host,
		Port:           porta,
		User:           "operador",
		Password:       senhaDeTeste,
		KnownHostsPath: caminho,
		Timeout:        10 * time.Second,
	}
}

func TestSSHRunCodigoDiferenteDeZeroNaoEErro(t *testing.T) {
	// A invariante central do Transport: `nginx -t` reprovando a
	// configuracao e resultado, nao falha de infraestrutura.
	s := subirServidorSSH(t, func(string) respostaExec {
		return respostaExec{
			stderr: "nginx: configuration file test failed\n",
			codigo: 1,
		}
	})

	tr := conectar(t, s)

	stdout, stderr, codigo, err := tr.Run(context.Background(), []string{"nginx", "-t"})

	require.NoError(t, err, "codigo de saida nao-zero e resultado, nunca erro")
	assert.Equal(t, 1, codigo)
	assert.Empty(t, stdout)
	assert.Contains(t, string(stderr), "test failed")
}

func TestSSHRunSucesso(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec {
		return respostaExec{stdout: "nginx version: 1.20.1\n"}
	})
	tr := conectar(t, s)

	stdout, _, codigo, err := tr.Run(context.Background(), []string{"nginx", "-v"})

	require.NoError(t, err)
	assert.Equal(t, 0, codigo)
	assert.Contains(t, string(stdout), "1.20.1")
}

func TestSSHRunQuedaDeConexaoEErroDeTransporte(t *testing.T) {
	// O canal fecha sem exit-status. Sem a distincao, isso viraria "codigo 0,
	// err nil" — um comando interrompido se passando por sucesso.
	s := subirServidorSSH(t, func(string) respostaExec {
		return respostaExec{stdout: "parcial", semStatus: true}
	})
	tr := conectar(t, s)

	_, _, codigo, err := tr.Run(context.Background(), []string{"nginx", "-T"})

	require.Error(t, err, "sessao sem status de saida e falha de transporte")
	var faltando *ssh.ExitMissingError
	assert.ErrorAs(t, err, &faltando)
	assert.Equal(t, 0, codigo)
}

func TestSSHRunEscapaCadaArgumento(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	_, _, _, err := tr.Run(context.Background(),
		[]string{"nginx", "-c", "/etc/nginx/a b; rm -rf /"})

	require.NoError(t, err)
	recebidos := s.comandosRecebidos()
	require.Len(t, recebidos, 1)
	assert.Equal(t, `'nginx' '-c' '/etc/nginx/a b; rm -rf /'`, recebidos[0])
	assert.NotContains(t, recebidos[0], "nginx -c /etc")
}

func TestSSHRunArgvVazio(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	_, _, _, err := tr.Run(context.Background(), nil)

	require.Error(t, err)
}

func TestSSHRunContextoCancelado(t *testing.T) {
	liberar := make(chan struct{})
	s := subirServidorSSH(t, func(string) respostaExec {
		<-liberar
		return respostaExec{}
	})
	t.Cleanup(func() { close(liberar) })

	tr := conectar(t, s)

	ctx, cancelar := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancelar()
	}()

	_, _, codigo, err := tr.Run(ctx, []string{"nginx", "-T"})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, codigo)
}

func TestSSHRunContextoJaCancelado(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()

	_, _, _, err := tr.Run(ctx, []string{"nginx", "-t"})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, s.comandosRecebidos(), "contexto morto nao gasta sessao")
}

func TestSSHDescribe(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	opts := opcoesPara(t, s)

	tr, _, err := SSHComDiagnosticos(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	assert.Equal(t, fmt.Sprintf("ssh://operador@%s:%d", opts.Host, opts.Port), tr.Describe())
	assert.True(t, strings.HasPrefix(tr.Describe(), "ssh://"))
}

func TestSSHCloseDuasVezesESeguro(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	require.NoError(t, tr.Close())
	assert.NotPanics(t, func() { _ = tr.Close() })
}

func TestSSHHostKeyDesconhecidoRecusa(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	opts := opcoesPara(t, s)
	// known_hosts vazio: o host passa a ser desconhecido.
	require.NoError(t, os.WriteFile(opts.KnownHostsPath, []byte("\n"), 0o600))

	tr, _, err := SSHComDiagnosticos(opts)

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.Contains(t, err.Error(), "host desconhecido")
}

func TestSSHConexaoRecusadaTemDiagnosticoAcionavel(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	opts := opcoesPara(t, s)
	opts.Password = "errada"

	tr, diags, err := SSHComDiagnosticos(opts)

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.NotNil(t, diags)
	assert.Contains(t, err.Error(), "nao foi possivel conectar")
	assert.Contains(t, err.Error(), "Metodos de autenticacao oferecidos")
}

func TestSSHSemHostEErroDeUso(t *testing.T) {
	tr, diags, err := SSHComDiagnosticos(SSHOptions{Host: "  "})

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.NotNil(t, diags, "a lista de diagnosticos nunca e nil")
}

// ---------------------------------------------------------------------------
// SFTP ponta a ponta: Open e Glob contra um servidor de verdade.
// ---------------------------------------------------------------------------

func TestSSHOpenEGlobPontaAPonta(t *testing.T) {
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	raiz := t.TempDir()
	confd := filepath.Join(raiz, "conf.d")
	require.NoError(t, os.MkdirAll(confd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(raiz, "nginx.conf"),
		[]byte("include conf.d/*.conf;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "gzip.conf"), []byte("gzip on;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "ssl.conf"), []byte("ssl on;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "leiame.txt"), []byte("nao\n"), 0o644))

	// Caminho remoto e POSIX: filepath.ToSlash cobre o cliente em Windows.
	raizPOSIX := filepath.ToSlash(raiz)

	rc, err := tr.Open(raizPOSIX + "/nginx.conf")
	require.NoError(t, err)
	conteudo, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, "include conf.d/*.conf;\n", string(conteudo))

	achados, err := tr.Glob(raizPOSIX + "/conf.d/*.conf")
	require.NoError(t, err)
	assert.Equal(t, []string{
		raizPOSIX + "/conf.d/gzip.conf",
		raizPOSIX + "/conf.d/ssl.conf",
	}, achados)

	vazio, err := tr.Glob(raizPOSIX + "/nao-existe/*.conf")
	require.NoError(t, err)
	assert.Equal(t, []string{}, vazio)

	_, err = tr.Open(raizPOSIX + "/sumiu.conf")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestSSHGlobPropagaErroDepoisDaConexaoCair(t *testing.T) {
	// O contrario do sftp.Client.Glob: com a conexao morta ele devolveria
	// (nil, nil) no caminho sem metacaractere e uma lista vazia no outro.
	s := subirServidorSSH(t, func(string) respostaExec { return respostaExec{} })
	tr := conectar(t, s)

	raiz := filepath.ToSlash(t.TempDir())
	require.NoError(t, tr.Close())

	_, err := tr.Glob(raiz + "/*.conf")
	require.Error(t, err, "conexao morta nao pode virar lista vazia")

	_, err = tr.Glob(raiz + "/nginx.conf")
	require.Error(t, err, "conexao morta nao pode virar lista vazia")
}

func conectar(t *testing.T, s *servidorSSHTeste) Transport {
	t.Helper()
	tr, diags, err := SSHComDiagnosticos(opcoesPara(t, s))
	require.NoError(t, err)
	require.NotNil(t, diags)
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
