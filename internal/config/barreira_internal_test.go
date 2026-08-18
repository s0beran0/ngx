package config

import (
	"errors"
	"io"
	"testing"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/stretchr/testify/require"
)

// The barrier is the second layer of defect 1: even with validarExpressoesIf
// blocking the known case, no panic coming from the dependency may escape as a
// stack trace out of a CLI that an agent reads as JSON. Here the panic is
// forced by an Open that panics -- it runs on the parser goroutine, which is
// the one the barrier covers.
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

// The up-front validation is the root-cause fix: it has to refuse exactly
// what validExpr (crossplane/util.go:57-67) used to refuse, no more and no
// less -- this test pins the equivalence table argument by argument.
func TestExpressaoValidaReplicaValidExpr(t *testing.T) {
	casos := []struct {
		args []string
		ok   bool
	}{
		{nil, false},
		{[]string{"()"}, false},          // the case that does Args[1:0] at util.go:83
		{[]string{"(", ")"}, false},      // becomes an empty Args at util.go:77-83
		{[]string{"($a)"}, true},         // l == 1 && len > 2
		{[]string{"($a", ")"}, true},     // l == 2 && len(args[0]) > 1
		{[]string{"(", "$a)"}, true},     // l == 2 && len(args[1]) > 1
		{[]string{"(", "$a", ")"}, true}, // l > 2
		{[]string{"$a"}, false},          // no parentheses
		{[]string{"($a"}, false},         // does not close
		{[]string{"$a)"}, false},         // does not open
	}
	for _, c := range casos {
		require.Equal(t, c.ok, expressaoValida(c.args), "args=%q", c.args)
	}
}
