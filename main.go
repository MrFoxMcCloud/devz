// Command devz collects the local development-environment commands that would
// otherwise be a drawer of shell scripts each person keeps their own copy of.
//
// It wraps our own glue, not other people's tools: there is no devz wrapper
// around kubectl, gh or terraform, because those are better documented and
// better completed than anything we would put in front of them.
package main

import (
	"os"
	"runtime/debug"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/commands"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// `go install ...@v1.2.3` does not pass ldflags, so an unstamped build falls
// back to the module version the go command recorded in the binary.
var version = "dev"

func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}

func main() {
	app := cli.New(resolveVersion())
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
