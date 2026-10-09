package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Account is the old top-level name for `devz claude account`. It takes
// claude-account's own arguments unchanged, as it always did.
func Account() *cli.Command {
	return &cli.Command{
		Name:       "account",
		Short:      "show or set the Claude Code account for this repo",
		Deprecated: "claude account",
		Hidden:     true,
		Usage: `usage: devz account [args...]

The older name for 'devz claude account'. It still works, and passes its
arguments to claude-account unchanged:

  devz account              which account this directory uses, and why
  devz account --select     pick from the accounts logged in on this machine
  devz account --list       accounts available here

See 'devz help claude'.`,
		Run: claudeAccountTool,
	}
}

// accountUsage is the help for `devz claude account`.
const accountUsage = `usage: devz claude account [show]
       devz claude account set <email|alias> [--root DIR]
       devz claude account pick | list | clear

  show    which account this directory uses, and which marker decided it
          (the default)
  set     set the repo's account: an email, an unambiguous prefix of one, or
          an alias such as 'personal'. The marker goes at the root of the
          repo's main checkout, so every linked worktree follows it. --root .
          inside a worktree sets that worktree alone
  pick    choose from the accounts logged in on this machine
  list    the accounts logged in on this machine
  clear   drop the marker that applies here

The account is a property of the repo: one .claude-account file, and every
worktree of the repo uses it. A marker naming an account nobody is logged into
is an error (exit 3), never a quiet fallback to the default account.

This runs claude-account from claude.sharedDir, which owns the marker rule.
Requires claude.enabled and claude.sharedDir in the config.`

// runClaudeAccount translates the subcommands into claude-account's own
// arguments. Anything it does not recognize is handed over as it is, so an
// option the tool has and devz does not name is still reachable.
func runClaudeAccount(ctx *cli.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			args = args[1:]
		case "set":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				return fmt.Errorf("set needs an account: an email, a prefix of one, or an alias")
			}
			args = args[1:]
		case "pick":
			args = append([]string{"--select"}, args[1:]...)
		case "list":
			args = append([]string{"--list"}, args[1:]...)
		case "clear":
			args = append([]string{"--clear"}, args[1:]...)
		case "help", "-h", "--help":
			fmt.Fprintln(ctx.Stdout, accountUsage)
			return nil
		}
	}
	return claudeAccountTool(ctx, args)
}

// claudeAccountTool runs claude-account with args. It is a passthrough rather
// than a reimplementation: claude-account owns the marker rule, and two
// implementations of one rule is how they drift.
func claudeAccountTool(ctx *cli.Context, args []string) error {
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s",
			mustConfigPath())
	}

	bin := "claude-account"
	if shared := config.Expand(ctx.Config.Claude.SharedDir); shared != "" {
		candidate := filepath.Join(shared, "bin", "claude-account")
		if exists(candidate) {
			bin = candidate
		}
	}
	if _, err := exec.LookPath(bin); err != nil && !exists(bin) {
		return fmt.Errorf("claude-account not found (looked in claude.sharedDir and PATH)")
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Tells the tool it was reached through devz, not by its old name.
	cmd.Env = append(os.Environ(), "DEVZ_VIA=1")
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// The tool has already said what went wrong, and its exit status
		// means something: 3 is "that account is not logged in here".
		return cli.ExitError{Code: exit.ExitCode()}
	}
	return err
}

// accountNames lists the logged-in accounts' emails and aliases, for
// completing `devz claude account set`.
func accountNames(cfg config.Config) []string {
	resolve := filepath.Join(config.Expand(cfg.Claude.SharedDir), "bin", "claude-account-resolve")
	out, err := output(resolve, "--list")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Split(line, "\t"); len(fields) >= 2 {
			names = append(names, fields[0], fields[1])
		}
	}
	return names
}

func mustConfigPath() string {
	path, err := config.Path()
	if err != nil {
		return "the devz config"
	}
	return path
}
