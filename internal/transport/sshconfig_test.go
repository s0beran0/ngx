package transport

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

// escreverConfig cria um ~/.ssh/config de teste e devolve o caminho.
func escreverConfig(t *testing.T, conteudo string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), 0o600))
	return caminho
}

// usuarioEsperado calcula o usuario corrente por conta propria, sem chamar a
// funcao sob teste: comparar a implementacao com ela mesma nao provaria nada.
func usuarioEsperado(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	nome := u.Username
	if i := strings.LastIndex(nome, `\`); i >= 0 {
		nome = nome[i+1:]
	}
	require.NotEmpty(t, nome)
	return nome
}

func TestResolverLeDiretivasDoArquivo(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  HostName 10.0.0.1
  User deploy
  Port 2222
  IdentityFile /keys/web1_ed25519
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "10.0.0.1", opts.Host, "HostName traduz o alias para o alvo real")
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
	assert.Equal(t, "/keys/web1_ed25519", opts.KeyPath)
}

func TestResolverCasaWildcardEmHost(t *testing.T) {
	caminho := escreverConfig(t, `
Host web*
  User deploy
  Port 2222
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web42"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "web42", opts.Host, "sem HostName o alvo continua sendo o alias")
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
}

func TestResolverHostAusenteDoArquivoUsaDefaults(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  User deploy
  Port 2222
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "db1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags, "host ausente do arquivo nao e anomalia")
	assert.Equal(t, "db1", opts.Host)
	assert.Equal(t, PortaSSHPadrao, opts.Port)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

func TestResolverArquivoAusenteUsaDefaultsSemAviso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "nao-existe", "config")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags, "quem nao tem ~/.ssh/config nao merece aviso")
	assert.Equal(t, 22, opts.Port)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

// TestResolverPrecedenciaDR2 prova os tres niveis num arquivo so: a flag vence
// o arquivo, o arquivo vence o default, e o default cobre o que ninguem disse.
func TestResolverPrecedenciaDR2(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  User deploy
  Port 2222
`)

	// Flag vence arquivo: User e Port vem da flag.
	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1", User: "root", Port: 22022}, caminho)
	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "root", opts.User)
	assert.Equal(t, 22022, opts.Port)

	// Arquivo vence default: sem flag, valem os valores do arquivo.
	opts, _, err = ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)
	require.NoError(t, err)
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)

	// Default cobre o resto: host que o arquivo nao menciona.
	opts, _, err = ResolverSSHConfig(SSHOptions{Host: "outro"}, caminho)
	require.NoError(t, err)
	assert.Equal(t, usuarioEsperado(t), opts.User)
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

// TestResolverFlagVaziaNaoSobrescreveArquivo trava o erro classico de
// precedencia: tratar "" e 0 como escolha do usuario apaga o que o arquivo
// diz e manda a conexao para o lugar errado.
func TestResolverFlagVaziaNaoSobrescreveArquivo(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  HostName 10.0.0.1
  User deploy
  Port 2222
  IdentityFile /keys/web1_ed25519
`)

	flags := SSHOptions{Host: "web1", User: "", Port: 0, KeyPath: ""}
	opts, diags, err := ResolverSSHConfig(flags, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
	assert.Equal(t, "/keys/web1_ed25519", opts.KeyPath)
}

