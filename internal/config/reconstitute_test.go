package config_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
)

// The property v0.2 rests on: a node's Span is exactly the bytes of that node,
// and everything between spans carries no meaning.
//
// The design spec states it as "the spans of all nodes, plus the gaps between
// them, reconstitute the file byte by byte", and calls it the test that has to
// break "before there is any code in production that can write".
//
// **Taken literally, that sentence cannot fail.** If the gap between two spans
// is the bytes between them -- and there is nothing else it could be -- then
// concatenating spans and gaps returns the source by construction, for ANY set
// of ordered spans, including one that is wrong everywhere. It is the shape of
// the tokenizer fuzz this project already threw away: four assertions, one of
// them `src[Start:End] == Raw` against code that built Raw by slicing
// `src[start:pos]`. Nine and a half million executions proving nothing.
//
// The intent is kept and the formulation replaced by two properties that can
// fail, each aimed at a distinct way a byte substitution corrupts a file:
//
//	PARTITION   spans are ordered, do not overlap, children live inside the
//	            BODY of their parent, and every byte outside every span is
//	            inert -- whitespace, a comment, or an enumerated exception. A
//	            span one byte short leaves a ';' in a gap, and a write against
//	            it duplicates the terminator.
//
//	REPARSE     the text of a Span, parsed on its own, gives back one node
//	            equal to the original. A boundary off by one changes the parse
//	            or fails it. A write replaces exactly those bytes, so this is
//	            what says the replacement lands on a whole directive.
//
// Both are verified able to fail by TestTheReconstitutionPropertiesCanFail,
// which is why they return problems instead of failing in place: a check whose
// failure was never observed is a claim, and the negative test needs to observe
// it without a synthetic *testing.T -- require's FailNow on one of those calls
// runtime.Goexit and takes the test with it.

// fixtures are the files this property runs over: every .conf in the repository
// that is meant to PARSE.
//
// include_broken.conf, syntax_error.conf and include_no_args.conf are excluded
// because being refused is their job -- include_no_args holds `include;` with no
// argument, which nginx rejects. A fixture that does not parse has no spans, so
// it can say nothing about whether spans partition a file.
func fixtures() []string {
	return []string{
		"testdata/simple.conf",
		"testdata/syntax_surface.conf",
		"../../test/bench/testdata/lua_surface.conf",
	}
}

func TestSpansPartitionTheFile(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(filepath.Base(f), func(t *testing.T) {
			file := parseFixture(t, f)
			problems := partitionProblems(file.Source, file.Nodes, 0, len(file.Source), "")
			problems = append(problems, commentAccountingProblems(file.Nodes)...)
			require.Emptyf(t, problems, "%d problem(s):\n%s",
				len(problems), strings.Join(problems, "\n"))
		})
	}
}

func TestSpanTextReparsesToTheSameNode(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(filepath.Base(f), func(t *testing.T) {
			file := parseFixture(t, f)
			dir := t.TempDir()

			var problems []string
			checked := 0
			walkNodes(file.Nodes, func(n *config.Node) {
				if n.IsComment() {
					return
				}
				problems = append(problems, reparseProblems(dir, file.Source, n)...)
				checked++
			})
			require.NotZero(t, checked, "the fixture produced no nodes to check")
			require.Emptyf(t, problems, "%d problem(s):\n%s",
				len(problems), strings.Join(problems, "\n"))
		})
	}
}

