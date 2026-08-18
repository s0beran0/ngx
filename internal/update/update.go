// Package update implements the self-update of ngx: it resolves the release
// of the requested channel, downloads the artifact, verifies signature and
// checksum, and only then swaps the binary on disk.
//
// The order above is the central guarantee of the package. Nothing is written
// over the binary in use before verification passes: the download goes to a
// temporary file in the same directory and the swap is a rename. An
// interrupted download, a tampered artifact or a binary built without an
// embedded public key all end with the current ngx intact.
package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/s0beran0/ngx/internal/output"
)

// Update diagnostic codes. Each failure mode has its own: a generic "it
// failed" sends the reader looking in the wrong place.
const (
	CodigoSemChavePublica    = "NGX-0301"
	CodigoChavePlaceholder   = "NGX-0302"
	CodigoChaveInvalida      = "NGX-0303"
	CodigoAssinaturaInvalida = "NGX-0304"
	CodigoChecksumAusente    = "NGX-0305"
	CodigoChecksumDivergente = "NGX-0306"
	CodigoRateLimit          = "NGX-0307"
	CodigoReleaseAusente     = "NGX-0308"
	CodigoAssetAusente       = "NGX-0309"
	CodigoPermissao          = "NGX-0310"
	CodigoTrocaFalhou        = "NGX-0311"
	CodigoCanalInvalido      = "NGX-0312"
	CodigoDowngrade          = "NGX-0313"
	CodigoRede               = "NGX-0314"
	CodigoArtefatoInvalido   = "NGX-0315"
)

// ChavePublica is the minisign public key embedded in the binary (DD2/DD3).
//
// WARNING -- PLACEHOLDER: THE REAL PUBLIC KEY DOES NOT EXIST YET.
//
// No key pair has been generated for the project so far, so this variable is
// born EMPTY on purpose. Empty means "this binary does not know how to verify
// anything", and Verify REFUSES to update in that state -- it never proceeds
// without verifying. Do not fill it with a plausible value to "unblock the
// flow": a key that looks real slips through review and goes to production
// giving a false guarantee of signature.
//
// When the pair exists, the value goes in at build time via -ldflags -X (see
// .goreleaser.yaml). The planned injection point is output.PublicKey (Task
// D2), which does not exist yet; while it does not, the command wiring must
// assign the value to this variable at initialization. The key is never
// downloaded at runtime (DD3).
var ChavePublica = ""

// PlaceholderChavePublica is the text that signals "key not generated yet".
// It exists so that a placeholder forgotten somewhere in the build chain
// fails with a message of its own instead of becoming an obscure parse error.
const PlaceholderChavePublica = "CHAVE-MINISIGN-PENDENTE-NAO-GERADA"

// Channel is the update channel. The channels are derived from the semver of
// the tag (DD1), not from branches: "v0.2.0" is stable, "v0.2.0-rc.1" is a
// prerelease. EnvCanal is the variable install.sh already uses to pick the
// channel. `ngx update` honors it for the same reason: whoever installed
// through the beta expects to stay on the beta without repeating the flag on
// every update.
const EnvCanal = "NGX_CHANNEL"

type Channel string

const (
	// ChannelStable accepts only non-prerelease releases.
	ChannelStable Channel = "stable"
	// ChannelBeta accepts prereleases as well.
	ChannelBeta Channel = "beta"
)

// ParseChannel converts text into a Channel. An unknown channel is a usage
// error: silently accepting any value would put the user on a channel they
// did not ask for.
func ParseChannel(s string) (Channel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ChannelStable):
		return ChannelStable, nil
	case string(ChannelBeta):
		return ChannelBeta, nil
	default:
		return "", output.Usage(
			"unknown channel %q: the valid channels are \"stable\" and \"beta\"", s)
	}
}

// CanalDoAmbiente reads NGX_CHANNEL. It takes the reading function so it can
// be tested without touching the process environment.
func CanalDoAmbiente(getenv func(string) string) (Channel, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseChannel(getenv("NGX_CHANNEL"))
}

// Opcoes describes one update run.
type Opcoes struct {
	// Canal is the channel consulted when Versao is empty.
	Canal Channel
	// Versao, when filled in, installs exactly that version -- including one
	// older than the current one. It is the only path to a downgrade:
	// without it, an older release is never applied.
	Versao string
	// VersaoAtual is the version of this binary (output.Version).
	VersaoAtual string
	// CaminhoBinario is the executable to be replaced. Empty uses
	// os.Executable().
	CaminhoBinario string
	// ChavePublica overrides the embedded key. It exists for testing; in
	// production it stays empty and the package uses ChavePublica.
	ChavePublicaOverride string
	// Cliente is the GitHub API client. Empty uses the default one.
	Cliente *Cliente
	// SomenteVerificar downloads and swaps nothing: it only reports whether
	// there is a new version.
	SomenteVerificar bool
	// SO and Arch select the artifact. Empty use the ones of the process.
	SO   string
	Arch string
}

// Resultado is what the command reports. The JSON names follow what Task D4
// specifies for the envelope's data field.
type Resultado struct {
	VersaoAtual  string  `json:"current_version"`
	VersaoRemota string  `json:"latest_version"`
	Canal        Channel `json:"channel"`
	Atualizado   bool    `json:"updated"`
	// Disponivel is true when there is a version newer than the current
	// one. With SomenteVerificar, it is the only information that matters.
	Disponivel bool `json:"update_available"`
}

