//go:build integration

// Integration tests of the remote path against the test bench (test/bancada):
// a throwaway container with sshd and nginx that reproduces the shape measured
// on a production nginx — three wildcard patterns, 130 files in the effective
// configuration, /etc/nginx readable by root only and a secret inside the
// configuration.
//
// They sit behind the `integration` tag because they need Docker: `go test
// ./...` without the tag touches no container at all. With the tag and no
// bench running, the tests SKIP with the instructions to bring it up — whoever
// cloned the project and has no Docker must not see a false failure.
//
// Run: make bancada-up && go test -tags integration ./... -race
//
// The package is transport_test, and not transport, because the explicit
// privilege test (DR5) uses internal/runtime, which imports
// internal/transport: from inside the package that would be an import cycle.
package transport_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/s0beran0/ngx/internal/transport"
)

const (
	hostBancada    = "127.0.0.1"
	usuarioBancada = "ngxtest"
	// The port is fixed in the Makefile so the test knows where to connect;
	// whoever brought the bench up with another BANCADA_PORTA says so
	// through the variable.
	portaBancadaPadrao = 2222
	envPortaBancada    = "NGX_BANCADA_PORTA"

	// The effective configuration of the container has 130 files, checked
	// during the image build itself. The tolerance matches the one in
	// smoke.sh: the number of dynamic modules changes if an nginx-mod-*
	// package comes in or goes out, and what the test proves is the order
	// of magnitude, not the inventory.
	arquivosDaBancada  = 130
	toleranciaArquivos = 5

	// marcadorArmadilha exists only in the same-named file on the LOCAL
	// disk (test/bancada/armadilha-local). Seeing it means the file from
	// the local disk was read instead of the one in the container.
	marcadorArmadilha = "ARMADILHA-LOCAL-NAO-DEVE-APARECER"
	arquivoArmadilha  = "zz-armadilha-local.conf"

	// The three secrets of the bench, in the three shapes it reproduces.
	tokenDaBancada    = "Bearer ngx-bancada-token-4f3c9a1b2e"
	htpasswdDaBancada = "/etc/nginx/secrets/htpasswd"
	chaveTLSDaBancada = "/etc/nginx/secrets/tls.key"

	// The remote fixture, written into the HOME of the bench user.
	arquivoTopoRemoto  = "ngx-remoto.conf"
	dirConfDRemoto     = "etc/nginx/conf.d"
	arquivoDoContainer = "10-do-container.conf"
	padraoConfD        = dirConfDRemoto + "/*.conf"
	padraoModulos      = "/usr/share/nginx/modules/*.conf"
)

// ---------------------------------------------------------------------------
// Bancada
// ---------------------------------------------------------------------------

func raizDoRepo(t *testing.T) string {
	t.Helper()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return raiz
}

func portaDaBancada(t *testing.T) int {
	t.Helper()
	valor := os.Getenv(envPortaBancada)
	if valor == "" {
		return portaBancadaPadrao
	}
	porta, err := strconv.Atoi(valor)
	require.NoErrorf(t, err, "%s=%q is not a port number", envPortaBancada, valor)
	return porta
}

// exigirBancada skips the test, instead of failing it, when the bench is not
// running: with no Docker the remote path cannot be exercised, and a failure
// there would say nothing about ngx.
func exigirBancada(t *testing.T) (chave string, porta int) {
	t.Helper()

	chave = filepath.Join(raizDoRepo(t), "test", "bancada", ".chave", "id_ed25519")
	porta = portaDaBancada(t)

	if _, err := os.Stat(chave); err != nil {
		t.Skipf("bench not running: the test key %s does not exist. "+
			"Run `make bancada-up` (needs Docker).", chave)
	}

	endereco := net.JoinHostPort(hostBancada, strconv.Itoa(porta))
	conn, err := net.DialTimeout("tcp", endereco, 2*time.Second)
	if err != nil {
		t.Skipf("bench not running: nothing listens on %s (%v). "+
			"Run `make bancada-up` (needs Docker).", endereco, err)
	}
	_ = conn.Close()

	// The ssh-agent of whoever runs the suite is left out: the bench only
	// accepts the generated key, and an agent holding several keys exhausts
	// the sshd MaxAuthTries before reaching it. With no agent the method
	// simply drops out of the list — and the path this test wants to
	// exercise is the key one.
	t.Setenv(transport.EnvSocketSSHAgent, "")

	return chave, porta
}

