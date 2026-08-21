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
	f.Add("# comment\nhttp { }")
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
	f.Add("# comment ç\nserver_name example.com.br;")
	f.Add("proxy_pass http://\"host\";")
	f.Add("foo \\")
	// R8: a Lua body is a single opaque token for crossplane, and has to be
	// one for us too -- with the braces, the ';' and the quotes inside it
	// belonging to the body and not to the configuration.
	f.Add("content_by_lua_block {\n    if t.x > 0 then ngx.say(\"oi; tchau\") end\n}\nlisten 80;\n")
	f.Add("set_by_lua_block $a { return 1 }\nlisten 80;\n")
	f.Add("content_by_lua_block { s = \"}\" }\nlisten 80;\n")
	f.Add("content_by_lua_block { a { b } c }\n")
	// The two inputs that separate "is at the start of a line" from
	// crossplane's real rule for directive position, which is what decides
	// whether the body is read as Lua at all -- see luaTriggers in lua.go.
	f.Add("foo bar\ncontent_by_lua_block { x }\n")
	f.Add("foo bar\n\ncontent_by_lua_block { x }\n")

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
			// The terminator of a *_by_lua_block does not exist in the file:
			// crossplane emits it out of thin air (lua.go:143) and we mirror
			// it with an empty span. Every other semicolon is a real byte.
			if tok.Start == tok.End && tok.Raw == "" {
				continue
			}
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
			if tok.Verbatim {
				expected := expectedValueForVerbatim(t, tok)
				if tok.Value != expected {
					t.Fatalf("verbatim TokenWord with value %q != expected %q (raw %q)",
						tok.Value, expected, tok.Raw)
				}
				continue
			}
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

// expectedValueForVerbatim recomputes the Value of a token read by the Lua
// lexer (R8): the literal text of the span, with the two braces of a
// *_by_lua_block body removed -- they are its delimiters, like the quotes of
// a quoted token -- and invalid UTF-8 replaced by U+FFFD, which is what
// bufio.ScanRunes hands crossplane. Nothing else is interpreted: inside a Lua
// body a backslash is a backslash, and that is the whole point of the mark.
func expectedValueForVerbatim(t *testing.T, tok config.Token) string {
	raw := tok.Raw
	if tok.Quoted {
		// the body of the block: Raw is "{...}", Value is what is inside
		if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
			t.Fatalf("verbatim quoted token with raw %q, expected it delimited by braces", raw)
		}
		raw = raw[1 : len(raw)-1]
	}
	return replaceInvalidRunes(raw)
}