// Executar resolves, downloads, verifies and swaps the binary. It is the
// function the `ngx update` command calls; it prints nothing and picks no
// exit code.
func Executar(ctx context.Context, opts Opcoes) (*Resultado, error) {
	canal := opts.Canal
	if canal == "" {
		canal = ChannelStable
	}
	if canal != ChannelStable && canal != ChannelBeta {
		return nil, output.Usage(
			"unknown channel %q: the valid channels are \"stable\" and \"beta\"", canal)
	}

	cli := opts.Cliente
	if cli == nil {
		cli = NovoCliente(0)
	}

	chave := opts.ChavePublicaOverride
	if chave == "" {
		chave = ChavePublica
	}
	// The key is checked BEFORE any download: a binary that cannot verify
	// anything should not even start downloading. Only --check escapes,
	// because it swaps no binary at all.
	if !opts.SomenteVerificar {
		if err := validarChave(chave); err != nil {
			return nil, err
		}
	}

	rel, err := resolverRelease(ctx, cli, canal, opts.Versao)
	if err != nil {
		return nil, err
	}

	res := &Resultado{
		VersaoAtual:  opts.VersaoAtual,
		VersaoRemota: rel.Version,
		Canal:        canal,
	}

	explicita := opts.Versao != ""
	res.Disponivel = maisNova(rel.Version, opts.VersaoAtual)

	if opts.SomenteVerificar {
		return res, nil
	}

	if !explicita {
		// Without --version, it only moves forward. Never go back a
		// version by accident: if the channel's release is older (or
		// equal), the update is a no-op.
		if !res.Disponivel {
			return res, nil
		}
	} else if mesmaVersao(rel.Version, opts.VersaoAtual) {
		// --version pointing at the already installed version: nothing to
		// do, and it is not an error.
		return res, nil
	}

	caminho := opts.CaminhoBinario
	if caminho == "" {
		caminho, err = os.Executable()
		if err != nil {
			return nil, output.Internal(err,
				"could not find the path of our own binary in order to replace it")
		}
		if resolvido, errLink := filepath.EvalSymlinks(caminho); errLink == nil {
			caminho = resolvido
		}
	}

	so, arch := opts.SO, opts.Arch
	if so == "" {
		so = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}

	artefato, err := rel.AssetDaPlataforma(so, arch)
	if err != nil {
		return nil, err
	}
	somas, err := rel.AssetPorNome(NomeChecksums)
	if err != nil {
		return nil, err
	}
	assinatura, err := rel.AssetPorNome(NomeAssinatura)
	if err != nil {
		return nil, err
	}

	dadosArtefato, err := cli.Baixar(ctx, artefato.URL)
	if err != nil {
		return nil, err
	}
	dadosSomas, err := cli.Baixar(ctx, somas.URL)
	if err != nil {
		return nil, err
	}
	dadosAssinatura, err := cli.Baixar(ctx, assinatura.URL)
	if err != nil {
		return nil, err
	}

	if err := Verify(dadosArtefato, dadosSomas, dadosAssinatura, chave, artefato.Name); err != nil {
		return nil, err
	}

	novo, err := ExtrairBinario(artefato.Name, dadosArtefato, so)
	if err != nil {
		return nil, err
	}

	if err := Apply(caminho, novo); err != nil {
		return nil, err
	}

	res.Atualizado = true
	return res, nil
}

// validarChave explicitly refuses a binary with no key. There is no bypass
// path: no environment variable, flag or "no verification" mode.
func validarChave(chave string) error {
	c := strings.TrimSpace(chave)
	if c == "" {
		return erro(CodigoSemChavePublica,
			"this binary was built without an embedded public verification key and "+
				"therefore cannot update itself: there is no way to prove the downloaded "+
				"release came from the project. Download the new version manually from "+
				"the releases page and check `checksums.txt` with minisign, or use an "+
				"official binary, which already ships with the key embedded")
	}
	if c == PlaceholderChavePublica {
		return erro(CodigoChavePlaceholder,
			"the embedded public key is still the placeholder %q: no minisign key pair "+
				"has been generated for the project. Updating without real verification "+
				"is not an option", PlaceholderChavePublica)
	}
	return nil
}

func resolverRelease(ctx context.Context, cli *Cliente, canal Channel, versao string) (*Release, error) {
	if versao != "" {
		return cli.PorVersao(ctx, versao)
	}
	return cli.Latest(ctx, canal)
}

// normalizarVersao returns the version in the format golang.org/x/mod/semver
// expects (with "v"), or "" if it is not valid semver.
func normalizarVersao(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// maisNova reports whether remota is newer than atual. An unreadable current
// version (a local build without -ldflags, for example) counts as "any
// release is newer": the opposite would lock the update away from whoever
// needs it most.
func maisNova(remota, atual string) bool {
	r := normalizarVersao(remota)
	if r == "" {
		return false
	}
	a := normalizarVersao(atual)
	if a == "" {
		return true
	}
	return semver.Compare(r, a) > 0
}

func mesmaVersao(a, b string) bool {
	na, nb := normalizarVersao(a), normalizarVersao(b)
	if na == "" || nb == "" {
		return false
	}
	return semver.Compare(na, nb) == 0
}

func erro(codigo, format string, args ...any) *output.Error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     codigo,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

func erroCausa(causa error, codigo, format string, args ...any) *output.Error {
	e := erro(codigo, format, args...)
	e.Err = causa
	return e
}
