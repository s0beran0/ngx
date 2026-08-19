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
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/s0beran0/ngx/internal/output"
)

// Update diagnostic codes. Each failure mode has its own: a generic "it
// failed" sends the reader looking in the wrong place.
const (
	CodeNoPublicKey      = "NGX-0301"
	CodePlaceholderKey   = "NGX-0302"
	CodeInvalidKey       = "NGX-0303"
	CodeInvalidSignature = "NGX-0304"
	CodeChecksumMissing  = "NGX-0305"
	CodeChecksumMismatch = "NGX-0306"
	CodeRateLimit        = "NGX-0307"
	CodeReleaseMissing   = "NGX-0308"
	CodeAssetMissing     = "NGX-0309"
	CodePermission       = "NGX-0310"
	CodeSwapFailed       = "NGX-0311"
	CodeInvalidChannel   = "NGX-0312"
	CodeDowngrade        = "NGX-0313"
	CodeNetwork          = "NGX-0314"
	CodeInvalidArtifact  = "NGX-0315"
	CodePackagedInstall  = "NGX-0316"
	CodeUnknownInstall   = "NGX-0317"
)

// PublicKey is the minisign public key embedded in the binary (DD2/DD3).
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
var PublicKey = ""

// PublicKeyPlaceholder is the text that signals "key not generated yet".
// It exists so that a placeholder forgotten somewhere in the build chain
// fails with a message of its own instead of becoming an obscure parse error.
const PublicKeyPlaceholder = "MINISIGN-KEY-PENDING-NOT-GENERATED"

// InstallChannel diz como este binario foi instalado (DC1). E um fato de
// build: quem empacota injeta o valor com
// `-ldflags -X github.com/s0beran0/ngx/internal/update.InstallChannel=<canal>`.
// O binario nunca tenta adivinhar — nao olha o proprio caminho, nao procura
// /usr/bin/dpkg, nao consulta o dono do arquivo. Toda inferencia erra em algum
// lugar, e o erro aqui corromperia o estado de outro programa.
//
// O default e "direct" porque e isso que um `go build ./cmd/ngx` produz: quem
// compila do fonte tem auto-update funcionando. Todo build empacotado
// sobrescreve.
//
// ATENCAO (DD6): `-ldflags -X` contra um simbolo inexistente falha em
// SILENCIO. Renomear esta variavel, mover o pacote ou trocar o tipo exige
// acompanhar `.goreleaser.yaml` e o workflow de release no mesmo commit —
// senao o canal volta a "direct" sem ninguem perceber, que e exatamente o
// estado que esta variavel existe para evitar.
var InstallChannel = "direct"

// InstallChannelDirect e o unico canal em que ngx se atualiza sozinho.
const InstallChannelDirect = "direct"

// upgradeCommands mapeia canal de instalacao para o comando que atualiza o ngx
// naquele canal. Estar nesta tabela significa "gerenciado por outro programa":
// ngx se recusa a atualizar e ensina o comando certo (DC2).
var upgradeCommands = map[string]string{
	"homebrew": "brew upgrade ngx",
	"deb":      "apt upgrade ngx",
	"rpm":      "dnf upgrade ngx",
	"aur":      "pacman -Syu ngx",
	"scoop":    "scoop update ngx",
	"winget":   "winget upgrade ngx",
	"apk":      "apk upgrade ngx",
}

// checkCommands is the counterpart for `--check`: how to ASK whether a newer
// version exists, in a channel that ngx does not manage.
//
// It exists because refusing `--check` without it answers the wrong question.
// The caller wanted to know whether it is up to date; telling it only that ngx
// will not update leaves that unanswered, and an answer taken from the GitHub
// releases would be worse than none -- in a packaged channel the newest release
// is not what the package manager is able to install, so reporting it invents
// an update the caller cannot apply.
var checkCommands = map[string]string{
	"homebrew": "brew outdated ngx",
	"deb":      "apt list --upgradable ngx",
	"rpm":      "dnf check-update ngx",
	"aur":      "pacman -Qu ngx",
	"scoop":    "scoop status ngx",
	"winget":   "winget upgrade --id ngx",
	"apk":      "apk version ngx",
}