// partitionProblems checks one level and recurses. from and to bound the region
// the nodes must live in.
func partitionProblems(src []byte, nodes []*config.Node, from, to int, path string) []string {
	var problems []string
	cursor := from

	for _, n := range nodes {
		// Comment nodes are deliberately NOT part of the sibling sequence,
		// and the reason is a finding rather than a convenience.
		//
		// A comment inside a directive's head -- `server_name a.com # prod\n
		// b.com;` -- is represented TWICE: once in the parent's HeadComments
		// (node.go), and once as a sibling node whose span sits inside the
		// parent's. Measured: the parent spans [0,33) and the comment node
		// spans [18,24). Treating it as a sibling reports an overlap that is
		// really the design working as intended, since HeadComments exists so
		// a v0.2 rewrite of the head does not erase the comment.
		//
		// So comments are checked by their own property instead, which is
		// stronger than the sibling rule would have been: see
		// commentAccountingProblems.
		if n.IsComment() {
			continue
		}

		where := path + "/" + n.Directive

		switch {
		case n.Span.Start > n.Span.End:
			problems = append(problems, fmt.Sprintf("%s: span is inverted (%d > %d)",
				where, n.Span.Start, n.Span.End))
			continue
		case n.Span.Start < cursor:
			problems = append(problems, fmt.Sprintf(
				"%s: span starts at %d, before the end of what came before (%d) -- spans overlap",
				where, n.Span.Start, cursor))
			continue
		case n.Span.End > to:
			problems = append(problems, fmt.Sprintf(
				"%s: span ends at %d, past the region it belongs to (%d)",
				where, n.Span.End, to))
			continue
		}

		problems = append(problems, inertProblems(src, cursor, n.Span.Start, where+" gap before")...)
		cursor = n.Span.End

		if len(n.Block) > 0 {
			inner, err := blockBody(src, n)
			if err != "" {
				problems = append(problems, where+": "+err)
				continue
			}
			problems = append(problems,
				partitionProblems(src, n.Block, inner.Start, inner.End, where)...)
		}
	}

	return append(problems, inertProblems(src, cursor, to, path+" gap after the last node")...)
}

// commentAccountingProblems requires every comment to be accounted for exactly
// once, which is the property a write depends on and the sibling rule could not
// express.
//
// The question is about the HEAD span, not the whole span, and getting that
// wrong is what the first version of this check did. A comment inside
// `http { ... }` is inside http's span, but it sits between http's CHILDREN --
// gap material at that level, which no write reaches, because a write targets a
// child's span or http's head, never the middle of http's body. Requiring http
// to list it produced eleven false findings on one fixture.
//
// The dangerous case is narrower: a comment inside a directive's HEAD, between
// its name and its terminator. `server_name a.com # prod\n b.com;` is the
// shape, and a `set` that replaces the head span deletes the comment. So:
//
//	INSIDE some directive's HeadSpan  =>  it MUST be in that directive's
//	                                      HeadComments, or a write over the
//	                                      head erases a comment the user wrote
//	                                      with nothing in the tree recording it
//
//	anywhere else                     =>  gap material, covered by
//	                                      inertProblems
func commentAccountingProblems(nodes []*config.Node) []string {
	var comments, directives []*config.Node
	walkNodes(nodes, func(n *config.Node) {
		if n.IsComment() {
			comments = append(comments, n)
			return
		}
		directives = append(directives, n)
	})

	var problems []string
	for _, c := range comments {
		for _, d := range directives {
			if c.Span.Start < d.HeadSpan.Start || c.Span.End > d.HeadSpan.End {
				continue
			}

			var listed bool
			for _, hc := range d.HeadComments {
				if hc == c.Span {
					listed = true
					break
				}
			}
			if !listed {
				problems = append(problems, fmt.Sprintf(
					"the comment at [%d,%d) is inside the HEAD of %q [%d,%d) but is not in "+
						"its HeadComments %v -- a write over the head would delete it and "+
						"nothing in the tree would say it had been there",
					c.Span.Start, c.Span.End, d.Directive,
					d.HeadSpan.Start, d.HeadSpan.End, d.HeadComments))
			}
		}
	}
	return problems
}

// blockBody returns the region between the "{" that opens a block and the "}"
// that closes it -- the region the children have to live in.
//
// The children do NOT live in the parent's whole span: that span also covers
// the parent's own name, its arguments and both braces. Passing it as the
// region made the first version of this test report the parent's head as an
// unattributed gap, which is a bug in the test rather than in the aligner.
//
// The "{" is found from the end of HeadSpan, which is exactly "name +
// arguments" (node.go), and the "}" is the last byte of the span. Both are
// asserted rather than assumed: an aligner that stopped producing them is a
// finding, not a reason to guess.
func blockBody(src []byte, n *config.Node) (config.Span, string) {
	if n.HeadSpan.End < n.Span.Start || n.HeadSpan.End > n.Span.End {
		return config.Span{}, fmt.Sprintf("head span [%d,%d) is not inside the span [%d,%d)",
			n.HeadSpan.Start, n.HeadSpan.End, n.Span.Start, n.Span.End)
	}

	open := bytes.IndexByte(src[n.HeadSpan.End:n.Span.End], '{')
	if open < 0 {
		return config.Span{}, "the node opens a block but there is no \"{\" after its head"
	}
	open += n.HeadSpan.End

	if n.Span.End == 0 || src[n.Span.End-1] != '}' {
		return config.Span{}, "the node opens a block but its span does not end in \"}\""
	}

	return config.Span{Start: open + 1, End: n.Span.End - 1}, ""
}

