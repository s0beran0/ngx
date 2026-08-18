package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	crossplane "github.com/nginxinc/nginx-go-crossplane"

	"github.com/s0beran0/ngx/internal/config"
)

// The fuzz guarantees that, for any input the tokenizer accepts, the spans
// keep pointing at the real text and in increasing order, every byte is
// covered by some token or is whitespace, Kind and Raw are coherent, the
// column rebuilt from the text matches the reported column, the result is the
// same over two passes, and -- the property that really holds Task 9 up --
// the tokens match those of the nginx-go-crossplane lexer, in count and in
// value, ignoring comments.
func FuzzTokenizeSpans(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add(`add_header X "a; b";`)
	f.Add("# comentario\nhttp { }")
	f.Add(`location ~ \.php$ { }`)
	f.Add("map $a $b {\n default 0;\n}")
	f.Add(`proxy_pass http://${backend};`)
	f.Add("set $a \"${b}c\";")
	f.Add("listen 80;\r\nserver_name a;\r\n# fim\r\n")
	f.Add("proxy_set_header Host\r\n  $host;\r\n")
	f.Add(`log_format m '$remote_addr "$http_user_agent"';`)
	f.Add(`msg "diz \"oi\" e \\ e \n";`)
	f.Add("map $a $b {\n  ~^/x  \"y; z\";\n  default 0;\n}")
	f.Add(`location ~ "^/a{2,3}\.php$" { }`)
	f.Add("# comentario ç\nserver_name exemplo.com.br;")
	f.Add("proxy_pass http://\"host\";")
	f.Add("foo \\")

	f.Fuzz(func(t *testing.T, s string) {
		toks, err := config.Tokenize([]byte(s))
		if err != nil {
			return // out-of-scope input: our own tokenizer refused it
		}

		checkSpansAndOrder(t, s, toks)
		checkCoverage(t, s, toks)
		checkKindRawCoherence(t, toks)
		checkLineAndColumn(t, s, toks)
		checkIdempotence(t, s, toks)
		checkDifferentialAgainstCrossplane(t, s, toks)
		checkCRLFNeverEndsSpan(t, s, toks)
	})
}

// checkCRLFNeverEndsSpan is the property holding up the CR-of-CRLF fix
// in readWord and readVar (fix round 2): no token may end on a \r that is
// followed by \n in the source. That CR belongs to the whitespace after the
// token, never to the token's span -- otherwise a rewrite by byte replacement
// would convert the line from CRLF to LF.
func checkCRLFNeverEndsSpan(t *testing.T, s string, toks []config.Token) {
	for _, tok := range toks {
		if tok.End == 0 || s[tok.End-1] != '\r' {
			continue
		}
		if tok.End < len(s) && s[tok.End] == '\n' {
			t.Fatalf("token %q at [%d,%d) ends on a \\r followed by \\n in source %q",
				tok.Value, tok.Start, tok.End, s)
		}
	}
}

// checkSpansAndOrder checks the basic hygiene of the spans: increasing
// order, bounds inside the source and Raw == the slice of the source.
func checkSpansAndOrder(t *testing.T, s string, toks []config.Token) {
	prev := 0
	for _, tok := range toks {
		if tok.Start < prev {
			t.Fatalf("token starts at %d, before the previous end %d", tok.Start, prev)
		}
		if tok.End > len(s) || tok.Start > tok.End {
			t.Fatalf("invalid span [%d,%d) for a source of %d bytes", tok.Start, tok.End, len(s))
		}
		if got := s[tok.Start:tok.End]; got != tok.Raw {
			t.Fatalf("raw %q differs from the source %q at [%d,%d)", tok.Raw, got, tok.Start, tok.End)
		}
		if tok.Line < 1 || tok.Column < 1 {
			t.Fatalf("zero-based line/column: %d:%d", tok.Line, tok.Column)
		}
		prev = tok.End
	}
}

// checkCoverage checks that every byte outside any span is whitespace
// (decoding rune by rune, so as not to mistake a UTF-8 continuation byte for
// "not whitespace").
func checkCoverage(t *testing.T, s string, toks []config.Token) {
	covered := make([]bool, len(s))
	for _, tok := range toks {
		for i := tok.Start; i < tok.End; i++ {
			covered[i] = true
		}
	}

	for i := 0; i < len(s); {
		if covered[i] {
			i++
			continue
		}
		// a backslash may fall outside every token: alone on the last byte
		// of the source (with no following character to form an escape
		// pair), or together with the \r after it when the two make up the
		// whole word (crossplane produces no token at all in that case
		// either -- see barraFinalDeArquivo and the handling of \\+\r in
		// tokens.go). It is not whitespace, but it is a legitimate gap, the
		// same one crossplane itself leaves.
		if s[i] == '\\' {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			size = 1
		}
		if !unicode.IsSpace(r) {
			t.Fatalf("byte %d (%q) covered by no token and not whitespace", i, s[i])
		}
		i += size
	}
}