// CheckCommand returns the command that asks about updates in this channel.
func CheckCommand(channel string) (string, bool) {
	cmd, ok := checkCommands[normalizeInstallChannel(channel)]
	return cmd, ok
}

// UpgradeCommand devolve o comando de atualizacao do canal, e se o canal e
// conhecido. "direct" nao esta na tabela: nele o comando de atualizacao e o
// proprio `ngx update`.
func UpgradeCommand(channel string) (string, bool) {
	cmd, ok := upgradeCommands[normalizeInstallChannel(channel)]
	return cmd, ok
}

// InstallChannels lista os canais gerenciados por pacote, em ordem estavel.
// A ordem e fixa porque o valor entra em mensagem de erro, e uma mensagem que
// muda de execucao para execucao e ruido para quem le a saida.
func InstallChannels() []string {
	nomes := make([]string, 0, len(upgradeCommands))
	for c := range upgradeCommands {
		nomes = append(nomes, c)
	}
	sort.Strings(nomes)
	return nomes
}

func normalizeInstallChannel(c string) string {
	return strings.ToLower(strings.TrimSpace(c))
}

// installChannelOf resolve o canal desta execucao. O override existe para
// teste, como PublicKeyOverride; em producao fica vazio e vale a variavel do
// pacote.
func installChannelOf(opts Options) string {
	if strings.TrimSpace(opts.InstallChannelOverride) != "" {
		return opts.InstallChannelOverride
	}
	return InstallChannel
}

// checkInstallChannel recusa a auto-atualizacao quando o binario nao veio do
// canal "direct" (DC2). Nao ha fallback, nao ha pergunta, nao ha --force:
// trocar o binario debaixo de um gerenciador de pacotes deixa o banco dele
// apontando para um arquivo que ele nao conhece mais.
//
// Um canal desconhecido tambem recusa. Um erro de digitacao na flag de quem
// empacota nao pode reabilitar auto-update em silencio — o modo de falha
// seguro aqui e recusar, porque quem recusa por engano perde um comando e quem
// aceita por engano corrompe uma instalacao.
//
// A recusa vale inclusive para --check: num canal empacotado, a ultima release
// no GitHub nao e a versao que aquele gerenciador tem para oferecer, entao
// responder "ha atualizacao disponivel" seria responder outra pergunta.
func checkInstallChannel(channel string, checkOnly bool) error {
	c := normalizeInstallChannel(channel)
	if c == InstallChannelDirect {
		return nil
	}
	if cmd, ok := upgradeCommands[c]; ok {
		// --check asked a different question -- "is there anything newer?" --
		// so it gets the command that answers THAT one. Sending someone to
		// `brew upgrade` when they only wanted to know is how a refusal turns
		// into a dead end.
		if checkOnly {
			return newError(CodePackagedInstall,
				"this ngx was installed through %s, so the newest release on GitHub is "+
					"not what that package manager has to offer, and reporting it would "+
					"invent an update you cannot apply. Run `%s` to ask %s instead",
				c, checkCommands[c], c)
		}
		return newError(CodePackagedInstall,
			"this ngx was installed through %s, which keeps track of its own versions: "+
				"replacing the binary in place would leave that package manager pointing "+
				"at a file it no longer knows. Run `%s` instead", c, cmd)
	}
	return newError(CodeUnknownInstall,
		"this binary declares the install channel %q, which ngx does not recognize, so it "+
			"refuses to update itself rather than risk corrupting whatever installed it. "+
			"The known channels are %s, plus \"%s\" for a build from source; until the build "+
			"is fixed, update by hand from the releases page",
		channel, strings.Join(InstallChannels(), ", "), InstallChannelDirect)
}

