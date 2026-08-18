package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ConectarSSH opens a remote transport and returns, along with it, what
// building it observed on the way — a host key accepted without verification,
// an unavailable ssh-agent, an unreadable key.
//
// It is a Context field, and not a direct call to transport, for the same
// reason GlobalSettingsPath is a field: a CLI test needs to exercise the flag
// wiring without opening a socket. In production the value is always
// transport.SSHComDiagnosticos.
type ConectarSSH func(transport.SSHOptions) (transport.Transport, []output.Diagnostic, error)

// flagsDeConexao are the flags that only make sense with --host. Passing any
// of them without a destination is a usage error, not a value silently
// ignored: whoever typed --user deploy without --host believes the connection
// is going to use that user.
//
// --sudo is left out on purpose: explicit privilege (DR5) applies to the local
// target too.
var flagsDeConexao = []string{"host", "user", "port", "key", "known-hosts", "insecure-host-key"}

// registrarFlagsDeConexao adds the global remote access flags.
//
// There is no password flag, and that is a security decision, not an
// oversight: a flag's value shows up in `ps`, in the shell history and in the
// log of any CI. The secret comes from NGX_SSH_PASSWORD or from a prompt with
// no echo, both handled inside transport.MontarAutenticacao.
//
// --port starts at 0, and not at 22, because zero is what distinguishes "not
// given" from "given as 22". DR2's precedence depends on that distinction: an
// explicit flag beats ~/.ssh/config, which beats the default.
func registrarFlagsDeConexao(p *pflag.FlagSet, f *GlobalFlags) {
	p.StringVar(&f.Host, "host", "", "operate on a remote host over SSH (~/.ssh/config alias or address)")
	p.StringVar(&f.User, "user", "", "SSH user")
	p.IntVar(&f.Port, "port", 0, "SSH port")
	p.StringVar(&f.Key, "key", "", "path to the private key")
	p.StringVar(&f.KnownHosts, "known-hosts", "", "path to the known_hosts file")
	p.BoolVar(&f.InsecureHostKey, "insecure-host-key", false,
		"accept the host key without verifying it (insecure; emits a warning in the output)")
	p.BoolVar(&f.Sudo, "sudo", false, "escalate privilege on the commands that require it")
}

// abrirTransporte decides the target of the execution and stores it in the
// Context.
//
// Without --host the path is the usual one: local transport, no ~/.ssh/config
// resolution, no socket. All of v0.1 is local use — a regression here would
// break what already works in order to serve what nobody uses yet.
//
// The diagnostics stay in the Context, and are not returned only to the
// caller, because they need to reach the envelope both on the success path and
// on the error one. An --insecure-host-key warning that vanishes from the
// output makes DR1's escape hatch silent, which is exactly what the decision
// exists to prevent.
func abrirTransporte(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags

	if f.Host == "" {
		if err := recusarFlagsDeConexaoSemHost(cmd); err != nil {
			return err
		}
		ctx.Transport = transport.Local()
		return nil
	}

	opts := transport.SSHOptions{
		Host:            f.Host,
		Port:            f.Port,
		User:            f.User,
		KeyPath:         f.Key,
		KnownHostsPath:  f.KnownHosts,
		InsecureHostKey: f.InsecureHostKey,
		Timeout:         f.Timeout,
		// Password is left empty on purpose: transport.MontarAutenticacao
		// reads NGX_SSH_PASSWORD or asks on the terminal. No secret crosses
		// the command line.
	}

	caminhoConfig, diagCaminho := caminhoSSHConfig(ctx)
	if diagCaminho != nil {
		ctx.TransportDiags = append(ctx.TransportDiags, *diagCaminho)
	}

	// DR2's precedence belongs entirely to transport: an explicit flag beats
	// ~/.ssh/config, which beats the default. Reimplementing it here would
	// create a second source of truth that can disagree with the first.
	resolvido, diags, err := transport.ResolverSSHConfig(opts, caminhoConfig)
	ctx.TransportDiags = append(ctx.TransportDiags, diags...)
	if err != nil {
		return err
	}

	tr, diagsConexao, err := ctx.conectar()(resolvido)
	ctx.TransportDiags = append(ctx.TransportDiags, diagsConexao...)
	if err != nil {
		return err
	}

	ctx.Transport = tr
	return nil
}

// recusarFlagsDeConexaoSemHost turns what would be a silent surprise into a
// usage error. It uses Changed, and not the value, so as to also catch
// --user "" and --port 0 typed explicitly.
func recusarFlagsDeConexaoSemHost(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	for _, nome := range flagsDeConexao {
		if nome == "host" {
			continue
		}
		if flag := cmd.Flags().Lookup(nome); flag != nil && flag.Changed {
			return output.Usage("--%s only makes sense together with --host", nome)
		}
	}
	return nil
}

