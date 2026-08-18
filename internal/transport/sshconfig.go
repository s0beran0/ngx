package transport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"

	"github.com/s0beran0/ngx/internal/output"
)

// PortaSSHPadrao is the port used when neither the flag nor ~/.ssh/config says
// what it is. The parsing library does not apply defaults — (*Config).Get
// returns an empty string for a missing key, never "22" —, so the default is
// ours.
const PortaSSHPadrao = 22

// CodigoAvisoSSHConfig identifies the DR7 diagnostic: ~/.ssh/config exists but
// cannot be read in full.
//
// The NGX-000N codes mirror exit codes and are always errors. Warnings, which
// by definition do not bring the command down, use the NGX-W### range.
const CodigoAvisoSSHConfig = "NGX-0210"

// SSHOptions describes how ngx reaches a remote host. Host is the final target
// of the connection — if ~/.ssh/config translates the alias through HostName,
// it is the HostName that ends up here.
//
// A zero Port, and the empty strings, mean "not provided": that is how the
// resolution tells an absent flag from a deliberately empty one, and it is
// what makes --user "" not erase the User that came from the file.
type SSHOptions struct {
	Host            string
	Port            int
	User            string
	KeyPath         string
	Password        string
	KnownHostsPath  string
	InsecureHostKey bool
	Timeout         time.Duration
}

// posicaoDeErro matches the "(line, column): " prefix that
// kevinburke/ssh_config puts in the message. The library formats the position
// inside the error string and does not expose the Position in any public type:
// recovering the exact place of the problem is only possible by re-reading the
// message.
var posicaoDeErro = regexp.MustCompile(`^\((\d+), (\d+)\): (.*)$`)

// ResolverSSHConfig resolves the connection options for the requested host,
// applying the DR2 precedence: an explicit flag beats the file, the file beats
// the default.
//
// An empty flag is not a flag: it does not override the value from the file.
// Whoever did not pass --port keeps the Port from ~/.ssh/config, and only in
// its absence falls back to port 22.
//
// The file path is a parameter, and not derived from os.UserHomeDir() in here,
// so that the resolution is testable without depending on the HOME of whoever
// runs it. Use CaminhoSSHConfigPadrao for the production path.
//
// An absent file is neither an error nor a warning: whoever has no
// ~/.ssh/config simply falls back to the defaults. A file that exists and
// cannot be read in full, on the other hand, returns a warning-severity
// diagnostic (DR7) — the resolution goes on with flags and defaults, but never
// in silence.
//
// The list of diagnostics is never nil.
func ResolverSSHConfig(flags SSHOptions, caminhoConfig string) (SSHOptions, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	alias := strings.TrimSpace(flags.Host)
	if alias == "" {
		return SSHOptions{}, diags, output.Usage("no target host provided")
	}

	resolvido := flags
	resolvido.Host = alias

	cfg, aviso := carregarSSHConfig(caminhoConfig)
	if aviso != nil {
		diags = append(diags, *aviso)
	}

	if cfg != nil {
		diags = aplicarArquivo(&resolvido, cfg, alias, caminhoConfig, diags)
	}

	// Defaults: the last level of the precedence, applied over whatever is
	// still empty after flags and file.
	if resolvido.Port == 0 {
		resolvido.Port = PortaSSHPadrao
	}
	if resolvido.User == "" {
		resolvido.User = usuarioCorrente()
	}

	return resolvido, diags, nil
}

// CaminhoSSHConfigPadrao returns ~/.ssh/config. filepath.Join uses the native
// separator, so the same code produces /home/x/.ssh/config and
// C:\Users\x\.ssh\config.
func CaminhoSSHConfigPadrao() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not locate the user's home directory; "+
			"pass --host, --user, --port and --key explicitly: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// carregarSSHConfig reads and parses the file. It returns (nil, nil) when the
