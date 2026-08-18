// Package cli builds the command tree. Commands produce typed values and
// errors; formatting and the exit code are output's responsibility.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/settings"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/s0beran0/ngx/internal/update"
	"github.com/spf13/cobra"
)

// Default paths of ngx's own configuration file. Execute uses these values to
// fill Context.GlobalSettingsPath and Context.LocalSettingsPath; white-box
// tests that need isolation from the real filesystem override the Context
// fields instead of depending on these constants.
const (
	GlobalSettingsPath = "/etc/ngx/ngx.yaml"
	LocalSettingsPath  = ".ngx/config.yaml"
)

// GlobalFlags mirrors the global flags from the spec.
type GlobalFlags struct {
	ConfigPath   string
	JSON         bool
	Human        bool
	Quiet        bool
	NoColor      bool
	NginxBin     string
	NginxVersion string
	Timeout      time.Duration
	Profile      string
	NoRedact     bool

	// Remote access flags. Without Host none of them is used and the target
	// is the local machine — the usual behavior.
	Host            string
	User            string
	Port            int
	Key             string
	KnownHosts      string
	InsecureHostKey bool
	Sudo            bool
}

// Context carries what every command needs.
type Context struct {
	Flags    *GlobalFlags
	Settings *settings.Settings
	Renderer *output.Renderer
	Command  string

	// GlobalSettingsPath and LocalSettingsPath are the paths preparar passes
	// to settings.Load. Execute fills them with the package's
	// GlobalSettingsPath/LocalSettingsPath constants; keeping them in the
	// Context instead of hardcoded in preparar's body is what lets a test
	// isolate the loading of the settings from the real filesystem without
	// changing the cwd of the whole process.
	GlobalSettingsPath string
	LocalSettingsPath  string

	// Transport is the target of the operations: the local machine or a
	// remote host. preparar fills it; executar closes it, including on the
	// error path.
	Transport transport.Transport

	// TransportDiags holds what building the transport observed (an
	// --insecure-host-key warning, a missing ssh-agent, an unreadable
	// ~/.ssh/config). It lives in the Context because it has to reach the
	// envelope both on success and on error.
	TransportDiags []output.Diagnostic

	// SSHConfigPath is the ~/.ssh/config to consult. Empty means the user's
	// default path; a test points it at a fixture file so as not to depend on
	// the HOME of whoever runs the suite.
	SSHConfigPath string

	// ConectarSSH opens the remote connection. Empty means
	// transport.SSHWithDiagnostics.
	ConectarSSH ConectarSSH

	// Getenv reads an environment variable. Injectable so that a test does
	// not depend on the environment of whoever runs the suite -- nor
	// contaminate it.
	Getenv func(string) string
}

// Execute runs the CLI and returns the exit code. It never calls os.Exit:
// that is main's responsibility, which keeps the whole CLI testable.
func Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode {
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: stdout, IsTTY: isTTY},
		GlobalSettingsPath: GlobalSettingsPath,
		LocalSettingsPath:  LocalSettingsPath,
		Getenv:             os.Getenv,
	}

	root := NewRoot(ctx)
	return executar(root, ctx, args, stderr)
}

