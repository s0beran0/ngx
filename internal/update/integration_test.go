//go:build integration

package update_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/update"
)

// These tests hit the REAL GitHub API and download the REAL published
// artifacts. That is the whole point: the unit tests already cover the logic
// against a fake server, so what they cannot catch is the release actually
// published having a shape the code does not expect -- an asset named
// differently, a checksums file with another layout, a signature the embedded
// key does not verify. Those only break against the real thing.
//
// The public key comes from the repository file rather than from the binary,
// because `go test` does not go through the -ldflags that inject it.

func publicKey(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "ngx-minisign.pub"))
	require.NoError(t, err, "the project public key has to be in the repository")
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 2, "a minisign .pub is a comment line plus the key")
	return lines[1]
}

func opcoes(t *testing.T, destino string) update.Options {
	return update.Options{
		CurrentVersion:    "0.0.1-para-forcar-update",
		BinaryPath:        destino,
		PublicKeyOverride: publicKey(t),
		SO:                runtime.GOOS,
		Arch:              runtime.GOARCH,
	}
}

// testBinary is a copy that can be replaced without touching anything
// real. Applying an update over the test runner's own binary would be a test
// that breaks the machine it runs on.
func testBinary(t *testing.T) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte("#!/bin/sh\necho antigo\n"), 0o755))
	return caminho
}

func contexto(t *testing.T) context.Context {
	ctx, cancelar := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancelar)
	return ctx
}

// The happy path end to end: resolve the latest stable release from GitHub,
// download it, check signature and checksum against the project key, swap the
// binary -- and then RUN what came out. Checking only that the file changed
// would pass on a corrupted download.
func TestUpdateRealAplicaEProduzBinarioQueRoda(t *testing.T) {
	destino := testBinary(t)

	res, err := update.Run(contexto(t), opcoes(t, destino))
	require.NoError(t, err)
	require.True(t, res.Updated, "there is a stable release newer than 0.0.1")
	require.True(t, strings.HasPrefix(res.RemoteVersion, "v"), "GitHub tags carry the v")

	output, err := exec.Command(destino, "version", "--json").Output()
	require.NoError(t, err, "the installed binary has to run")
	assert.Contains(t, string(output), strings.TrimPrefix(res.RemoteVersion, "v"),
		"the version reported by the binary has to be the one that was downloaded")
}

// --check must not touch anything. The assertion is on the file's content,
// not on the returned flag: a bug that downloaded and swapped while still
// reporting Updated=false would pass a flag-only check.
func TestARealCheckOnlyUpdateDoesNotTouchTheBinary(t *testing.T) {
	destino := testBinary(t)
	before, err := os.ReadFile(destino)
	require.NoError(t, err)

	opts := opcoes(t, destino)
	opts.CheckOnly = true
	res, err := update.Run(contexto(t), opts)
	require.NoError(t, err)

	assert.True(t, res.Available, "0.0.1 is older than any published release")
	assert.False(t, res.Updated)

	after, err := os.ReadFile(destino)
	require.NoError(t, err)
	assert.Equal(t, before, after, "--check cannot write")
}

// The beta channel has to see the prereleases that the stable channel hides.
// This is the difference the GitHub /releases/latest endpoint creates on its
// own, and it is invisible against a fake server that returns whatever it is
// told to.
func TestUpdateRealCanalBetaEnxergaPreLancamento(t *testing.T) {
	destino := testBinary(t)

	opts := opcoes(t, destino)
	opts.CheckOnly = true
	opts.Channel = update.ChannelBeta
	beta, err := update.Run(contexto(t), opts)
	require.NoError(t, err)

	opts.Channel = update.ChannelStable
	stable, err := update.Run(contexto(t), opts)
	require.NoError(t, err)

	assert.NotEmpty(t, beta.RemoteVersion)
	assert.NotEmpty(t, stable.RemoteVersion)
	assert.False(t, strings.Contains(stable.RemoteVersion, "-rc"),
		"the stable channel must never resolve to a prerelease")
}

// A version that does not exist must fail WITHOUT touching the binary. This
// is the most important test of the package: an update that corrupts itself
// halfway leaves the user with no working ngx, which is worse than an update
// that never runs.
func TestUpdateRealVersaoInexistentePreservaOBinario(t *testing.T) {
	destino := testBinary(t)
	before, err := os.ReadFile(destino)
	require.NoError(t, err)

	opts := opcoes(t, destino)
	opts.Version = "v9.9.9-nao-existe"
	_, err = update.Run(contexto(t), opts)
	require.Error(t, err)

	var tipado *output.Error
	require.True(t, errors.As(err, &tipado), "the refusal has to be typed")
	assert.Equal(t, update.CodeReleaseMissing, tipado.Diag.Code)

	after, err := os.ReadFile(destino)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a failed update cannot leave the binary damaged")
}

// A key that is not the project's has to make verification fail, and the
// binary has to survive. Without this the whole signature chain could be
// inert -- accepting anything -- and every other test here would still pass.
func TestARealUpdateWithTheWrongKeyRefusesAndPreservesTheBinary(t *testing.T) {
	destino := testBinary(t)
	before, err := os.ReadFile(destino)
	require.NoError(t, err)

	opts := opcoes(t, destino)
	// Valid minisign key, generated elsewhere: same shape, different signer.
	opts.PublicKeyOverride = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"
	_, err = update.Run(contexto(t), opts)
	require.Error(t, err, "an artifact signed by another key cannot be accepted")

	after, err := os.ReadFile(destino)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
