// Command ngx is the entry point. The only responsibility here is the wiring
// and the translation of the exit code.
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
