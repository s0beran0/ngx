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
	// CodigoNginxAusente: there is no nginx binary on the target, or it is
	// not executable. Distinct from "nginx ran and rejected".
	CodigoNginxAusente = "NGX-0220"

	// CodigoPrivilegioNecessario: the command exists and ran, but nginx
	// could not read what it needed for lack of permission. Without --sudo
	// ngx reports and stops -- it does not retry the command with sudo
	// (DR5).
	CodigoPrivilegioNecessario = "NGX-0221"

	// CodigoSudoIndisponivel: --sudo was requested, but the target's sudo
	// requires a password, requires a terminal or does not exist. Since ngx
	// runs with no shell and no TTY, there is nowhere to send the password.
	CodigoSudoIndisponivel = "NGX-0222"

	// CodigoSaidaNaoReconhecida: the command ran, but the output does not
	// have the expected format. Inventing fields out of output that was not
	// understood is worse than admitting it was not understood.
	CodigoSaidaNaoReconhecida = "NGX-0223"

	// CodigoTesteConfig: a diagnostic translated from a line of `nginx -t`
	// or `nginx -T`. A single code for every level: the level becomes
	// severity, it does not become a code.
	CodigoTesteConfig = "NGX-0224"

	// CodigoEstadoProcesso: something about the state of the process -- the
	// evidence that it is not running, or the reason why it could not be
	// determined. An omitted field without this diagnostic alongside it
	// would be degrading in silence.
	CodigoEstadoProcesso = "NGX-0225"
)

// BinarioPadrao is what ngx executes when nobody says otherwise. A plain
// name, resolved by the target's PATH: an absolute path guessed here would be
// wrong on half of the distributions.
const BinarioPadrao = "nginx"

// Runtime executes the nginx of a target through a Transport.
type Runtime struct {
	tr      transport.Transport
	binario string
	sudo    bool
}

// Opcao configures a Runtime at construction time.
type Opcao func(*Runtime)

// ComBinario swaps the invoked binary. Useful when nginx is not on the
// target's PATH or when there is more than one installation.
func ComBinario(caminho string) Opcao {
	return func(r *Runtime) {
		if caminho != "" {
			r.binario = caminho
		}
	}
}

// ComSudo turns on the explicit privilege escalation (DR5). Without it, a
// command that needs privilege is reported, never retried with sudo.
func ComSudo(ativo bool) Opcao {
	return func(r *Runtime) { r.sudo = ativo }
}

// New assembles the runtime on top of a transport.
func New(tr transport.Transport, opcoes ...Opcao) *Runtime {
	r := &Runtime{tr: tr, binario: BinarioPadrao}
	for _, o := range opcoes {
		o(r)
	}
	return r
}

// Alvo identifies what this runtime operates against, for the envelope's
// meta.
func (r *Runtime) Alvo() string { return r.tr.Describe() }

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
	out = append(out, r.binario)
	return append(out, args...)
}

// execucao is the raw result of an nginx invocation that ran to completion.
type execucao struct {
	argv   []string
	stdout string
	stderr string
	exit   int
}

// saida returns stderr concatenated with stdout. nginx writes diagnostics to
// stderr, but transports that merge both channels exist, and a parser that
// only looked at one of them would fail silently in those cases.
func (e *execucao) saida() string {
	if e.stderr == "" {
		return e.stdout
	}
	if e.stdout == "" {
		return e.stderr
	}
	return e.stderr + "\n" + e.stdout
}

// executar runs nginx with the given arguments and classifies what prevents a
// result from existing: missing binary, sudo unavailable, missing privilege
// and transport failure. A non-zero exit code for any other reason comes back
// as an execucao, with a nil err -- it is a result.
func (r *Runtime) executar(ctx context.Context, args ...string) (*execucao, error) {
	argv := r.argv(args...)
	stdout, stderr, exit, err := r.tr.Run(ctx, argv)

	e := &execucao{
		argv:   argv,
		stdout: string(stdout),
		stderr: string(stderr),
		exit:   exit,
	}

	if err != nil {
		if binarioAusente(err) {
			return nil, erroNginxAusente(r, e, err)
		}
		return nil, output.Internal(err,
			"failed to execute %s on %s", strings.Join(argv, " "), r.tr.Describe())
	}

	if exit == 0 {
		return e, nil
	}

	texto := e.saida()

	if r.sudo && sudoIndisponivel(texto) {
		return nil, erroSudoIndisponivel(r, e)
	}
	if naoEncontradoNaSaida(exit, texto) {
		return nil, erroNginxAusente(r, e, nil)
	}
	if exigePrivilegio(texto) {
		return nil, erroPrivilegio(r, e)
	}

	return e, nil
}

