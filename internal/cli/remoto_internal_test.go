package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// transporteFalso registra o que o CLI pediu ao transporte sem tocar em
// socket nenhum.
type transporteFalso struct {
	descricao string
	fechado   int
	argv      [][]string
	stdout    string
}

func (t *transporteFalso) Open(string) (io.ReadCloser, error) { return nil, errors.New("sem uso") }
func (t *transporteFalso) Glob(string) ([]string, error)      { return []string{}, nil }

func (t *transporteFalso) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.argv = append(t.argv, argv)
	return []byte(t.stdout), nil, 0, nil
}

func (t *transporteFalso) Close() error { t.fechado++; return nil }

func (t *transporteFalso) Describe() string {
	if t.descricao == "" {
		return "ssh://deploy@10.0.0.9:22"
	}
	return t.descricao
}

// conectorFalso substitui transport.SSHComDiagnosticos e guarda as opcoes
// que a resolucao produziu, que e o que os testes de precedencia observam.
type conectorFalso struct {
	chamadas int
	opts     transport.SSHOptions
	tr       *transporteFalso
	diags    []output.Diagnostic
	err      error
}

func (c *conectorFalso) conectar(opts transport.SSHOptions) (transport.Transport, []output.Diagnostic, error) {
	c.chamadas++
	c.opts = opts
	if c.err != nil {
		return nil, c.diags, c.err
	}
	if c.tr == nil {
		c.tr = &transporteFalso{}
	}
	return c.tr, c.diags, nil
}

// contextoDeTeste monta um Context isolado do filesystem real e do HOME de
// quem roda a suite.
func contextoDeTeste(t *testing.T, con *conectorFalso) (*Context, *bytes.Buffer) {
	t.Helper()
	global, local := caminhosIsolados(t)
	var out bytes.Buffer
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: &out, IsTTY: false},
		GlobalSettingsPath: global,
		LocalSettingsPath:  local,
	}
	if con != nil {
		ctx.ConectarSSH = con.conectar
	}
	return ctx, &out
}

func envelopeDe(t *testing.T, out *bytes.Buffer) output.Envelope {
	t.Helper()
	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	return env
}

// sshConfigDeTeste escreve um ~/.ssh/config de fixture e devolve o caminho.
func sshConfigDeTeste(t *testing.T, conteudo string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), 0o600))
	return caminho
}

// Sem --host o comportamento e exatamente o de hoje: transporte local,
// nenhuma conexao construida, e nem o ~/.ssh/config chega a ser lido. Toda a
// v0.1 e uso local; uma regressao aqui quebra o que ja funciona.
//
// O SSHConfigPath aponta para um arquivo deliberadamente invalido: se a
// resolucao remota rodasse, ele produziria um aviso da DR7 no envelope. A
// ausencia de qualquer diagnostico e a prova de que ninguem o leu.
func TestSemHostOTransporteEhLocalENadaDeSSHEhConstruido(t *testing.T) {
	con := &conectorFalso{}
	ctx, out := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "Match user deploy\n  Port nao-e-numero\n")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, []string{"version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Zero(t, con.chamadas, "nenhuma conexao SSH pode ser construida sem --host")

	env := envelopeDe(t, out)
	require.True(t, env.OK)
	require.Equal(t, "local", env.Meta.Target)
	require.Empty(t, env.Diagnostics)
}

// A prova de nao-regressao no proprio ponto de entrada de producao: sem
// --host, Execute monta o transporte local e o inspect le a fixture do disco
// pelo mesmo os.Open/filepath.Glob de sempre.
func TestSemHostInspectContinuaLendoODiscoLocal(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := Execute(
		[]string{"-c", filepath.Join("testdata", "exemplo.conf"), "inspect"},
		&out, &errBuf, false,
	)

	require.Equal(t, output.ExitOK, code)

	env := envelopeDe(t, &out)
	require.True(t, env.OK)
	require.Equal(t, "local", env.Meta.Target)
	require.Empty(t, env.Diagnostics)
	require.NotEmpty(t, env.Meta.ConfigHash)
}