// executar dispatches the already-built command and translates the error into
// an exit code. It is separate from Execute so that a white-box test can
// inject a root with an extra command (for example, one that returns a typed
// error wrapped with %w) without duplicating the error normalization and
// envelope rendering logic.
func executar(root *cobra.Command, ctx *Context, args []string, stderr io.Writer) output.ExitCode {
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)

	// The transport always closes, including when the command fails: an SSH
	// connection left open survives the process only for as long as the
	// server's timeout, and in a test it becomes a leaking goroutine.
	defer func() {
		avisarFalhaAoFechar(stderr, ctx.fecharTransporte())
	}()

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// A command that already wrote its own envelope only wants the exit code.
	// That is the case of `test` with a rejected configuration: the result
	// went out whole, and rendering an error envelope on top would put two
	// JSON documents on stdout.
	var jaRenderizado *erroJaRenderizado
	if errors.As(err, &jaRenderizado) {
		return output.CodeOf(err)
	}

	// errors.As, not a direct type assertion: a command may return an
	// *output.Error wrapped with %w to attach context (the idiomatic pattern,
	// e.g. fmt.Errorf("while reading %s: %w", caminho,
	// output.InvalidConfig(...))). A direct assertion does not traverse the
	// wrapping — it would treat that error as raw and replace it with a
	// generic Usage, losing the original exit code and diagnostic. Cobra also
	// returns a raw error (with no type at all) for invalid flags and
	// commands; that is the only case where the substitution below should
	// happen.
	var e *output.Error
	if !errors.As(err, &e) {
		err = output.Usage("%s", err.Error())
	}

	renderErro(ctx, stderr, err)
	return output.CodeOf(err)
}

// renderErro draws the error envelope. ctx.Renderer is always built by Execute
// (or by the white-box test that assembles the Context), so it is never nil
// here.
func renderErro(ctx *Context, stderr io.Writer, err error) {
	env := ctx.NovoEnvelope(comandoDe(ctx))
	var e *output.Error
	if errors.As(err, &e) {
		env.AddDiagnostic(e.Diag)
		// The extras tell what happened BEFORE the failure -- which files
		// required privilege, for example. Losing them here would leave the
		// error envelope less informative than the success one, exactly when
		// the reader needs context the most.
		for _, d := range e.Extras {
			env.AddDiagnostic(d)
		}
	} else {
		env.AddDiagnostic(output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message:  err.Error(),
		})
	}

	r := ctx.Renderer
	// An error is never suppressed by --quiet nor blocked by the --no-redact
	// gate: the agent needs to know what went wrong.
	r.Quiet = false
	r.NoRedact = false
	if renderErr := r.Render(env); renderErr != nil {
		// Cobra is running with SilenceErrors; if rendering the error
		// envelope itself fails, the user cannot be left with an exit code
		// and zero bytes on every stream. This falls to stderr as a last
		// resort.
		fmt.Fprintln(stderr, renderErr)
	}
}

// NewRoot builds the root command with the global flags.
func NewRoot(ctx *Context) *cobra.Command {
	root := &cobra.Command{
		Use:           "ngx",
		Short:         "Operate nginx with structured output and transactional changes",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return preparar(ctx, cmd)
		},
	}

	f := ctx.Flags
	p := root.PersistentFlags()
	p.StringVarP(&f.ConfigPath, "config", "c", "", "nginx main configuration file")
	p.BoolVar(&f.JSON, "json", false, "force JSON output")
	p.BoolVar(&f.Human, "human", false, "force human-readable output")
	p.BoolVarP(&f.Quiet, "quiet", "q", false, "errors only")
	p.BoolVar(&f.NoColor, "no-color", false, "turn colors off")
	p.StringVar(&f.NginxBin, "nginx-bin", "", "path to the nginx binary")
	p.StringVar(&f.NginxVersion, "nginx-version", "", "assume this nginx version")
	p.DurationVar(&f.Timeout, "timeout", 30*time.Second, "timeout for the operations")
	p.StringVar(&f.Profile, "profile", "", "profile from ngx's configuration file")
	p.BoolVar(&f.NoRedact, "no-redact", false, "show sensitive values (terminal only)")
	registrarFlagsDeConexao(p, f)

	root.AddCommand(newVersionCmd(ctx))
	root.AddCommand(newInspectCmd(ctx))
	root.AddCommand(newTestCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	root.AddCommand(newUpdateCmd(ctx))
	return root
}

