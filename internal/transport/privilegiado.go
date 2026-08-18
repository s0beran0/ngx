package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/s0beran0/ngx/internal/output"
)

// CodigoLeituraPrivilegiada reports that a file can only be read with
// privilege. Info severity: it is not a problem, it is the record that an
// escalation happened -- reading a server configuration with sudo cannot
// happen silently.
const CodigoLeituraPrivilegiada = "NGX-0230"

// CodigoPrivilegioNegado covers the case where not even privilege worked.
const CodigoPrivilegioNegado = "NGX-0231"

// CodigoLeituraPeloDump reports that the content came from `nginx -T` because
// neither the direct read nor the `sudo cat` reached the file.
const CodigoLeituraPeloDump = "NGX-0232"

// CodigoElevacaoForaDaArvore marks a privileged read of a path outside any
// directory the configuration had already reached without privilege. Warning
// severity, not error: it is legitimate and does not block -- but it is news,
// and news involving sudo deserves to be seen.
const CodigoElevacaoForaDaArvore = "NGX-0233"

// privilegiado wraps a Transport and, when a plain read runs into permissions,
// repeats the read of that file with privilege.
//
// Why a decorator and not a branch inside the SSH transport: the rule holds
// the same for any target, and keeping it out of the SSH client leaves the
// transport with a single responsibility. It also makes the behavior testable
// with no network, using a fake Transport.
//
// The escalation is MINIMAL on purpose. It does not read everything with
// sudo: it tries without privilege first and only repeats the file that was
// refused. In a configuration of 132 files where one is restricted, 131 keep
// being read as the ordinary user, and the diagnostic names the only one that
// demanded more.
type privilegiado struct {
	Transport
	ctx context.Context

	// dump is the last resort: `nginx -T` executed with privilege. It
	// exists because a hardened server usually allows SPECIFIC commands in
	// sudoers -- typically nginx itself -- and not a generic `cat`. On
	// those hosts the `sudo cat` fails and the dump works, and without it
	// privileged reading would be useless precisely where sudo is well
	// configured.
	//
	// It is not the first resort because `nginx -T` requires a VALID
	// configuration: the moment you most need to read the configuration is
	// when it broke, and then the dump does not answer. Reading file by
	// file always answers.
	dump      func(context.Context) (map[string][]byte, error)
	dumpFeito bool
	dumpCache map[string][]byte
	dumpErro  error

	mu sync.Mutex

	// arvore holds the directories the configuration reached WITHOUT
	// privilege, plus the one of the top-level file. It is not a fixed list
	// of paths: a fixed list would break a non-standard installation, and
	// the very server we measured includes from /etc/letsencrypt, outside
	// /etc/nginx. The tree is derived from what the configuration actually
	// references.
	arvore       map[string]bool
	elevados     []string
	foraDaArvore []string
	viaDump      []string
	recusados    map[string]string
}

// ComLeituraPrivilegiada returns a Transport that repeats with privilege the
// read that was refused for permissions. Passing ativo=false returns the
// original transport untouched: the decision to escalate belongs to the
// caller, and DR5 requires it to be explicit.
func ComLeituraPrivilegiada(ctx context.Context, tr Transport, ativo bool) Transport {
	return ComLeituraPrivilegiadaEDump(ctx, tr, ativo, nil)
}

// ComLeituraPrivilegiadaEDump adds the last resort: a function that returns
// the whole effective configuration (in practice, `nginx -T` with privilege),
// consulted only when the per-file read did not reach it.
func ComLeituraPrivilegiadaEDump(
	ctx context.Context,
	tr Transport,
	ativo bool,
	dump func(context.Context) (map[string][]byte, error),
) Transport {
	if !ativo {
		return tr
	}
	return &privilegiado{
		Transport: tr, ctx: ctx, dump: dump,
		arvore: map[string]bool{}, recusados: map[string]string{},
	}
}

// conteudoDoDump returns the content of a path according to the dump, running
// it at most once per transport. One `nginx -T` per refused file would be
// absurd in a configuration of 132 files.
func (p *privilegiado) conteudoDoDump(caminho string) ([]byte, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpFeito {
		p.dumpFeito = true
		p.dumpCache, p.dumpErro = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErro
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}
	conteudo, ok := cache[caminho]
	return conteudo, ok
}

