// pail is pail-cli: one binary that talks to any Pail installation.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

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
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		Home:    home,
		Cwd:     cwd,
		TTY:     term.IsTerminal(stdin) && term.IsTerminal(int(os.Stderr.Fd())),
		Version: version,
		OpenURL: openURL,
		ReadSecret: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(stdin)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
	}))
}

// openURL hands a URL to the system's default browser.
func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	}
	return exec.Command("xdg-open", url).Run()
}
