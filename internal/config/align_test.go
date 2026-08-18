package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHeadSpanCoversDirectiveAndArgs(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})
	require.NotNil(t, listen)

	require.Equal(t, "listen 443 ssl", string(src[listen.HeadSpan.Start:listen.HeadSpan.End]))
}

func TestSpanOfSimpleDirectiveEndsAtSemicolon(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.Equal(t, "listen 443 ssl;", string(src[listen.Span.Start:listen.Span.End]))
}

func TestSpanOfBlockEndsAtClosingBrace(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var upstream *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "upstream" {
			upstream = n
			return false
		}
		return true
	})
	require.NotNil(t, upstream)

	text := string(src[upstream.Span.Start:upstream.Span.End])
	require.True(t, strings.HasPrefix(text, "upstream backend_v1"))
	require.True(t, strings.HasSuffix(text, "}"))
	require.Contains(t, text, "server 10.0.0.1:8080;")

	require.Equal(t, "upstream backend_v1", string(src[upstream.HeadSpan.Start:upstream.HeadSpan.End]),
		"the head does not include the block")
}

func TestLineAndColumnComeFromTokenizer(t *testing.T) {
	tree := parseSimple(t)

	var serverName *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" {
			serverName = n
			return false
		}
		return true
	})
	require.NotNil(t, serverName)

	require.Greater(t, serverName.Line, 0)
	require.Greater(t, serverName.Column, 0)

	lines := strings.Split(string(tree.Files[0].Source), "\n")
	require.Contains(t, lines[serverName.Line-1], "server_name")
}

// Quotes containing a semicolon are the case that breaks a naive alignment.
func TestAlignmentSurvivesQuotesWithSemicolon(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var addHeader *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "add_header" {
			addHeader = n
			return false
		}
		return true
	})
	require.NotNil(t, addHeader)

	require.Equal(t, `add_header X-A "b; c";`, string(src[addHeader.Span.Start:addHeader.Span.End]))
}

// Containment invariant: the span of a child lives inside the span of its parent.
func TestChildSpansAreContainedInParent(t *testing.T) {
	tree := parseSimple(t)

	var check func(nodes []*config.Node, parent *config.Node)
	check = func(nodes []*config.Node, parent *config.Node) {
		prevEnd := -1
		for _, n := range nodes {
			if parent != nil {
				require.GreaterOrEqual(t, n.Span.Start, parent.Span.Start,
					"%s starts before its parent %s", n.Directive, parent.Directive)
				require.LessOrEqual(t, n.Span.End, parent.Span.End,
					"%s ends after its parent %s", n.Directive, parent.Directive)
			}
			require.GreaterOrEqual(t, n.Span.Start, prevEnd,
				"%s overlaps the previous sibling", n.Directive)
			prevEnd = n.Span.End
			check(n.Block, n)
		}
	}

	for _, f := range tree.Files {
		check(f.Nodes, nil)
	}
}

// Coverage: every significant byte of the file belongs to the span of some
// root-level node. This is the concrete formulation of the property that holds
// the architecture up: if it holds, the token-tree matching is correct.
func TestRootSpansCoverEverySignificantByte(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	covered := make([]bool, len(src))
	for _, n := range tree.Files[0].Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			covered[i] = true
		}
	}

	for i, b := range src {
		if covered[i] {
			continue
		}
		require.True(t, b == ' ' || b == '\t' || b == '\n' || b == '\r',
			"byte %d (%q) on an uncovered line is not whitespace", i, string(b))
	}
}

// A comment between arguments: crossplane/parse.go:286-290 strips "# prod"
// out of Args and crossplane/parse.go:435-445 attaches it as a sibling "#"
// node after the whole directive (Task 9, defect 1).
func TestCommentBetweenArgsDoesNotBreakAlignment(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.conf")
	src := "server_name a.com # prod\n  b.com;\nlisten 80;\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	nodes := tree.Files[0].Nodes
	require.Len(t, nodes, 3, "server_name, the comment from the arguments and listen")

	serverName := nodes[0]
	require.Equal(t, "server_name", serverName.Directive)
	require.Equal(t, []string{"a.com", "b.com"}, serverName.Args)
	require.Equal(t, "server_name a.com # prod\n  b.com;", string(src[serverName.Span.Start:serverName.Span.End]))

	comment := nodes[1]
	require.True(t, comment.IsComment())
	require.NotNil(t, comment.Comment)
	require.Equal(t, " prod", *comment.Comment)
	require.Equal(t, "# prod", string(src[comment.Span.Start:comment.Span.End]))

	listen := nodes[2]
	require.Equal(t, "listen", listen.Directive)
	require.Equal(t, "listen 80;", string(src[listen.Span.Start:listen.Span.End]))
}

// A comment between the name/arguments and the block: same crossplane
// mechanism, but now the "#" node lands after the directive AND after its
// block (Task 9, defect 1, second example).
func TestCommentBeforeBlockDoesNotBreakAlignment(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.conf")
	src := "location /api # gw\n{ proxy_pass http://a; }\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	nodes := tree.Files[0].Nodes
	require.Len(t, nodes, 2, "location and the comment from its arguments")

	location := nodes[0]
	require.Equal(t, "location", location.Directive)
	require.Equal(t, []string{"/api"}, location.Args)
	require.True(t, location.HasBlock())
	require.Equal(t, "location /api", string(src[location.HeadSpan.Start:location.HeadSpan.End]),
		"the head span does not include the comment, which comes after the last arg")
	require.Equal(t, "location /api # gw\n{ proxy_pass http://a; }",
		string(src[location.Span.Start:location.Span.End]))

	comment := nodes[1]
	require.True(t, comment.IsComment())
	require.Equal(t, "# gw", string(src[comment.Span.Start:comment.Span.End]))
}

// if with isolated parentheses: crossplane/util.go:71-86 (prepareIfArgs)
// strips "(" and ")" out of Args when they come isolated, so len(n.Args) does
// not count the real word tokens between "if" and the terminator (Task 9,
// defect 2).
func TestIfWithSpacedParenthesesAligns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.conf")
	src := "http { server { if ( $a = b ) { return 404; } } }\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	var ifNode *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "if" {
			ifNode = n
			return false
		}
		return true
	})
	require.NotNil(t, ifNode)
	require.Equal(t, []string{"$a", "=", "b"}, ifNode.Args)
	require.Equal(t, "if ( $a = b )", string(src[ifNode.HeadSpan.Start:ifNode.HeadSpan.End]))
	require.True(t, ifNode.HasBlock())
	require.Equal(t, "if ( $a = b ) { return 404; }", string(src[ifNode.Span.Start:ifNode.Span.End]))
}

func TestEmptyBlockIsRecognized(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.conf")
	require.NoError(t, os.WriteFile(p, []byte("events {}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	events := tree.Files[0].Nodes[0]
	require.Equal(t, "events", events.Directive)
	require.True(t, events.HasBlock(), "events {} opens a block, empty though it is")
	require.Equal(t, "events {}", string(tree.Files[0].Source[events.Span.Start:events.Span.End]))
}
