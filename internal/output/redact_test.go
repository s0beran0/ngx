package output_test

import (
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// dadoRedactavelDeTeste existe apenas para garantir, em tempo de compilacao,
// que output.Redactable e uma interface implementavel com a assinatura
// esperada. Se a assinatura mudar sem querer, o pacote inteiro para de
// compilar aqui, e nao so em algum consumidor futuro.
type dadoRedactavelDeTeste struct{}

func (dadoRedactavelDeTeste) Redacted(output.RedactSet) any { return output.RedactedValue }

var _ output.Redactable = dadoRedactavelDeTeste{}

func TestRedactedValueEhTresAsteriscos(t *testing.T) {
	require.Equal(t, "***", output.RedactedValue)
}

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

// Regras cujo "nome de diretiva" nao casa o alfabeto real de diretivas do
// nginx (letras, digitos, underscore) sao regras mortas: elas passam pelo
// parse sem erro mas nunca casam nada, e o usuario acha que esta protegido
// quando nao esta. Isso e pior que um erro alto, porque falha em silencio.
func TestParseRedactRuleRejeitaDiretivaInvalida(t *testing.T) {
	casos := []string{
		"ssl_certificate_key;",       // ponto-e-virgula copiado do .conf
		"*.ssl_certificate_key",      // glob sem o prefixo "**." exato
		"**ssl_certificate_key",      // "**" sem o ponto
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

// O prefixo de contexto "**." precisa continuar valido depois da validacao
// de alfabeto: ele e removido antes de checar o nome da diretiva.
func TestParseRedactRuleAceitaPrefixoDeContextoValido(t *testing.T) {
	r, err := output.ParseRedactRule("**.auth_basic_user_file")
	require.NoError(t, err)
	require.Equal(t, "auth_basic_user_file", r.Directive)
}

// strings.Fields ja tolera espaco extra ao redor e entre os campos; este
// teste trava esse comportamento contra uma futura refatoracao para
// strings.Split, que nao teria a mesma tolerancia.
func TestParseRedactRuleToleraEspacoExtra(t *testing.T) {
	r, err := output.ParseRedactRule("  proxy_set_header   Authorization  ")
	require.NoError(t, err)
	require.Equal(t, "proxy_set_header", r.Directive)
	require.Equal(t, []string{"Authorization"}, r.ArgPrefix)
}

// strings.Fields nao entende aspas: um campo como "X-Api-Key" chegaria com
// as aspas literais no ArgPrefix e nunca casaria um argumento real. O par de
// aspas circundante (simples ou duplas) e removido antes da validacao.
func TestParseRedactRuleRemoveAspasCircundantesDoArgumento(t *testing.T) {
	r, err := output.ParseRedactRule(`proxy_set_header "X-Api-Key"`)
	require.NoError(t, err)
	require.Equal(t, "proxy_set_header", r.Directive)
	require.Equal(t, []string{"X-Api-Key"}, r.ArgPrefix)

	require.True(t, r.Matches("proxy_set_header", []string{"X-Api-Key", "segredo"}))
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

// Nomes de header HTTP sao case-insensitive e o nginx propaga a caixa como
// foi escrita no .conf. Uma comparacao exata deixaria "authorization" ou
// "AUTHORIZATION" vazarem o token inteiro so porque a caixa nao bateu com a
// regra padrao "proxy_set_header Authorization". So o argumento e
// case-insensitive; o nome da diretiva continua exato.
func TestRuleCasaArgumentoIndependenteDeCaixa(t *testing.T) {
	r, err := output.ParseRedactRule("proxy_set_header Authorization")
	require.NoError(t, err)

	require.True(t, r.Matches("proxy_set_header", []string{"authorization", "Bearer x"}))
	require.True(t, r.Matches("proxy_set_header", []string{"AUTHORIZATION", "Bearer x"}))
}

// Com prefixo de dois argumentos, so o primeiro casar nao basta: todos os
// elementos do prefixo precisam bater, na ordem.
func TestRuleComPrefixoDeDoisArgumentosExigeAmbos(t *testing.T) {
	r, err := output.ParseRedactRule("some_directive X-Custom Foo")
	require.NoError(t, err)

	require.True(t, r.Matches("some_directive", []string{"X-Custom", "Foo", "extra"}))
	require.False(t, r.Matches("some_directive", []string{"X-Custom", "Bar"}),
		"segundo argumento diferente nao deve casar mesmo com o primeiro igual")
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
		"a mensagem de erro precisa nomear a regra quebrada, para o usuario saber qual corrigir")
}
