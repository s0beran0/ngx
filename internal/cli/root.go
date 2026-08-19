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
	ConfigPath string
	JSON       bool
	Human      bool

	// Format is --format: the long form of --json/--human plus the two
	// formats that have no shorthand, nginx and table. It is a separate
	// field from JSON/Human because it also has to be told apart from "not
	// given" -- an empty value falls back to output.format in the settings
	// file, and "auto" typed explicitly does not.
	Format string

	Quiet        bool
	NoColor      bool
	NginxBin     string
	NginxVersion string
	Timeout      time.Duration
	Profile      string
	NoRedact     bool

	// Field is the dot path of --field: it prints a single value from the
	// envelope, raw, instead of the envelope itself.
	Field string

	// Query is the jq expression of --query: it applies the expression to
	// the envelope and prints one line per result. jq is not a dependency
	// -- the evaluator is embedded -- because jq was not installed on the
	// host this project was validated against.
	Query string

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

	// GlobalSettingsPath and LocalSettingsPath are the paths prepare passes
	// to settings.Load. Execute fills them with the package's
	// GlobalSettingsPath/LocalSettingsPath constants; keeping them in the
	// Context instead of hardcoded in prepare's body is what lets a test
	// isolate the loading of the settings from the real filesystem without
	// changing the cwd of the whole process.
	GlobalSettingsPath string
	LocalSettingsPath  string

	// Transport is the target of the operations: the local machine or a
	// remote host. prepare fills it; execute closes it, including on the
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

	// SSHConnector opens the remote connection. Empty means
	// transport.SSHWithDiagnostics.
	SSHConnector SSHConnector

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
	return execute(root, ctx, args, stderr)
}

// execute dispatches the already-built command and translates the error into
// an exit code. It is separate from Execute so that a white-box test can
// inject a root with an extra command (for example, one that returns a typed
// error wrapped with %w) without duplicating the error normalization and
// envelope rendering logic.
func execute(root *cobra.Command, ctx *Context, args []string, stderr io.Writer) output.ExitCode {
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)

	// The transport always closes, including when the command fails: an SSH
	// connection left open survives the process only for as long as the
	// server's timeout, and in a test it becomes a leaking goroutine.
	defer func() {
		warnCloseFailure(stderr, ctx.closeTransport())
	}()

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// A command that already wrote its own envelope only wants the exit code.
	// That is the case of `test` with a rejected configuration: the result
	// went out whole, and rendering an error envelope on top would put two
	// JSON documents on stdout.
	var alreadyRendered *alreadyRenderedError
	if errors.As(err, &alreadyRendered) {
		return output.CodeOf(err)
	}

	// errors.As, not a direct type assertion: a command may return an
	// *output.Error wrapped with %w to attach context (the idiomatic pattern,
	// e.g. fmt.Errorf("while reading %s: %w", path,
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

	renderError(ctx, stderr, err)
	return output.CodeOf(err)
}

// renderError draws the error envelope. ctx.Renderer is always built by Execute
// (or by the white-box test that assembles the Context), so it is never nil
// here.
func renderError(ctx *Context, stderr io.Writer, err error) {
	env := ctx.NewEnvelope(commandOf(ctx))
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

	// nginx text and TSV cannot carry a failure: the error envelope has no
	// configuration and no rows, so the requested format would refuse it and
	// the refusal would REPLACE the real diagnostic -- the caller would read
	// "this output is not nginx text" instead of "no tree was read". The
	// error falls back to the ordinary presentation, which is the only one
	// that can hold a diagnostic.
	if r.Format == output.FormatNginx || r.Format == output.FormatTable {
		r.Format = output.FormatAuto
	}
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
			return prepare(ctx, cmd)
		},
	}

	f := ctx.Flags
	p := root.PersistentFlags()
	p.StringVarP(&f.ConfigPath, "config", "c", "", "nginx main configuration file")
	p.BoolVar(&f.JSON, "json", false, "force JSON output")
	p.BoolVar(&f.Human, "human", false, "force human-readable output")
	p.StringVar(&f.Format, "format", "",
		"output format: auto, json, human, nginx (the configuration text) or table (TSV, flat results only)")
	p.BoolVarP(&f.Quiet, "quiet", "q", false, "errors only")
	p.BoolVar(&f.NoColor, "no-color", false, "turn colors off")
	p.StringVar(&f.NginxBin, "nginx-bin", "", "path to the nginx binary")
	p.StringVar(&f.NginxVersion, "nginx-version", "", "assume this nginx version")
	p.DurationVar(&f.Timeout, "timeout", 30*time.Second, "timeout for the operations")
	p.StringVar(&f.Profile, "profile", "", "profile from ngx's configuration file")
	p.BoolVar(&f.NoRedact, "no-redact", false, "show sensitive values (terminal only)")
	p.StringVar(&f.Field, "field", "", "print a single value from the envelope, by dot path (e.g. data.nginx.version)")
	p.StringVar(&f.Query, "query", "", "apply a jq expression to the envelope, one line per result (e.g. '.data.config[].path')")
	registerConnectionFlags(p, f)

	root.AddCommand(newVersionCmd(ctx))
	root.AddCommand(newInspectCmd(ctx))
	root.AddCommand(newTestCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	root.AddCommand(newUpdateCmd(ctx))
	return root
}

