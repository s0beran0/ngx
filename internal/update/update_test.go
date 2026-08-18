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
