// Package runtime operates the nginx of the target: it discovers which binary
// exists and how it was compiled, runs `nginx -t` in a structured way and
// reads the effective configuration with `nginx -T`.
//
// Nothing here executes anything on its own. Every invocation goes through a
// transport.Transport, so the same code -- and the same test -- holds for the
// local machine and for a remote host over SSH. The parsers in this package
// do not know where the bytes came from, and that ignorance is deliberate: it
// is what guarantees that a remote read is not a second code path, with a
// second set of defects.
//
// Two invariants run through the package:
//
//   - A non-zero exit code is a result, not an error. A `nginx -t` that
//     rejects the configuration returns a TestResult with OK false and a nil
//     err. An error is the binary not existing, the connection dropping or
//     the command requiring privilege that was not granted.
//
//   - An unavailable field is omitted, never estimated. Consumers handle a
//     missing field far better than a wrong number.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

// Runtime diagnostic codes. The 0200-0299 range belongs to the transport and
// to SSH; the runtime uses 0220 onwards, inside that range, because
// everything it reports is born from a command executed on the target.
//
// Severity never goes into the code: the Diagnostic already has a severity
// field, and repeating it in the prefix would create two sources of truth.
const (
	// CodeNginxMissing: there is no nginx binary on the target, or it is
	// not executable. Distinct from "nginx ran and rejected".
	CodeNginxMissing = "NGX-0220"

	// CodePrivilegeRequired: the command exists and ran, but nginx
	// could not read what it needed for lack of permission. Without --sudo
	// ngx reports and stops -- it does not retry the command with sudo
	// (DR5).
	CodePrivilegeRequired = "NGX-0221"

	// CodeSudoUnavailable: --sudo was requested, but the target's sudo
	// requires a password, requires a terminal or does not exist. Since ngx
	// runs with no shell and no TTY, there is nowhere to send the password.
	CodeSudoUnavailable = "NGX-0222"

	// CodeUnrecognizedOutput: the command ran, but the output does not
	// have the expected format. Inventing fields out of output that was not
	// understood is worse than admitting it was not understood.
	CodeUnrecognizedOutput = "NGX-0223"

	// CodeConfigTest: a diagnostic translated from a line of `nginx -t`
	// or `nginx -T`. A single code for every level: the level becomes
	// severity, it does not become a code.
	CodeConfigTest = "NGX-0224"

	// CodeProcessState: something about the state of the process -- the
	// evidence that it is not running, or the reason why it could not be
	// determined. An omitted field without this diagnostic alongside it
	// would be degrading in silence.
	CodeProcessState = "NGX-0225"
)

// DefaultBinary is what ngx executes when nobody says otherwise. A plain
// name, resolved by the target's PATH: an absolute path guessed here would be
// wrong on half of the distributions.
const DefaultBinary = "nginx"

// Runtime executes the nginx of a target through a Transport.
type Runtime struct {
	tr     transport.Transport
	binary string
	sudo   bool
}

// Option configures a Runtime at construction time.
type Option func(*Runtime)

// WithBinary swaps the invoked binary. Useful when nginx is not on the
// target's PATH or when there is more than one installation.
func WithBinary(path string) Option {
	return func(r *Runtime) {
		if path != "" {
			r.binary = path
		}
	}
}

// WithSudo turns on the explicit privilege escalation (DR5). Without it, a
// command that needs privilege is reported, never retried with sudo.
func WithSudo(enabled bool) Option {
	return func(r *Runtime) { r.sudo = enabled }
}

