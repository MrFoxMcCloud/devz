package commands

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Account is the old top-level name for `devz claude account`. It takes the
// arguments the claude-account script took, as it always did.
func Account() *cli.Command {
	return &cli.Command{
		Name:       "account",
		Short:      "show or set the Claude Code account for this repo",
		Deprecated: "claude account",
		Hidden:     true,
		Usage: `usage: devz account [args...]

The older name for 'devz claude account'. It still works, and takes the
arguments the claude-account script took:

  devz account                    which account this directory uses, and why
  devz account <email> [--root D] set it
  devz account --select           pick from the accounts logged in here
  devz account --list             accounts available here
  devz account --clear            drop the marker that applies here

See 'devz help claude'.`,
		Run: runLegacyAccount,
	}
}

// runLegacyAccount maps the script's arguments onto the subcommands.
func runLegacyAccount(ctx *cli.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "--select", "-s":
			args = append([]string{"pick"}, args[1:]...)
		case "--list", "--accounts":
			args = append([]string{"list"}, args[1:]...)
		case "--clear":
			args = append([]string{"clear"}, args[1:]...)
		case "help", "--help", "-h", "docs":
			args = []string{"help"}
		case "--colors", "--workspaces":
			// Title-bar tints were dropped from devz. The script that draws
			// them is still reachable as a plugin where it is installed.
			return runClaude(ctx, append([]string{"workspaces"}, args...))
		default:
			if strings.HasPrefix(args[0], "-") {
				return fmt.Errorf("unknown option %q", args[0])
			}
			args = append([]string{"set"}, args...)
		}
	}
	return runClaudeAccount(ctx, args)
}

// accountUsage is the help for `devz claude account`.
const accountUsage = `usage: devz claude account [show] [DIR]
       devz claude account set <email|alias> [--root DIR]
       devz claude account pick | list | clear
       devz claude account resolve [--config-dir|--alias|--info] [DIR]
       devz claude account resolve --list | --check <email|alias>

  show     which account a directory uses, and which marker decided it
           (the default)
  set      set the repo's account: an email, an unambiguous prefix of one, or
           an alias such as 'personal'. The marker goes at the root of the
           repo's main checkout, so every linked worktree follows it. --root .
           inside a worktree sets that worktree alone
  pick     choose from the accounts logged in on this machine
  list     the accounts logged in on this machine
  clear    drop the marker that applies here
  resolve  the same answer for scripts, one value on stdout:
             (nothing)     the account email
             --config-dir  the CLAUDE_CONFIG_DIR value; an empty line means
                           the default account, reached by unsetting it
             --alias       the short name: personal, or what follows .claude-
             --info        email, alias, config dir and org, tab-separated
             --list        --info for every account
             --check NAME  --info for the account a name resolves to

HOW IT WORKS

Each Claude login lives in its own config dir: ~/.claude, the default, and
~/.claude-<name>. Claude Code picks one through CLAUDE_CONFIG_DIR. Accounts are
discovered from those dirs, so logging into another one makes it usable with
nothing to configure:

    CLAUDE_CONFIG_DIR=~/.claude-<name> claude     # then /login

Which account a directory gets is decided per repo, by a .claude-account file
holding one word: an email, an unambiguous prefix of one, or an alias.

  1. The marker in this worktree: searched from the directory up to the repo
     root, never above it.
  2. In a linked worktree with none: the marker at the root of the repo's main
     checkout. Set the account once and every worktree follows.
  3. No marker: the default account.

A marker inside one worktree overrides the repo's, which is how one repo can
be worked on two accounts at once. A marker naming an account nobody is logged
into is an error, exit status 3, never a quiet fallback to the default.

The marker is added to your global git ignore the first time one is written,
so it never shows in a repo's status.

'devz claude exec' applies the rule when launching Claude Code.
Requires claude.enabled in the config.`

func runClaudeAccount(ctx *cli.Context, args []string) error {
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s", mustConfigPath())
	}
	sub := "show"
	if len(args) > 0 {
		switch args[0] {
		case "show", "set", "pick", "list", "clear", "resolve":
			sub, args = args[0], args[1:]
		case "help", "-h", "--help":
			fmt.Fprintln(ctx.Stdout, accountUsage)
			return nil
		default:
			if strings.HasPrefix(args[0], "-") {
				return fmt.Errorf("unknown option %q; see 'devz claude account help'", args[0])
			}
		}
	}

	var err error
	switch sub {
	case "show":
		err = accountShow(ctx, args)
	case "set":
		err = accountSet(ctx, args)
	case "pick":
		err = accountPick(ctx, args)
	case "list":
		err = accountList(ctx)
	case "clear":
		err = accountClear(ctx)
	case "resolve":
		err = accountResolve(ctx, args)
	}
	return unresolvedExit(ctx, err)
}

// unresolvedExit turns "that account is not logged in here" into its message
// on stderr and exit status 3, which launchers test for.
func unresolvedExit(ctx *cli.Context, err error) error {
	var unresolved unresolvedError
	if errors.As(err, &unresolved) {
		fmt.Fprintf(ctx.Stderr, "devz claude account: %s\n", unresolved.msg)
		return cli.ExitError{Code: exitUnresolved}
	}
	return err
}