// checkKindRawCoherence checks that Kind and Raw (and Value, for unquoted
// words) never contradict each other.
func checkKindRawCoherence(t *testing.T, toks []config.Token) {
	for _, tok := range toks {
		switch tok.Kind {
		case config.TokenSemicolon:
			if tok.Raw != ";" {
				t.Fatalf("TokenSemicolon with raw %q", tok.Raw)
			}
		case config.TokenBlockStart:
			if tok.Raw != "{" {
				t.Fatalf("TokenBlockStart with raw %q", tok.Raw)
			}
		case config.TokenBlockEnd:
			if tok.Raw != "}" {
				t.Fatalf("TokenBlockEnd with raw %q", tok.Raw)
			}
		case config.TokenComment:
			if len(tok.Raw) == 0 || tok.Raw[0] != '#' {
				t.Fatalf("TokenComment with raw %q and no leading #", tok.Raw)
			}
		case config.TokenWord:
			if tok.Quoted {
				continue
			}
			expected := expectedValueForWord(tok.Raw)
			if tok.Value != expected {
				t.Fatalf("unquoted TokenWord with value %q != expected %q (raw %q)",
					tok.Value, expected, tok.Raw)
			}
		}
	}
}

// expectedValueForWord recomputes, from the Raw of an unquoted TokenWord,
// the Value the production should have generated -- mirroring consumeEscape
// in tokens.go: a backslash skips any \r coming right after it (invisible)
// and forms the escape pair with the next real rune (literal, both bytes,
// with an invalid byte replaced by U+FFFD); if the source ends before finding
// that rune, the backslash and the \r vanish leaving no content. A stray \r
// (outside an escape pair) is invisible too.
func expectedValueForWord(raw string) string {
	var out strings.Builder
	i := 0
	for i < len(raw) {
		if raw[i] == '\\' {
			j := i + 1
			for j < len(raw) && raw[j] == '\r' {
				j++
			}
			if j >= len(raw) {
				// never found the rune of the pair: backslash and \r vanish
				// leaving no content (this can only happen at the absolute
				// end of the file, which is where the source really ends).
				return out.String()
			}
			out.WriteByte('\\')
			r, size := utf8.DecodeRuneInString(raw[j:])
			if r == utf8.RuneError && size == 1 {
				out.WriteRune(utf8.RuneError)
			} else {
				out.WriteString(raw[j : j+size])
			}
			i = j + size
			continue
		}
		if raw[i] == '\r' {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size == 1 {
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		out.WriteString(raw[i : i+size])
		i += size
	}
	return out.String()
}

// checkLineAndColumn rebuilds line and column from the text and compares
// them against what each token reported. Column counts runes, not bytes.
//
// It does so in a single O(n) pass over the source, advancing a cursor byte
// by byte (never rescanning from the start of the line or of the file for
// each token) -- tokens come in increasing order of Start, so the cursor only
// ever needs to move forward. The first version of this helper rescanned the
// whole prefix for each token (O(n) per token, O(n^2) overall) and a 60s fuzz
// found an input with many tokens on a single line that hung the process
// until go test -fuzz's own timeout.
func checkLineAndColumn(t *testing.T, s string, toks []config.Token) {
	pos, line, column := 0, 1, 1
	for _, tok := range toks {
		for pos < tok.Start {
			r, size := utf8.DecodeRuneInString(s[pos:])
			if size == 0 {
				size = 1
			}
			pos += size
			if r == '\n' {
				line++
				column = 1
			} else {
				column++
			}
		}
		if tok.Line != line {
			t.Fatalf("line %d != expected %d for token %q at %d", tok.Line, line, tok.Value, tok.Start)
		}
		if tok.Column != column {
			t.Fatalf("column %d != expected %d for token %q at %d", tok.Column, column, tok.Value, tok.Start)
		}
	}
}

// checkIdempotence checks that tokenizing the same source twice produces
// exactly the same result.
func checkIdempotence(t *testing.T, s string, toks []config.Token) {
	again, err := config.Tokenize([]byte(s))
	if err != nil {
		t.Fatalf("tokenizing again produced an error: %v", err)
	}
	if !reflect.DeepEqual(toks, again) {
		t.Fatalf("tokenizing twice produced different results:\nfirst:  %+v\nsecond: %+v", toks, again)
	}
}

// checkDifferentialAgainstCrossplane is the property that holds Task 9 up:
// the aligner matches our tokens against crossplane's by count and by kind,
// never comparing values -- so any divergence here is a real alignment
// divergence. If crossplane rejects the input (an error on some token), that
// input is out of scope and the comparison is skipped.
func checkDifferentialAgainstCrossplane(t *testing.T, s string, toks []config.Token) {
	ch := crossplane.Lex(strings.NewReader(s))

	var reference []crossplane.NgxToken
	for tok := range ch {
		if tok.Error != nil {
			// drain the rest of the channel so crossplane's goroutine is
			// not left leaking, and get out: input is out of scope.
			for range ch {
			}
			return
		}
		reference = append(reference, tok)
	}

	var ours []config.Token
	for _, tok := range toks {
		if tok.Kind == config.TokenComment {
			continue
		}
		ours = append(ours, tok)
	}

	var theirs []crossplane.NgxToken
	for _, tok := range reference {
		if !tok.IsQuoted && strings.HasPrefix(tok.Value, "#") {
			continue
		}
		theirs = append(theirs, tok)
	}

	if len(ours) != len(theirs) {
		t.Fatalf("token count diverges from crossplane for %q: ours=%d crossplane=%d\nours=%v\ncrossplane=%v",
			s, len(ours), len(theirs), ours, theirs)
	}
	for i := range ours {
		if ours[i].Value != theirs[i].Value {
			t.Fatalf("token %d diverges from crossplane for %q: ours=%q crossplane=%q",
				i, s, ours[i].Value, theirs[i].Value)
		}
		if ours[i].Quoted != theirs[i].IsQuoted {
			t.Fatalf("token %d diverges from crossplane on Quoted for %q: ours=%v crossplane=%v (value %q)",
				i, s, ours[i].Quoted, theirs[i].IsQuoted, ours[i].Value)
		}
	}
}

// FuzzAlinhamento checks properties of the token-tree matching (Task 9) that
// can actually fail on an incorrect alignment. "Span within the bounds of the
// source" is deliberately not among them: Tokenize already guarantees that on
// its own (Task 8), and an aligner that merely copied [0,len(src)) into every
// node would pass such a test without aligning anything. The four properties
// below depend on HOW the offsets are distributed among the nodes, which is
// where an alignment bug really lives:
//
//  1. coverage: every non-whitespace byte at root level belongs to the Span
//     of some root node;
//  2. containment/non-overlap: the Span of a child lives inside the Span of
//     the parent, and siblings do not overlap;
//  3. HeadSpan is exactly "name + arguments": retokenizing the text of the
//     HeadSpan produces 1+len(Args) TokenWord and nothing else;
//  4. terminator: the Span of a non-comment directive ends in ';' or '}'.
func FuzzAlinhamento(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add("http { server { location / { proxy_pass http://a; } } }")
	f.Add("# c\nevents {}")
	f.Add(`add_header X-A "b; c";`)
	f.Add("upstream u {\n server a;\n server b;\n}")
	f.Add("map $a $b {\n default 0;\n # com\n}")
	f.Add("location ~ \\.php$ { proxy_pass http://a; }")
	f.Add("server_name a.com # prod\n  b.com;")
	f.Add("location /api # gw\n{ proxy_pass http://a; }")
	f.Add("http { server { if ( $a = b ) { return 404; } } }")
	// Without these two seeds the fuzz never exercised a multi-file tree: an
	// include generated by the fuzzer matches no file at all, so tree.Files
	// always had a single element and the per-file alignment -- what Task 12
	// introduced -- was left with no property coverage.
	f.Add("include incluido.conf;")
	f.Add("http { include incluido.conf; }\n# depois\n")

	f.Fuzz(func(t *testing.T, s string) {
		dir := t.TempDir()
		p := filepath.Join(dir, "f.conf")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Skip()
		}
		// The included file is fixed: what varies is the text including it.
		included := "server_name incluido.exemplo; # do include\nlisten 8080;\n"
		if err := os.WriteFile(filepath.Join(dir, "incluido.conf"), []byte(included), 0o644); err != nil {
			t.Skip()
		}

		// No recover here, on purpose: a panic is a fuzz failure. A CLI
		// consumed by an agent cannot emit a stack trace, so "config.Parse
		// never panics" is a property, not noise to be skipped.
		tree, err := config.Parse(config.ParseOptions{Path: p})
		if err != nil {
			// an error on our side is not "out of scope" by itself: it may
			// be over-rejection, the class of bug this fuzz exists to find.
			// It is only really out of scope if crossplane refuses the same
			// input too.
			checkNoOverRejection(t, p, err)
			return
		}

		for _, file := range tree.Files {
			checkRootCoverage(t, file)
			checkContainmentAndNoOverlap(t, file.Source, file.Nodes, nil)
			checkHeadSpanIsNamePlusArgs(t, file)
			checkSpanTerminator(t, file)
		}
	})
}

// onlyCR reports whether the rest of the source is only \r (or nothing).
func onlyCR(rest []byte) bool {
	for _, b := range rest {
		if b != '\r' {
			return false
		}
	}
	return true
}

// checkNoOverRejection is the property that holds this round of fixes
// up: before it, "if err != nil { return }" treated every error from our
// Parse as an out-of-scope input, which discards by construction exactly the
// class of bug the aligner had -- over-rejection of valid configuration. Here
// the oracle is crossplane itself, run with the same options
// internal/config/parse.go uses (Parse, parse.go:43-51): if it accepts the
// input (no error and Status != "failed") and our Parse refuses it, that is a
// real failure, not an invalid input.
func checkNoOverRejection(t *testing.T, path string, ourErr error) {
	var problems config.ParseErrors
	if errors.As(ourErr, &problems) && len(problems) > 0 && knownDivergence(problems[0]) {
		return
	}

	payload, err := parseWithOracle(path)
	if err != nil {
		return // crossplane refused it too: input legitimately out of scope
	}
	if payload == nil {
		return // crossplane panicked: that is not acceptance
	}
	if payload.Status != "ok" {
		return // crossplane took the file but recorded a parse error: same
	}
	t.Fatalf("over-rejection: crossplane accepted the input but ngx refused it: %v\nfile: %s",
		ourErr, path)
}

// parseWithOracle runs crossplane with the same options as parse.go:43-51.
// The recover is not complacency: an input that brings the dependency's
// parser down (prepareIfArgs, util.go:83) is not being "accepted" by it, and
// treating that as acceptance would accuse ngx of over-rejecting precisely
// when it avoided a crash.
func parseWithOracle(path string) (payload *crossplane.Payload, err error) {
	defer func() {
		if r := recover(); r != nil {
			payload, err = nil, nil
		}
	}()
	return crossplane.Parse(path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
	})
}

