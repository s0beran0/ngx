package config

import (
	"errors"
	"fmt"
	"strings"
)

// Why SkipDirectiveArgsCheck is turned on in Parse (parse.go), and why this
// validation exists in spite of that:
//
// Crossplane's argument check (analyze.go:206-247) runs against a directive
// table generated for one specific version of nginx and of its modules. An
// unknown directive is already ignored before that -- analyze.go:176-178
// returns nil when !knownDirective, regardless of the flag --, so the flag is
// NOT there to accept third-party directives: it is there so we do not refuse
// a KNOWN directive whose arity changed between versions of nginx or of the
// module. An ngx that refuses a .conf the user's nginx accepts is worse than
// an ngx that accepts something nginx would refuse: what validates semantics
// is "nginx -t", and ngx is a tool for reading and editing.
//
// Except that one of the suppressed guards was not about arity:
// analyze.go:212 (`(mask&ngxConfExpr) > 0 && !validExpr(stmt)`) is the only
// point that keeps crossplane from handing a malformed "if" to prepareIfArgs
// (util.go:71-86), which for Args == ["()"] does d.Args[1:0] and brings the
// process down (util.go:83). This function puts exactly that guard back, for
// "if" only, replicating validExpr (util.go:57-67) argument by argument --
// no arity, no context.
//
// Running it before handing the file to crossplane is what makes this a
// root-cause fix and not a workaround: the panic stops happening, instead of
// being caught afterwards. The recover barrier in parse.go stays around for
// the next surprise from the dependency, not for this one.

// validateBeforeParse returns the refusals that have to be decided BEFORE a
// single token reaches crossplane's parser. It tokenizes the source once and
// hands the tokens to the checks that need them.
//
// Two things live here, for two different reasons. The malformed "if" is here
// because further down the line it brings the process down (see below). The
// malformed *_by_lua_block is here for the diagnostic: crossplane fails on it
// too, but with "premature end of file" pointing at the end of the file, when
// what the reader needs to know is which directive has no body -- and that is
// something only the tokenizer knows.
//
// Any other tokenizing failure yields zero refusals on purpose: at that point
// the decision belongs to the aligner (which classifies the refusal) or to
// crossplane itself, and guessing about tokens that do not exist would only
// produce a wrong message.
func validateBeforeParse(path string, src []byte) ParseErrors {
	toks, err := Tokenize(src)
	if err != nil {
		var lua *LuaBlockError
		if errors.As(err, &lua) {
			return ParseErrors{{
				File:    path,
				Line:    lua.Line,
				Message: lua.Error(),
				Class:   RefusalInvalidLuaBlock,
				Token:   lua.Directive,
			}}
		}
		return nil
	}
	return validateIfExpressions(path, toks)
}

// validateIfExpressions returns the refusals of the "if" directives whose
// expression is not parenthesized. It works over this package's tokens, which
// match crossplane's lexer token for token.
func validateIfExpressions(path string, toks []Token) ParseErrors {
	var problems ParseErrors
	// mapLike counts the open map-like blocks. Inside them crossplane never
	// even reaches analyze/prepareIfArgs: parse.go:304-321 appends the
	// statement and moves on. An "if" in there is a map parameter, not a
	// directive -- refusing it would be over-rejection.
	mapLike := 0
	i := 0
	for i < len(toks) {
		t := toks[i]
		switch t.Kind {
		case TokenComment, TokenSemicolon:
			i++
			continue
		case TokenBlockEnd:
			if mapLike > 0 {
				mapLike--
			}
			i++
			continue
		case TokenBlockStart:
			// "{" where a directive name was expected. Crossplane treats it
			// as the name (parse.go:256-261); here we only need to keep the
			// block count coherent.
			i++
			continue
		}

		name := t
		i++
		var args []string
		for i < len(toks) {
			k := toks[i].Kind
			if k == TokenSemicolon || k == TokenBlockStart || k == TokenBlockEnd {
				break
			}
			if k == TokenWord {
				args = append(args, toks[i].Value)
			}
			// A TokenComment in the middle of the arguments does not go into
			// Args (crossplane/parse.go:286-290).
			i++
		}

		opensBlock := i < len(toks) && toks[i].Kind == TokenBlockStart
		if opensBlock {
			if isMapLikeBody(name.Value) || mapLike > 0 {
				mapLike++
			}
			i++
		} else if i < len(toks) && toks[i].Kind == TokenSemicolon {
			i++
		}

		// stmt.Directive is the token VALUE, quoted or not: crossplane
		// compares `stmt.Directive == "if"` without looking at IsQuoted
		// (parse.go:352-354), so `"if" ()` lands in the same prepareIfArgs.
		if mapLike > 0 {
			continue
		}
		if name.Value != "if" {
			continue
		}
		if validIfExpr(args) {
			continue
		}
		problems = append(problems, ParseError{
			File:    path,
			Line:    name.Line,
			Message: fmt.Sprintf("directive \"if\" with expression %q: the expression must be parenthesized and cannot be empty", strings.Join(args, " ")),
			Class:   RefusalInvalidIfExpression,
			Token:   name.Raw,
		})
	}
	return problems
}

// validIfExpr replicates validExpr (crossplane/util.go:57-67) over the
// argument values: the first argument must start with "(", the last one must
// end with ")", and the expression between them cannot be empty -- and
// emptiness is tested by the LENGTH of the edge tokens, exactly as in the
// original, because that is what prepareIfArgs (util.go:71-86) assumes.
func validIfExpr(args []string) bool {
	l := len(args)
	if l == 0 {
		return false
	}
	b, e := 0, l-1
	if !strings.HasPrefix(args[b], "(") || !strings.HasSuffix(args[e], ")") {
		return false
	}
	switch {
	case l == 1:
		return len(args[b]) > 2
	case l == 2:
		return len(args[b]) > 1 || len(args[e]) > 1
	default:
		return true
	}
}
