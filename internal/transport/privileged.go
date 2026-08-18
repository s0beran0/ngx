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

// CodePrivilegedRead reports that a file can only be read with
// privilege. Info severity: it is not a problem, it is the record that an
// escalation happened -- reading a server configuration with sudo cannot
// happen silently.
const CodePrivilegedRead = "NGX-0230"

// CodePrivilegeDenied covers the case where not even privilege worked.
const CodePrivilegeDenied = "NGX-0231"

// CodeReadViaDump reports that the content came from `nginx -T` because
// neither the direct read nor the `sudo cat` reached the file.
const CodeReadViaDump = "NGX-0232"

// CodeElevationOutsideTree marks a privileged read of a path outside any
// directory the configuration had already reached without privilege. Warning
// severity, not error: it is legitimate and does not block -- but it is news,
// and news involving sudo deserves to be seen.
const CodeElevationOutsideTree = "NGX-0233"

// privilegedTransport wraps a Transport and, when a plain read runs into permissions,
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
type privilegedTransport struct {
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
	dumpDone  bool
	dumpCache map[string][]byte
	dumpErr   error

	mu sync.Mutex

	// knownTree holds the directories the configuration reached WITHOUT
	// privilege, plus the one of the top-level file. It is not a fixed list
	// of paths: a fixed list would break a non-standard installation, and
	// the very server we measured includes from /etc/letsencrypt, outside
	// /etc/nginx. The tree is derived from what the configuration actually
	// references.
	knownTree        map[string]bool
	elevated         []string
	outsideKnownTree []string
	fromDump         []string
	refused          map[string]string
}

// WithPrivilegedRead returns a Transport that repeats with privilege the
// read that was refused for permissions. Passing enabled=false returns the
// original transport untouched: the decision to escalate belongs to the
// caller, and DR5 requires it to be explicit.
func WithPrivilegedRead(ctx context.Context, tr Transport, enabled bool) Transport {
	return WithPrivilegedReadAndDump(ctx, tr, enabled, nil)
}

// WithPrivilegedReadAndDump adds the last resort: a function that returns
// the whole effective configuration (in practice, `nginx -T` with privilege),
// consulted only when the per-file read did not reach it.
func WithPrivilegedReadAndDump(
	ctx context.Context,
	tr Transport,
	enabled bool,
	dump func(context.Context) (map[string][]byte, error),
) Transport {
	if !enabled {
		return tr
	}
	return &privilegedTransport{
		Transport: tr, ctx: ctx, dump: dump,
		knownTree: map[string]bool{}, refused: map[string]string{},
	}
}

// dumpContent returns the content of a path according to the dump, running
// it at most once per transport. One `nginx -T` per refused file would be
// absurd in a configuration of 132 files.
func (p *privilegedTransport) dumpContent(filePath string) ([]byte, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpDone {
		p.dumpDone = true
		p.dumpCache, p.dumpErr = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErr
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}
	content, ok := cache[filePath]
	return content, ok
}

func (p *privilegedTransport) Open(filePath string) (io.ReadCloser, error) {
	// The directory of the FIRST requested path enters the tree before
	// anything else: it is the configuration the operator named, so there
	// is nothing new about it, even if it needs privilege.
	p.seedKnownTree(filePath)

	rc, err := p.Transport.Open(filePath)
	if err == nil {
		// Reached without privilege: its directory becomes known, and an
		// elevated sibling inside it stops being news.
		p.recordInKnownTree(filePath)
		return rc, nil
	}
	if !isPermissionDenied(err) {
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
	stdout, stderr, exitCode, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "cat", "--", filePath})
	if errRun != nil {
		return nil, errRun
	}
	if exitCode != 0 {
		if content, ok := p.dumpContent(filePath); ok {
			p.recordFromDump(filePath)
			return io.NopCloser(bytes.NewReader(content)), nil
		}
		p.recordRefusal(filePath, firstLine(string(stderr)))
		// Returns the ORIGINAL permission error: for the caller, the file
		// is still unreadable, and the cause is still permissions. The
		// detail of what sudo answered goes in the diagnostic, not in the
		// error.
		return nil, err
	}

	p.recordElevated(filePath)
	return io.NopCloser(bytes.NewReader(stdout)), nil
}

func (p *privilegedTransport) Glob(pattern string) ([]string, error) {
	matches, err := p.Transport.Glob(pattern)
	if err == nil || !isPermissionDenied(err) {
		return matches, err
	}

	// A directory the ordinary user cannot list. `ls -1` on a single
	// directory, without recursion, is the minimum that answers the
	// question.
	dir, base := path.Split(pattern)
	dir = path.Clean(dir)
	// `--` for the same reason as with cat: without it a directory whose
	// name starts with `-` would become an ls option.
	stdout, stderr, exitCode, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "ls", "-1", "--", dir})
	if errRun != nil {
		return nil, errRun
	}
	if exitCode != 0 {
		// The dump already knows ALL the files of the effective
		// configuration, so it answers the pattern without listing any
		// directory. This is the hardened server case: sudoers allows
		// nginx and refuses both `cat` and `ls`.
		if matched, ok := p.globFromDump(pattern); ok {
			p.recordFromDump(dir)
			return matched, nil
		}
		p.recordRefusal(dir, firstLine(string(stderr)))
		return nil, err
	}

	matched := []string{}
	for _, name := range strings.Split(string(stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// path.Match, never filepath.Match: the remote target uses the
		// POSIX separator even when ngx runs on Windows.
		if ok, _ := path.Match(base, name); ok {
			matched = append(matched, path.Join(dir, name))
		}
	}
	sort.Strings(matched)
	p.recordElevated(dir)
	return matched, nil
}