func opcoesDaBancada(chave string, porta int, knownHosts string) transport.SSHOptions {
	return transport.SSHOptions{
		Host:           hostBancada,
		Port:           porta,
		User:           usuarioBancada,
		KeyPath:        chave,
		KnownHostsPath: knownHosts,
		Timeout:        20 * time.Second,
	}
}

// prefixoDaLinhaKnownHosts is what the unknown-host message writes right
// before the line that is ready for known_hosts.
const prefixoDaLinhaKnownHosts = "append the line to the file: "

func linhaDoKnownHosts(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)

	_, linha, achou := strings.Cut(e.Diag.Message, prefixoDaLinhaKnownHosts)
	require.Truef(t, achou,
		"the unknown-host message did not carry the known_hosts line: %s", e.Diag.Message)
	return strings.TrimSpace(linha)
}

// knownHostsAprendido records the bench host key in a temporary known_hosts,
// learning it from the first-access refusal itself.
//
// No test here uses --insecure-host-key: they all connect with real host key
// verification, which is how ngx runs in production.
func knownHostsAprendido(t *testing.T, chave string, porta int) string {
	t.Helper()

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if err == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts: an unknown host has to be refused")
	}

	require.NoError(t, os.WriteFile(caminho, []byte(linhaDoKnownHosts(t, err)+"\n"), 0o600))
	return caminho
}