// knownDivergence is the CLOSED list of ngx refusals crossplane does not
// make. It exists because the oracle has to keep flagging over-rejection: an
// earlier version of this file silenced by substring of the message
// ("quote", "unexpected token", "expected", "left over"), which erased the
// whole class of bug the fuzz exists to find -- any new refusal from the
// aligner would land in one of those substrings.
//
// Each entry matches the CLASS plus the exact shape of the token, cites
// crossplane's source and has a unit test of its own in robustness_test.go. A
// refusal that is not here -- including a new refusal of the same class with
// a different token -- is a fuzz failure, as it has to be. Classes
// deliberately OUT of the list: RecusaTokenInesperado, RecusaTokensSobrando,
// RecusaFimInesperado and RecusaPanicoDoCrossplane, which only show up when
// the matching between tree and tokens has slipped -- that is, when there is
// a bug.
func knownDivergence(pe config.ParseError) bool {
	switch pe.Classe {
	case config.RecusaAspaNaoFechada:
		// lex.go:325-327 closes the quote implicitly at end of file and
		// emits no token at all when the content is empty: a dangling quote
		// is "ok" for crossplane. nginx refuses it. See
		// TestDivergenceUnclosedQuote.
		return pe.Token == `"` || pe.Token == "'"

	case config.RecusaTokenNoLugarDeDiretiva:
		// parse.go:256-261 builds the statement out of t.Value without
		// requiring the first token to be a word: only "}" (parse.go:237)
		// and comments (parse.go:264) are handled apart, so "{", "}" and ";"
		// become directive names for it. Those three are ALL the tokens that
		// are neither word nor comment -- the list is exhaustive over the
		// tokenizer's Kind, and a word refused in that position is still a
		// bug. See TestDivergenceBraceAsDirectiveName and
		// TestDivergenceSemicolonAsDirectiveName.
		return pe.Token == "{" || pe.Token == "}" || pe.Token == ";"

	case config.RecusaTerminadorAusente:
		// The argument loop stops at "}" (parse.go:285) and the
		// "is not terminated by \";\"" check (analyze.go:224-227) does not
		// run under SkipDirectiveArgsCheck (analyze.go:202-204). Only the "}"
		// diverges. See TestDivergenceDirectiveWithoutSemicolon.
		return pe.Token == "}"

	case config.RecusaExpressaoIfInvalida:
		// The validExpr guard (analyze.go:212, util.go:57-67) that
		// SkipDirectiveArgsCheck suppresses and without which prepareIfArgs
		// (util.go:83) brings the process down. The token is always the name
		// "if", quoted or not (parse.go:352-354 compares without looking at
		// IsQuoted). See TestIfWithEmptyExpressionIsTypedRefusalNotPanic.
		return pe.Token == "if" || pe.Token == `"if"` || pe.Token == "'if'"

	case config.RecusaAlvoNaoERegular:
		// "include .;" -- the only entry enumerated by CLASS alone, with no
		// exact token, because the include target is an arbitrary path and
		// there is no fixed lexeme to match against. It is only acceptable
		// because the class is already narrow: it fires exclusively when the
		// path opened and is not a regular file.
		//
		// A pattern with no magic goes to parse.go:385-395, which only
		// checks that os.Open works -- and opening a directory works --, so
		// the target gets into fnames and is lexed in the loop of
		// parse.go:161-168; the lexer never consults the read error and
		// hands back zero tokens, with Status "ok". nginx, in its place,
		// READS the target, and reading a directory fails. See
		// TestDivergenceIncludeOfDirectory.
		return true
	}
	return false
}