// Channel is the update channel. The channels are derived from the semver of
// the tag (DD1), not from branches: "v0.2.0" is stable, "v0.2.0-rc.1" is a
// prerelease. EnvChannel is the variable install.sh already uses to pick the
// channel. `ngx update` honors it for the same reason: whoever installed
// through the beta expects to stay on the beta without repeating the flag on
// every update.
const EnvChannel = "NGX_CHANNEL"

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

// ChannelFromEnv reads NGX_CHANNEL. It takes the reading function so it can
// be tested without touching the process environment.
func ChannelFromEnv(getenv func(string) string) (Channel, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseChannel(getenv("NGX_CHANNEL"))
}

// Options describes one update run.
type Options struct {
	// Channel is the channel consulted when Version is empty.
	Channel Channel
	// Version, when filled in, installs exactly that version -- including one
	// older than the current one. It is the only path to a downgrade:
	// without it, an older release is never applied.
	Version string
	// CurrentVersion is the version of this binary (output.Version).
	CurrentVersion string
	// BinaryPath is the executable to be replaced. Empty uses
	// os.Executable().
	BinaryPath string
	// PublicKey overrides the embedded key. It exists for testing; in
	// production it stays empty and the package uses PublicKey.
	PublicKeyOverride string
	// InstallChannelOverride substitui InstallChannel. Existe para teste,
	// pelo mesmo motivo de PublicKeyOverride; em producao fica vazio e vale
	// a variavel do pacote.
	InstallChannelOverride string
	// Client is the GitHub API client. Empty uses the default one.
	Client *Client
	// CheckOnly downloads and swaps nothing: it only reports whether
	// there is a new version.
	CheckOnly bool
	// SO and Arch select the artifact. Empty use the ones of the process.
	SO   string
	Arch string
}

// Result is what the command reports. The JSON names follow what Task D4
// specifies for the envelope's data field.
type Result struct {
	CurrentVersion string  `json:"current_version"`
	RemoteVersion  string  `json:"latest_version"`
	Channel        Channel `json:"channel"`
	Updated        bool    `json:"updated"`
	// Available is true when there is a version newer than the current
	// one. With CheckOnly, it is the only information that matters.
	Available bool `json:"update_available"`
}

