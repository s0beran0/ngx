package ops

import (
	"fmt"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// Remove deletes a directive.
//
// The span taken is the directive's whole Span -- name, arguments, terminator
// and, for a block, the block and its closing brace -- plus the indentation
// before it and the line ending after it, when the directive is alone on its
// line. Without those, removing a directive leaves an empty indented line
// behind, which is a change to the file nobody asked for.
//
// Blank lines around it are NOT touched. A blank line is somebody's paragraph
// break, and removing one directive is not a licence to reflow the file.
func Remove(tree *config.Tree, root, ref string) (*plan.Plan, error) {
	node := config.FindByRef(tree, ref)
	if node == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s names no node in this configuration", ref)}
	}
	source := sourceOf(tree, node.File)
	if source == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s belongs to %s, which is not part of the configuration that was read",
			ref, node.File)}
	}

	// A comment inside the head goes with the directive, which is right -- it
	// is on the same line and about it -- but a comment AFTER the last argument
	// and before the terminator is also inside the span and would go silently.
	// That one is enumerated as safe for `set` precisely because set does not
	// take it; here it does, so the caller is told.
	span := expandToWholeLines(source, node.Span)

	return &plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Edits: []plan.Edit{{
			File:   node.File,
			Ref:    node.Ref,
			Span:   span,
			Before: string(source[span.Start:span.End]),
			After:  "",
			Reason: "remove " + node.Directive,
		}},
	}, nil
}

// expandToWholeLines grows a span to cover the indentation before it and the
// line ending after it, but only when nothing else shares those bytes.
//
// "Only when nothing else shares them" is the whole subtlety. In
// `server { listen 80; }` the listen directive is not alone on its line, and
// swallowing the space before it would eat the brace's separator; in
//
//	server {
//	    listen 80;
//	}
//
// it is alone, and NOT swallowing the indentation leaves "    \n" behind.
func expandToWholeLines(src []byte, span config.Span) config.Span {
	start := span.Start
	for start > 0 && (src[start-1] == ' ' || src[start-1] == '\t') {
		start--
	}
	// Only if what precedes the indentation is the start of a line. Otherwise
	// the spaces belong to whatever came before.
	if start > 0 && src[start-1] != '\n' {
		start = span.Start
	}

	end := span.End
	// Trailing spaces or tabs, then the line ending. A CRLF file keeps its CR:
	// converting a line ending is the off-target change D1 exists to prevent.
	probe := end
	for probe < len(src) && (src[probe] == ' ' || src[probe] == '\t') {
		probe++
	}
	switch {
	case probe < len(src) && src[probe] == '\n':
		end = probe + 1
	case probe+1 < len(src) && src[probe] == '\r' && src[probe+1] == '\n':
		end = probe + 2
	case probe == len(src):
		// End of file with no newline: take the trailing whitespace, since
		// there is no line to leave behind.
		end = probe
	}

	// If the expansion did not reach a line boundary on the left, do not take
	// the line ending on the right either: the directive shares its line with
	// something, and removing the newline would join two lines that were not
	// joined.
	if start == span.Start && span.Start > 0 && src[span.Start-1] != '\n' {
		return span
	}
	return config.Span{Start: start, End: end}
}