func accountShow(ctx *cli.Context, args []string) error {
	dir := "."
	if len(args) > 1 {
		return fmt.Errorf("show takes one DIR")
	}
	if len(args) == 1 {
		dir = args[0]
	}
	acct, marker, err := resolveAccount(ctx.Config, dir)
	if err != nil {
		return err
	}
	if marker != "" {
		fmt.Fprintf(ctx.Stdout, "%s   (from %s)\n", acct.Email, marker)
	} else {
		fmt.Fprintf(ctx.Stdout, "%s   (default: no %s for this repo)\n", acct.Email, accountMarker)
	}
	fmt.Fprintf(ctx.Stdout, "config dir: %s\n", acct.ConfigDir)
	fmt.Fprintln(ctx.Stdout, "run 'devz claude account help' for how this works")
	return nil
}

func accountList(ctx *cli.Context) error {
	known := claudeAccounts(ctx.Config)
	if len(known) == 0 {
		return unresolvedError{"no config dir is logged in: run `claude` and /login"}
	}
	printAccounts(ctx.Stdout, known)
	return nil
}

func printAccounts(w interface{ Write([]byte) (int, error) }, known []claudeAccount) {
	for i, a := range known {
		fmt.Fprintf(w, "  %d) %-30s %-10s %s\n", i+1, a.Email, "["+a.Alias+"]", a.Org)
	}
}

func accountSet(ctx *cli.Context, args []string) error {
	var name, root string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--root":
			if i+1 >= len(args) {
				return fmt.Errorf("--root needs a directory")
			}
			i++
			root = args[i]
		case strings.HasPrefix(args[i], "--root="):
			root = strings.TrimPrefix(args[i], "--root=")
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown option %q", args[i])
		case name != "":
			return fmt.Errorf("set takes one account, got %q and %q", name, args[i])
		default:
			name = args[i]
		}
	}
	if name == "" {
		return fmt.Errorf("set needs an account: an email, a prefix of one, or an alias")
	}
	return writeAccountMarker(ctx, name, root)
}

// writeAccountMarker validates name against the logged-in accounts first, so
// a marker that cannot resolve is never left behind, and stores the canonical
// email whatever spelling was given.
func writeAccountMarker(ctx *cli.Context, name, root string) error {
	known := claudeAccounts(ctx.Config)
	if len(known) == 0 {
		return unresolvedError{"no config dir is logged in: run `claude` and /login"}
	}
	acct, err := matchAccount(name, known)
	if err != nil {
		return err
	}
	if root == "" {
		root = accountMarkerRoot(".")
	} else {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("not a directory: %s", root)
		}
		if root, err = filepath.Abs(root); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(root, accountMarker), []byte(acct.Email+"\n"), 0o644); err != nil {
		return err
	}
	org := acct.Org
	if org == "" {
		org = "(no org)"
	}
	fmt.Fprintf(ctx.Stdout, "%s -> %s\n  %s -- %s\n", root, acct.Email, org, acct.ConfigDir)

	// Once, globally, rather than a tracked .gitignore line that would show
	// as a change in every checkout.
	if ignore, err := globalExcludesFile(); err == nil {
		if added, err := ensureLine(ignore, accountMarker, false); err == nil && added {
			fmt.Fprintf(ctx.Stdout, "added %s to %s\n", accountMarker, tildePath(ignore))
		}
	}
	fmt.Fprintln(ctx.Stdout, "Reload the VS Code window, or restart the Claude session, to pick it up.")
	return nil
}

func accountPick(ctx *cli.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("pick takes no arguments")
	}
	known := claudeAccounts(ctx.Config)
	if len(known) == 0 {
		return unresolvedError{"no config dir is logged in: run `claude` and /login"}
	}
	fmt.Fprintln(ctx.Stderr, "Accounts on this machine:")
	printAccounts(ctx.Stderr, known)
	if !isTerminal(os.Stdin) {
		return fmt.Errorf("not a terminal; name the account: devz claude account set <email>")
	}
	fmt.Fprintf(ctx.Stderr, "select [1-%d]: ", len(known))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(known) {
		return fmt.Errorf("not a choice between 1 and %d: %q", len(known), strings.TrimSpace(line))
	}
	return writeAccountMarker(ctx, known[n-1].Email, "")
}

func accountClear(ctx *cli.Context) error {
	marker := findAccountMarker(".")
	if marker == "" {
		fmt.Fprintln(ctx.Stdout, "no marker found")
		return nil
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "removed %s\n", marker)
	return nil
}

