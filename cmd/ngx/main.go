// Command ngx e o ponto de entrada. A unica responsabilidade aqui e o wiring
// e a traducao do exit code.
package main

import (
	"os"

	"github.com/s0beran0/ngx/internal/cli"
	"golang.org/x/term"
)

func main() {
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	code := cli.Execute(os.Args[1:], os.Stdout, os.Stderr, isTTY)
	os.Exit(int(code))
}
