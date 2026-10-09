package commands

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// claudeBuiltins are the subcommands compiled in, with their one-line
// descriptions. Anything else is looked for as a devz-claude-<sub> plugin.
var claudeBuiltins = []struct{ name, short string }{
	{"account", "show or set the Claude Code account for this repo"},
	{"eject", "how to take this machine off the shared setup"},
	{"exec", "run a command, normally Claude Code, as this repo's account"},
	{"memory", "share Claude memory and plans per repo, org and host"},
	{"shell-init", "print the claude function for a shell rc file"},
	{"sync", "keep every account on one set of settings, skills and MCP servers"},
	{"worktree", "work on several branches of a repo at once, on its account"},
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

  account      show or set the Claude Code account for this repo
  eject        how to take this machine off the shared setup
  exec         run a command, normally Claude Code, as this repo's account
  memory       share Claude memory and plans per repo, org and host
  shell-init   print the claude function for a shell rc file
  sync         keep every account on one set of settings, skills and MCP servers
  worktree     work on several branches of a repo at once, on its account

Run 'devz claude <subcommand> help' for any of them except shell-init, and
'devz claude' to list the plugins as well.

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
	case "eject":
		return runClaudeEject(ctx, rest)
	case "exec":
		return runClaudeExec(ctx, rest)
	case "sync":
		return runClaudeSync(ctx, rest)
	case "memory":
		return runMemory(ctx, rest)
	case "shell-init":
		return runClaudeShellInit(ctx, rest)
	case "worktree":
		return runClaudeWorktree(ctx, rest)
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
			return []string{"show", "set", "pick", "list", "clear", "resolve"}
		case len(args) == 2 && args[1] == "resolve":
			return []string{"--config-dir", "--alias", "--info", "--list", "--check"}
		case len(args) == 2 && args[1] == "set":
			return accountNames(ctx.Config)
		case len(args) > 2 && args[1] == "set" && !strings.HasPrefix(args[len(args)-1], "--root"):
			return []string{"--root"}
		}
	case "memory":
		return completeMemory(ctx, args[1:])
	case "shell-init":
		if len(args) == 1 {
			return []string{"zsh", "bash"}
		}
	case "exec":
		if len(args) == 1 {
			return []string{"--keep-env", "--"}
		}
	case "sync":
		switch {
		case len(args) == 1:
			return []string{"push", "status", "pull", "--dry-run"}
		case len(args) == 2 && args[1] == "pull":
			return accountNames(ctx.Config)
		}
	case "eject":
		if len(args) == 1 {
			return []string{"--apply"}
		}
	case "worktree":
		switch {
		case len(args) == 1:
			return []string{"add", "list", "rm"}
		case args[1] == "add":
			return []string{"--name", "--account"}
		case args[1] == "rm" && len(args) == 2:
			var names []string
			if main := mainCheckout("."); main != "" {
				for _, info := range worktreeInfos(main) {
					if info.Path != main {
						names = append(names, filepath.Base(info.Path))
					}
				}
			}
			return names
		}
	}
	return nil
}
