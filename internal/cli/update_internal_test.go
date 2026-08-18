package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/s0beran0/ngx/internal/update"
)

// A precedencia do canal: flag vence ambiente, ambiente vence default. A
// variavel importa porque o install.sh ja a usa -- quem instalou pelo beta
// espera continuar no beta sem repetir a flag a cada atualizacao, e cair
// silenciosamente para stable faria o update dizer que nao ha versao nova.
func TestCanalEscolhidoRespeitaPrecedencia(t *testing.T) {
	comEnv := func(v string) *Context {
		return &Context{Getenv: func(k string) string {
			if k == update.EnvCanal {
				return v
			}
			return ""
		}}
	}

	assert.Equal(t, "beta", canalEscolhido(comEnv("stable"), "beta"),
		"flag explicita vence a variavel de ambiente")
	assert.Equal(t, "beta", canalEscolhido(comEnv("beta"), ""),
		"sem flag, a variavel de ambiente vale")
	assert.Equal(t, "", canalEscolhido(comEnv(""), ""),
		"sem nenhum dos dois, quem decide o default e o pacote update")
	assert.Equal(t, "", canalEscolhido(&Context{}, ""),
		"Context sem Getenv nao pode entrar em panico")
}