// caminhoSSHConfig returns the file to consult. A Context with the field
// filled in wins — that is what allows testing the resolution without
// depending on the HOME of whoever runs the tests.
//
// Failing to locate the user's directory does not abort the connection: the
// resolution goes on with flags and defaults, and the warning (DR7) says why
// ~/.ssh/config was not consulted. Aborting would break whoever passed --host,
// --user and --port explicitly and does not need the file at all.
func caminhoSSHConfig(ctx *Context) (string, *output.Diagnostic) {
	if ctx.SSHConfigPath != "" {
		return ctx.SSHConfigPath, nil
	}

	caminho, err := transport.CaminhoSSHConfigPadrao()
	if err != nil {
		return "", &output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     transport.CodigoAvisoSSHConfig,
			Message: fmt.Sprintf(
				"~/.ssh/config was not consulted (%v); only the flags and the defaults apply",
				err,
			),
		}
	}
	return caminho, nil
}

// conectar returns the connector to use. The production default lives here,
// and not in Execute, so that a Context assembled by hand by a test about
// another subject keeps working.
//
// It is SSHComDiagnosticos, never SSH: the latter discards the host key and
// ssh-agent diagnostics, and a lost warning is a warning that does not exist.
func (c *Context) conectar() ConectarSSH {
	if c.ConectarSSH != nil {
		return c.ConectarSSH
	}
	return transport.SSHComDiagnosticos
}

// transporte returns the target of the operations, falling back to the local
// one when the Context was assembled without going through preparar (tests
// about other subjects).
func (c *Context) transporte() transport.Transport {
	if c.Transport == nil {
		return transport.Local()
	}
	return c.Transport
}

// NovoEnvelope creates the command's envelope already with the target in meta
// and with the connection diagnostics inside.
//
// Every command builds its output through here instead of calling output.New
// directly: target and connection warnings apply to any command, and what
// every command has to remember to do, some command forgets.
func (c *Context) NovoEnvelope(comando string) *output.Envelope {
	env := output.New(comando)
	if c.Transport != nil {
		env.Meta.Target = c.Transport.Describe()
	}
	for _, d := range c.TransportDiags {
		env.AddDiagnostic(d)
	}
	return env
}

// NovoRuntime builds the runtime on top of the context's transport.
//
// ComSudo carries the --sudo flag directly: without it, a command that needs
// privilege is reported, never retried with sudo (DR5).
// TransporteDeLeitura returns the transport the commands use to READ
// configuration, already with privileged reading when --sudo was asked for.
//
// The escalation is minimal: each file is tried first as the connection user,
// and only what is refused for permission is retried with sudo. On a
// configuration where one file out of 132 is restricted -- measured on a real
// production nginx --, 131 keep being read with no privilege at all.
//
// Without --sudo it returns the raw transport: DR5 requires privilege to be
// asked for, never inferred.
func (c *Context) TransporteDeLeitura(ctx context.Context) transport.Transport {
	sudo := c.Flags != nil && c.Flags.Sudo
	return transport.ComLeituraPrivilegiadaEDump(ctx, c.transporte(), sudo, c.dumpDeFallback)
}

// dumpDeFallback delivers the effective configuration via `nginx -T`, the last
// resort for reading. On a hardened server the sudoers file allows specific
// commands -- typically nginx -- and refuses a generic `cat`; there this is
// the only path that works.
func (c *Context) dumpDeFallback(ctx context.Context) (map[string][]byte, error) {
	d, err := c.NovoRuntime().DumpConfig(ctx)
	if err != nil {
		return nil, err
	}
	arquivos := make(map[string][]byte, len(d.Files))
	for _, f := range d.Files {
		arquivos[f.Path] = []byte(f.Content)
	}
	return arquivos, nil
}

// DiagnosticosDeLeitura collects what the reading transport observed -- which
// paths required privilege, which did not open even with it. Reading a
// server's configuration with sudo cannot happen silently.
func DiagnosticosDeLeitura(tr transport.Transport) []output.Diagnostic {
	return transport.Diagnosticos(tr)
}

func (c *Context) NovoRuntime() *runtime.Runtime {
	if c.Flags == nil {
		return runtime.New(c.transporte())
	}
	return runtime.New(c.transporte(),
		runtime.ComBinario(c.Flags.NginxBin),
		runtime.ComSudo(c.Flags.Sudo),
	)
}

// fecharTransporte releases the connection. Calling it twice is safe by the
// Transport contract, and the field is zeroed so that a reused Context does
// not point at a dead transport.
func (c *Context) fecharTransporte() error {
	if c.Transport == nil {
		return nil
	}
	tr := c.Transport
	c.Transport = nil
	return tr.Close()
}

// avisarFalhaAoFechar is the last resort for a Close that failed after the
// envelope had already been written. The envelope is immutable by then, and a
// connection that did not close cleanly does not change the command's result —
// but it also cannot disappear.
func avisarFalhaAoFechar(stderr io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(stderr, "ngx: failed to close the connection: %v\n", err)
}