func (p *privilegiado) Open(caminho string) (io.ReadCloser, error) {
	// The directory of the FIRST requested path enters the tree before
	// anything else: it is the configuration the operator named, so there
	// is nothing new about it, even if it needs privilege.
	p.semearArvore(caminho)

	rc, err := p.Transport.Open(caminho)
	if err == nil {
		// Reached without privilege: its directory becomes known, and an
		// elevated sibling inside it stops being news.
		p.registrarNaArvore(caminho)
		return rc, nil
	}
	if !ehPermissao(err) {
		return rc, err
	}

	// Explicit argv, no shell: a file name with a space, a quote or a
	// dollar sign does not become an injection. The `--` closes the option
	// list, and it is what prevents the other injection, the ARGUMENT one:
	// without it, a path starting with `-` would be read as a flag by cat
	// instead of as a file. The path comes from an `include` directive in
	// the target's configuration, which is not trusted input -- and here
	// the command runs with privilege, so the cost of getting it wrong is
	// high and the cost of preventing it is one token.
	//
	// We do not refuse a path starting with `-`: with the `--` it works,
	// and refusing would break a legitimately named file out of redundant
	// caution.
	//
	// The -n in sudo avoids hanging waiting for a password in a process
	// with no terminal.
	stdout, stderr, saida, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "cat", "--", caminho})
	if errRun != nil {
		return nil, errRun
	}
	if saida != 0 {
		if conteudo, ok := p.conteudoDoDump(caminho); ok {
			p.registrarViaDump(caminho)
			return io.NopCloser(bytes.NewReader(conteudo)), nil
		}
		p.registrarRecusa(caminho, primeiraLinha(string(stderr)))
		// Returns the ORIGINAL permission error: for the caller, the file
		// is still unreadable, and the cause is still permissions. The
		// detail of what sudo answered goes in the diagnostic, not in the
		// error.
		return nil, err
	}

	p.registrarElevado(caminho)
	return io.NopCloser(bytes.NewReader(stdout)), nil
}

func (p *privilegiado) Glob(padrao string) ([]string, error) {
	achados, err := p.Transport.Glob(padrao)
	if err == nil || !ehPermissao(err) {
		return achados, err
	}

	// A directory the ordinary user cannot list. `ls -1` on a single
	// directory, without recursion, is the minimum that answers the
	// question.
	dir, arquivo := path.Split(padrao)
	dir = path.Clean(dir)
	// `--` for the same reason as with cat: without it a directory whose
	// name starts with `-` would become an ls option.
	stdout, stderr, saida, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "ls", "-1", "--", dir})
	if errRun != nil {
		return nil, errRun
	}
	if saida != 0 {
		// The dump already knows ALL the files of the effective
		// configuration, so it answers the pattern without listing any
		// directory. This is the hardened server case: sudoers allows
		// nginx and refuses both `cat` and `ls`.
		if casados, ok := p.globPeloDump(padrao); ok {
			p.registrarViaDump(dir)
			return casados, nil
		}
		p.registrarRecusa(dir, primeiraLinha(string(stderr)))
		return nil, err
	}

	casados := []string{}
	for _, nome := range strings.Split(string(stdout), "\n") {
		nome = strings.TrimSpace(nome)
		if nome == "" {
			continue
		}
		// path.Match, never filepath.Match: the remote target uses the
		// POSIX separator even when ngx runs on Windows.
		if ok, _ := path.Match(arquivo, nome); ok {
			casados = append(casados, path.Join(dir, nome))
		}
	}
	sort.Strings(casados)
	p.registrarElevado(dir)
	return casados, nil
}

// globPeloDump matches the pattern against the paths the dump knows. It
// returns ok=false when there is no dump: an empty list with ok=true would be
// indistinguishable from "the pattern matched nothing", and presenting an
// incomplete configuration as complete is the defect DR6 exists to prevent.
func (p *privilegiado) globPeloDump(padrao string) ([]string, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpFeito {
		p.dumpFeito = true
		p.dumpCache, p.dumpErro = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErro
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}

	casados := []string{}
	for caminho := range cache {
		if ok, _ := path.Match(padrao, caminho); ok {
			casados = append(casados, caminho)
		}
	}
	sort.Strings(casados)
	return casados, true
}