// New assembles the runtime on top of a transport.
func New(tr transport.Transport, opts ...Option) *Runtime {
	r := &Runtime{tr: tr, binary: DefaultBinary}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Target identifies what this runtime operates against, for the envelope's
// meta.
func (r *Runtime) Target() string { return r.tr.Describe() }

// argv assembles the command line. No shell, no interpolation: each argument
// is one element of the list.
//
// sudo goes with -n (non-interactive) because ngx runs with no TTY. A sudo
// that decided to ask for a password would hang until the context timed out,
// and the operator would see an unexplainable slowness instead of a clear
// refusal.
func (r *Runtime) argv(args ...string) []string {
	var out []string
	if r.sudo {
		out = append(out, "sudo", "-n")
	}
	out = append(out, r.binary)
	return append(out, args...)
}

// execution is the raw result of an nginx invocation that ran to completion.
type execution struct {
	argv   []string
	stdout string
	stderr string
	exit   int
}

// combinedOutput returns stderr concatenated with stdout. nginx writes
// diagnostics to stderr, but transports that merge both channels exist, and a
// parser that only looked at one of them would fail silently in those cases.
func (e *execution) combinedOutput() string {
	if e.stderr == "" {
		return e.stdout
	}
	if e.stdout == "" {
		return e.stderr
	}
	return e.stderr + "\n" + e.stdout
}

// run runs nginx with the given arguments and classifies what prevents a
// result from existing: missing binary, sudo unavailable, missing privilege
// and transport failure. A non-zero exit code for any other reason comes back
// as an execution, with a nil err -- it is a result.
func (r *Runtime) run(ctx context.Context, args ...string) (*execution, error) {
	argv := r.argv(args...)
	stdout, stderr, exit, err := r.tr.Run(ctx, argv)

	e := &execution{
		argv:   argv,
		stdout: string(stdout),
		stderr: string(stderr),
		exit:   exit,
	}

	if err != nil {
		if missingBinary(err) {
			return nil, nginxMissingError(r, e, err)
		}
		return nil, output.Internal(err,
			"failed to execute %s on %s", strings.Join(argv, " "), r.tr.Describe())
	}

	if exit == 0 {
		return e, nil
	}

	text := e.combinedOutput()

	if r.sudo && sudoUnavailable(text) {
		return nil, sudoUnavailableError(r, e)
	}
	if notFoundInOutput(exit, text) {
		return nil, nginxMissingError(r, e, nil)
	}
	if requiresPrivilege(text) {
		return nil, privilegeError(r, e)
	}

	return e, nil
}

// missingBinary recognizes the transport failure meaning "that program does
// not exist". The local transport returns exec.ErrNotFound for a PATH name
// and an fs.ErrNotExist for an absolute path.
func missingBinary(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// notFoundInOutput recognizes the same case coming from a remote shell,
// where a missing binary is not a transport error: ssh executes, the target's
// shell answers 127 and writes the complaint to stderr.
func notFoundInOutput(exit int, text string) bool {
	if exit != 127 {
		return false
	}
	t := strings.ToLower(text)
	return strings.Contains(t, "command not found") ||
		strings.Contains(t, "no such file or directory") ||
		strings.Contains(t, "not found")
}

var privilegePatterns = []string{
	"permission denied",
	"operation not permitted",
	"must be run as root",
	"you must be root",
	"are you root",
}

// requiresPrivilege decides whether the output says "permission was missing".
// It is deliberately conservative: recognizing too little makes the user see
// the raw nginx message, which is still the truth; recognizing too much would
// turn a syntax error into a privilege request, which is a lie.
func requiresPrivilege(text string) bool {
	t := strings.ToLower(text)
	for _, p := range privilegePatterns {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

var sudoPatterns = []string{
	"sudo: a password is required",
	"a terminal is required",
	"no tty present",
	"is not in the sudoers file",
	"sudo: command not found",
	"sudo: not found",
}

func sudoUnavailable(text string) bool {
	t := strings.ToLower(text)
	for _, p := range sudoPatterns {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// privilegedCommand returns the exact line the operator would have to
// authorize. DR5 requires saying what the command is: "needs privilege"
// without the command forces the reader to guess, and guessing in production
// is how privilege gets escalated by mistake.
func privilegedCommand(argv []string) string {
	if len(argv) > 0 && argv[0] == "sudo" {
		return strings.Join(argv, " ")
	}
	return "sudo -n " + strings.Join(argv, " ")
}

func privilegeError(r *Runtime, e *execution) error {
	var msg string
	if r.sudo {
		msg = fmt.Sprintf(
			"the command `%s` ran with --sudo on %s and still did not have "+
				"permission to read the configuration. nginx output: %s",
			strings.Join(e.argv, " "), r.tr.Describe(), summarize(e.combinedOutput()))
	} else {
		msg = fmt.Sprintf(
			"the command `%s` requires privilege on %s and ngx does not escalate on "+
				"its own: retry with --sudo, which runs `%s`. nginx output: %s",
			strings.Join(e.argv, " "), r.tr.Describe(),
			privilegedCommand(e.argv), summarize(e.combinedOutput()))
	}
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodePrivilegeRequired,
			Message:  msg,
		},
	}
}

func sudoUnavailableError(r *Runtime, e *execution) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeSudoUnavailable,
			Message: fmt.Sprintf(
				"--sudo was requested, but the sudo on %s cannot be used without interaction: %s. "+
					"ngx runs with no shell and no terminal, so there is nowhere to type a password; "+
					"allow the command in sudoers or run ngx as a user that already has "+
					"read access to the configuration",
				r.tr.Describe(), summarize(e.combinedOutput())),
		},
	}
}

func nginxMissingError(r *Runtime, e *execution, cause error) error {
	err := &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeNginxMissing,
			Message: fmt.Sprintf(
				"there is no executable nginx on %s: `%s` cannot be executed. "+
					"If the binary exists under another name or outside the PATH, give the path",
				r.tr.Describe(), strings.Join(e.argv, " ")),
		},
		Err: cause,
	}
	return err
}

func unrecognizedOutputError(r *Runtime, e *execution, what string) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodeUnrecognizedOutput,
			Message: fmt.Sprintf(
				"the output of `%s` on %s does not have the expected format (%s): %s",
				strings.Join(e.argv, " "), r.tr.Describe(), what, summarize(e.combinedOutput())),
		},
	}
}

var whitespace = regexp.MustCompile(`\s+`)

// summarize condenses a multi-line output into a short single line, so it fits
// in a diagnostic message without destroying the readability of the JSON.
func summarize(text string) string {
	t := strings.TrimSpace(whitespace.ReplaceAllString(text, " "))
	if t == "" {
		return "(no output)"
	}
	const limit = 300
	if len(t) > limit {
		return t[:limit] + "..."
	}
	return t
}