// executionContext applies the global --timeout to an operation that runs
// something on the target. The cancel function is always returned and the
// caller always defers it: a timeout of zero (or negative, typed by mistake)
// cannot become an operation with no limit at all hanging on an SSH
// connection, so in that case the flag default applies.
func (c *Context) executionContext(pai context.Context) (context.Context, context.CancelFunc) {
	if pai == nil {
		pai = context.Background()
	}
	if c.Flags == nil || c.Flags.Timeout <= 0 {
		return context.WithCancel(pai)
	}
	return context.WithTimeout(pai, c.Flags.Timeout)
}

func prepare(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags
	ctx.Command = cmd.Name()

	if f.JSON && f.Human {
		return output.Usage("--json and --human are mutually exclusive")
	}

	// --format is the same choice as --json/--human, spelled in full. Two
	// spellings of it on the same command line have no coherent winner, and
	// picking one silently would make the other flag look broken.
	if f.Format != "" {
		switch {
		case f.JSON:
			return output.Usage("--format and --json are mutually exclusive")
		case f.Human:
			return output.Usage("--format and --human are mutually exclusive")
		}
	}

	// --field and --query both take something OUT of the envelope, each in
	// its own shape, and there is no coherent answer to being asked for two
	// projections of the same output at once. Whichever won silently would
	// make the other flag look broken.
	if f.Field != "" && f.Query != "" {
		return output.Usage("--field and --query are mutually exclusive")
	}

	// The flags that choose how the envelope is presented conflict with
	// asking for a projection of it: --json and --human have no coherent
	// answer to "one value and the whole envelope", and --quiet would
	// suppress exactly what was asked for. The check is here, on the flags,
	// and not in the renderer: output.format also comes from the
	// configuration file, where it is an ambient default that --field and
	// --query legitimately override.
	for _, sel := range []struct{ flag, value string }{
		{"--field", f.Field},
		{"--query", f.Query},
	} {
		if sel.value == "" {
			continue
		}
		switch {
		case f.JSON:
			return output.Usage("%s and --json are mutually exclusive", sel.flag)
		case f.Human:
			return output.Usage("%s and --human are mutually exclusive", sel.flag)
		case f.Format != "":
			return output.Usage("%s and --format are mutually exclusive", sel.flag)
		case f.Quiet:
			return output.Usage("%s and --quiet are mutually exclusive", sel.flag)
		}
	}

	// The expression is parsed BEFORE the renderer is told about it, and
	// before the transport opens. Before the transport, so a typo does not
	// cost an SSH handshake; before the renderer, so the refusal is not
	// filtered through the very expression that is broken -- it would come
	// out of renderQuery's error path with no envelope at all, instead of
	// the usage envelope on stdout that every other refusal produces.
	if f.Query != "" {
		if err := output.ValidateQuery(f.Query); err != nil {
			return err
		}
	}

	// The renderer learns about --field/--query before anything else can
	// fail: whatever prepare rejects from here on is rendered through the
	// projection too, so the flags also work when the failure happens
	// before the command runs. The refusals above are the exception, on
	// purpose -- they are about the flags themselves, and filtering them
	// through the rejected combination would hide the reason for the
	// refusal.
	ctx.Renderer.Field = f.Field
	ctx.Renderer.Query = f.Query

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
	format := resolveFormat(f, s)
	if err := validateFormat(format); err != nil {
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

	ctx.Renderer.Format = format
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	// The transport is the last step of prepare: connecting before validating
	// the flags would charge an SSH handshake to whoever typed --json --human.
	return openTransport(ctx, cmd)
}

func resolveFormat(f *GlobalFlags, s *settings.Settings) output.Format {
	switch {
	case f.Format != "":
		return output.Format(f.Format)
	case f.JSON:
		return output.FormatJSON
	case f.Human:
		return output.FormatHuman
	default:
		return output.Format(s.Output.Format)
	}
}

// validateFormat rejects any format outside auto/json/human/nginx/table. The
// --json/--human flags only produce a valid value by construction; the two
// sources that can be wrong are --format, typed by hand, and output.format
// from the configuration file, which is free-form YAML.
//
// Both are rejected here, before the transport opens and before the --quiet
// gate in the renderer: a bad format with --quiet would otherwise suppress
// the very error that explains why nothing came out.
func validateFormat(format output.Format) error {
	switch format {
	case output.FormatAuto, output.FormatJSON, output.FormatHuman,
		output.FormatNginx, output.FormatTable, "":
		return nil
	default:
		return output.Usage(
			"invalid output format: %q (expected auto, json, human, nginx or table)",
			string(format),
		)
	}
}

func newVersionCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the ngx version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			env := ctx.NewEnvelope("version")
			data := map[string]string{
				"version": output.Version,
				// Always present, never omitted: "I do not know how I was
				// installed" is not a state that can exist, because the
				// variable has a default. Reporting it is what lets a caller
				// find out that `ngx update` will refuse BEFORE running it,
				// and what lets a packager check their build did what they
				// meant.
				"install_channel": update.InstallChannel,
			}

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
				data["update_public_key"] = update.PublicKey
			}

			env.Data = data
			return ctx.Renderer.Render(env)
		},
	}
}
