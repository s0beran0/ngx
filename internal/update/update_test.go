package update

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenario assembles a complete release served by httptest: artifact,
// checksums.txt and checksums.txt.minisig signed with a pair generated on the
// spot.
type scenario struct {
	srv       *fakeServer
	key       string
	artifact  []byte
	served    []byte
	assetName string
	binary    []byte
}

func newScenario(t *testing.T, version, binaryContent string, opts ...func(*scenario)) *scenario {
	t.Helper()
	pub, priv := keyPair(t)

	c := &scenario{
		srv:       newServer(t),
		key:       keyText(t, pub),
		assetName: "ngx_" + version + "_linux_amd64.tar.gz",
		binary:    []byte(binaryContent),
	}
	c.artifact = tarGzWith(t, map[string][]byte{"ngx": c.binary, "LICENSE": []byte("MIT")})
	for _, o := range opts {
		o(c)
	}

	checksums := checksumsFor(map[string][]byte{c.assetName: c.artifact})
	sig := minisign.Sign(priv, checksums)

	// The checksums.txt covers c.artifact; the server delivers c.served when
	// the scenario asks for tampering. They are two fields precisely because
	// computing the checksum over the tampered bytes would prove the opposite
	// of what is intended.
	served := c.artifact
	if c.served != nil {
		served = c.served
	}
	c.srv.respondBytes("/dl/"+c.assetName, served)
	c.srv.respondBytes("/dl/"+ChecksumsName, checksums)
	c.srv.respondBytes("/dl/"+SignatureName, sig)

	rel := Release{Version: "v" + version, Assets: []Asset{
		{Name: c.assetName, URL: c.srv.URL + "/dl/" + c.assetName},
		{Name: ChecksumsName, URL: c.srv.URL + "/dl/" + ChecksumsName},
		{Name: SignatureName, URL: c.srv.URL + "/dl/" + SignatureName},
	}}
	c.srv.respondJSON("/repos/s0beran0/ngx/releases/latest", rel)
	c.srv.respondJSON("/repos/s0beran0/ngx/releases", []Release{rel})
	c.srv.respondJSON("/repos/s0beran0/ngx/releases/tags/v"+version, rel)
	return c
}

// corruptArtifact delivers bytes different from the ones covered by the
// signed checksums.txt -- the tampered or corrupted download case.
func corruptArtifact(t *testing.T) func(*scenario) {
	t.Helper()
	return func(c *scenario) {
		c.served = tarGzWith(t, map[string][]byte{"ngx": []byte("TAMPERED BINARY")})
		require.NotEqual(t, c.artifact, c.served,
			"the served artifact has to differ from what the checksums.txt covers")
	}
}

func (c *scenario) options(path, currentVersion string) Options {
	return Options{
		Channel:           ChannelStable,
		CurrentVersion:    currentVersion,
		BinaryPath:        path,
		PublicKeyOverride: c.key,
		Client:            c.srv.client(),
		SO:                "linux",
		Arch:              "amd64",
	}
}

