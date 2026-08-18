package config

import "fmt"

// RefusalClass names the reason why ngx refused a configuration. It exists
// for one thing only: the FuzzAlignment oracle compares ngx against
// crossplane and needs to tell "deliberate refusal, with a known token shape"
// apart from "over-rejection, which is a bug". Each class below is one
// enumerated, narrow divergence with a unit test of its own -- see
// knownDivergence in fuzz_test.go. A refusal with no class
// (RefusalCrossplane) is the refusal crossplane itself reported, and is never
// a divergence.
type RefusalClass string

const (
	// RefusalCrossplane is the error that came out of the crossplane payload.
	RefusalCrossplane RefusalClass = ""

	// RefusalUnclosedQuote: the source ends inside an open quote.
	RefusalUnclosedQuote RefusalClass = "unclosed_quote"

	// RefusalTokenInsteadOfDirective: some other token showed up where a
	// directive name was expected.
	RefusalTokenInsteadOfDirective RefusalClass = "token_instead_of_directive"

	// RefusalUnexpectedToken: a token out of place at any other position of
	// the matching (argument, block close, comment). It is no known
	// divergence at all: if it shows up in the fuzz, it is an aligner bug.
	RefusalUnexpectedToken RefusalClass = "unexpected_token"

	// RefusalMissingTerminator: the directive neither ends in ';' nor opens '{'.
	RefusalMissingTerminator RefusalClass = "missing_terminator"

	// RefusalLeftoverTokens: the tree ran out before the tokens did.
	RefusalLeftoverTokens RefusalClass = "leftover_tokens"

	// RefusalUnexpectedEnd: the tokens ran out before the tree did.
	RefusalUnexpectedEnd RefusalClass = "unexpected_end"

	// RefusalInvalidIfExpression: "if" with no parenthesized expression.
	RefusalInvalidIfExpression RefusalClass = "invalid_if_expression"

	// RefusalTargetNotRegular: the path exists and opened, but is not a
	// regular file -- directory, socket, fifo, device.
	RefusalTargetNotRegular RefusalClass = "target_not_regular_file"

	// RefusalCrossplanePanic: crossplane panicked while parsing.
	RefusalCrossplanePanic RefusalClass = "crossplane_panic"

	// RefusalReadFailure: reading one of the configuration files failed
	// midway -- the .conf may well be intact, what failed was the I/O. Not a
	// fuzz divergence: the fuzz reads from memory and never produces it.
	RefusalReadFailure RefusalClass = "read_failure"

	// RefusalPermissionDenied is a read failure whose cause is specifically a
	// permission denial. It exists as a class of its own because callers act
	// on it: the CLI suggests --sudo, which does resolve it, and would be
	// wrong to suggest for a dropped connection.
	//
	// It exists as a CLASS, and not as a substring of the message, because
	// branching on human-readable text breaks the moment the text changes.
	// That is not hypothetical here: the CLI used to match "permission" in the
	// message, and translating the project to English silently removed the
	// --sudo hint, with no test noticing.
	RefusalPermissionDenied RefusalClass = "permission_denied"
)

// ParseError is a problem found while reading the configuration, with the
// location preserved so that the diagnostic can point at the exact spot.
//
// Class and Token are there for the comparison against crossplane in the
// fuzz: Token keeps the raw text of the lexeme that motivated the refusal, so
// that the enumerated divergence matches the exact shape of the token ("{",
// "}") instead of matching the message by substring.
type ParseError struct {
	File    string
	Line    int
	Message string
	Class   RefusalClass
	Token   string
}

// ParseErrors aggregates the problems of a parse. It implements error so it
// can be returned by Parse, and keeps the items reachable through errors.As
// so that the output layer can turn them into located diagnostics.
type ParseErrors []ParseError

// Error sums up the problems: the first item, plus the count of the remaining
// ones when there is more than one. The full detail stays available through
// errors.As for whoever needs the file and line of each item.
func (e ParseErrors) Error() string {
	if len(e) == 0 {
		return "parse failed without detailing the error"
	}

	first := e[0]

	// Line zero means "there is no line", not "line zero": a file that never
	// even opened has no position to offer. Printing `file:0` invents a
	// reference that does not exist and reads like a defect -- and the rule
	// of this project is to omit what is unavailable, never to make it up.
	loc := first.File
	if first.Line > 0 {
		loc = fmt.Sprintf("%s:%d", first.File, first.Line)
	}
	msg := fmt.Sprintf("%s: %s", loc, first.Message)
	if len(e) > 1 {
		msg = fmt.Sprintf("%s (and %d more error(s))", msg, len(e)-1)
	}
	return msg
}
