package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
)

// Diagnostic codes in the 0100 range, which §6.0 of the design spec reserves
// for configuration. They name the CONDITION; the exit code stays 2, because
// what was wrong was the invocation, not the .conf. This is the direction the
// spec records as intended: the generic NGX-0002 remains the code for usage
// errors that have no range of their own.
const (
	// CodeFileAmbiguous: --file names more than one file. ngx never picks
	// one -- a tool that guesses teaches nobody to be precise.
	CodeFileAmbiguous = "NGX-0101"

	// CodeFileNoMatch: --file names no file. The message lists what WAS
	// there, because an empty result and a misspelt name look identical
	// otherwise.
	CodeFileNoMatch = "NGX-0102"

	// CodeServerAmbiguous: --server names more than one server_name.
	CodeServerAmbiguous = "NGX-0103"

	// CodeServerNoMatch: --server names no server_name.
	CodeServerNoMatch = "NGX-0104"

	// CodePartialResult: the emitted tree is a filtered subset. Info
	// severity: it is the announced outcome of the flags, not a problem.
	CodePartialResult = "NGX-0105"
)

// maxListed caps how many candidates a message names. A fragment like ".conf"
// can match every file of a 132-file configuration, and a diagnostic that
// pastes 132 paths costs the caller more context than the answer it replaces.
const maxListed = 20

// inspectFilter holds the values of --file and --server.
//
// The two combine with AND, and it is written down because the other reading
// is equally plausible: --file a.conf --server x.example.com means "the
// x.example.com server blocks that live in a.conf", never their union. The
// scope of --server is therefore whatever --file already narrowed to --
// including the list of names offered when nothing matches.
//
// Both filter what is EMITTED, not what is read. --file could in principle
// prune the read as well; --server structurally cannot, because knowing which
// file declares a server_name requires reading the file. Neither prunes here,
// and the help text does not claim otherwise.
type inspectFilter struct {
	File   string
	Server string
}

func (f inspectFilter) active() bool { return f.File != "" || f.Server != "" }

