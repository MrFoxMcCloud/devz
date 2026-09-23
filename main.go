// Command devz collects the local development-environment commands that would
// otherwise be a drawer of shell scripts each person keeps their own copy of.
//
// It wraps our own glue, not other people's tools: there is no devz wrapper
// around kubectl, gh or terraform, because those are better documented and
// better completed than anything we would put in front of them.
package main

import (
	"os"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/commands"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
var version = "dev"

func main() {
	app := cli.New(version)
	app.Register(
		commands.Doctor(),
		commands.Secrets(),
		commands.Account(),
		commands.Config(),
		commands.Completion(app),
		commands.Version(),
	)
	os.Exit(app.Run(os.Args[1:]))
}