// file does not exist — absence is normal — and (nil, warning) when it exists
// but cannot be read or parsed.
func carregarSSHConfig(caminho string) (*ssh_config.Config, *output.Diagnostic) {
	if caminho == "" {
		return nil, nil
	}

	f, err := os.Open(caminho)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		d := avisoSSHConfig(caminho, 0, 0, err.Error())
		return nil, &d
	}
	defer f.Close()

	cfg, err := ssh_config.Decode(f)
	if err != nil {
		linha, coluna, motivo := posicaoDoErro(err)
		d := avisoSSHConfig(caminho, linha, coluna, motivo)
		return nil, &d
	}
	return cfg, nil
}

// aplicarArquivo fills the fields the flag left empty with what the file says
// for that alias.
func aplicarArquivo(
	opts *SSHOptions,
	cfg *ssh_config.Config,
	alias, caminho string,
	diags []output.Diagnostic,
) []output.Diagnostic {
	ler := func(chave string) string {
		v, err := cfg.Get(alias, chave)
		if err != nil {
			diags = append(diags, avisoSSHConfig(caminho, 0, 0,
				fmt.Sprintf("could not read %s: %v", chave, err)))
			return ""
		}
		return strings.TrimSpace(v)
	}

	// HostName does not compete with --host: --host gives the alias to look
	// up in the file, and HostName is the translation of that alias into
	// the real target. It is the same thing ssh does when `ssh web1`
	// connects to 10.0.0.1.
	if hostName := ler("HostName"); hostName != "" {
		opts.Host = hostName
	}

	if opts.User == "" {
		opts.User = ler("User")
	}

	if opts.Port == 0 {
		if porta := ler("Port"); porta != "" {
			n, err := strconv.Atoi(porta)
			if err != nil || n <= 0 || n > 65535 {
				diags = append(diags, avisoSSHConfig(caminho, 0, 0,
					fmt.Sprintf("Port %q is not a valid port number; using %d", porta, PortaSSHPadrao)))
			} else {
				opts.Port = n
			}
		}
	}

	if opts.KeyPath == "" {
		if key := ler("IdentityFile"); key != "" {
			opts.KeyPath = expandirTil(key)
		}
	}

	return diags
}

// avisoSSHConfig builds the DR7 diagnostic. The message says what was not read
// and what still holds, because a warning that only says "it failed" leads
// whoever consumes the output to think the host was not in the file — and the
// cause is another one.
func avisoSSHConfig(caminho string, linha, coluna int, motivo string) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoSSHConfig,
		Message: fmt.Sprintf(
			"%s could not be read (%s); the ssh_config resolution was skipped and only "+
				"the explicit flags (--host, --user, --port, --key) and the defaults "+
				"(port %d, current user) apply",
			caminho, motivo, PortaSSHPadrao),
		File:   caminho,
		Line:   linha,
		Column: coluna,
	}
}

// posicaoDoErro splits the position from the message. An error with no
// position returns zeros, and Diagnostic omits the fields.
func posicaoDoErro(err error) (linha, coluna int, motivo string) {
	msg := err.Error()
	m := posicaoDeErro.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0, msg
	}
	linha, _ = strconv.Atoi(m[1])
	coluna, _ = strconv.Atoi(m[2])
	return linha, coluna, m[3]
}

// expandirTil resolves "~/" against the user's home directory. A tilde that
// cannot be resolved is left as it is: it is better to return the literal
// path, which fails visibly on open, than to invent a directory.
func expandirTil(caminho string) string {
	if caminho != "~" && !strings.HasPrefix(caminho, "~/") && !strings.HasPrefix(caminho, `~\`) {
		return caminho
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return caminho
	}
	if caminho == "~" {
		return home
	}
	return filepath.Join(home, filepath.FromSlash(caminho[2:]))
}

// usuarioCorrente returns the OS user. os/user.Current works without cgo (Go
// falls back to a pure reader of /etc/passwd), but it can fail in a container
// with no user entry; there the environment variables are the last attempt.
func usuarioCorrente() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return nomeSemDominio(u.Username)
	}
	for _, env := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(env); v != "" {
			return nomeSemDominio(v)
		}
	}
	return ""
}

// nomeSemDominio strips the domain prefix Windows puts in Username
// ("DOMAIN\user"): SSH wants only the name.
func nomeSemDominio(nome string) string {
	if i := strings.LastIndex(nome, `\`); i >= 0 {
		return nome[i+1:]
	}
	return nome
}