// inertProblems reports anything in src[from:to) that carries meaning.
//
// Inert is whitespace, a comment, or the one enumerated exception below. It is
// deliberately NOT "anything the parser ignored": a directive the aligner
// forgot to span would also be ignored, and would pass such a rule.
func inertProblems(src []byte, from, to int, where string) []string {
	if from >= to || from < 0 || to > len(src) {
		return nil
	}

	region := src[from:to]
	for i := 0; i < len(region); {
		switch {
		case region[i] == '#':
			// A comment runs to end of line. It is inert for a write: it
			// belongs to no span at this level, and a substitution that
			// respects span boundaries never reaches it.
			for i < len(region) && region[i] != '\n' {
				i++
			}

		case region[i] == '\\':
			// The one enumerated exception, inherited rather than chosen: a
			// backslash alone at end of file, or a backslash plus the \r after
			// it, produces no token at all -- crossplane leaves the same gap.
			// Documented in checkCoverage, fuzz_test.go.
			i++

		default:
			r, size := utf8.DecodeRune(region[i:])
			if size == 0 {
				size = 1
			}
			if !unicode.IsSpace(r) {
				return []string{fmt.Sprintf(
					"%s: byte %d is %q, which is not whitespace, not a comment and not "+
						"the enumerated backslash -- it belongs to no span, so a write "+
						"replacing the spans around it would leave it behind (gap: %q)",
					where, from+i, string(r), string(region))}
			}
			i += size
		}
	}
	return nil
}

// reparseProblems parses the text of one span on its own and requires the same
// node back.
//
// A trailing newline is added because a directive at the very end of input with
// nothing after it is a different input to the lexer. Nothing else is
// normalised: no trimming, no reindentation.
func reparseProblems(dir string, src []byte, n *config.Node) []string {
	text := string(src[n.Span.Start:n.Span.End])
	path := filepath.Join(dir, "one.conf")
	if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
		return []string{"could not write the probe file: " + err.Error()}
	}

	sub, err := config.Parse(config.ParseOptions{Path: path})
	if err != nil {
		return []string{fmt.Sprintf(
			"the span of %q does not parse on its own, so it is not a whole directive: %v\n  %q",
			n.Directive, err, text)}
	}
	if len(sub.Files) == 0 {
		return []string{fmt.Sprintf("the span of %q produced no file:\n  %q", n.Directive, text)}
	}

	// One DIRECTIVE, and any number of comments alongside it.
	//
	// Not "exactly one node": a comment inside the head comes back as its own
	// node next to the directive, which is how crossplane represents it and why
	// HeadComments exists. `server_name a.com # prod\n b.com;` reparses as two
	// nodes, and that is the input round-tripping correctly rather than a span
	// being wrong.
	var directives, comments []*config.Node
	for _, got := range sub.Files[0].Nodes {
		if got.IsComment() {
			comments = append(comments, got)
			continue
		}
		directives = append(directives, got)
	}
	if len(directives) != 1 {
		return []string{fmt.Sprintf("the span of %q parses as %d directives instead of one:\n  %q",
			n.Directive, len(directives), text)}
	}

	got := directives[0]

	// The comments that come back are NOT counted against HeadComments, and
	// that was learned by trying: the fuzz produced `listen 80 # c\n;` within
	// seconds. There the comment sits AFTER the last argument and before the
	// terminator, so it is inside Span, outside HeadSpan, and absent from
	// HeadComments -- which is not a defect. HeadComments is documented as the
	// comments inside HeadSpan, and this one is not one of them.
	//
	// See TestACommentAfterTheLastArgumentIsOutsideTheHead for why that is safe
	// for the operations v0.2 plans. What DOES have to hold -- every comment
	// inside a HeadSpan is recorded -- is asserted directly on the original
	// tree by commentAccountingProblems, which needs no reparse to do it.
	_ = comments
	var problems0 []string
	problems := problems0
	if got.Directive != n.Directive {
		problems = append(problems, fmt.Sprintf("the span of %q reparses as %q:\n  %q",
			n.Directive, got.Directive, text))
	}
	if !equalArgs(got.Args, n.Args) {
		problems = append(problems, fmt.Sprintf("the span of %q reparses with different arguments %q vs %q:\n  %q",
			n.Directive, got.Args, n.Args, text))
	}
	if got.HasBlock() != n.HasBlock() {
		problems = append(problems, fmt.Sprintf(
			"the span of %q disagrees on whether it opens a block:\n  %q", n.Directive, text))
	}
	return problems
}

