package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// worktreeUsage is the help for `devz claude worktree`.
const worktreeUsage = `usage: devz claude worktree add [--name TOPIC] [--account NAME] <branch> [<start-point>]
       devz claude worktree list
       devz claude worktree rm [--force] <topic|branch|path>

Work on several branches of one repository at once, without a second clone.
Run it anywhere inside the repository.

  add    make a linked worktree for <branch>, beside the main checkout, as
         .wt-<repo>-<topic>. TOPIC defaults to the last part of the branch
         name. An existing local branch is checked out; a branch that exists
         only on origin is tracked; otherwise the branch is created from
         <start-point>, by default origin's default branch.
         --account puts this one worktree on another Claude account. Without
         it the worktree uses the repo's, like every other
  list   every working tree of this repo: its branch and its Claude account
  rm     remove a worktree. The branch is kept. git refuses if the worktree
         has uncommitted work; --force passes that on

A worktree needs no other setup. Claude Code reads the main checkout's
.claude/settings.local.json in every worktree of a repo, so memory and plans
are already shared, and the repo's .claude-account applies to all of them.

Why beside the main checkout and not inside it: the folder above a checkout
is where the org's CLAUDE.md is linked, so a sibling loads the same shared
context. Claude Code's own agent worktrees, under .claude/worktrees/, do too.`

func runClaudeWorktree(ctx *cli.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected add, list or rm; see 'devz claude worktree help'")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprintln(ctx.Stdout, worktreeUsage)
		return nil
	}
	main := mainCheckout(".")
	if main == "" {
		return fmt.Errorf("not inside a git repository with a main checkout")
	}
	switch sub {
	case "add":
		return worktreeAdd(ctx, main, rest)
	case "list", "ls":
		return worktreeList(ctx, main)
	case "rm", "remove":
		return worktreeRemove(ctx, main, rest)
	}
	return fmt.Errorf("unknown subcommand %q; expected add, list or rm", sub)
}

var notSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

// worktreeTopic turns a branch name into the short name a worktree's
// directory ends in: what follows the last slash, in lower case.
func worktreeTopic(branch string) string {
	topic := branch[strings.LastIndex(branch, "/")+1:]
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(topic), "-"), "-.")
}

// worktreePath is where a worktree for topic goes: beside the main checkout.
func worktreePath(main, topic string) string {
	return filepath.Join(filepath.Dir(main), ".wt-"+filepath.Base(main)+"-"+topic)
}

func worktreeAdd(ctx *cli.Context, main string, args []string) error {
	var name, account string
	var words []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--name" || args[i] == "--account":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", args[i])
			}
			if args[i] == "--name" {
				name = args[i+1]
			} else {
				account = args[i+1]
			}
			i++
		case strings.HasPrefix(args[i], "--name="):
			name = strings.TrimPrefix(args[i], "--name=")
		case strings.HasPrefix(args[i], "--account="):
			account = strings.TrimPrefix(args[i], "--account=")
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("unknown flag %q", args[i])
		default:
			words = append(words, args[i])
		}
	}
	if len(words) < 1 || len(words) > 2 {
		return fmt.Errorf("usage: devz claude worktree add [--name TOPIC] [--account NAME] <branch> [<start-point>]")
	}
	branch := words[0]
	topic := worktreeTopic(branch)
	if name != "" {
		topic = worktreeTopic(name)
	}
	if topic == "" {
		return fmt.Errorf("cannot make a directory name from %q; give one with --name", branch)
	}
	path := worktreePath(main, topic)
	if exists(path) {
		return fmt.Errorf("%s already exists; pick another name with --name", tildePath(path))
	}

	// Validate the account before anything is created, so a worktree is
	// never left behind on an account that cannot resolve.
	var override claudeAccount
	if account != "" {
		known := claudeAccounts(ctx.Config)
		acct, err := matchAccount(account, known)
		if err != nil {
			return unresolvedExit(ctx, err)
		}
		override = acct
	}

	hasRef := func(ref string) bool {
		_, err := output("git", "-C", main, "rev-parse", "-q", "--verify", ref)
		return err == nil
	}
	var gitArgs []string
	var how string
	switch {
	case hasRef("refs/heads/" + branch):
		if len(words) == 2 {
			return fmt.Errorf("%s already exists, so a start point does not apply", branch)
		}
		gitArgs, how = []string{path, branch}, "existing branch"
	case len(words) == 1 && hasRef("refs/remotes/origin/"+branch):
		gitArgs, how = []string{"--track", "-b", branch, path, "origin/" + branch}, "tracking origin/"+branch
	default:
		start := "HEAD"
		if len(words) == 2 {
			start = words[1]
		} else if def, err := output("git", "-C", main, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil && def != "" {
			start = def
		}
		// A new branch should not quietly track the branch it started from:
		// its first push would then aim at that branch's name.
		gitArgs, how = []string{"--no-track", "-b", branch, path, start}, "new branch from "+start
	}

	cmd := exec.Command("git", append([]string{"-C", main, "worktree", "add"}, gitArgs...)...)
	cmd.Stdout, cmd.Stderr = ctx.Stderr, ctx.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git worktree add failed")
	}

	if account != "" {
		if err := os.WriteFile(filepath.Join(path, accountMarker), []byte(override.Email+"\n"), 0o644); err != nil {
			return err
		}
		if ignore, err := globalExcludesFile(); err == nil {
			ensureLine(ignore, accountMarker, false)
		}
	}

	fmt.Fprintf(ctx.Stdout, "%s  (%s, %s)\n", path, branch, how)
	if ctx.Config.Claude.Enabled {
		acct, _, err := resolveAccount(ctx.Config, path)
		switch {
		case err != nil:
			fmt.Fprintf(ctx.Stdout, "  account  does not resolve: %v\n", err)
		case account != "":
			fmt.Fprintf(ctx.Stdout, "  account  %s (this worktree only)\n", acct.Email)
		default:
			fmt.Fprintf(ctx.Stdout, "  account  %s (the repo's)\n", acct.Email)
		}
		if l, err := resolveMemoryRepo(ctx.Config, path); err == nil {
			if shared, _ := memorySettingsState(l); shared {
				fmt.Fprintf(ctx.Stdout, "  memory   shared with the repo: %s\n", tildePath(l.repoMemory()))
			} else {
				fmt.Fprintf(ctx.Stdout, "  memory   not set up for this repo yet: devz claude memory init %s\n", tildePath(l.Top))
			}
		}
	}
	return nil
}

