package output

import (
	"fmt"
	"regexp"
	"strings"
)

// validDirectiveName matches the real alphabet of nginx directive names:
// letters, digits and underscore. Anything outside that (a semicolon copied
// from the .conf, "*", ".", "/") means the rule was typed wrong, and it is
// better to fail loudly at parse time than to leave a dead rule that silently
// never matches anything.
var validDirectiveName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// RedactedValue replaces the value of a sensitive directive. The directive,
// the id and the line stay visible: making the whole node disappear would
// lead the agent to conclude the directive does not exist, which is worse
// than hiding the value.
const RedactedValue = "***"

// Redactable is implemented by any data that knows how to produce a redacted
// copy of itself. Redaction happens at serialization time, never on the
// in-memory tree: if the tree were redacted at parse time, fmt would write
// *** inside the user's .conf.
type Redactable interface {
	Redacted(rs RedactSet) any
}

// RedactRule matches a directive by name, optionally requiring a prefix of
// arguments.
type RedactRule struct {
	Directive string
	ArgPrefix []string
}

// ParseRedactRule reads an output.redact entry. It accepts the three formats
// the spec uses: directive name, name with an argument prefix, and the
// context prefix "**." -- which is redundant, because rules already apply in
// any context, but is accepted so configurations written from the spec do not
// break.
//
// Each field may come wrapped in a single pair of single or double quotes
// (e.g. proxy_set_header "X-Api-Key"); that pair is stripped before
// validation. The directive name (first field, already unquoted and without
// the "**." prefix) is validated against the real alphabet of nginx directive
// names -- anything outside letters/digits/underscore is an error, not a dead
// rule.
func ParseRedactRule(s string) (RedactRule, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "**.")

	raw := strings.Fields(s)
	if len(raw) == 0 {
		return RedactRule{}, fmt.Errorf("empty redaction rule")
	}

	fields := make([]string, len(raw))
	for i, c := range raw {
		fields[i] = stripSurroundingQuotes(c)
	}

	if !validDirectiveName.MatchString(fields[0]) {
		return RedactRule{}, fmt.Errorf("invalid directive name: %q", fields[0])
	}

	r := RedactRule{Directive: fields[0]}
	if len(fields) > 1 {
		r.ArgPrefix = fields[1:]
	}
	return r, nil
}

// stripSurroundingQuotes strips a single pair of single or double quotes
// wrapping the whole field. strings.Fields does not understand quotes, so a
// field like "X-Api-Key" would arrive here with the literal quotes and would
// never match a real argument.
func stripSurroundingQuotes(field string) string {
	if len(field) < 2 {
		return field
	}
	first, last := field[0], field[len(field)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return field[1 : len(field)-1]
	}
	return field
}

// Matches reports whether the given directive should have its value redacted.
// The directive name is compared exactly, because nginx directives are always
// lowercase. The arguments are compared case-insensitively, because HTTP
// header names are case-insensitive and nginx propagates the case exactly as
// written in the .conf: a "proxy_set_header Authorization" rule also needs to
// match "authorization" or "AUTHORIZATION", otherwise the whole token leaks.
// The cost is over-redacting if an argument prefix happens to be a file path
// that differs only in case -- the safe side of the trade-off.
func (r RedactRule) Matches(directive string, args []string) bool {
	if r.Directive != directive {
		return false
	}
	if len(args) < len(r.ArgPrefix) {
		return false
	}
	for i, p := range r.ArgPrefix {
		if !strings.EqualFold(args[i], p) {
			return false
		}
	}
	return true
}

// RedactSet is the set of active rules.
type RedactSet struct {
	rules []RedactRule
}

// NewRedactSet compiles the output.redact entries. The error must be treated
// as fatal, not as a warning: on the error path the function returns a
// zero-value RedactSet{} whose Matches is always false, that is, ZERO
// redaction -- not partial redaction of the rules that were valid up to that
// point. A consumer that merely logs the error and carries on ends up running
// with no protection at all against secret leakage.
func NewRedactSet(entries []string) (RedactSet, error) {
	var set RedactSet
	for _, e := range entries {
		r, err := ParseRedactRule(e)
		if err != nil {
			return RedactSet{}, fmt.Errorf("redaction rule %q: %w", e, err)
		}
		set.rules = append(set.rules, r)
	}
	return set, nil
}

// Empty reports whether no rule is active.
func (s RedactSet) Empty() bool { return len(s.rules) == 0 }

// Matches reports whether some rule matches the given directive.
func (s RedactSet) Matches(directive string, args []string) bool {
	for _, r := range s.rules {
		if r.Matches(directive, args) {
			return true
		}
	}
	return false
}
