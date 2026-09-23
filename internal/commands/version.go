package commands

import (
	"fmt"
	"runtime"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// Version prints the running build.
func Version() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Short: "print the devz version",
		Usage: `usage: devz version

Prints the version, which is stamped at build time. On a team, the first
question about any odd behavior is "which version are you on" -- this answers it.`,
		Run: func(ctx *cli.Context, args []string) error {
			fmt.Fprintf(ctx.Stdout, "devz %s (%s/%s, %s)\n",
				ctx.Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
			return nil
		},
	}
}
