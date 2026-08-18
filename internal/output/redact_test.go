package output_test

import (
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// dadoRedactavelDeTeste exists only to guarantee, at compile time, that
// output.Redactable is an implementable interface with the expected
// signature. If the signature changes by accident, the whole package stops
// compiling here, and not only in some future consumer.
type dadoRedactavelDeTeste struct{}

func (dadoRedactavelDeTeste) Redacted(output.RedactSet) any { return output.RedactedValue }

var _ output.Redactable = dadoRedactavelDeTeste{}

func TestRedactedValueEhTresAsteriscos(t *testing.T) {
	require.Equal(t, "***", output.RedactedValue)
}

// The spec uses three formats for the same thing. All of them need to work as
// written, so that a configuration copied from the spec does not fail
// silently.
func TestParseRedactRuleAceitaOsTresFormatosDaSpec(t *testing.T) {
	casos := []struct {
		entrada     string
		wantDir     string
		wantArgPref []string
	}{
		{"ssl_certificate_key", "ssl_certificate_key", nil},
		{"proxy_set_header Authorization", "proxy_set_header", []string{"Authorization"}},
		{"**.auth_basic_user_file", "auth_basic_user_file", nil},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			r, err := output.ParseRedactRule(c.entrada)
			require.NoError(t, err)
			require.Equal(t, c.wantDir, r.Directive)
			require.Equal(t, c.wantArgPref, r.ArgPrefix)
		})
	}
}

func TestParseRedactRuleRejeitaEntradaVazia(t *testing.T) {
	_, err := output.ParseRedactRule("   ")
	require.Error(t, err)
}

// Rules whose "directive name" does not match the real alphabet of nginx
// directives (letters, digits, underscore) are dead rules: they pass the
// parse without error but never match anything, and the user believes they
// are protected when they are not. That is worse than failing loudly, because
// it fails silently.
func TestParseRedactRuleRejeitaDiretivaInvalida(t *testing.T) {
	casos := []string{
		"ssl_certificate_key;",  // semicolon copied from the .conf
		"*.ssl_certificate_key", // glob without the exact "**." prefix
		"**ssl_certificate_key", // "**" without the dot
		"http.server.ssl_certificate_key",
		"server/**/auth_basic_user_file",
	}

	for _, entrada := range casos {
		t.Run(entrada, func(t *testing.T) {
			_, err := output.ParseRedactRule(entrada)
			require.Error(t, err)
		})
	}
}

// The "**." context prefix needs to stay valid after the alphabet
// validation: it is stripped before the directive name is checked.
func TestParseRedactRuleAceitaPrefixoDeContextoValido(t *testing.T) {
	r, err := output.ParseRedactRule("**.auth_basic_user_file")
	require.NoError(t, err)
	require.Equal(t, "auth_basic_user_file", r.Directive)
}

// strings.Fields already tolerates extra whitespace around and between the
// fields; this test locks that behavior against a future refactor to
// strings.Split, which would not be as tolerant.
func TestParseRedactRuleToleraEspacoExtra(t *testing.T) {
	r, err := output.ParseRedactRule("  proxy_set_header   Authorization  ")
	require.NoError(t, err)
	require.Equal(t, "proxy_set_header", r.Directive)
	require.Equal(t, []string{"Authorization"}, r.ArgPrefix)
}

// strings.Fields does not understand quotes: a field like "X-Api-Key" would
// arrive with the literal quotes in ArgPrefix and would never match a real
// argument. The surrounding pair of quotes (single or double) is stripped
// before validation.
func TestParseRedactRuleRemoveAspasCircundantesDoArgumento(t *testing.T) {
	r, err := output.ParseRedactRule(`proxy_set_header "X-Api-Key"`)
	require.NoError(t, err)
	require.Equal(t, "proxy_set_header", r.Directive)
	require.Equal(t, []string{"X-Api-Key"}, r.ArgPrefix)

	require.True(t, r.Matches("proxy_set_header", []string{"X-Api-Key", "secret"}))
}

func TestRuleCasaPorNomeDeDiretiva(t *testing.T) {
	r, err := output.ParseRedactRule("ssl_certificate_key")
	require.NoError(t, err)

	require.True(t, r.Matches("ssl_certificate_key", []string{"/etc/ssl/priv.key"}))
	require.False(t, r.Matches("ssl_certificate", []string{"/etc/ssl/pub.crt"}))
}

func TestRuleComPrefixoDeArgsExigeOsArgs(t *testing.T) {
	r, err := output.ParseRedactRule("proxy_set_header Authorization")
	require.NoError(t, err)

	require.True(t, r.Matches("proxy_set_header", []string{"Authorization", "Bearer xyz"}))
	require.False(t, r.Matches("proxy_set_header", []string{"Host", "$host"}),
		"another header must not be redacted")
	require.False(t, r.Matches("proxy_set_header", nil),
		"with no args it cannot match a rule that requires a prefix")
}

// HTTP header names are case-insensitive and nginx propagates the case
// exactly as written in the .conf. An exact comparison would let
// "authorization" or "AUTHORIZATION" leak the whole token just because the
// case did not match the default rule "proxy_set_header Authorization". Only
// the argument is case-insensitive; the directive name stays exact.
func TestRuleCasaArgumentoIndependenteDeCaixa(t *testing.T) {
	r, err := output.ParseRedactRule("proxy_set_header Authorization")
	require.NoError(t, err)

	require.True(t, r.Matches("proxy_set_header", []string{"authorization", "Bearer x"}))
	require.True(t, r.Matches("proxy_set_header", []string{"AUTHORIZATION", "Bearer x"}))
}

// With a two-argument prefix, matching only the first one is not enough:
// every element of the prefix has to match, in order.
func TestRuleComPrefixoDeDoisArgumentosExigeAmbos(t *testing.T) {
	r, err := output.ParseRedactRule("some_directive X-Custom Foo")
	require.NoError(t, err)

	require.True(t, r.Matches("some_directive", []string{"X-Custom", "Foo", "extra"}))
	require.False(t, r.Matches("some_directive", []string{"X-Custom", "Bar"}),
		"a different second argument must not match even with the first one equal")
}

func TestRedactSetCasaQualquerRegra(t *testing.T) {
	set, err := output.NewRedactSet([]string{
		"ssl_certificate_key",
		"proxy_set_header Authorization",
	})
	require.NoError(t, err)

	require.False(t, set.Empty())
	require.True(t, set.Matches("ssl_certificate_key", []string{"/k.pem"}))
	require.True(t, set.Matches("proxy_set_header", []string{"Authorization", "Bearer x"}))
	require.False(t, set.Matches("listen", []string{"443", "ssl"}))
}

func TestRedactSetVazioNaoCasaNada(t *testing.T) {
	set, err := output.NewRedactSet(nil)
	require.NoError(t, err)

	require.True(t, set.Empty())
	require.False(t, set.Matches("ssl_certificate_key", []string{"/k.pem"}))
}

func TestNewRedactSetPropagaRegraInvalida(t *testing.T) {
	_, err := output.NewRedactSet([]string{"ok_directive", "ssl_certificate_key;"})
	require.Error(t, err)
	require.ErrorContains(t, err, "ssl_certificate_key;",
		"the error message needs to name the broken rule, so the user knows which one to fix")
}