func (p *privilegiado) registrarElevado(caminho string) {
	dentro := p.dentroDaArvore(caminho)
	p.mu.Lock()
	defer p.mu.Unlock()
	if dentro {
		p.elevados = append(p.elevados, caminho)
		return
	}
	p.foraDaArvore = append(p.foraDaArvore, caminho)
}

// semearArvore records the directory of the top-level file, exactly once.
func (p *privilegiado) semearArvore(caminho string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.arvore) == 0 {
		p.arvore[path.Dir(caminho)] = true
	}
}

func (p *privilegiado) registrarNaArvore(caminho string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.arvore[path.Dir(caminho)] = true
}

// dentroDaArvore tells whether the path is in some directory the configuration
// already reached without privilege, or below it.
func (p *privilegiado) dentroDaArvore(caminho string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := path.Dir(caminho)
	for conhecido := range p.arvore {
		if dir == conhecido || strings.HasPrefix(dir, conhecido+"/") {
			return true
		}
	}
	return false
}

func (p *privilegiado) registrarViaDump(caminho string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.viaDump = append(p.viaDump, caminho)
}

func (p *privilegiado) registrarRecusa(caminho, motivo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recusados[caminho] = motivo
}

// Diagnosticos reports what required privilege and what did not work even with
// it. Escalating in silence would hide from the operator that reading their
// server configuration went through sudo.
func (p *privilegiado) Diagnosticos() []output.Diagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()

	diags := []output.Diagnostic{}
	if len(p.elevados) > 0 {
		lista := append([]string(nil), p.elevados...)
		sort.Strings(lista)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodigoLeituraPrivilegiada,
			Message: fmt.Sprintf(
				"%d path(s) could only be read with privilege, because --sudo was "+
					"requested: %s", len(lista), resumirCaminhos(lista)),
		})
	}
	if len(p.foraDaArvore) > 0 {
		lista := append([]string(nil), p.foraDaArvore...)
		sort.Strings(lista)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     CodigoElevacaoForaDaArvore,
			Message: fmt.Sprintf(
				"%d path(s) were read with privilege OUTSIDE any directory the "+
					"configuration already reached without it: %s. Check whether the "+
					"include that led there is expected",
				len(lista), resumirCaminhos(lista)),
		})
	}

	if len(p.viaDump) > 0 {
		lista := append([]string(nil), p.viaDump...)
		sort.Strings(lista)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodigoLeituraPeloDump,
			Message: fmt.Sprintf(
				"%d path(s) came from `nginx -T` with privilege, because sudo on the "+
					"target does not allow reading the file directly: %s",
				len(lista), resumirCaminhos(lista)),
		})
	}

	for caminho, motivo := range p.recusados {
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoPrivilegioNegado,
			File:     caminho,
			Message: fmt.Sprintf(
				"not even with privilege was it possible to read this path (%s); check "+
					"whether sudo on the target allows `cat` without a password for the "+
					"user running ngx", motivo),
		})
	}
	return diags
}

// ehPermissao recognizes a permission refusal coming from any target. The SFTP
// error matches fs.ErrPermission -- verified against a real server --, so the
// same check serves both local and remote.
func ehPermissao(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

func primeiraLinha(texto string) string {
	texto = strings.TrimSpace(texto)
	if i := strings.IndexByte(texto, '\n'); i >= 0 {
		return texto[:i]
	}
	if texto == "" {
		return "no detail from sudo"
	}
	return texto
}

// resumirCaminhos avoids dumping a list of hundreds of paths into a single
// diagnostic line. The exact count already appears before it; here a few
// examples are enough for the operator to recognize what it is about.
func resumirCaminhos(lista []string) string {
	const mostrar = 3
	if len(lista) <= mostrar {
		return strings.Join(lista, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(lista[:mostrar], ", "), len(lista)-mostrar)
}

// Diagnosticos collects what a transport observed, when it has something to
// tell. It exists as a function and not as an interface method because only
// the privileged transport has something to report: adding this to Transport
// would force every implementation to carry an empty method.
func Diagnosticos(tr Transport) []output.Diagnostic {
	if d, ok := tr.(interface{ Diagnosticos() []output.Diagnostic }); ok {
		return d.Diagnosticos()
	}
	return nil
}