// globFromDump matches the pattern against the paths the dump knows. It
// returns ok=false when there is no dump: an empty list with ok=true would be
// indistinguishable from "the pattern matched nothing", and presenting an
// incomplete configuration as complete is the defect DR6 exists to prevent.
func (p *privilegedTransport) globFromDump(pattern string) ([]string, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpDone {
		p.dumpDone = true
		p.dumpCache, p.dumpErr = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErr
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}

	matched := []string{}
	for filePath := range cache {
		if ok, _ := path.Match(pattern, filePath); ok {
			matched = append(matched, filePath)
		}
	}
	sort.Strings(matched)
	return matched, true
}

func (p *privilegedTransport) recordElevated(filePath string) {
	inside := p.insideKnownTree(filePath)
	p.mu.Lock()
	defer p.mu.Unlock()
	if inside {
		p.elevated = append(p.elevated, filePath)
		return
	}
	p.outsideKnownTree = append(p.outsideKnownTree, filePath)
}

// seedKnownTree records the directory of the top-level file, exactly once.
func (p *privilegedTransport) seedKnownTree(filePath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.knownTree) == 0 {
		p.knownTree[path.Dir(filePath)] = true
	}
}

func (p *privilegedTransport) recordInKnownTree(filePath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knownTree[path.Dir(filePath)] = true
}

// insideKnownTree tells whether the path is in some directory the configuration
// already reached without privilege, or below it.
func (p *privilegedTransport) insideKnownTree(filePath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := path.Dir(filePath)
	for known := range p.knownTree {
		if dir == known || strings.HasPrefix(dir, known+"/") {
			return true
		}
	}
	return false
}

func (p *privilegedTransport) recordFromDump(filePath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fromDump = append(p.fromDump, filePath)
}

func (p *privilegedTransport) recordRefusal(filePath, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused[filePath] = reason
}

// Diagnostics reports what required privilege and what did not work even with
// it. Escalating in silence would hide from the operator that reading their
// server configuration went through sudo.
func (p *privilegedTransport) Diagnostics() []output.Diagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()

	diags := []output.Diagnostic{}
	if len(p.elevated) > 0 {
		list := append([]string(nil), p.elevated...)
		sort.Strings(list)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodePrivilegedRead,
			Message: fmt.Sprintf(
				"%d path(s) could only be read with privilege, because --sudo was "+
					"requested: %s", len(list), summarizePaths(list)),
		})
	}
	if len(p.outsideKnownTree) > 0 {
		list := append([]string(nil), p.outsideKnownTree...)
		sort.Strings(list)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     CodeElevationOutsideTree,
			Message: fmt.Sprintf(
				"%d path(s) were read with privilege OUTSIDE any directory the "+
					"configuration already reached without it: %s. Check whether the "+
					"include that led there is expected",
				len(list), summarizePaths(list)),
		})
	}

	if len(p.fromDump) > 0 {
		list := append([]string(nil), p.fromDump...)
		sort.Strings(list)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodeReadViaDump,
			Message: fmt.Sprintf(
				"%d path(s) came from `nginx -T` with privilege, because sudo on the "+
					"target does not allow reading the file directly: %s",
				len(list), summarizePaths(list)),
		})
	}

	for filePath, reason := range p.refused {
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodePrivilegeDenied,
			File:     filePath,
			Message: fmt.Sprintf(
				"not even with privilege was it possible to read this path (%s); check "+
					"whether sudo on the target allows `cat` without a password for the "+
					"user running ngx", reason),
		})
	}
	return diags
}

// isPermissionDenied recognizes a permission refusal coming from any target. The SFTP
// error matches fs.ErrPermission -- verified against a real server --, so the
// same check serves both local and remote.
func isPermissionDenied(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	if text == "" {
		return "no detail from sudo"
	}
	return text
}

// summarizePaths avoids dumping a list of hundreds of paths into a single
// diagnostic line. The exact count already appears before it; here a few
// examples are enough for the operator to recognize what it is about.
func summarizePaths(list []string) string {
	const maxShown = 3
	if len(list) <= maxShown {
		return strings.Join(list, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(list[:maxShown], ", "), len(list)-maxShown)
}

// Diagnostics collects what a transport observed, when it has something to
// tell. It exists as a function and not as an interface method because only
// the privileged transport has something to report: adding this to Transport
// would force every implementation to carry an empty method.
func Diagnostics(tr Transport) []output.Diagnostic {
	if d, ok := tr.(interface{ Diagnostics() []output.Diagnostic }); ok {
		return d.Diagnostics()
	}
	return nil
}