func conectarNaBancada(t *testing.T) transport.Transport {
	t.Helper()

	chave, porta := exigirBancada(t)
	tr, diags, err := transport.SSHComDiagnosticos(
		opcoesDaBancada(chave, porta, knownHostsAprendido(t, chave, porta)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	for _, d := range diags {
		require.NotEqualf(t, transport.CodigoAvisoHostKeyInsegura, d.Code,
			"the test has to connect with host key verification: %s", d.Message)
	}
	return tr
}

// ---------------------------------------------------------------------------
// Fixture remota
// ---------------------------------------------------------------------------

// The fixture lives in the HOME of the bench user because /etc/nginx is
// 0700 root:root on purpose — the DR5 trap — and neither `nginx -T` nor an
// SFTP read reaches the real configuration as ngxtest. The files below repeat
// the shapes that matter: a relative wildcard (which resolves against the
// directory of the top file, and is therefore the one the local-disk trap
// competes for), an absolute wildcard over real container files, and the three
// secret shapes of the bench.
const topoRemoto = `# Fixture of the ngx integration test.

# Absolute wildcard: four real container files, from the nginx-mod-* packages.
# They do not exist on the disk of whoever runs the test.
include ` + padraoModulos + `;

events {
    worker_connections 1024;
}

http {
    # Relative wildcard, resolved against the directory of the top file (the
    # remote HOME). On the local disk, the same pattern hits the trap.
    include ` + padraoConfD + `;
}
`

const confDoContainer = `# MARCADOR-DO-CONTAINER
#
# The three secret shapes are the same as in the real bench configuration
# (test/bancada/gerar-config.sh).
server {
    listen 8080;
    server_name do-container.bancada.local;

    location / {
        auth_basic "area restrita da bancada";
        auth_basic_user_file ` + htpasswdDaBancada + `;

        proxy_set_header Authorization "` + tokenDaBancada + `";
        proxy_pass http://127.0.0.1:9000;
    }
}

server {
    listen 8443 ssl;
    server_name tls.bancada.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key ` + chaveTLSDaBancada + `;

    location / {
        return 200 "tls da bancada\n";
    }
}
`

// montarFixtureRemota writes the fixture into the container HOME and removes
// it at the end. The `sh -c` is here, in the test, and not in ngx: ngx never
// assembles a shell line, and what it runs on the target is still explicit
// argv.
func montarFixtureRemota(t *testing.T, tr transport.Transport) {
	t.Helper()

	script := fmt.Sprintf(`set -e
rm -rf "$HOME/etc" "$HOME/%[1]s"
mkdir -p "$HOME/%[2]s"
cat > "$HOME/%[1]s" <<'FIM'
%[3]s
FIM
cat > "$HOME/%[2]s/%[4]s" <<'FIM'
%[5]s
FIM
`, arquivoTopoRemoto, dirConfDRemoto, topoRemoto, arquivoDoContainer, confDoContainer)

	rodar(t, tr, script)
	t.Cleanup(func() {
		_, _, _, _ = tr.Run(context.Background(),
			[]string{"sh", "-c", fmt.Sprintf(`rm -rf "$HOME/etc" "$HOME/%s"`, arquivoTopoRemoto)})
	})
}

func rodar(t *testing.T, tr transport.Transport, script string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	stdout, stderr, saida, err := tr.Run(ctx, []string{"sh", "-c", script})
	require.NoError(t, err)
	require.Zerof(t, saida, "setting up the remote fixture failed: %s %s", stdout, stderr)
}

func caminhosDaArvore(t *config.Tree) []string {
	caminhos := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		caminhos = append(caminhos, f.Path)
	}
	return caminhos
}

// ---------------------------------------------------------------------------
// 1. The glob resolves the CONTAINER files
// ---------------------------------------------------------------------------

// This is the test that keeps the defect Task R3 fixed from coming back: with
// Glob not injected, crossplane resolved `include conf.d/*.conf` through
// filepath.Glob over the disk of whoever ran ngx, and presented the operator's
// own machine files as the server configuration (DR4).
//
// The trap is a same-named file on the local disk. The test changes the
// current directory to it and proves, in the same run, both halves: that the
// pattern does match the trap locally (otherwise the test would pass
// vacuously), and that the tree read from the bench has the container file and
// no trace of the trap.
func TestGlobResolveOsArquivosDoContainerENaoOsDoDiscoLocal(t *testing.T) {
	armadilha := filepath.Join(raizDoRepo(t), "test", "bancada", "armadilha-local")

	tr := conectarNaBancada(t)
	montarFixtureRemota(t, tr)

	t.Chdir(armadilha)

	locais, err := transport.Local().Glob(padraoConfD)
	require.NoError(t, err)
	require.Containsf(t, locais, filepath.Join(dirConfDRemoto, arquivoArmadilha),
		"the local trap is not in place: %s should match %s from %s",
		padraoConfD, arquivoArmadilha, armadilha)

	arvore, err := config.Parse(config.ParseOptions{
		Path: arquivoTopoRemoto,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	caminhos := caminhosDaArvore(arvore)
	require.Contains(t, caminhos, dirConfDRemoto+"/"+arquivoDoContainer,
		"the relative wildcard did not bring in the container file")

	modulos := 0
	for _, f := range arvore.Files {
		require.NotContainsf(t, f.Path, "armadilha",
			"a file from the local disk got into the tree read from the bench: %s", f.Path)
		require.NotContainsf(t, string(f.Source), marcadorArmadilha,
			"the local trap marker leaked into the tree, coming from %s", f.Path)
		if strings.HasPrefix(f.Path, "/usr/share/nginx/modules/") {
			modulos++
		}
	}
	require.Positive(t, modulos, "the absolute wildcard brought in no container module")
	require.Contains(t, caminhos, arquivoTopoRemoto)
}

// ---------------------------------------------------------------------------
// 2. The effective configuration of the container, with its ~130 files
// ---------------------------------------------------------------------------

// The 130 files are only reachable through `nginx -T` with privilege:
// /etc/nginx is 0700 root:root, so the SFTP read (the inspect path) stops at
// the first file. This test is therefore the other half of the pair with the
// privilege one below: with --sudo the dump exists and it is the container's.
func TestDumpRemotoComSudoDevolveAConfiguracaoEfetivaDoContainer(t *testing.T) {
	tr := conectarNaBancada(t)

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelar()

	dump, err := runtime.New(tr, runtime.ComSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.Contains(t, dump.ConfigFile, "/etc/nginx/nginx.conf")
	require.InDeltaf(t, arquivosDaBancada, len(dump.Files), toleranciaArquivos,
		"the effective configuration of the container has %d files; the dump brought %d",
		arquivosDaBancada, len(dump.Files))

	// The three bench wildcards resolved inside the container.
	porPrefixo := map[string]int{
		"/etc/nginx/conf.d/":        0,
		"/etc/nginx/default.d/":     0,
		"/usr/share/nginx/modules/": 0,
	}
	for _, f := range dump.Files {
		for prefixo := range porPrefixo {
			if strings.HasPrefix(f.Path, prefixo) {
				porPrefixo[prefixo]++
			}
		}
		require.NotContainsf(t, f.Content, marcadorArmadilha,
			"the local trap marker showed up in the container dump, in %s", f.Path)
	}
	for prefixo, n := range porPrefixo {
		require.Positivef(t, n, "no file came from %s", prefixo)
	}
	require.Greater(t, porPrefixo["/etc/nginx/conf.d/"], 100,
		"conf.d is the big directory of the bench")
}

// ---------------------------------------------------------------------------
// 3. An unknown host is refused BEFORE it enters known_hosts
// ---------------------------------------------------------------------------

// DR1 demands two different messages: a first access is normal friction, a
// changed key is a possible attack. Mixing them up is the dangerous defect —
// whoever reads "the key changed" on a first access learns to ignore the
// warning that will one day matter for real.
func TestHostDesconhecidoEhRecusadoAntesDeEntrarNoKnownHosts(t *testing.T) {
	chave, porta := exigirBancada(t)

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, erroPrimeiroAcesso := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if erroPrimeiroAcesso == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts")
	}

	var e *output.Error
	require.ErrorAs(t, erroPrimeiroAcesso, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)
	require.NotEqual(t, transport.CodigoHostKeyAlterada, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "unknown host")
	require.Contains(t, msg, "first access")
	require.NotContains(t, msg, "CHANGED")
	require.NotContains(t, msg, "attack")
	require.Equal(t, caminho, e.Diag.File)

	// ngx does not learn the key on its own: the operator is who records it.
	conteudo, err := os.ReadFile(caminho)
	require.NoError(t, err)
	require.Empty(t, conteudo, "known_hosts was written by ngx")

	// And with the key recorded the same connection goes through — the
	// refusal came from the verification, not from the credential.
	linha := linhaDoKnownHosts(t, erroPrimeiroAcesso)
	require.NoError(t, os.WriteFile(caminho, []byte(linha+"\n"), 0o600))

	tr, diags, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", usuarioBancada, hostBancada, porta), tr.Describe())
	for _, d := range diags {
		require.NotEqual(t, transport.CodigoAvisoHostKeyInsegura, d.Code)
	}
}

// ---------------------------------------------------------------------------
// 4. Explicit privilege (DR5)
// ---------------------------------------------------------------------------

// The bench was built with this trap: `nginx -T` fails for the ordinary user
// and passwordless sudo does exist, restricted to the nginx binary. The path
// that "just works" would be to escalate silently; ngx reports and stops.
func TestSemSudoONgxReportaAExigenciaDePrivilegioENaoEscalaSozinho(t *testing.T) {
	tr := conectarNaBancada(t)

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelar()

	dump, err := runtime.New(tr).DumpConfig(ctx)
	require.Nil(t, dump, "with no privilege there is no dump: an unavailable field is omitted")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, runtime.CodigoPrivilegioNecessario, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "`nginx -T`", "the command that ran carried no sudo")
	require.Contains(t, msg, "--sudo")
	require.Contains(t, msg, "sudo -n nginx -T", "the message has to say what the privileged command is")
	require.NotContains(t, msg, tokenDaBancada)

	// The same call, with --sudo, works: the bench allows passwordless sudo
	// for nginx. That is, the refusal above was a decision by ngx, not a
	// missing path.
	dump, err = runtime.New(tr, runtime.ComSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.NotEmpty(t, dump.Files)
}

// ---------------------------------------------------------------------------
// 5. Redaction: the three bench secrets do not leak
// ---------------------------------------------------------------------------

// What is proved here is that the secret IS in the configuration read from the
// container — which is what makes the CLI redaction test (internal/cli) a real
// proof, and not a proof over an empty configuration.
func TestOsTresSegredosEstaoNaConfiguracaoLidaDaBancada(t *testing.T) {
	tr := conectarNaBancada(t)
	montarFixtureRemota(t, tr)

	arvore, err := config.Parse(config.ParseOptions{
		Path: arquivoTopoRemoto,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	var texto strings.Builder
	for _, f := range arvore.Files {
		texto.Write(f.Source)
	}
	for _, segredo := range []string{tokenDaBancada, htpasswdDaBancada, chaveTLSDaBancada} {
		require.Containsf(t, texto.String(), segredo,
			"the configuration read from the bench does not have the secret %q", segredo)
	}
}