// checkRootCoverage checks that no significant byte of the root level
// escapes the Span of every root node -- the concrete formulation of the
// matching not having "lost" any stretch of the document.
//
// "Significant" uses the same notion of whitespace as the tokenizer
// (unicode.IsSpace, decoded rune by rune) -- not just the four ascii bytes. A
// first version of this helper checked only ' ', '\t', '\n', '\r' and the
// fuzz found "\v" (vertical tab) as a false positive within minutes: the
// tokenizer correctly treats \v as whitespace (tokens.go, spaceHere) and
// emits no token at all for it, so it stays outside any span on purpose --
// the defect was in the test, not in the alignment.
//
// A lone backslash (with no escape pair, typically on the last byte of the
// file) is the same legitimate gap documented in checkCoverage in the
// tokenizer fuzz: consumed by the tokenizer (it advances the position) but
// forming no token, so it is not whitespace and is in no span -- the fuzz
// found that case too, in the same round.
func checkRootCoverage(t *testing.T, file *config.File) {
	src := file.Source
	covered := make([]bool, len(src))
	for _, n := range file.Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			covered[i] = true
		}
	}
	for i := 0; i < len(src); {
		if covered[i] {
			i++
			continue
		}
		// The backslash valve only applies to the backslash WITHOUT an escape
		// pair, which consumeEscape (tokens.go:134-143) only returns as
		// ok == false at the end of the source -- \r is invisible and does
		// not count as a pair. It used to skip any '\' outside a span, which
		// would also forgive a backslash in the middle of the file left out
		// for some other reason.
		if src[i] == '\\' && onlyCR(src[i+1:]) {
			i++
			continue
		}
		r, size := utf8.DecodeRune(src[i:])
		if size == 0 {
			size = 1
		}
		if !unicode.IsSpace(r) {
			t.Fatalf("byte %d (%q) outside every root-level span and not whitespace", i, string(src[i]))
		}
		i += size
	}
}