// contextoDeExecucao applies the global --timeout to an operation that runs
// something on the target. The cancel function is always returned and the
// caller always defers it: a timeout of zero (or negative, typed by mistake)
// cannot become an operation with no limit at all hanging on an SSH
// connection, so in that case the flag default applies.
func (c *Context) contextoDeExecucao(pai context.Context) (context.Context, context.CancelFunc) {
	if pai == nil {
		pai = context.Background()
	}
	if c.Flags == nil || c.Flags.Timeout <= 0 {
		return context.WithCancel(pai)
	}
	return context.WithTimeout(pai, c.Flags.Timeout)
}

func preparar(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags
	ctx.Command = cmd.Name()

	if f.JSON && f.Human {
		return output.Usage("--json and --human are mutually exclusive")
	}

	s, err := settings.Load(ctx.GlobalSettingsPath, ctx.LocalSettingsPath)
	if err != nil {
		// The cause (file path, raw error from the YAML parser) stays only in
		// the Err field of output.Internal, reachable via errors.Unwrap; the
		// diagnostic message must not leak internal detail to the agent.
		return output.Internal(err, "could not load the ngx configuration")
	}
	ctx.Settings = s

	// The format is validated here, right after loading the settings, and not
	// only inside Renderer.Render: output.format comes from a free-form YAML
	// configuration, and Render is only reached after the --quiet gate. If the
	// invalid value were caught only there, "ngx --quiet" with a bad format
	// would suppress the usage error itself and the user would have no sign of
	// the problem.
	formato := resolverFormato(f, s)
	if err := validarFormato(formato); err != nil {
		return err
	}

	set, err := output.NewRedactSet(s.Output.Redact)
	if err != nil {
		return output.Usage("%s", err.Error())
	}

	// An empty redact list turns redaction off through the settings file,
	// without going through --no-redact's terminal gate. It is a legitimate
	// path, but it cannot be silent: a `.ngx/config.yaml` relative to the cwd
	// is enough for an AI agent to start dumping secrets into the pipe with
	// nothing in the output indicating that the protection was turned off. The
	// warning is what keeps that from being invisible to consumers.
	if set.Empty() {
		ctx.TransportDiags = append(ctx.TransportDiags, output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     "NGX-0004",
			Message: "redaction is OFF: the output.redact list in the settings " +
				"file is empty, so sensitive values go out as they are. " +
				"This does not go through --no-redact's terminal gate",
		})
	}

	ctx.Renderer.Format = formato
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	// The transport is the last step of preparar: connecting before validating
	// the flags would charge an SSH handshake to whoever typed --json --human.
	return abrirTransporte(ctx, cmd)
}

func resolverFormato(f *GlobalFlags, s *settings.Settings) output.Format {
	switch {
	case f.JSON:
		return output.FormatJSON
	case f.Human:
		return output.FormatHuman
	default:
		return output.Format(s.Output.Format)
	}
}

// validarFormato rejects any format outside auto/json/human. The
// --json/--human flags only produce one of those values by construction; the
// only possible source of an invalid format in preparar is output.format from
// the configuration file.
func validarFormato(formato output.Format) error {
	switch formato {
	case output.FormatAuto, output.FormatJSON, output.FormatHuman, "":
		return nil
	default:
		return output.Usage(
			"invalid output.format in the configuration: %q (expected auto, json or human)",
			string(formato),
		)
	}
}

func newVersionCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the ngx version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			env := ctx.NovoEnvelope("version")
			dados := map[string]string{"version": output.Version}

			// The embedded public key goes out here for two reasons. Users
			// can check it against the project's published key before
			// trusting an `ngx update`. And the build can PROVE that
			// `-ldflags -X` worked: against a nonexistent symbol the linker
			// ignores it silently and the binary comes out with no key, but
			// the value still shows up in `strings` because Go records the
			// ldflags in the build info -- so only asking the running binary
			// tells the two cases apart.
			//
			// An unavailable field is omitted: a binary with no key does not
			// show the field, instead of showing it empty.
			if update.PublicKey != "" && update.PublicKey != update.PublicKeyPlaceholder {
				dados["update_public_key"] = update.PublicKey
			}

			env.Data = dados
			return ctx.Renderer.Render(env)
		},
	}
}