// Run resolves, downloads, verifies and swaps the binary. It is the
// function the `ngx update` command calls; it prints nothing and picks no
// exit code.
func Run(ctx context.Context, opts Options) (*Result, error) {
	// Primeira coisa da funcao, antes de validar flag e antes de qualquer
	// rede: se este binario nao pode se atualizar, nada mais precisa
	// acontecer.
	if err := checkInstallChannel(installChannelOf(opts), opts.CheckOnly); err != nil {
		return nil, err
	}

	channel := opts.Channel
	if channel == "" {
		channel = ChannelStable
	}
	if channel != ChannelStable && channel != ChannelBeta {
		return nil, output.Usage(
			"unknown channel %q: the valid channels are \"stable\" and \"beta\"", channel)
	}

	cli := opts.Client
	if cli == nil {
		cli = NewClient(0)
	}

	key := opts.PublicKeyOverride
	if key == "" {
		key = PublicKey
	}
	// The key is checked BEFORE any download: a binary that cannot verify
	// anything should not even start downloading. Only --check escapes,
	// because it swaps no binary at all.
	if !opts.CheckOnly {
		if err := validateKey(key); err != nil {
			return nil, err
		}
	}

	rel, err := resolveRelease(ctx, cli, channel, opts.Version)
	if err != nil {
		return nil, err
	}

	res := &Result{
		CurrentVersion: opts.CurrentVersion,
		RemoteVersion:  rel.Version,
		Channel:        channel,
	}

	explicit := opts.Version != ""
	res.Available = newerThan(rel.Version, opts.CurrentVersion)

	if opts.CheckOnly {
		return res, nil
	}

	if !explicit {
		// Without --version, it only moves forward. Never go back a
		// version by accident: if the channel's release is older (or
		// equal), the update is a no-op.
		if !res.Available {
			return res, nil
		}
	} else if sameVersion(rel.Version, opts.CurrentVersion) {
		// --version pointing at the already installed version: nothing to
		// do, and it is not an error.
		return res, nil
	}

	path := opts.BinaryPath
	if path == "" {
		path, err = os.Executable()
		if err != nil {
			return nil, output.Internal(err,
				"could not find the path of our own binary in order to replace it")
		}
		if resolved, errLink := filepath.EvalSymlinks(path); errLink == nil {
			path = resolved
		}
	}

	goos, goarch := opts.SO, opts.Arch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	artifact, err := rel.AssetForPlatform(goos, goarch)
	if err != nil {
		return nil, err
	}
	checksums, err := rel.AssetByName(ChecksumsName)
	if err != nil {
		return nil, err
	}
	signature, err := rel.AssetByName(SignatureName)
	if err != nil {
		return nil, err
	}

	artifactData, err := cli.Download(ctx, artifact.URL)
	if err != nil {
		return nil, err
	}
	checksumsData, err := cli.Download(ctx, checksums.URL)
	if err != nil {
		return nil, err
	}
	signatureData, err := cli.Download(ctx, signature.URL)
	if err != nil {
		return nil, err
	}

	if err := Verify(artifactData, checksumsData, signatureData, key, artifact.Name); err != nil {
		return nil, err
	}

	newBinary, err := ExtractBinary(artifact.Name, artifactData, goos)
	if err != nil {
		return nil, err
	}

	if err := Apply(path, newBinary); err != nil {
		return nil, err
	}

	res.Updated = true
	return res, nil
}

// validateKey explicitly refuses a binary with no key. There is no bypass
// path: no environment variable, flag or "no verification" mode.
func validateKey(key string) error {
	c := strings.TrimSpace(key)
	if c == "" {
		return newError(CodeNoPublicKey,
			"this binary was built without an embedded public verification key and "+
				"therefore cannot update itself: there is no way to prove the downloaded "+
				"release came from the project. Download the new version manually from "+
				"the releases page and check `checksums.txt` with minisign, or use an "+
				"official binary, which already ships with the key embedded")
	}
	if c == PublicKeyPlaceholder {
		return newError(CodePlaceholderKey,
			"the embedded public key is still the placeholder %q: no minisign key pair "+
				"has been generated for the project. Updating without real verification "+
				"is not an option", PublicKeyPlaceholder)
	}
	return nil
}

func resolveRelease(ctx context.Context, cli *Client, channel Channel, version string) (*Release, error) {
	if version != "" {
		return cli.ByVersion(ctx, version)
	}
	return cli.Latest(ctx, channel)
}

// normalizeVersion returns the version in the format golang.org/x/mod/semver
// expects (with "v"), or "" if it is not valid semver.
func normalizeVersion(v string) string {
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

// newerThan reports whether remote is newer than current. An unreadable current
// version (a local build without -ldflags, for example) counts as "any
// release is newer": the opposite would lock the update away from whoever
// needs it most.
func newerThan(remote, current string) bool {
	r := normalizeVersion(remote)
	if r == "" {
		return false
	}
	a := normalizeVersion(current)
	if a == "" {
		return true
	}
	return semver.Compare(r, a) > 0
}

func sameVersion(a, b string) bool {
	na, nb := normalizeVersion(a), normalizeVersion(b)
	if na == "" || nb == "" {
		return false
	}
	return semver.Compare(na, nb) == 0
}

func newError(code, format string, args ...any) *output.Error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     code,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

func wrapError(cause error, code, format string, args ...any) *output.Error {
	e := newError(code, format, args...)
	e.Err = cause
	return e
}