// replaceInvalidRunes mirrors consumeIntoValue (tokens.go): each byte that is
// not valid UTF-8 becomes U+FFFD, which is what crossplane's rune scanner
// produces for it.
func replaceInvalidRunes(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		out.WriteString(s[i : i+size])
		i += size
	}
	return out.String()
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
	// The oracle runs with the SAME lexer extensions as production
	// (config.LuaLexOptions, registered in parse.go): a crossplane without
	// the Lua lexer reads a different language from the one ngx parses, and
	// comparing against it would prove nothing about the alignment.
	ch := crossplane.LexWithOptions(strings.NewReader(s), config.LuaLexOptions())

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

	// Comments come out of both lexers and are dropped from both sides, because
	// the two carry different text for the same comment: ours holds " comment"
	// and crossplane's holds "# comment". This test compares count and kind, so
	// normalising that text would be work in service of nothing.
	//
	// A "#" token is treated as a comment only when BOTH sides say so. That is
	// the rule, and it replaced two attempts that were wrong in the same way --
	// they tried to decide, on the oracle's stream alone, whether a "#" was a
	// comment or data.
	//
	// It cannot be decided there. The first argument of `set_by_lua_block` is
	// read as a run of non-space characters with no notion of quoting or
	// comments (crossplane/lua.go:68-90), so in `set_by_lua_block # {}` the "#"
	// is a VARIABLE NAME, emitted identically to a comment. Filtering by
	// "unquoted and starts with #" deleted it from the oracle's side alone.
	// Qualifying that with "unless the previous token was set_by_lua_block"
	// broke on `0 set_by_lua_block #`, where the name is an argument. Adding
	// nginx's directive-position rule broke on `''set_by_lua_block # {}`, where
	// the two lexers disagree about position after an empty quoted token.
	//
	// Each fix was a further step into re-implementing the lexer on the oracle
	// side, which is the thing this project's rules say not to do: derive, do
	// not guess. Walking the two streams together needs no such decision. When
	// they agree it is a comment, both drop it; when they disagree, both keep it
	// and the comparison below says so -- which is the failure this test exists
	// to report, rather than one the filter hides.
	var ours []config.Token
	var theirs []crossplane.NgxToken
	i, j := 0, 0
	for i < len(toks) && j < len(reference) {
		ourComment := toks[i].Kind == config.TokenComment
		theirComment := !reference[j].IsQuoted && strings.HasPrefix(reference[j].Value, "#")

		switch {
		case ourComment && theirComment:
			i++
			j++
		case ourComment:
			// Ours calls it a comment and theirs does not. Dropping only ours
			// leaves the counts unequal on purpose: that asymmetry IS the
			// divergence, and hiding it is how the earlier filters failed.
			i++
		default:
			ours = append(ours, toks[i])
			theirs = append(theirs, reference[j])
			i++
			j++
		}
	}
	// Whatever is left over stays in, so a length divergence is reported rather
	// than trimmed away.
	for ; i < len(toks); i++ {
		if toks[i].Kind != config.TokenComment {
			ours = append(ours, toks[i])
		}
	}
	for ; j < len(reference); j++ {
		if reference[j].IsQuoted || !strings.HasPrefix(reference[j].Value, "#") {
			theirs = append(theirs, reference[j])
		}
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

// FuzzAlignment checks properties of the token-tree matching (Task 9) that
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
//  4. terminator: the Span of a non-comment directive ends in ';' or '}';
//  5. per-argument spans (R5): slicing the source by ArgSpans[i] and running
//     it back through the tokenizer gives exactly one word whose Value is
//     Args[i] -- with "if" excluded, where the spans are reported as
//     unavailable because crossplane rewrites Args.
func FuzzAlignment(f *testing.F) {
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
	f.Add("include included.conf;")
	f.Add("http { include included.conf; }\n# after\n")
	// R8: what the seeds are really for here is the property that the block
	// does not throw off what comes AFTER it -- hence a directive following
	// every Lua body.
	f.Add("content_by_lua_block {\n    if t.x > 0 then ngx.say(\"oi; tchau\") end\n}\nlisten 80;\n")
	f.Add("http { server { content_by_lua_block { if x then end }\n listen 80; } }\n")
	f.Add("set_by_lua_block $a { return 1 }\nlisten 80;\n")
	f.Add("content_by_lua_block { s = \"}\" }\nlisten 80;\n")

	f.Fuzz(func(t *testing.T, s string) {
		dir := t.TempDir()
		p := filepath.Join(dir, "f.conf")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Skip()
		}
		// The included file is fixed: what varies is the text including it.
		included := "server_name included.example; # do include\nlisten 8080;\n"
		if err := os.WriteFile(filepath.Join(dir, "included.conf"), []byte(included), 0o644); err != nil {
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
			checkArgSpans(t, file)
		}
	})
}