// Precedencia da DR2: a flag explicita vence o ~/.ssh/config, que vence o
// default. Quem faz isso e transport.ResolverSSHConfig; o teste prova que o
// CLI o alimenta com as flags certas e nao reimplementa a ordem por fora.
func TestPrecedenciaDeFlagsSobreSSHConfig(t *testing.T) {
	const arquivo = "Host web1\n" +
		"  HostName 10.0.0.9\n" +
		"  User deploy\n" +
		"  Port 2222\n" +
		"  IdentityFile /keys/id_web1\n"

	t.Run("flag vence o arquivo", func(t *testing.T) {
		con := &conectorFalso{}
		ctx, _ := contextoDeTeste(t, con)
		ctx.SSHConfigPath = sshConfigDeTeste(t, arquivo)

		var errBuf bytes.Buffer
		code := executar(NewRoot(ctx), ctx,
			[]string{"--host", "web1", "--port", "2200", "--user", "root", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, 1, con.chamadas)
		require.Equal(t, "10.0.0.9", con.opts.Host)
		require.Equal(t, 2200, con.opts.Port)
		require.Equal(t, "root", con.opts.User)
		require.Equal(t, "/keys/id_web1", con.opts.KeyPath)
	})

	t.Run("arquivo vence o default", func(t *testing.T) {
		con := &conectorFalso{}
		ctx, _ := contextoDeTeste(t, con)
		ctx.SSHConfigPath = sshConfigDeTeste(t, arquivo)

		var errBuf bytes.Buffer
		code := executar(NewRoot(ctx), ctx, []string{"--host", "web1", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, 2222, con.opts.Port)
		require.Equal(t, "deploy", con.opts.User)
	})

	t.Run("default quando ninguem diz", func(t *testing.T) {
		con := &conectorFalso{}
		ctx, _ := contextoDeTeste(t, con)
		ctx.SSHConfigPath = sshConfigDeTeste(t, "")

		var errBuf bytes.Buffer
		code := executar(NewRoot(ctx), ctx, []string{"--host", "10.0.0.9", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, transport.PortaSSHPadrao, con.opts.Port)
		require.NotEmpty(t, con.opts.User)
	})
}

// O --timeout global vale para a conexao: sem isso, um host inalcancavel
// ficaria pendurado no timeout interno do transporte, e nao no que o operador
// pediu.
func TestTimeoutGlobalChegaNasOpcoesDeConexao(t *testing.T) {
	con := &conectorFalso{}
	ctx, _ := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, []string{"--host", "h", "--timeout", "5s", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, "5s", con.opts.Timeout.String())
}

// Nenhum segredo atravessa a linha de comando: --password nao existe, e o
// cobra recusa a flag desconhecida com exit de uso.
func TestFlagDeSenhaNaoExiste(t *testing.T) {
	con := &conectorFalso{}
	ctx, out := contextoDeTeste(t, con)

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, []string{"--host", "h", "--password", "s3cr3t", "version"}, &errBuf)

	require.Equal(t, output.ExitUsage, code)
	require.Zero(t, con.chamadas)

	env := envelopeDe(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// A senha tambem nunca e montada pelo CLI: o campo Password sai vazio das
// opcoes, para que transport.MontarAutenticacao a busque em
// NGX_SSH_PASSWORD ou no prompt sem eco.
func TestOCLINuncaPreencheASenhaNasOpcoes(t *testing.T) {
	con := &conectorFalso{}
	ctx, _ := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Empty(t, con.opts.Password)
}

// O escape da DR1 nunca e silencioso: --insecure-host-key chega ao transporte
// e o aviso que ele devolve aparece no envelope, sem derrubar o ok.
func TestAvisoDeHostKeyInseguraChegaNoEnvelope(t *testing.T) {
	con := &conectorFalso{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodigoAvisoHostKeyInsegura,
			Message:  "host key aceita sem verificacao",
		}},
		tr: &transporteFalso{descricao: "ssh://deploy@10.0.0.9:22"},
	}
	ctx, out := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx,
		[]string{"--host", "10.0.0.9", "--insecure-host-key", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.True(t, con.opts.InsecureHostKey)

	env := envelopeDe(t, out)
	require.True(t, env.OK, "um aviso nao derruba o comando")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)
	require.Equal(t, transport.CodigoAvisoHostKeyInsegura, env.Diagnostics[0].Code)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
}

// Um diagnostico da conexao tambem tem que sobreviver ao caminho de erro:
// quem le a falha precisa saber que a host key nao foi verificada.
func TestDiagnosticoDaConexaoSobreviveAoErroDoComando(t *testing.T) {
	con := &conectorFalso{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodigoAvisoHostKeyInsegura,
			Message:  "host key aceita sem verificacao",
		}},
	}
	ctx, out := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	root := NewRoot(ctx)
	root.AddCommand(&cobra.Command{
		Use:  "falha",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return output.InvalidConfig("configuracao invalida")
		},
	})

	var errBuf bytes.Buffer
	code := executar(root, ctx, []string{"--host", "h", "--insecure-host-key", "falha"}, &errBuf)

	require.Equal(t, output.ExitInvalidConfig, code)

	env := envelopeDe(t, out)
	require.False(t, env.OK)
	codigos := make([]string, 0, len(env.Diagnostics))
	for _, d := range env.Diagnostics {
		codigos = append(codigos, d.Code)
	}
	require.Contains(t, codigos, transport.CodigoAvisoHostKeyInsegura)
	require.Contains(t, codigos, "NGX-0003")
}

