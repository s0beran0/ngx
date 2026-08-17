package output_test

import (
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// A spec usa tres formatos para a mesma coisa. Todos precisam funcionar como
// escritos, para que uma configuracao copiada da spec nao falhe em silencio.
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
		"outro header nao deve ser redigido")
	require.False(t, r.Matches("proxy_set_header", nil),
		"sem args nao pode casar uma regra que exige prefixo")
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
	_, err := output.NewRedactSet([]string{"ok_directive", ""})
	require.Error(t, err)
}