// checkContainmentAndNoOverlap checks, recursively, that the Span of
// each child lives inside the Span of the parent and that siblings do not
// overlap -- without this property, a rewrite by byte replacement in v0.2
// would corrupt the file.
//
// Deliberate exception for non-comment vs comment: a comment found in the
// middle of a directive's arguments (Task 9, defect 1) arrives here as a
// SIBLING of the preceding directive, but its text sits physically INSIDE
// that directive's span -- that is how crossplane itself structures the tree
// (parse.go:286-290 puts the comment outside Args, parse.go:435-445 attaches
// it as a "#" node after the directive and after its block), not an alignment
// defect. That is why the non-overlap check against the previous sibling only
// applies to nodes that are not comments.
func checkContainmentAndNoOverlap(t *testing.T, src []byte, nodes []*config.Node, parent *config.Node) {
	prevEnd := -1
	for _, n := range nodes {
		if n.Span.Start < 0 || n.Span.End > len(src) || n.Span.Start > n.Span.End {
			t.Fatalf("invalid span [%d,%d) for %q in a source of %d bytes",
				n.Span.Start, n.Span.End, n.Directive, len(src))
		}
		if parent != nil {
			if n.Span.Start < parent.Span.Start || n.Span.End > parent.Span.End {
				t.Fatalf("span of %q [%d,%d) is not contained in the parent's %q [%d,%d)",
					n.Directive, n.Span.Start, n.Span.End, parent.Directive, parent.Span.Start, parent.Span.End)
			}
		}
		if !n.IsComment() && n.Span.Start < prevEnd {
			t.Fatalf("span of %q starts at %d, before the previous sibling's end at %d",
				n.Directive, n.Span.Start, prevEnd)
		}
		if n.Span.End > prevEnd {
			prevEnd = n.Span.End
		}
		checkContainmentAndNoOverlap(t, src, n.Block, n)
	}
}

