// Package ops turns an intention into a plan. Nothing here writes: every
// function takes a configuration that was just read and returns a plan.Plan,
// which internal/apply is the only thing allowed to act on.
//
// The separation is the safety design of v0.2, and it has a second benefit that
// shows up immediately: an operation can VALIDATE ITS OWN OUTPUT before
// emitting it. Every function below builds the replacement text and then feeds
// it back through the parser, refusing if what comes out is not what was asked
// for. That turns questions like "did I quote this argument correctly" from a
// belief into a check, using the parser as the oracle rather than reasoning
// about nginx's lexical rules a second time.
package ops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// RefusalCode enumerates why an operation will not produce a plan. A caller
// branches on this, never on the message.
type RefusalCode string

const (
	// CodeRefNotFound is a ref that names no node in this configuration.
	CodeRefNotFound RefusalCode = "ops_ref_not_found"

	// CodeUnsupportedTarget is a node this operation refuses to touch, for a
	// reason particular to the node rather than to the request.
	CodeUnsupportedTarget RefusalCode = "ops_unsupported_target"

	// CodeInvalidArguments is a replacement that does not survive being parsed
	// back -- the operation checked its own output and did not recognise it.
	CodeInvalidArguments RefusalCode = "ops_invalid_arguments"

	// CodeNoChange is a request that would produce the bytes already there. It
	// is a refusal and not a silent success, because an empty plan applied is
	// indistinguishable from a plan that did nothing wrong, and a caller that
	// asked for a change deserves to know it did not happen.
	CodeNoChange RefusalCode = "ops_no_change"
)

// Refusal is why an operation produced no plan.
type Refusal struct {
	Code    RefusalCode
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// CodeOf returns the refusal code of an error, if it is one.
func CodeOf(err error) (RefusalCode, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code, true
	}
	return "", false
}

// Set replaces the arguments of an existing directive.
//
// It is the lowest-risk operation and the one the spans already support:
// HeadSpan is exactly "name + arguments", so the terminator, the block and
// everything after it are not part of the substitution and cannot be disturbed
// by it.
func Set(tree *config.Tree, root, ref string, args []string) (*plan.Plan, error) {
	node := config.FindByRef(tree, ref)
	if node == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s names no node in this configuration. Refs come from the output of "+
				"`ngx get` or `ngx inspect`, and they are only valid for the "+
				"configuration they were read from", ref)}
	}
	if len(args) == 0 {
		return nil, &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"set on %s was given no arguments. A directive with no arguments is a "+
				"different directive, not an empty one -- remove it instead", ref)}
	}

	if err := setRefusesTarget(node); err != nil {
		return nil, err
	}

	source := sourceOf(tree, node.File)
	if source == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s belongs to %s, which is not part of the configuration that was read",
			ref, node.File)}
	}

	before := string(source[node.HeadSpan.Start:node.HeadSpan.End])
	after := node.Directive + " " + strings.Join(quoteArgs(args), " ")
	if after == before {
		return nil, &Refusal{CodeNoChange, fmt.Sprintf(
			"%s already reads %q, so there is nothing to change", ref, before)}
	}

	// The operation checks its own output. If the text it built does not parse
	// back into the directive and arguments that were asked for, the quoting is
	// wrong and the right answer is to refuse rather than to write it.
	if err := verifyHead(after, node.Directive, args); err != nil {
		return nil, err
	}

	return &plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Edits: []plan.Edit{{
			File:   node.File,
			Ref:    node.Ref,
			Span:   node.HeadSpan,
			Before: before,
			After:  after,
			Reason: "set " + node.Directive,
		}},
	}, nil
}