func TestExecuteUpdatesOnTheHappyPath(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	res, err := Run(context.Background(), c.options(path, "v0.2.0"))

	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.True(t, res.Available)
	assert.Equal(t, "v0.3.0", res.RemoteVersion)
	assert.Equal(t, ChannelStable, res.Channel)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecuteWithFailingVerificationPreservesTheCurrentBinary(t *testing.T) {
	// The test that matters most in this package: an artifact that does not
	// match the signed checksums.txt must not come near the binary in use.
	// After the failure, the current ngx has to be intact byte for byte, with
	// the same permission, and with no temporary file left in the directory.
	c := newScenario(t, "0.3.0", "ngx v0.3.0", corruptArtifact(t))
	path := testBinary(t, "ngx v0.2.0 IN USE", 0o755)
	before, err := os.Stat(path)
	require.NoError(t, err)

	res, err := Run(context.Background(), c.options(path, "v0.2.0"))

	require.Nil(t, res)
	assert.Equal(t, CodeChecksumMismatch, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0 IN USE", contentOf(t, path))

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.Mode().Perm(), after.Mode().Perm())
	assert.Equal(t, before.Size(), after.Size())

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the verification failure left junk in the directory")
}

func TestExecuteWithSignatureFromAnotherKeyPreservesTheCurrentBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	otherPub, _ := keyPair(t)
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.PublicKeyOverride = keyText(t, otherPub)

	_, err := Run(context.Background(), opts)

	assert.Equal(t, CodeInvalidSignature, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecuteWithoutPublicKeyRefusesBeforeDownloadingAnything(t *testing.T) {
	// A binary built without -ldflags cannot update itself, and the refusal
	// comes before any request: there is no reason to download what cannot be
	// verified.
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.PublicKeyOverride = ""
	// The package key is empty by default as well (the pair does not exist yet).
	require.Empty(t, PublicKey, "PublicKey must be born empty until the real key exists")

	_, err := Run(context.Background(), opts)

	assert.Equal(t, CodeNoPublicKey, codeOf(t, err))
	assert.Empty(t, c.srv.visited(), "it should not have touched the network")
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecuteCheckOnlyNeedsNoKeyAndSwapsNothing(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.PublicKeyOverride = ""
	opts.CheckOnly = true

	res, err := Run(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Available)
	assert.False(t, res.Updated)
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecuteCheckOnlyWhenAlreadyUpToDate(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.CheckOnly = true

	res, err := Run(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Available)
	assert.False(t, res.Updated)
}

func TestExecuteDoesNotDowngradeUnasked(t *testing.T) {
	// A downgrade is possible, never accidental: if the channel's release is
	// older than the installed one, the update is a no-op.
	c := newScenario(t, "0.2.0", "ngx v0.2.0")
	path := testBinary(t, "ngx v0.9.0", 0o755)

	res, err := Run(context.Background(), c.options(path, "v0.9.0"))

	require.NoError(t, err)
	assert.False(t, res.Updated)
	assert.False(t, res.Available)
	assert.Equal(t, "ngx v0.9.0", contentOf(t, path))
}

func TestExecuteDowngradesWhenTheVersionIsRequestedExplicitly(t *testing.T) {
	c := newScenario(t, "0.2.0", "ngx v0.2.0")
	path := testBinary(t, "ngx v0.9.0", 0o755)

	opts := c.options(path, "v0.9.0")
	opts.Version = "v0.2.0"

	res, err := Run(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
	assert.Contains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases/tags/v0.2.0")
}

func TestExecuteWithVersionEqualToInstalledSwapsNothing(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0 rebuilt")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.Version = "v0.3.0"

	res, err := Run(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Updated)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecuteDoesNotUpdateWhenAlreadyOnTheChannelVersion(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0 another build")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	res, err := Run(context.Background(), c.options(path, "v0.3.0"))

	require.NoError(t, err)
	assert.False(t, res.Updated)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecuteWithUnreadableCurrentVersionStillUpdates(t *testing.T) {
	// A local build without -ldflags has a version that is not semver.
	// Blocking the update in exactly that case would block whoever needs it
	// most.
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx dev", 0o755)

	res, err := Run(context.Background(), c.options(path, "dev-no-version"))

	require.NoError(t, err)
	assert.True(t, res.Updated)
}

func TestExecuteOnBetaChannelQueriesTheReleaseList(t *testing.T) {
	c := newScenario(t, "0.4.0-rc.1", "ngx v0.4.0-rc.1")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.Channel = ChannelBeta

	res, err := Run(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.Equal(t, ChannelBeta, res.Channel)
	assert.Equal(t, "v0.4.0-rc.1", res.RemoteVersion)
	assert.Contains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases")
	assert.NotContains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases/latest")
}

func TestExecuteRefusesUnknownChannel(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.Channel = Channel("nightly")

	_, err := Run(context.Background(), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel")
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecuteWithoutArtifactForThePlatformPreservesTheBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.SO = "openbsd"

	_, err := Run(context.Background(), opts)

	assert.Equal(t, CodeAssetMissing, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecuteWithInterruptedDownloadPreservesTheBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)
	// The artifact stops being served midway.
	c.srv.respond("/dl/"+c.assetName, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := Run(context.Background(), c.options(path, "v0.2.0"))

	assert.Equal(t, CodeNetwork, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	archive := tarGzWith(t, map[string][]byte{"ngx": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtractBinary("ngx_1_linux_amd64.tar.gz", archive, "linux")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
}

func TestExtractBinaryFromWindowsZip(t *testing.T) {
	archive := zipWith(t, map[string][]byte{"ngx.exe": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtractBinary("ngx_1_windows_amd64.zip", archive, "windows")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
}

func TestExtractBinaryWithoutTheExecutableInside(t *testing.T) {
	archive := tarGzWith(t, map[string][]byte{"LICENSE": []byte("MIT")})

	_, err := ExtractBinary("ngx_1_linux_amd64.tar.gz", archive, "linux")

	assert.Equal(t, CodeInvalidArtifact, codeOf(t, err))
}

func TestExtractBinaryWithUnknownFormat(t *testing.T) {
	_, err := ExtractBinary("ngx_1_linux_amd64.rar", []byte("x"), "linux")

	assert.Equal(t, CodeInvalidArtifact, codeOf(t, err))
}

func TestNewerThanFollowsSemver(t *testing.T) {
	assert.True(t, newerThan("v0.3.0", "v0.2.9"))
	assert.False(t, newerThan("v0.2.0", "v0.3.0"))
	assert.False(t, newerThan("v0.3.0", "v0.3.0"))
	// A prerelease comes before the release of the same version.
	assert.False(t, newerThan("v0.3.0-rc.1", "v0.3.0"))
	assert.True(t, newerThan("v0.3.0", "v0.3.0-rc.1"))
	// An unreadable remote version never counts as newer.
	assert.False(t, newerThan("junk", "v0.1.0"))
}

// comCanalDeInstalacao troca a variavel de pacote e a restaura no fim. Serve
// para exercitar o caminho de PRODUCAO — o valor que `-ldflags -X` injeta —
// e nao apenas o override de Options.
func comCanalDeInstalacao(t *testing.T, canal string) {
	t.Helper()
	anterior := InstallChannel
	InstallChannel = canal
	t.Cleanup(func() { InstallChannel = anterior })
}

func TestInstallChannelIsDirectByDefault(t *testing.T) {
	// O default e o que um `go build ./cmd/ngx` produz. Se alguem trocar
	// este valor para "agradar o goreleaser", todo build do fonte perde o
	// auto-update em silencio.
	assert.Equal(t, InstallChannelDirect, InstallChannel)
	assert.NotContains(t, upgradeCommands, InstallChannelDirect,
		"direct nao e gerenciado por ninguem: nao pode estar na tabela de recusa")
}

func TestUpgradeCommandCoversEveryPackagedChannel(t *testing.T) {
	expected := map[string]string{
		"homebrew": "brew upgrade ngx",
		"deb":      "apt upgrade ngx",
		"rpm":      "dnf upgrade ngx",
		"aur":      "pacman -Syu ngx",
		"scoop":    "scoop update ngx",
		"winget":   "winget upgrade ngx",
		"apk":      "apk upgrade ngx",
	}
	for channel, command := range expected {
		got, ok := UpgradeCommand(channel)
		require.True(t, ok, "channel %q has to be known", channel)
		assert.Equal(t, command, got)
	}

	// Counting is what makes this test catch an ADDITION. A packaged channel
	// missing from the table does not fail loudly: it falls through to the
	// unknown-channel refusal, which tells the user nothing about how to
	// upgrade. That is exactly what happened when the .apk started being built
	// and "apk" was not here.
	assert.Equal(t, len(expected), len(upgradeCommands),
		"every channel in the table needs a case here")
	assert.Equal(t, len(upgradeCommands), len(checkCommands),
		"a channel that can be upgraded also has to be checkable")

	_, ok := UpgradeCommand("direct")
	assert.False(t, ok, "direct has no external upgrade command")
}

// A prova que importa: num canal empacotado, ngx nao troca o binario. Verificar
// so `Updated == false` passaria com um defeito que recusa com uma mao e troca
// com a outra, entao o teste olha o arquivo em disco — conteudo, permissao,
// tamanho — e ainda exige que nenhuma requisicao tenha saido.
func TestExecuteRefusesPackagedChannelWithoutTouchingTheBinary(t *testing.T) {
	casos := []struct {
		canal   string
		comando string
	}{
		{"homebrew", "brew upgrade ngx"},
		{"deb", "apt upgrade ngx"},
		{"rpm", "dnf upgrade ngx"},
		{"aur", "pacman -Syu ngx"},
		{"scoop", "scoop update ngx"},
		{"winget", "winget upgrade ngx"},
		{"HOMEBREW", "brew upgrade ngx"},
		{"  deb  ", "apt upgrade ngx"},
	}

	for _, caso := range casos {
		t.Run(caso.canal, func(t *testing.T) {
			c := newScenario(t, "0.3.0", "ngx v0.3.0")
			path := testBinary(t, "ngx v0.2.0 EM USO", 0o755)
			antes, err := os.Stat(path)
			require.NoError(t, err)

			opts := c.options(path, "v0.2.0")
			opts.InstallChannelOverride = caso.canal

			res, err := Run(context.Background(), opts)

			require.Nil(t, res)
			assert.Equal(t, CodePackagedInstall, codeOf(t, err))
			assert.Contains(t, err.Error(), caso.comando,
				"a recusa tem de nomear o comando certo daquele canal")

			assert.Equal(t, "ngx v0.2.0 EM USO", contentOf(t, path),
				"o binario em uso foi trocado apesar da recusa")
			depois, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, antes.Mode().Perm(), depois.Mode().Perm())
			assert.Equal(t, antes.Size(), depois.Size())

			entradas, err := os.ReadDir(filepath.Dir(path))
			require.NoError(t, err)
			assert.Len(t, entradas, 1, "a recusa deixou lixo no diretorio")

			assert.Empty(t, c.srv.visited(),
				"a recusa vem antes de qualquer requisicao")
		})
	}
}

// Um erro de digitacao na flag de quem empacota nao pode reabilitar o
// auto-update. O modo de falha seguro e recusar: quem recusa por engano perde
// um comando, quem aceita por engano corrompe uma instalacao.
func TestExecuteRefusesUnknownInstallChannel(t *testing.T) {
	for _, canal := range []string{"homebrwe", "brew", "apt", "", "   ", "nixpkgs"} {
		t.Run("canal="+canal, func(t *testing.T) {
			c := newScenario(t, "0.3.0", "ngx v0.3.0")
			path := testBinary(t, "ngx v0.2.0 EM USO", 0o755)
			comCanalDeInstalacao(t, canal)

			res, err := Run(context.Background(), c.options(path, "v0.2.0"))

			require.Nil(t, res)
			assert.Equal(t, CodeUnknownInstall, codeOf(t, err))
			assert.Contains(t, err.Error(), "homebrew",
				"a mensagem tem de listar os canais que existem")
			assert.Equal(t, "ngx v0.2.0 EM USO", contentOf(t, path))
			assert.Empty(t, c.srv.visited())
		})
	}
}

// O caminho de producao e a variavel de pacote, nao o override de Options:
// e nela que `-ldflags -X` escreve. Se a recusa so olhasse o override, todo
// build empacotado continuaria se atualizando sozinho.
func TestExecuteHonorsTheInjectedPackageVariable(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0 EM USO", 0o755)
	comCanalDeInstalacao(t, "homebrew")

	res, err := Run(context.Background(), c.options(path, "v0.2.0"))

	require.Nil(t, res)
	assert.Equal(t, CodePackagedInstall, codeOf(t, err))
	assert.Contains(t, err.Error(), "brew upgrade ngx")
	assert.Equal(t, "ngx v0.2.0 EM USO", contentOf(t, path))
	assert.Empty(t, c.srv.visited())
}

// --check tambem recusa: num canal empacotado, a ultima release no GitHub nao
// e a versao que o gerenciador tem para oferecer, entao "ha atualizacao
// disponivel" seria a resposta de outra pergunta.
func TestExecuteCheckOnlyAlsoRefusesOnPackagedChannel(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0 EM USO", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.InstallChannelOverride = "deb"
	opts.CheckOnly = true

	res, err := Run(context.Background(), opts)

	require.Nil(t, res)
	assert.Equal(t, CodePackagedInstall, codeOf(t, err))

	// --check asked "is there anything newer?", so the refusal has to answer
	// THAT question with the command that can. Sending it to `apt upgrade`
	// would turn a refusal into a dead end: the caller never wanted to
	// upgrade, only to know.
	assert.Contains(t, err.Error(), "apt list --upgradable ngx")
	assert.NotContains(t, err.Error(), "apt upgrade ngx",
		"the upgrade command answers a question --check did not ask")

	assert.Equal(t, "ngx v0.2.0 EM USO", contentOf(t, path))
	assert.Empty(t, c.srv.visited(), "--check on a packaged channel must not reach the network")
}