// O erro de conexao mantem o codigo do transporte no envelope, e o meta nao
// inventa um alvo: sem transporte construido, o campo e omitido.
func TestFalhaDeConexaoPreservaODiagnosticoDoTransporte(t *testing.T) {
	con := &conectorFalso{err: &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     transport.CodigoHostDesconhecido,
			Message:  "host desconhecido",
		},
	}}
	ctx, out := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf)

	require.Equal(t, output.ExitInternal, code)

	env := envelopeDe(t, out)
	require.False(t, env.OK)
	require.Equal(t, transport.CodigoHostDesconhecido, env.Diagnostics[0].Code)
	require.Empty(t, env.Meta.Target, "alvo nao confirmado nao pode ser estimado")
}

// Close roda sempre — no sucesso e no erro do comando.
func TestTransporteFechaNosDoisCaminhos(t *testing.T) {
	t.Run("sucesso", func(t *testing.T) {
		tr := &transporteFalso{}
		con := &conectorFalso{tr: tr}
		ctx, _ := contextoDeTeste(t, con)
		ctx.SSHConfigPath = sshConfigDeTeste(t, "")

		var errBuf bytes.Buffer
		require.Equal(t, output.ExitOK,
			executar(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf))
		require.Equal(t, 1, tr.fechado)
	})

	t.Run("erro do comando", func(t *testing.T) {
		tr := &transporteFalso{}
		con := &conectorFalso{tr: tr}
		ctx, _ := contextoDeTeste(t, con)
		ctx.SSHConfigPath = sshConfigDeTeste(t, "")

		root := NewRoot(ctx)
		root.AddCommand(&cobra.Command{
			Use:  "falha",
			Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				return output.InvalidConfig("configuracao invalida")
			},
		})

		var errBuf bytes.Buffer
		require.Equal(t, output.ExitInvalidConfig,
			executar(root, ctx, []string{"--host", "h", "falha"}, &errBuf))
		require.Equal(t, 1, tr.fechado)
	})
}