// apply narrows the tree to what the filters name. The returned files are new
// values: the parsed tree is never mutated, so a later pass (redaction, a
// future fmt) still sees the whole configuration.
func (f inspectFilter) apply(t *config.Tree) ([]*config.File, *output.Error) {
	files := t.Files
	var err *output.Error
	if f.File != "" {
		if files, err = f.applyFile(files); err != nil {
			return nil, err
		}
	}
	if f.Server != "" {
		if files, err = f.applyServer(files); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (f inspectFilter) applyFile(files []*config.File) ([]*config.File, *output.Error) {
	available := availablePaths(files)

	matches := make([]string, 0, 1)
	for _, path := range available {
		if matchesPath(f.File, path) {
			matches = append(matches, path)
		}
	}

	switch len(matches) {
	case 0:
		return nil, usageWithCode(CodeFileNoMatch,
			"--file %q matches no file. Read: %s",
			f.File, formatCandidates(available))
	case 1:
	default:
		return nil, usageWithCode(CodeFileAmbiguous,
			"--file %q matches %d files: %s. Use a longer fragment, or the absolute path, which matches exactly",
			f.File, len(matches), formatCandidates(matches))
	}

	path := matches[0]
	out := make([]*config.File, 0, 1)
	for _, file := range files {
		nodes := nodesFromFile(file.Nodes, path)
		// A file that matched by path but holds no directive at all still
		// comes out: it exists, it was read, and dropping it would report
		// "matches no file" on the next call for the same name.
		if len(nodes) == 0 && file.Path != path {
			continue
		}
		out = append(out, &config.File{Path: file.Path, Source: file.Source, Nodes: nodes})
	}
	return out, nil
}

func (f inspectFilter) applyServer(files []*config.File) ([]*config.File, *output.Error) {
	available := availableServerNames(files)
	matches := matchServerNames(f.Server, available)

	switch len(matches) {
	case 0:
		if len(available) == 0 {
			return nil, usageWithCode(CodeServerNoMatch,
				"--server %q matches no server_name: no server block in scope declares one",
				f.Server)
		}
		return nil, usageWithCode(CodeServerNoMatch,
			"--server %q matches no server_name. Declared: %s",
			f.Server, formatCandidates(available))
	case 1:
	default:
		return nil, usageWithCode(CodeServerAmbiguous,
			"--server %q matches %d server names: %s. Use a longer fragment; an exact server_name matches only itself",
			f.Server, len(matches), formatCandidates(matches))
	}

	name := matches[0]
	out := make([]*config.File, 0, 1)
	for _, file := range files {
		nodes := serversNamed(file.Nodes, name)
		if len(nodes) == 0 {
			continue
		}
		out = append(out, &config.File{Path: file.Path, Source: file.Source, Nodes: nodes})
	}
	return out, nil
}

// matchesPath decides whether the filter value names a path. A value starting
// with "/" is an absolute path and matches exactly; anything else is a
// fragment and matches by substring against the WHOLE path, never against the
// base name. There is no globbing rule to learn, and matching the whole path
// is what makes the same base name in two directories surface as an ambiguity
// instead of being silently resolved.
func matchesPath(value, path string) bool {
	if strings.HasPrefix(value, "/") {
		return value == path
	}
	return strings.Contains(path, value)
}

// matchServerNames resolves the filter value to the server names it addresses.
//
// An exact server_name wins outright. On a site holding "example.com" and
// "api.example.com", the substring reading alone would make `--server
// example.com` permanently ambiguous and the exact name unusable. With no
// exact hit the value is a fragment and matches by substring, the same way a
// path fragment does.
//
// Ambiguity is over the NAME, not over the number of blocks: one server_name
// served by a :80 block and a :443 block is one name and two emitted nodes,
// which is ordinary nginx and not a question for the caller.
func matchServerNames(value string, available []string) []string {
	if slices.Contains(available, value) {
		return []string{value}
	}
	out := make([]string, 0, 1)
	for _, name := range available {
		if strings.Contains(name, value) {
			out = append(out, name)
		}
	}
	return out
}

// availablePaths lists the files the answer could have come from, in tree
// order and without repeats.
//
// It reads File.Path AND Node.File because the two disagree after --combine:
// the combined tree is one File holding nodes from every included file, and
// each node keeps the path it really came from. Reading only File.Path would
// make --file useless with --combine; reading only Node.File would lose a
// file that holds no directive.
func availablePaths(files []*config.File) []string {
	out := make([]string, 0, len(files))
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, file := range files {
		add(file.Path)
		walkNodes(file.Nodes, func(n *config.Node) {
			add(n.File)
		})
	}
	return out
}

// availableServerNames lists every server_name declared in scope, in tree
// order and without repeats.
func availableServerNames(files []*config.File) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, file := range files {
		walkNodes(file.Nodes, func(n *config.Node) {
			if n.IsComment() || n.Directive != "server_name" {
				return
			}
			for _, name := range n.Args {
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
			}
		})
	}
	return out
}

// nodesFromFile keeps the nodes that came from path, preserving the ancestor
// chain: a server kept inside http comes back inside http, so the ids and the
// shape of the tree stay the ones the unfiltered read would have produced.
func nodesFromFile(nodes []*config.Node, path string) []*config.Node {
	out := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		kept := nodesFromFile(n.Block, path)
		switch {
		case n.File == path:
			clone := *n
			if n.HasBlock() {
				clone.Block = kept
			}
			out = append(out, &clone)
		case len(kept) > 0:
			clone := *n
			clone.Block = kept
			out = append(out, &clone)
		}
	}
	return out
}

// serversNamed keeps the server blocks that declare name, with their ancestor
// chain. A matched block comes out whole -- asking for a server means asking
// for what is inside it.
func serversNamed(nodes []*config.Node, name string) []*config.Node {
	out := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		if serverDeclares(n, name) {
			out = append(out, n)
			continue
		}
		if !n.HasBlock() {
			continue
		}
		kept := serversNamed(n.Block, name)
		if len(kept) == 0 {
			continue
		}
		clone := *n
		clone.Block = kept
		out = append(out, &clone)
	}
	return out
}

// serverDeclares reports whether n is a server BLOCK declaring name. HasBlock
// is the test, not the directive name: "server 10.0.0.1:8080;" inside an
// upstream is also called server and is not one.
func serverDeclares(n *config.Node, name string) bool {
	if n.Directive != "server" || !n.HasBlock() {
		return false
	}
	for _, child := range n.Block {
		if child.IsComment() || child.Directive != "server_name" {
			continue
		}
		if slices.Contains(child.Args, name) {
			return true
		}
	}
	return false
}

// walkNodes visits every node in pre-order. config.Tree.Walk cannot be used
// here: the filters run over a []*config.File that no longer has a Tree
// around it, and the hash of such a tree would be the hash of a subset.
func walkNodes(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walkNodes(n.Block, fn)
	}
}

func formatCandidates(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	if len(items) <= maxListed {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s (and %d more)",
		strings.Join(items[:maxListed], ", "), len(items)-maxListed)
}

// usageWithCode is a usage error carrying a code from the 0100 range instead
// of the generic NGX-0002. The exit code stays 2: the caller typed something
// that does not address anything, and ngx itself and the .conf are both fine.
func usageWithCode(code, format string, args ...any) *output.Error {
	e := output.Usage(format, args...)
	e.Diag.Code = code
	return e
}