// setRefusesTarget holds the two nodes set will not touch, each for a reason
// that lives in the tree rather than in this package.
func setRefusesTarget(node *config.Node) error {
	// "if" rewrites its own arguments inside crossplane: prepareIfArgs strips
	// the parentheses, so an entry of Args is a substring of the lexeme it came
	// from and one lexeme vanishes entirely. Node.ArgSpans is nil to say so
	// (node.go), and rebuilding a head from Args would produce a directive that
	// is not the one that was there.
	if node.ArgSpans == nil {
		return &Refusal{CodeUnsupportedTarget, fmt.Sprintf(
			"%s is a %q, whose arguments ngx cannot map back to bytes: the parser "+
				"rewrites them, so rebuilding the directive from them would change what "+
				"it says. Edit this one by hand",
			node.Ref, node.Directive)}
	}

	// A comment INSIDE the head -- `server_name a.com # prod\n b.com;` -- is
	// recorded in HeadComments precisely so a rewrite does not erase it. v0.2
	// refuses instead of deciding where to put it back: any placement is a
	// guess about what the comment refers to, and a comment moved to the wrong
	// argument is worse than one left alone.
	if len(node.HeadComments) > 0 {
		return &Refusal{CodeUnsupportedTarget, fmt.Sprintf(
			"%s has a comment between its arguments, and ngx will not guess where that "+
				"comment belongs in the new text. Edit this one by hand", node.Ref)}
	}

	return nil
}

// verifyHead parses the replacement text and requires it to be the directive
// that was asked for, with exactly those arguments.
//
// This is the oracle, and it is the parser rather than a second reading of
// nginx's quoting rules. A quoting bug shows up here as "what I wrote is not
// what I meant" instead of as a configuration nginx refuses -- or worse, one it
// accepts with a different meaning.
func verifyHead(head, directive string, args []string) error {
	dir, err := os.MkdirTemp("", "ngx-ops-*")
	if err != nil {
		return &Refusal{CodeInvalidArguments,
			"could not check the replacement text: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "probe.conf")
	if err := os.WriteFile(path, []byte(head+";\n"), 0o600); err != nil {
		return &Refusal{CodeInvalidArguments,
			"could not check the replacement text: " + err.Error()}
	}

	tree, err := config.Parse(config.ParseOptions{Path: path})
	if err != nil {
		return &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"the arguments given produce %q, which is not valid configuration: %v", head, err)}
	}

	var directives []*config.Node
	for _, n := range tree.Files[0].Nodes {
		if !n.IsComment() {
			directives = append(directives, n)
		}
	}
	if len(directives) != 1 {
		return &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"the arguments given produce %q, which reads as %d directives instead of one. "+
				"An argument holding a %q or a %q cannot be expressed here",
			head, len(directives), ";", "{")}
	}

	got := directives[0]
	if got.Directive != directive || !equal(got.Args, args) {
		return &Refusal{CodeInvalidArguments, fmt.Sprintf(
			"the arguments given produce %q, which reads back as %q with arguments %q "+
				"instead of %q. ngx will not write text whose meaning it cannot confirm",
			head, got.Directive, got.Args, args)}
	}
	return nil
}

// quoteArgs quotes the arguments that need it.
//
// The rule is derived from what the tokenizer accepts rather than from a list of
// special characters: anything that is not a bare run of non-space, non-quote,
// non-terminator bytes gets quoted. Getting the rule slightly wrong is not
// dangerous here BECAUSE verifyHead re-reads the result -- a missed case becomes
// a refusal, not a corrupted directive.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quoteArg(a)
	}
	return out
}

func quoteArg(a string) string {
	if a == "" {
		return `""`
	}
	if !strings.ContainsAny(a, " \t\r\n;{}#'\"\\") {
		return a
	}
	// Double quotes, with the quote character and the backslash escaped. A
	// single-quoted form would need the same treatment for "'", so there is
	// nothing to choose between them and one form is easier to read back.
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(a); i++ {
		if a[i] == '"' || a[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(a[i])
	}
	b.WriteByte('"')
	return b.String()
}

func sourceOf(tree *config.Tree, path string) []byte {
	for _, f := range tree.Files {
		if f.Path == path {
			return f.Source
		}
	}
	return nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