// checkArgSpans is property 5. It is the per-argument twin of
// checkHeadSpanIsNamePlusArgs, and it is deliberately checked against
// crossplane's Args and not against our own token stream: the aligner records
// the span of whatever token it consumed, without ever comparing it to the
// argument, so a desync between the two parsers -- which is exactly what
// registering the Lua lexer (R8) will risk -- shows up here as a Value that
// does not match instead of as a silently wrong offset.
//
// "if" is skipped for its arguments and checked for its absence: prepareIfArgs
// (crossplane/util.go:71-86) rewrites Args, so there is no 1-to-1 span to
// record, and publishing one anyway is the failure this asserts against.
func checkArgSpans(t *testing.T, file *config.File) {
	var walk func(nodes []*config.Node)
	walk = func(nodes []*config.Node) {
		for _, n := range nodes {
			if n.Directive == "if" {
				if n.ArgSpans != nil {
					t.Fatalf("if published %d arg spans; the correspondence with Args does not exist there",
						len(n.ArgSpans))
				}
				walk(n.Block)
				continue
			}
			if n.ArgSpans == nil {
				t.Fatalf("%q has no arg spans and is not an if", n.Directive)
			}
			if len(n.ArgSpans) != len(n.Args) {
				t.Fatalf("%q has %d args and %d arg spans", n.Directive, len(n.Args), len(n.ArgSpans))
			}

			// A statement whose LAST argument starts with "{" was read by the
			// Lua lexer (R8): no other lexeme can start with a brace, since
			// the tokenizer breaks words there. Its arguments do not
			// round-trip through Tokenize on their own -- the body is Lua
			// code, and outside the directive it is not even nginx --, so
			// the property is checked directly against the bytes, which is
			// stronger: the span has to REPRODUCE the argument.
			luaLexed := len(n.ArgSpans) > 0 &&
				file.Source[n.ArgSpans[len(n.ArgSpans)-1].Start] == '{'

			previous := n.HeadSpan.Start
			for i, s := range n.ArgSpans {
				if s.Start < previous || s.End <= s.Start || s.End > n.HeadSpan.End {
					t.Fatalf("arg span %d of %q is [%d,%d), out of order or outside the head [%d,%d)",
						i, n.Directive, s.Start, s.End, n.HeadSpan.Start, n.HeadSpan.End)
				}
				previous = s.End

				text := file.Source[s.Start:s.End]
				if luaLexed && checkLuaArgSpan(t, n, i, string(text)) {
					continue
				}
				toks, err := config.Tokenize(text)
				if err != nil {
					t.Fatalf("arg span %d of %q does not retokenize (%v); text=%q",
						i, n.Directive, err, string(text))
				}
				if len(toks) != 1 || toks[0].Kind != config.TokenWord {
					t.Fatalf("arg span %d of %q yields %d tokens, expected one word; text=%q",
						i, n.Directive, len(toks), string(text))
				}
				if toks[0].Value != n.Args[i] {
					t.Fatalf("arg span %d of %q holds %q, crossplane read %q; text=%q",
						i, n.Directive, toks[0].Value, n.Args[i], string(text))
				}
			}
			walk(n.Block)
		}
	}
	walk(file.Nodes)
}

