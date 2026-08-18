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
	c.srv.respondBytes("/dl/"+NomeChecksums, checksums)
	c.srv.respondBytes("/dl/"+NomeAssinatura, sig)

	rel := Release{Version: "v" + version, Assets: []Asset{
		{Name: c.assetName, URL: c.srv.URL + "/dl/" + c.assetName},
		{Name: NomeChecksums, URL: c.srv.URL + "/dl/" + NomeChecksums},
		{Name: NomeAssinatura, URL: c.srv.URL + "/dl/" + NomeAssinatura},
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

func (c *scenario) options(path, currentVersion string) Opcoes {
	return Opcoes{
		Canal:                ChannelStable,
		VersaoAtual:          currentVersion,
		CaminhoBinario:       path,
		ChavePublicaOverride: c.key,
		Cliente:              c.srv.client(),
		SO:                   "linux",
		Arch:                 "amd64",
	}
}

func TestExecutarUpdatesOnTheHappyPath(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	res, err := Executar(context.Background(), c.options(path, "v0.2.0"))

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.True(t, res.Disponivel)
	assert.Equal(t, "v0.3.0", res.VersaoRemota)
	assert.Equal(t, ChannelStable, res.Canal)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecutarWithFailingVerificationPreservesTheCurrentBinary(t *testing.T) {
	// The test that matters most in this package: an artifact that does not
	// match the signed checksums.txt must not come near the binary in use.
	// After the failure, the current ngx has to be intact byte for byte, with
	// the same permission, and with no temporary file left in the directory.
	c := newScenario(t, "0.3.0", "ngx v0.3.0", corruptArtifact(t))
	path := testBinary(t, "ngx v0.2.0 IN USE", 0o755)
	before, err := os.Stat(path)
	require.NoError(t, err)

	res, err := Executar(context.Background(), c.options(path, "v0.2.0"))

	require.Nil(t, res)
	assert.Equal(t, CodigoChecksumDivergente, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0 IN USE", contentOf(t, path))

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.Mode().Perm(), after.Mode().Perm())
	assert.Equal(t, before.Size(), after.Size())

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the verification failure left junk in the directory")
}

func TestExecutarWithSignatureFromAnotherKeyPreservesTheCurrentBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	otherPub, _ := keyPair(t)
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.ChavePublicaOverride = keyText(t, otherPub)

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoAssinaturaInvalida, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecutarWithoutPublicKeyRefusesBeforeDownloadingAnything(t *testing.T) {
	// A binary built without -ldflags cannot update itself, and the refusal
	// comes before any request: there is no reason to download what cannot be
	// verified.
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.ChavePublicaOverride = ""
	// The package key is empty by default as well (the pair does not exist yet).
	require.Empty(t, ChavePublica, "ChavePublica must be born empty until the real key exists")

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoSemChavePublica, codeOf(t, err))
	assert.Empty(t, c.srv.visited(), "it should not have touched the network")
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecutarCheckOnlyNeedsNoKeyAndSwapsNothing(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.ChavePublicaOverride = ""
	opts.SomenteVerificar = true

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Disponivel)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecutarCheckOnlyWhenAlreadyUpToDate(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.SomenteVerificar = true

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Disponivel)
	assert.False(t, res.Atualizado)
}

func TestExecutarDoesNotDowngradeUnasked(t *testing.T) {
	// A downgrade is possible, never accidental: if the channel's release is
	// older than the installed one, the update is a no-op.
	c := newScenario(t, "0.2.0", "ngx v0.2.0")
	path := testBinary(t, "ngx v0.9.0", 0o755)

	res, err := Executar(context.Background(), c.options(path, "v0.9.0"))

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.False(t, res.Disponivel)
	assert.Equal(t, "ngx v0.9.0", contentOf(t, path))
}

func TestExecutarDowngradesWhenTheVersionIsRequestedExplicitly(t *testing.T) {
	c := newScenario(t, "0.2.0", "ngx v0.2.0")
	path := testBinary(t, "ngx v0.9.0", 0o755)

	opts := c.options(path, "v0.9.0")
	opts.Versao = "v0.2.0"

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
	assert.Contains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases/tags/v0.2.0")
}

func TestExecutarWithVersionEqualToInstalledSwapsNothing(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0 rebuilt")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.Versao = "v0.3.0"

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecutarDoesNotUpdateWhenAlreadyOnTheChannelVersion(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0 another build")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	res, err := Executar(context.Background(), c.options(path, "v0.3.0"))

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", contentOf(t, path))
}

func TestExecutarWithUnreadableCurrentVersionStillUpdates(t *testing.T) {
	// A local build without -ldflags has a version that is not semver.
	// Blocking the update in exactly that case would block whoever needs it
	// most.
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx dev", 0o755)

	res, err := Executar(context.Background(), c.options(path, "dev-no-version"))

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
}

func TestExecutarOnBetaChannelQueriesTheReleaseList(t *testing.T) {
	c := newScenario(t, "0.4.0-rc.1", "ngx v0.4.0-rc.1")
	path := testBinary(t, "ngx v0.3.0", 0o755)

	opts := c.options(path, "v0.3.0")
	opts.Canal = ChannelBeta

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.Equal(t, ChannelBeta, res.Canal)
	assert.Equal(t, "v0.4.0-rc.1", res.VersaoRemota)
	assert.Contains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases")
	assert.NotContains(t, c.srv.visited(), "/repos/s0beran0/ngx/releases/latest")
}

func TestExecutarRefusesUnknownChannel(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.Canal = Channel("nightly")

	_, err := Executar(context.Background(), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel")
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecutarWithoutArtifactForThePlatformPreservesTheBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)

	opts := c.options(path, "v0.2.0")
	opts.SO = "openbsd"

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoAssetAusente, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExecutarWithInterruptedDownloadPreservesTheBinary(t *testing.T) {
	c := newScenario(t, "0.3.0", "ngx v0.3.0")
	path := testBinary(t, "ngx v0.2.0", 0o755)
	// The artifact stops being served midway.
	c.srv.respond("/dl/"+c.assetName, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := Executar(context.Background(), c.options(path, "v0.2.0"))

	assert.Equal(t, CodigoRede, codeOf(t, err))
	assert.Equal(t, "ngx v0.2.0", contentOf(t, path))
}

func TestExtrairBinarioFromTarGz(t *testing.T) {
	archive := tarGzWith(t, map[string][]byte{"ngx": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_linux_amd64.tar.gz", archive, "linux")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
}

func TestExtrairBinarioFromWindowsZip(t *testing.T) {
	archive := zipWith(t, map[string][]byte{"ngx.exe": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_windows_amd64.zip", archive, "windows")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
}

func TestExtrairBinarioWithoutTheExecutableInside(t *testing.T) {
	archive := tarGzWith(t, map[string][]byte{"LICENSE": []byte("MIT")})

	_, err := ExtrairBinario("ngx_1_linux_amd64.tar.gz", archive, "linux")

	assert.Equal(t, CodigoArtefatoInvalido, codeOf(t, err))
}

func TestExtrairBinarioWithUnknownFormat(t *testing.T) {
	_, err := ExtrairBinario("ngx_1_linux_amd64.rar", []byte("x"), "linux")

	assert.Equal(t, CodigoArtefatoInvalido, codeOf(t, err))
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
