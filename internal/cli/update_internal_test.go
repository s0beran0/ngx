package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/s0beran0/ngx/internal/update"
)

// The channel precedence: the flag beats the environment, the environment
// beats the default. The variable matters because install.sh already uses it
// -- whoever installed from beta expects to stay on beta without repeating the
// flag on every update, and silently falling back to stable would make update
// say there is no new version.
func TestChosenChannelRespectsPrecedence(t *testing.T) {
	comEnv := func(v string) *Context {
		return &Context{Getenv: func(k string) string {
			if k == update.EnvChannel {
				return v
			}
			return ""
		}}
	}

	assert.Equal(t, "beta", chosenChannel(comEnv("stable"), "beta"),
		"an explicit flag beats the environment variable")
	assert.Equal(t, "beta", chosenChannel(comEnv("beta"), ""),
		"with no flag, the environment variable applies")
	assert.Equal(t, "", chosenChannel(comEnv(""), ""),
		"with neither of the two, the update package decides the default")
	assert.Equal(t, "", chosenChannel(&Context{}, ""),
		"a Context with no Getenv cannot panic")
}