// accountResolve is the machine-readable form: one value on stdout, and exit
// status 3 when the account named is not logged in here.
func accountResolve(ctx *cli.Context, args []string) error {
	mode, dir := "email", "."
	info := func(a claudeAccount) string {
		return a.Email + "\t" + a.Alias + "\t" + a.ConfigDir + "\t" + a.Org
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--list":
			for _, a := range claudeAccounts(ctx.Config) {
				fmt.Fprintln(ctx.Stdout, info(a))
			}
			return nil
		case "--check":
			if len(args) < 2 {
				return fmt.Errorf("--check needs an account")
			}
			known := claudeAccounts(ctx.Config)
			if len(known) == 0 {
				return unresolvedError{"no config dir is logged in: run `claude` and /login"}
			}
			acct, err := matchAccount(args[1], known)
			if err != nil {
				return err
			}
			fmt.Fprintln(ctx.Stdout, info(acct))
			return nil
		case "--config-dir", "--alias", "--info", "--email":
			mode = args[0][2:]
		default:
			return fmt.Errorf("unknown flag %q", args[0])
		}
		args = args[1:]
	}
	if len(args) > 1 {
		return fmt.Errorf("resolve takes one DIR")
	}
	if len(args) == 1 {
		dir = args[0]
	}
	acct, _, err := resolveAccount(ctx.Config, dir)
	if err != nil {
		return err
	}
	switch mode {
	case "email":
		fmt.Fprintln(ctx.Stdout, acct.Email)
	case "alias":
		fmt.Fprintln(ctx.Stdout, acct.Alias)
	case "info":
		fmt.Fprintln(ctx.Stdout, info(acct))
	case "config-dir":
		// Empty means the default dir, which is reached by unsetting the
		// variable, not by setting it to ~/.claude.
		fmt.Fprintln(ctx.Stdout, acct.configDirEnv())
	}
	return nil
}

// execUsage is the help for `devz claude exec`.
const execUsage = `usage: devz claude exec [--keep-env] [--] <command> [args]

Runs a command, normally Claude Code itself, as the account this directory's
repo uses: it sets CLAUDE_CONFIG_DIR for a named account, or unsets it for the
default one, then replaces itself with the command.

  --keep-env   leave a CLAUDE_CONFIG_DIR that is already set alone. For a
               shell, where setting it by hand is how you ask for an account.
               Without it the marker always decides, which is right for an
               editor: its windows inherit an environment that says nothing
               about the folder they have open.

If the marker names an account nobody is logged into, nothing is run and the
exit status is 3. Starting on the wrong account is never the fallback.

  VS Code:  set claudeCode.claudeProcessWrapper to a script containing
                exec devz claude exec -- "$@"
  a shell:  see 'devz claude shell-init zsh'`

// runClaudeExec starts a command as the account that owns the current
// directory. It is the one launch path: an editor's process wrapper and the
// shell function both end here.
func runClaudeExec(ctx *cli.Context, args []string) error {
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s", mustConfigPath())
	}
	keep := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--keep-env":
			keep = true
		case "--":
			args = args[1:]
			goto parsed
		case "-h", "--help":
			fmt.Fprintln(ctx.Stdout, execUsage)
			return nil
		default:
			return fmt.Errorf("unknown flag %q", args[0])
		}
		args = args[1:]
	}
parsed:
	if len(args) == 0 {
		return fmt.Errorf("usage: devz claude exec [--keep-env] [--] <command> [args]")
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}

	env := os.Environ()
	if !keep || os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		acct, _, err := resolveAccount(ctx.Config, ".")
		if err != nil {
			return unresolvedExit(ctx, err)
		}
		env = withConfigDir(env, acct.configDirEnv())
	}
	// Replace devz rather than run a child, so stdio, signals and the exit
	// status are the command's own.
	return syscall.Exec(path, args, env)
}

// withConfigDir returns env with CLAUDE_CONFIG_DIR set to dir, or removed
// when dir is "".
func withConfigDir(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			out = append(out, kv)
		}
	}
	if dir != "" {
		out = append(out, "CLAUDE_CONFIG_DIR="+dir)
	}
	return out
}

// shellInit is what `devz claude shell-init zsh` prints: a `claude` function
// to paste into an rc file. It is printed, not written, because the rc file
// is the user's.
const shellInit = `# Claude Code on the account this directory's repo uses. See
# 'devz claude account help'. A CLAUDE_CONFIG_DIR you set by hand still wins.
# If devz is missing or the marker does not resolve, nothing is started:
# being quietly on the wrong account is the failure this prevents.
# DEVZ_BIN points it at another devz build for one shell.
claude() {
  command "${DEVZ_BIN:-devz}" claude exec --keep-env -- claude "$@"
}
`

func runClaudeShellInit(ctx *cli.Context, args []string) error {
	if len(args) != 1 || (args[0] != "zsh" && args[0] != "bash") {
		return fmt.Errorf("usage: devz claude shell-init <zsh|bash>   (prints a function for your rc file)")
	}
	fmt.Fprint(ctx.Stdout, shellInit)
	return nil
}

// accountNames lists the logged-in accounts' emails and aliases, for
// completing `devz claude account set`.
func accountNames(cfg config.Config) []string {
	var names []string
	for _, a := range claudeAccounts(cfg) {
		names = append(names, a.Email, a.Alias)
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