// TestResolverMatchNaoSuportadoDegradaComAviso e a DR7. Um `Match user` e
// valido para o ssh e faz a kevinburke/ssh_config falhar o arquivo INTEIRO —
// inclusive os blocos Host que ela entenderia. O ngx tem que resolver assim
// mesmo e dizer o que perdeu, com arquivo e linha.
func TestResolverMatchNaoSuportadoDegradaComAviso(t *testing.T) {
	caminho := escreverConfig(t,
		"Host web1\n"+ // linha 1
			"  HostName 10.0.0.1\n"+ // linha 2
			"  User deploy\n"+ // linha 3
			"  Port 2222\n"+ // linha 4
			"\n"+ // linha 5
			"Match user deploy\n"+ // linha 6
			"  IdentityFile /keys/deploy\n") // linha 7

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1", User: "root"}, caminho)

	// Lado 1: resolve mesmo assim, com a flag e os defaults. Nada do arquivo
	// entra — nem o bloco Host web1, que sozinho seria legivel.
	require.NoError(t, err, "arquivo ilegivel nunca aborta")
	assert.Equal(t, "web1", opts.Host, "o HostName do arquivo nao foi lido")
	assert.Equal(t, "root", opts.User, "a flag explicita continua valendo")
	assert.Equal(t, PortaSSHPadrao, opts.Port, "o Port do arquivo nao foi lido")

	// Lado 2: o aviso sai, e diz onde. Um resolvedor que so nao aborta,
	// e segue calado, passa no lado 1 e falha aqui — e esse e o defeito
	// que a DR7 existe para impedir.
	require.Len(t, diags, 1)
	d := diags[0]
	assert.Equal(t, output.SeverityWarning, d.Severity, "aviso, nao erro: o comando segue")
	assert.Equal(t, CodigoAvisoSSHConfig, d.Code)
	assert.Equal(t, caminho, d.File)
	assert.Equal(t, 6, d.Line, "a linha do Match que a biblioteca nao entende")
	assert.Positive(t, d.Column)
	assert.Contains(t, d.Message, caminho)
	assert.Contains(t, d.Message, `unsupported Match criterion "user"`,
		"a mensagem diz o que nao foi entendido, nao apenas que falhou")
	assert.Contains(t, d.Message, "--host", "e diz o que continua funcionando")
}

// TestResolverMatchExecDegradaComAviso cobre o outro criterio rejeitado — a
// biblioteca recusa `Match exec` de proposito, para nao rodar comando de um
// arquivo nao confiavel, e o ngx concorda com a recusa.
func TestResolverMatchExecDegradaComAviso(t *testing.T) {
	caminho := escreverConfig(t, "Match exec \"true\"\n  User deploy\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, 1, diags[0].Line)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

func TestResolverArquivoIlegivelDegradaComAviso(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root le qualquer arquivo; o caso nao existe")
	}
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")
	require.NoError(t, os.Chmod(caminho, 0o000))
	t.Cleanup(func() { _ = os.Chmod(caminho, 0o600) })

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, CodigoAvisoSSHConfig, diags[0].Code)
	assert.Equal(t, caminho, diags[0].File)
	assert.Zero(t, diags[0].Line, "sem linha quando o problema nao esta numa linha")
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

func TestResolverPortaInvalidaNoArquivoAvisaEUsaDefault(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  Port setenta\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "setenta")
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

func TestResolverExpandeTilNoIdentityFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	caminho := escreverConfig(t, "Host web1\n  IdentityFile ~/.ssh/id_web1\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, filepath.Join(home, ".ssh", "id_web1"), opts.KeyPath)
}

func TestResolverPreservaCamposQueOArquivoNaoInfluencia(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")
	flags := SSHOptions{
		Host:            "web1",
		KnownHostsPath:  "/custom/known_hosts",
		InsecureHostKey: true,
		Password:        "segredo",
	}

	opts, _, err := ResolverSSHConfig(flags, caminho)

	require.NoError(t, err)
	assert.Equal(t, "/custom/known_hosts", opts.KnownHostsPath)
	assert.True(t, opts.InsecureHostKey)
	assert.Equal(t, "segredo", opts.Password)
}

func TestResolverSemHostEErroDeUso(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")

	_, diags, err := ResolverSSHConfig(SSHOptions{Host: "  "}, caminho)

	require.Error(t, err)
	assert.Equal(t, output.ExitUsage, output.CodeOf(err))
	assert.NotNil(t, diags, "a lista de diagnosticos nunca e nil")
}