// Flag de conexao sem --host e erro de uso, nao um valor ignorado em
// silencio.
func TestFlagDeConexaoSemHostEhErroDeUso(t *testing.T) {
	for _, args := range [][]string{
		{"--user", "deploy", "version"},
		{"--port", "2222", "version"},
		{"--key", "/keys/id", "version"},
		{"--insecure-host-key", "version"},
		{"--known-hosts", "/tmp/kh", "version"},
	} {
		t.Run(args[0], func(t *testing.T) {
			con := &conectorFalso{}
			ctx, out := contextoDeTeste(t, con)

			var errBuf bytes.Buffer
			code := executar(NewRoot(ctx), ctx, args, &errBuf)

			require.Equal(t, output.ExitUsage, code)
			require.Zero(t, con.chamadas)

			env := envelopeDe(t, out)
			require.False(t, env.OK)
			require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
			require.Contains(t, env.Diagnostics[0].Message, "--host")
		})
	}
}

// --sudo e local: privilegio explicito vale para os dois alvos, entao a flag
// nao exige --host.
func TestSudoNaoExigeHost(t *testing.T) {
	con := &conectorFalso{}
	ctx, _ := contextoDeTeste(t, con)

	var errBuf bytes.Buffer
	require.Equal(t, output.ExitOK,
		executar(NewRoot(ctx), ctx, []string{"--sudo", "version"}, &errBuf))
	require.Zero(t, con.chamadas)
	require.True(t, ctx.Flags.Sudo)
}

// A DR5 na fiacao: com --sudo, o runtime montado pelo contexto escala; sem
// ela, nunca. O que o transporte recebe e a prova.
func TestSudoChegaAoRuntime(t *testing.T) {
	casos := []struct {
		nome     string
		sudo     bool
		primeiro string
	}{
		{"com --sudo", true, "sudo"},
		{"sem --sudo", false, "nginx"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			tr := &transporteFalso{stdout: "configuration file /etc/nginx/nginx.conf test is successful\n"}
			ctx := &Context{Flags: &GlobalFlags{Sudo: c.sudo}, Transport: tr}

			_, err := ctx.NovoRuntime().TestConfig(context.Background())
			require.NoError(t, err)

			require.Len(t, tr.argv, 1)
			require.Equal(t, c.primeiro, tr.argv[0][0])
		})
	}
}

// O binario configurado por --nginx-bin tambem faz parte da fiacao.
func TestNginxBinChegaAoRuntime(t *testing.T) {
	tr := &transporteFalso{stdout: "test is successful\n"}
	ctx := &Context{Flags: &GlobalFlags{NginxBin: "/opt/nginx/sbin/nginx"}, Transport: tr}

	_, err := ctx.NovoRuntime().TestConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/opt/nginx/sbin/nginx", tr.argv[0][0])
}

// Todo codigo de diagnostico produzido por este caminho segue a secao 6.0 do
// spec: NGX- mais quatro digitos, sem letra e sem severidade embutida.
func TestCodigosDeDiagnosticoSeguemOFormato(t *testing.T) {
	con := &conectorFalso{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodigoAvisoSSHConfig,
			Message:  "aviso",
		}},
	}
	ctx, out := contextoDeTeste(t, con)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	var errBuf bytes.Buffer
	require.Equal(t, output.ExitOK,
		executar(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf))

	env := envelopeDe(t, out)
	require.NotEmpty(t, env.Diagnostics)
	for _, d := range env.Diagnostics {
		require.Regexp(t, `^NGX-\d{4}$`, d.Code)
	}
}

// O aviso de ~/.ssh/config inacessivel usa o codigo da DR7 e nao aborta a
// conexao: a resolucao segue com flags e defaults.
func TestCaminhoSSHConfigIndisponivelViraAvisoENaoErro(t *testing.T) {
	ctx, _ := contextoDeTeste(t, nil)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	caminho, diag := caminhoSSHConfig(ctx)
	if diag == nil {
		// Em plataformas onde o diretorio do usuario e descoberto por outro
		// meio, nao ha o que avisar — mas ai o caminho tem que existir.
		require.NotEmpty(t, caminho)
		return
	}
	require.Empty(t, caminho)
	require.Equal(t, output.SeverityWarning, diag.Severity)
	require.Equal(t, transport.CodigoAvisoSSHConfig, diag.Code)
	require.True(t, strings.HasPrefix(diag.Code, "NGX-"))
}