// The negative verification this project requires of any property test: break
// it on purpose and confirm it accuses. Without this, both properties above are
// claims rather than checks.
func TestTheReconstitutionPropertiesCanFail(t *testing.T) {
	file := parseFixture(t, "testdata/syntax_surface.conf")

	// A directive with a terminator and no block, so shrinking its span leaves
	// the ';' stranded -- the exact shape that makes a write duplicate it.
	var target *config.Node
	walkNodes(file.Nodes, func(n *config.Node) {
		if target == nil && !n.IsComment() && !n.HasBlock() && n.Span.Len() > 2 {
			target = n
		}
	})
	require.NotNil(t, target, "the fixture has no plain directive to break")

	t.Run("partition catches a span one byte short", func(t *testing.T) {
		original := target.Span
		target.Span.End--
		defer func() { target.Span = original }()

		require.NotEmpty(t, partitionProblems(file.Source, file.Nodes, 0, len(file.Source), ""),
			"a span one byte short went unnoticed, so the partition property is decoration")
	})

	t.Run("reparse catches a span one byte short", func(t *testing.T) {
		shrunk := *target
		shrunk.Span.End--

		require.NotEmpty(t, reparseProblems(t.TempDir(), file.Source, &shrunk),
			"a span one byte short still reparsed to the same node, so the reparse "+
				"property is decoration")
	})

	t.Run("partition catches two spans that overlap", func(t *testing.T) {
		// Two root DIRECTIVES: comment nodes are skipped by the partition
		// walk on purpose, so breaking one would prove nothing.
		var roots []*config.Node
		for _, n := range file.Nodes {
			if !n.IsComment() {
				roots = append(roots, n)
			}
		}
		require.GreaterOrEqual(t, len(roots), 2, "the fixture needs two root directives")

		original := roots[1].Span
		roots[1].Span.Start = roots[0].Span.End - 1
		defer func() { roots[1].Span = original }()

		require.NotEmpty(t, partitionProblems(file.Source, file.Nodes, 0, len(file.Source), ""),
			"overlapping spans went unnoticed")
	})

	t.Run("partition catches a child span outside its parent", func(t *testing.T) {
		var parent *config.Node
		walkNodes(file.Nodes, func(n *config.Node) {
			if parent == nil && len(n.Block) > 0 {
				parent = n
			}
		})
		require.NotNil(t, parent)

		child := parent.Block[0]
		original := child.Span
		child.Span.End = parent.Span.End + 1
		defer func() { child.Span = original }()

		require.NotEmpty(t, partitionProblems(file.Source, file.Nodes, 0, len(file.Source), ""),
			"a child span reaching past its parent went unnoticed")
	})
}

// --- helpers ---------------------------------------------------------------

func parseFixture(t *testing.T, rel string) *config.File {
	t.Helper()

	src, err := os.ReadFile(rel)
	require.NoErrorf(t, err, "fixture %s", rel)

	// Copied into a temp dir so an `include` inside the fixture resolves
	// against a directory the test controls.
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.Base(rel))
	require.NoError(t, os.WriteFile(path, src, 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: path})
	require.NoErrorf(t, err, "the fixture %s does not parse", rel)
	require.NotEmpty(t, tree.Files)

	for _, f := range tree.Files {
		if strings.HasSuffix(f.Path, filepath.Base(rel)) {
			require.NotEmpty(t, f.Source, "the fixture parsed with no source")
			return f
		}
	}
	t.Fatalf("the parsed tree does not contain %s", rel)
	return nil
}

func walkNodes(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walkNodes(n.Block, fn)
	}
}

