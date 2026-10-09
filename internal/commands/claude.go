package commands

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// claudeBuiltins are the subcommands compiled in, with their one-line
// descriptions. Anything else is looked for as a devz-claude-<sub> plugin.
var claudeBuiltins = []struct{ name, short string }{
	{"account", "show or set the Claude Code account for this repo"},
	{"memory", "share Claude memory and plans per repo, org and host"},
}

// Claude groups everything devz does for Claude Code on a machine with more
// than one login: which account a repo uses, and the memory, plans and
// settings the accounts share.
//
// It is a group so the pieces can move in one at a time. A subcommand that is
// still a script is reached as a devz-claude-<sub> plugin under the same name
// it will have once it is compiled in.
func Claude() *cli.Command {
	return &cli.Command{
		Name:  "claude",
		Short: "Claude Code accounts and shared memory on this machine",
		Group: true,
		Usage: `usage: devz claude <subcommand> [args]

  account   show or set the Claude Code account for this repo
  memory    share Claude memory and plans per repo, org and host

Run 'devz claude <subcommand> help' for either, and 'devz claude' to list the
plugins as well.

A subcommand that is not built in is looked up as devz-claude-<name> on PATH,
the same way 'devz <name>' finds devz-<name>. A plugin gets DEVZ_VIA=1 in its
environment. A built-in subcommand always wins over a plugin of the same name.

Requires claude.enabled in the config.`,
		Run:      runClaude,
		Complete: completeClaude,
	}
}

func runClaude(ctx *cli.Context, args []string) error {
	if len(args) == 0 {
		printClaudeOverview(ctx)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "account":
		return runClaudeAccount(ctx, rest)
	case "memory":
		return runMemory(ctx, rest)
	case "help", "-h", "--help":
		printClaudeOverview(ctx)
		return nil
	}
	if path, err := exec.LookPath("devz-claude-" + sub); err == nil {
		if code := cli.RunPlugin(path, rest); code != 0 {
			return cli.ExitError{Code: code}
		}
		return nil
	}
	return fmt.Errorf("unknown subcommand %q; run 'devz claude' to see what is available", sub)
}

func printClaudeOverview(ctx *cli.Context) {
	w := ctx.Stdout
	plugins := cli.GroupPlugins("claude")
	width := 0
	for _, b := range claudeBuiltins {
		width = max(width, len(b.name))
	}
	for _, p := range plugins {
		width = max(width, len(p.Name))
	}
	fmt.Fprint(w, "usage: devz claude <subcommand> [args]\n\n")
	for _, b := range claudeBuiltins {
		fmt.Fprintf(w, "  %-*s  %s\n", width, b.name, b.short)
	}
	if len(plugins) > 0 {
		fmt.Fprintln(w, "\nplugins (devz-claude-* on PATH):")
		for _, p := range plugins {
			fmt.Fprintf(w, "  %-*s  %s\n", width, p.Name, cli.DescribePlugin(p.Path))
		}
	}
	fmt.Fprintln(w, "\nrun 'devz help claude' for detail.")
}

func completeClaude(ctx *cli.Context, args []string) []string {
	if len(args) == 0 {
		var out []string
		for _, b := range claudeBuiltins {
			out = append(out, b.name)
		}
		for _, p := range cli.GroupPlugins("claude") {
			out = append(out, p.Name)
		}
		return out
	}
	switch args[0] {
	case "account":
		switch {
		case len(args) == 1:
			return []string{"show", "set", "pick", "list", "clear"}
		case len(args) == 2 && args[1] == "set":
			return accountNames(ctx.Config)
		case len(args) > 2 && args[1] == "set" && !strings.HasPrefix(args[len(args)-1], "--root"):
			return []string{"--root"}
		}
	case "memory":
		return completeMemory(ctx, args[1:])
	}
	return nil
}