// checkLuaArgSpan checks the arguments of a statement read by the Lua lexer
// and reports whether it took charge of this one.
//
// Two shapes come out of that lexer (crossplane/lua.go). The body of the
// block is the last argument, and its span covers "{...}" -- the braces are
// its delimiters, exactly as the quotes are of a quoted argument, and
// including them is what makes the span a self-contained lexeme to overwrite.
// The first argument of set_by_lua_block is a run of non-space characters
// read with no notion of quote or escape (lua.go:68-90), so `"$a"` is its
// value, quotation marks and all.
//
// Every other argument -- the `content_by_lua_block` that ended up as the
// argument of another directive, for instance -- was read by the ordinary
// word reader and goes back to the general check.
func checkLuaArgSpan(t *testing.T, n *config.Node, i int, text string) bool {
	if i == len(n.ArgSpans)-1 {
		if len(text) < 2 || text[0] != '{' || text[len(text)-1] != '}' {
			t.Fatalf("body span of %q is %q, expected it delimited by braces", n.Directive, text)
		}
		if got := replaceInvalidRunes(text[1 : len(text)-1]); got != n.Args[i] {
			t.Fatalf("body span of %q holds %q, crossplane read %q", n.Directive, got, n.Args[i])
		}
		return true
	}
	if n.Directive != "set_by_lua_block" {
		return false
	}
	if got := replaceInvalidRunes(text); got != n.Args[i] {
		t.Fatalf("arg span %d of %q holds %q, crossplane read %q", i, n.Directive, got, n.Args[i])
	}
	return true
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
		LexOptions:                config.LuaLexOptions(),
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
// deliberately OUT of the list: RefusalUnexpectedToken, RefusalLeftoverTokens,
// RefusalUnexpectedEnd and RefusalCrossplanePanic, which only show up when
// the matching between tree and tokens has slipped -- that is, when there is
// a bug.
func knownDivergence(pe config.ParseError) bool {
	switch pe.Class {
	case config.RefusalUnclosedQuote:
		// lex.go:325-327 closes the quote implicitly at end of file and
		// emits no token at all when the content is empty: a dangling quote
		// is "ok" for crossplane. nginx refuses it. See
		// TestDivergenceUnclosedQuote.
		return pe.Token == `"` || pe.Token == "'"

	case config.RefusalTokenInsteadOfDirective:
		// parse.go:256-261 builds the statement out of t.Value without
		// requiring the first token to be a word: only "}" (parse.go:237)
		// and comments (parse.go:264) are handled apart, so "{", "}" and ";"
		// become directive names for it. Those three are ALL the tokens that
		// are neither word nor comment -- the list is exhaustive over the
		// tokenizer's Kind, and a word refused in that position is still a
		// bug. See TestDivergenceBraceAsDirectiveName and
		// TestDivergenceSemicolonAsDirectiveName.
		return pe.Token == "{" || pe.Token == "}" || pe.Token == ";"

	case config.RefusalMissingTerminator:
		// The argument loop stops at "}" (parse.go:285) and the
		// "is not terminated by \";\"" check (analyze.go:224-227) does not
		// run under SkipDirectiveArgsCheck (analyze.go:202-204). Only the "}"
		// diverges. See TestDivergenceDirectiveWithoutSemicolon.
		if pe.Token == "}" {
			return true
		}

		// A Lua block preceded by a comment on the same line, found by the
		// fuzz: `set_by_lua_block $x #c {return 1}`. crossplane's Lua hook
		// takes over at the directive name and never sees that the "#" has
		// commented out the rest of the line -- including the "{" -- so it
		// accepts a block that does not exist.
		//
		// Our refusal is the correct one, and this is not reasoning: OpenResty
		// 1.27.1.2 refuses the same file with `unexpected "}"`, verified in
		// the Lua bench. The "#" comments to end of line, so the block never
		// opens.
		//
		// The token is what the tokeniser found where a ";" or "{" belonged,
		// which for this shape is the brace-delimited text the comment
		// swallowed. Narrow on purpose: any other token in this position is
		// still a bug.
		return strings.HasPrefix(pe.Token, "{") && strings.HasSuffix(pe.Token, "}")

	case config.RefusalInvalidIfExpression:
		// The validExpr guard (analyze.go:212, util.go:57-67) that
		// SkipDirectiveArgsCheck suppresses and without which prepareIfArgs
		// (util.go:83) brings the process down. The token is always the name
		// "if", quoted or not (parse.go:352-354 compares without looking at
		// IsQuoted). See TestIfWithEmptyExpressionIsTypedRefusalNotPanic.
		return pe.Token == "if" || pe.Token == `"if"` || pe.Token == "'if'"

	case config.RefusalInvalidLuaBlock:
		// The hook of lex.go:186-206 reads one character past the directive
		// name before handing over to the Lua lexer and re-processes it
		// AFTER the block: for `content_by_lua_blockx { y }` crossplane's
		// stream is [content_by_lua_block, " y ", ";", x], with the "x"
		// coming out after text that sits behind it. Tokens with byte spans
		// cannot describe that, and nginx refuses all of these anyway --
		// unknown directive, or a *_by_lua_block with no body. The token is
		// always the name of a Lua directive, which is a closed list. See
		// TestDivergenceLuaBlockWithoutBody.
		return config.IsLuaBlockDirective(pe.Token)

	case config.RefusalTargetNotRegular:
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
				// The implied terminator of a *_by_lua_block (R8) occupies
				// no byte, so it is inside every span that ends at the "}"
				// of the block -- including this one. It is not the aligner
				// advancing too far: there is nothing to advance over.
				if tk.Kind == config.TokenSemicolon && tk.Start == tk.End {
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