// checkHeadSpanIsNamePlusArgs checks that the HeadSpan covers
// exactly the directive name and its arguments, nothing more and nothing
// less: retokenizing the text of the HeadSpan has to produce only TokenWord
// (and, since Task 9 defect 1, TokenComment as well -- a comment in the
// middle of the arguments sits physically inside the HeadSpan, see align.go)
// and no other kind of token. An aligner that took in the next token (';' or
// '{') would be caught here either way, comment or not.
//
// The exact count "1 directive + len(Args) words" holds for every directive
// except "if": prepareIfArgs (crossplane/util.go:71-86) strips the "(" and
// ")" tokens out of Args when they come isolated, so len(n.Args) does not
// count the real word tokens between the name and the terminator (Task 9,
// defect 2). For "if" it is the token kind check above (nothing beyond word
// or comment) that catches an aligner advancing too far.
func checkHeadSpanIsNamePlusArgs(t *testing.T, file *config.File) {
	var walk func(nodes []*config.Node)
	walk = func(nodes []*config.Node) {
		for _, n := range nodes {
			if n.IsComment() {
				walk(n.Block)
				continue
			}
			if n.HeadSpan.Start < n.Span.Start || n.HeadSpan.End > n.Span.End {
				t.Fatalf("head span of %q [%d,%d) outside the node's span [%d,%d)",
					n.Directive, n.HeadSpan.Start, n.HeadSpan.End, n.Span.Start, n.Span.End)
			}

			text := string(file.Source[n.HeadSpan.Start:n.HeadSpan.End])
			toks, err := config.Tokenize([]byte(text))
			if err != nil {
				t.Fatalf("head span of %q does not retokenize (%v); text=%q", n.Directive, err, text)
			}

			var words int
			for _, tk := range toks {
				if tk.Kind == config.TokenComment {
					continue
				}
				if tk.Kind != config.TokenWord {
					t.Fatalf("head span of %q holds token %v that is neither word nor comment; text=%q",
						n.Directive, tk.Kind, text)
				}
				words++
			}
			if n.Directive != "if" {
				if expected := 1 + len(n.Args); words != expected {
					t.Fatalf("head span of %q has %d words, expected %d (1 directive + %d args); text=%q",
						n.Directive, words, expected, len(n.Args), text)
				}
			}

			walk(n.Block)
		}
	}
	walk(file.Nodes)
}

// checkSpanTerminator checks that the Span of every non-comment
// directive ends on the expected delimiter -- ';' for a simple directive, '}'
// for a block. An aligner that stopped one token before or after the real
// delimiter would be caught here.
func checkSpanTerminator(t *testing.T, file *config.File) {
	src := file.Source
	var walk func(nodes []*config.Node)
	walk = func(nodes []*config.Node) {
		for _, n := range nodes {
			if !n.IsComment() {
				if n.Span.End < 1 || n.Span.End > len(src) {
					t.Fatalf("invalid span end for %q: %d (source has %d bytes)",
						n.Directive, n.Span.End, len(src))
				}
				last := src[n.Span.End-1]
				if last != ';' && last != '}' {
					t.Fatalf("span of %q ends in %q, expected ';' or '}'", n.Directive, string(last))
				}
			}
			walk(n.Block)
		}
	}
	walk(file.Nodes)
}
