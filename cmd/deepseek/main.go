// Command deepseek talks to the DeepSeek API from the shell.
package main

import (
	"os"

	"github.com/thevibeworks/deepseek-cli/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