// Add inserts a directive inside a block.
//
// The indentation is copied from the block's existing children rather than
// chosen: a file indented with two spaces keeps two, a file with tabs keeps
// tabs, and a file with none gets none. The line ending is copied from the file
// for the same reason.
//
// parentRef must name a directive that opens a block. Adding at the top level
// of a file is a different operation -- there is no parent to copy indentation
// from -- and it is not part of v0.2.
func Add(tree *config.Tree, root, parentRef, directive string, args []string) (*plan.Plan, error) {
	if directive == "" {
		return nil, &Refusal{CodeInvalidArguments, "add was given no directive name"}
	}

	parent := config.FindByRef(tree, parentRef)
	if parent == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s names no node in this configuration", parentRef)}
	}
	if !parent.HasBlock() {
		return nil, &Refusal{CodeUnsupportedTarget, fmt.Sprintf(
			"%s is a %q, which does not open a block, so there is nothing to add inside it",
			parentRef, parent.Directive)}
	}
	source := sourceOf(tree, parent.File)
	if source == nil {
		return nil, &Refusal{CodeRefNotFound, fmt.Sprintf(
			"%s belongs to %s, which is not part of the configuration that was read",
			parentRef, parent.File)}
	}

	text := directive
	if len(args) > 0 {
		text += " " + strings.Join(quoteArgs(args), " ")
	}
	if err := verifyHead(text, directive, args); err != nil {
		return nil, err
	}

	closing, err := closingBrace(source, parent)
	if err != nil {
		return nil, err
	}

	indent := indentOf(source, parent)
	eol := lineEndingOf(source)

	// Inserted just before the line that holds the closing brace, so the new
	// directive is the last child. Anywhere else needs a rule for "where", and
	// "at the end" is the only position that needs no argument.
	at := startOfLine(source, closing)
	insertion := indent + text + ";" + eol

	return &plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Edits: []plan.Edit{{
			File: parent.File,
			Ref:  parent.Ref,
			// A zero-width span: an insertion replaces no bytes. Before is
			// empty and its length matches, which is what keeps plan.Validate
			// happy without a special case for insertions.
			Span:   config.Span{Start: at, End: at},
			Before: "",
			After:  insertion,
			Reason: "add " + directive + " to " + parent.Directive,
		}},
	}, nil
}

// closingBrace finds the "}" that closes the parent's block.
//
// It is the last byte of the parent's span, and that is asserted rather than
// assumed: a node that opens a block whose span does not end in "}" is a
// finding in the aligner, and guessing past it would put the insertion
// somewhere arbitrary.
func closingBrace(src []byte, parent *config.Node) (int, error) {
	if parent.Span.End == 0 || parent.Span.End > len(src) || src[parent.Span.End-1] != '}' {
		return 0, &Refusal{CodeUnsupportedTarget, fmt.Sprintf(
			"the span of %s does not end in \"}\", so ngx cannot tell where its block "+
				"closes", parent.Ref)}
	}
	return parent.Span.End - 1, nil
}

// indentOf returns the indentation the block's children use.
//
// Copied from the first child that starts its own line, because that is what
// the file already does -- including when that indentation is EMPTY, which is a
// fact about the file and not a missing answer.
//
// The four-space fallback applies only when the block offers no evidence at all:
// it is empty, or written entirely on one line. A guess is acceptable there
// because there is nothing to copy; it is not acceptable anywhere else.
func indentOf(src []byte, parent *config.Node) string {
	for _, child := range parent.Block {
		line := startOfLine(src, child.Span.Start)
		if line == child.Span.Start {
			// The child starts at column 1. That is EVIDENCE of empty
			// indentation, not the absence of evidence -- treating it as
			// "nothing to copy" and falling through to the default is how the
			// first version inserted four spaces into a file that used none.
			return ""
		}
		gap := src[line:child.Span.Start]
		if isBlank(gap) {
			return string(gap)
		}
	}

	parentLine := startOfLine(src, parent.Span.Start)
	parentIndent := ""
	if gap := src[parentLine:parent.Span.Start]; isBlank(gap) {
		parentIndent = string(gap)
	}
	return parentIndent + "    "
}

// lineEndingOf returns the line ending the file uses. A CRLF file stays CRLF:
// converting one is the off-target change the project promises never to make.
func lineEndingOf(src []byte) string {
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			if i > 0 && src[i-1] == '\r' {
				return "\r\n"
			}
			return "\n"
		}
	}
	return "\n"
}

func startOfLine(src []byte, at int) int {
	for at > 0 && src[at-1] != '\n' {
		at--
	}
	return at
}

func isBlank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