// worktreeInfo is one working tree of a repo.
type worktreeInfo struct {
	Path, Branch string
	Bare         bool
}

// worktreeInfos lists the working trees of the repo containing dir, the main
// checkout first.
func worktreeInfos(dir string) []worktreeInfo {
	out, err := output("git", "-C", dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var infos []worktreeInfo
	for _, block := range strings.Split(out, "\n\n") {
		var info worktreeInfo
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				info.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch refs/heads/"):
				info.Branch = strings.TrimPrefix(line, "branch refs/heads/")
			case line == "detached":
				info.Branch = "(detached)"
			case line == "bare":
				info.Bare = true
			}
		}
		if info.Path != "" && !info.Bare {
			infos = append(infos, info)
		}
	}
	return infos
}

func worktreeList(ctx *cli.Context, main string) error {
	infos := worktreeInfos(main)
	known := claudeAccounts(ctx.Config)
	nameWidth, branchWidth := 0, 0
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = tildePath(info.Path)
		nameWidth = max(nameWidth, len(names[i]))
		branchWidth = max(branchWidth, len(info.Branch))
	}
	for i, info := range infos {
		account := ""
		if ctx.Config.Claude.Enabled && len(known) > 0 {
			acct, marker, err := resolveAmong(known, info.Path)
			switch {
			case err != nil:
				account = "account does not resolve"
			case info.Path != main && marker != "" && filepath.Dir(marker) == info.Path:
				account = acct.Email + " (this worktree only)"
			default:
				account = acct.Email
			}
		}
		note := ""
		if info.Path == main {
			note = "  main checkout"
		}
		fmt.Fprintf(ctx.Stdout, "%-*s  %-*s  %s%s\n", nameWidth, names[i], branchWidth, info.Branch, account, note)
	}
	return nil
}

func worktreeRemove(ctx *cli.Context, main string, args []string) error {
	force := false
	var target string
	for _, a := range args {
		switch {
		case a == "--force" || a == "-f":
			force = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		case target != "":
			return fmt.Errorf("rm takes one worktree")
		default:
			target = a
		}
	}
	if target == "" {
		return fmt.Errorf("usage: devz claude worktree rm [--force] <topic|branch|path>")
	}

	// A path, a branch, the directory's name, or the topic it was made with.
	var match *worktreeInfo
	abs, _ := filepath.Abs(target)
	infos := worktreeInfos(main)
	for i, info := range infos {
		if info.Path == abs || info.Branch == target || filepath.Base(info.Path) == target ||
			info.Path == worktreePath(main, worktreeTopic(target)) {
			match = &infos[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no worktree of this repo matches %q; see 'devz claude worktree list'", target)
	}
	if match.Path == main {
		return fmt.Errorf("%s is the main checkout, not a linked worktree", tildePath(main))
	}
	gitArgs := []string{"-C", main, "worktree", "remove"}
	if force {
		gitArgs = append(gitArgs, "--force")
	}
	cmd := exec.Command("git", append(gitArgs, match.Path)...)
	cmd.Stdout, cmd.Stderr = ctx.Stderr, ctx.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git worktree remove failed; the worktree is untouched")
	}
	fmt.Fprintf(ctx.Stdout, "removed %s; the branch %s is kept\n", tildePath(match.Path), match.Branch)
	return nil
}