// binarioAusente recognizes the transport failure meaning "that program does
// not exist". The local transport returns exec.ErrNotFound for a PATH name
// and an fs.ErrNotExist for an absolute path.
func binarioAusente(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// naoEncontradoNaSaida recognizes the same case coming from a remote shell,
// where a missing binary is not a transport error: ssh executes, the target's
// shell answers 127 and writes the complaint to stderr.
func naoEncontradoNaSaida(exit int, texto string) bool {
	if exit != 127 {
		return false
	}
	t := strings.ToLower(texto)
	return strings.Contains(t, "command not found") ||
		strings.Contains(t, "no such file or directory") ||
		strings.Contains(t, "not found")
}

var padroesPrivilegio = []string{
	"permission denied",
	"operation not permitted",
	"must be run as root",
	"you must be root",
	"are you root",
}

// exigePrivilegio decides whether the output says "permission was missing".
// It is deliberately conservative: recognizing too little makes the user see
// the raw nginx message, which is still the truth; recognizing too much would
// turn a syntax error into a privilege request, which is a lie.
func exigePrivilegio(texto string) bool {
	t := strings.ToLower(texto)
	for _, p := range padroesPrivilegio {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

var padroesSudo = []string{
	"sudo: a password is required",
	"a terminal is required",
	"no tty present",
	"is not in the sudoers file",
	"sudo: command not found",
	"sudo: not found",
}

func sudoIndisponivel(texto string) bool {
	t := strings.ToLower(texto)
	for _, p := range padroesSudo {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// comandoPrivilegiado returns the exact line the operator would have to
// authorize. DR5 requires saying what the command is: "needs privilege"
// without the command forces the reader to guess, and guessing in production
// is how privilege gets escalated by mistake.
func comandoPrivilegiado(argv []string) string {
	if len(argv) > 0 && argv[0] == "sudo" {
		return strings.Join(argv, " ")
	}
	return "sudo -n " + strings.Join(argv, " ")
}

func erroPrivilegio(r *Runtime, e *execucao) error {
	var msg string
	if r.sudo {
		msg = fmt.Sprintf(
			"the command `%s` ran with --sudo on %s and still did not have "+
				"permission to read the configuration. nginx output: %s",
			strings.Join(e.argv, " "), r.tr.Describe(), resumo(e.saida()))
	} else {
		msg = fmt.Sprintf(
			"the command `%s` requires privilege on %s and ngx does not escalate on "+
				"its own: retry with --sudo, which runs `%s`. nginx output: %s",
			strings.Join(e.argv, " "), r.tr.Describe(),
			comandoPrivilegiado(e.argv), resumo(e.saida()))
	}
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoPrivilegioNecessario,
			Message:  msg,
		},
	}
}

func erroSudoIndisponivel(r *Runtime, e *execucao) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSudoIndisponivel,
			Message: fmt.Sprintf(
				"--sudo was requested, but the sudo on %s cannot be used without interaction: %s. "+
					"ngx runs with no shell and no terminal, so there is nowhere to type a password; "+
					"allow the command in sudoers or run ngx as a user that already has "+
					"read access to the configuration",
				r.tr.Describe(), resumo(e.saida())),
		},
	}
}

func erroNginxAusente(r *Runtime, e *execucao, causa error) error {
	err := &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoNginxAusente,
			Message: fmt.Sprintf(
				"there is no executable nginx on %s: `%s` cannot be executed. "+
					"If the binary exists under another name or outside the PATH, give the path",
				r.tr.Describe(), strings.Join(e.argv, " ")),
		},
		Err: causa,
	}
	return err
}

func erroSaidaNaoReconhecida(r *Runtime, e *execucao, oQue string) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSaidaNaoReconhecida,
			Message: fmt.Sprintf(
				"the output of `%s` on %s does not have the expected format (%s): %s",
				strings.Join(e.argv, " "), r.tr.Describe(), oQue, resumo(e.saida())),
		},
	}
}

var espacos = regexp.MustCompile(`\s+`)

// resumo condenses a multi-line output into a short single line, so it fits
// in a diagnostic message without destroying the readability of the JSON.
func resumo(texto string) string {
	t := strings.TrimSpace(espacos.ReplaceAllString(texto, " "))
	if t == "" {
		return "(no output)"
	}
	const limite = 300
	if len(t) > limite {
		return t[:limite] + "..."
	}
	return t
}