func equalArgs(a, b []string) bool {
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

// FuzzReconstitution runs both properties over generated input.
//
// The fixtures above are files somebody wrote on purpose; this is the half that
// finds the shapes nobody would write. It is a separate target from
// FuzzAlignment because the two ask different questions: FuzzAlignment checks
// that our token stream matches crossplane's and that spans nest, while this
// asks whether the spans TILE the file with nothing meaningful left over --
// which is the question a byte substitution depends on.
//
// Inputs crossplane refuses are out of scope: a file with no tree has no spans.
func FuzzReconstitution(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add("http { server { location / { proxy_pass http://a; } } }")
	f.Add("# c\nevents {}")
	f.Add("events { worker_connections 16; } # trailing\n")
	f.Add(`add_header X-A "b; c";`)
	f.Add("map $a $b {\n default 0;\n # com\n}")
	f.Add("location /api # gw\n{ proxy_pass http://a; }")
	f.Add("server_name a.com # prod\n  b.com;")
	f.Add("content_by_lua_block { local s = [[ } ]] }")
	f.Add("content_by_lua_block { -- c\n ngx.say(1) }")
	f.Add("upstream u {\n\tserver a;\r\n\tserver b;\n}")
	f.Add("\n\n  server {\n\n  }\n\n")

	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, src string) {
		path := filepath.Join(dir, "fuzz.conf")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Skip()
		}
		tree, err := config.Parse(config.ParseOptions{Path: path})
		if err != nil || len(tree.Files) == 0 {
			return
		}
		file := tree.Files[0]
		if len(file.Source) == 0 {
			return
		}

		problems := partitionProblems(file.Source, file.Nodes, 0, len(file.Source), "")
		problems = append(problems, commentAccountingProblems(file.Nodes)...)
		if len(problems) > 0 {
			t.Fatalf("spans do not partition %q:\n%s", src, strings.Join(problems, "\n"))
		}

		probe := t.TempDir()
		walkNodes(file.Nodes, func(n *config.Node) {
			if n.IsComment() {
				return
			}
			if problems := reparseProblems(probe, file.Source, n); len(problems) > 0 {
				t.Fatalf("the span of a node does not reparse, in %q:\n%s",
					src, strings.Join(problems, "\n"))
			}
		})
	})
}

// Enumerated fact, found by FuzzReconstitution rather than by reading: a
// comment placed after a directive's last argument and before its terminator
// lives inside Span, outside HeadSpan, and is recorded nowhere.
//
// It is a gap in the sense Phase 0.2 uses the word -- something outside every
// accounting -- and it is enumerated instead of being closed, because for the
// operations v0.2 plans it is safe:
//
//	set  replaces HeadSpan, which stops before the comment, so the comment
//	     survives the edit. Asserted below, since that is the whole reason this
//	     is acceptable rather than a defect.
//
//	rm   removes the whole Span, and the comment goes with it. That is the
//	     right answer: it is on the same line as the directive being removed.
//
// If a future operation replaces Span while intending to keep the directive --
// there is no such operation planned -- this becomes a defect and HeadComments
// has to grow to cover the tail.
func TestACommentAfterTheLastArgumentIsOutsideTheHead(t *testing.T) {
	file := parseFixtureFromString(t, "listen 80 # c\n;")

	var directive, comment *config.Node
	for _, n := range file.Nodes {
		if n.IsComment() {
			comment = n
			continue
		}
		directive = n
	}
	require.NotNil(t, directive)
	require.NotNil(t, comment)

	require.Equal(t, "listen 80", string(file.Source[directive.HeadSpan.Start:directive.HeadSpan.End]),
		"the head stops before the comment")
	require.Empty(t, directive.HeadComments,
		"the comment is outside the head, so HeadComments does not record it")
	require.True(t,
		comment.Span.Start >= directive.Span.Start && comment.Span.End <= directive.Span.End,
		"the comment is nonetheless inside the directive's span")
	require.Greater(t, comment.Span.Start, directive.HeadSpan.End,
		"and it sits after the head, which is what makes a head-only edit safe")

	// The reason this is enumerated rather than fixed: replacing the head
	// leaves the comment standing.
	edited := string(file.Source[:directive.HeadSpan.Start]) +
		"listen 8443" +
		string(file.Source[directive.HeadSpan.End:])
	require.Equal(t, "listen 8443 # c\n;", edited,
		"a set over the head has to preserve a comment that follows it")
}

func parseFixtureFromString(t *testing.T, src string) *config.File {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "n.conf")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: path})
	require.NoError(t, err)
	require.NotEmpty(t, tree.Files)
	return tree.Files[0]
}
