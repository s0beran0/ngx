package config

import (
	"errors"
	"io"
	"testing"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/stretchr/testify/require"
)

// A barreira e a segunda camada do defeito 1: mesmo com validarExpressoesIf
// barrando o caso conhecido, nenhum panic vindo da dependencia pode escapar
// como stack trace de uma CLI que um agente le como JSON. Aqui o panic e
// forcado por um Open que entra em panico -- ele roda na goroutine do parser,
// que e a que a barreira cobre.
func TestBarreiraConvertePanicEmRecusaTipada(t *testing.T) {
	payload, err := parseComBarreira("qualquer.conf", &crossplane.ParseOptions{
		Open: func(string) (io.ReadCloser, error) { panic("boom da dependencia") },
	})

	require.Nil(t, payload)
	require.Error(t, err)

	var problemas ParseErrors
	require.True(t, errors.As(err, &problemas))
	require.Equal(t, RecusaPanicoDoCrossplane, problemas[0].Classe)
	require.Contains(t, problemas[0].Message, "boom da dependencia")
}

// A validacao previa e o conserto de causa raiz: ela precisa recusar
// exatamente o que validExpr (crossplane/util.go:57-67) recusava, nem mais
// nem menos -- este teste trava a tabela de equivalencia argumento a
// argumento.
func TestExpressaoValidaReplicaValidExpr(t *testing.T) {
	casos := []struct {
		args []string
		ok   bool
	}{
		{nil, false},
		{[]string{"()"}, false},          // o caso que faz Args[1:0] em util.go:83
		{[]string{"(", ")"}, false},      // vira Args vazio em util.go:77-83
		{[]string{"($a)"}, true},         // l == 1 && len > 2
		{[]string{"($a", ")"}, true},     // l == 2 && len(args[0]) > 1
		{[]string{"(", "$a)"}, true},     // l == 2 && len(args[1]) > 1
		{[]string{"(", "$a", ")"}, true}, // l > 2
		{[]string{"$a"}, false},          // sem parenteses
		{[]string{"($a"}, false},         // nao fecha
		{[]string{"$a)"}, false},         // nao abre
	}
	for _, c := range casos {
		require.Equal(t, c.ok, expressaoValida(c.args), "args=%q", c.args)
	}
}
