package commands

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"

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
			label := ""
			if !isRelease(ctx.Version) {
				label = ", dev build"
			}
			fmt.Fprintf(ctx.Stdout, "devz %s (%s/%s, %s%s)\n",
				ctx.Version, runtime.GOOS, runtime.GOARCH, runtime.Version(), label)
			return nil
		},
	}
}

// releaseVersion matches a tag exactly: v1.2.3, or a prerelease like
// v2.0.0-alpha.1. Everything a build of an untagged tree reports fails it --
// "dev", a bare commit, `git describe` output (v1.2.3-4-gabc1234), Go
// pseudo-versions (v1.2.4-0.20260923185155-effaf67b0d2c) and anything +dirty --
// because each of those has a second hyphen, a "+", or no leading v. The one
// that slips through, a tag with uncommitted changes (v1.2.3-dirty), is
// excluded by isRelease.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// isRelease reports whether version names a tagged release rather than a
// build of a working tree.
func isRelease(version string) bool {
	return releaseVersion.MatchString(version) && !strings.HasSuffix(version, "-dirty")
}
