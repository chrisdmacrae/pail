// pail is pail-cli: one binary that talks to any Pail installation.
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/chrisdmacrae/pail/internal/cli"
)

// version is set at release with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	stdin := int(os.Stdin.Fd())

	os.Exit(cli.Run(cli.Env{
		Args:    os.Args[1:],
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		Home:    home,
		Cwd:     cwd,
		TTY:     term.IsTerminal(stdin) && term.IsTerminal(int(os.Stderr.Fd())),
		Version: version,
		ReadSecret: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(stdin)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
	}))
}
