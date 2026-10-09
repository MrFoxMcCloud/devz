package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Doctor checks that this machine is set up the way devz expects.
//
// On a shared tool this is the highest-value command: "is my machine right"
// is the question that otherwise becomes a direct message to whoever wrote it.
func Doctor(app *cli.App) *cli.Command {
	return &cli.Command{
		Complete: func(*cli.Context, []string) []string { return []string{"--quiet", "--all"} },
		Name:     "doctor",
		Short:    "check this machine's development environment",
		Usage: `usage: devz doctor [--quiet] [--all]

Runs every check that applies to this machine and prints a fix for anything
that is off. Exits non-zero if any check fails, so it is usable in a script.

  --quiet    print only problems
  --all      also check every checkout under claude.memory.roots, and the
             Claude account of each of their linked worktrees

Checks are skipped per-machine via "doctor.skip" in the config, which is how a
teammate on a different OS turns off what does not apply to them. See
'devz config show'.`,
		Run: func(ctx *cli.Context, args []string) error { return runDoctor(ctx, app, args) },
	}
}

func runDoctor(ctx *cli.Context, app *cli.App, args []string) error {
	quiet, all := false, false
	for _, a := range args {
		switch a {
		case "--quiet", "-q":
			quiet = true
		case "--all":
			all = true
		default:
			return fmt.Errorf("unknown flag %q", a)
		}
	}

	cfg := ctx.Config
	var results []result
	add := func(rs ...result) {
		for _, r := range rs {
			if cfg.Doctor.Skipped(r.id) {
				r = skip(r.id, "skipped by config")
			}
			// --all can reach a finding the checks for this directory already
			// made, such as the one host file every repo shares.
			if slices.ContainsFunc(results, func(seen result) bool {
				return seen.id == r.id && seen.detail == r.detail
			}) {
				continue
			}
			results = append(results, r)
		}
	}

	add(checkBuild(ctx.Version))
	add(checkConfig(cfg))
	add(checkTools(cfg)...)
	add(checkPython())
	if cfg.Secrets.Backend == "pass" {
		add(checkGPG(cfg)...)
		add(checkPassStore(cfg)...)
	}
	if cfg.Claude.Enabled {
		add(checkClaude(cfg)...)
		if all {
			add(checkAllCheckouts(cfg)...)
		}
	}
	add(checkGH())
	add(checkCompletion()...)
	add(checkPlugins(app)...)
	add(checkDeprecated(time.Now()))

	width := 0
	for _, r := range results {
		if len(r.id) > width {
			width = len(r.id)
		}
	}
	color := colorEnabled(ctx.Stdout)

	failures, warnings := 0, 0
	for _, r := range results {
		switch r.status {
		case statusFail:
			failures++
		case statusWarn:
			warnings++
		}
		if quiet && (r.status == statusOK || r.status == statusSkip) {
			continue
		}
		r.write(ctx.Stdout, width, color)
	}

	fmt.Fprintf(ctx.Stdout, "\n%d checks, %d failed, %d warnings (profile: %s)\n",
		len(results), failures, warnings, cfg.Profile)
	if failures > 0 {
		return fmt.Errorf("%d check(s) failed", failures)
	}
	return nil
}

// checkBuild flags that the running devz is a work-in-progress build.
// `make install` keeps those in the checkout; one that reaches PATH anyway is
// how a stale, unreleased build ends up answering every call for weeks.
func checkBuild(version string) result {
	if isRelease(version) {
		return ok("devz:build", "release "+version)
	}
	return warn("devz:build", "dev build "+version,
		"go install github.com/MrFoxMcCloud/devz@v1 to return to a release")
}

func checkConfig(cfg config.Config) result {
	path, err := config.Path()
	if err != nil {
		return fail("config", err.Error(), "")
	}
	if !exists(path) {
		return warn("config", "no config file; using defaults",
			"devz config init")
	}
	return ok("config", path)
}

func checkTools(cfg config.Config) []result {
	var out []result
	for _, tool := range cfg.Doctor.RequiredTools {
		id := "tool:" + tool
		if path, found := look(tool); found {
			out = append(out, ok(id, path))
		} else {
			out = append(out, fail(id, "not on PATH", "install "+tool))
		}
	}
	return out
}

// checkPython exists because a conda base environment that auto-activates
// silently owns `python3` for every script with a `#!/usr/bin/env python3`
// shebang -- including ones that have nothing to do with conda.
func checkPython() result {
	path, found := look("python3")
	if !found {
		return warn("python", "no python3 on PATH", "")
	}
	if strings.Contains(path, "conda") || strings.Contains(path, "miniconda") {
		return warn("python", "python3 resolves to conda: "+path,
			"conda config --set auto_activate_base false (then open a new shell)")
	}
	return ok("python", path)
}

func checkGPG(cfg config.Config) []result {
	var out []result

	if _, found := look("gpg"); !found {
		return []result{fail("gpg", "not on PATH", "install gnupg")}
	}

	if cfg.Secrets.GPGKey != "" {
		if _, err := output("gpg", "--list-secret-keys", cfg.Secrets.GPGKey); err != nil {
			out = append(out, fail("gpg:key",
				"secret key "+cfg.Secrets.GPGKey+" not in this keyring",
				"import the key, or fix secrets.gpgKey in the config"))
		} else {
			out = append(out, ok("gpg:key", cfg.Secrets.GPGKey))
		}
	}

	// pinentry-curses cannot prompt without GPG_TTY. This is the single most
	// common reason a passphrase prompt never appears.
	if os.Getenv("GPG_TTY") == "" {
		out = append(out, warn("gpg:tty", "GPG_TTY is unset",
			`add: export GPG_TTY="$TTY"  (zsh) to your shell rc`))
	} else {
		out = append(out, ok("gpg:tty", os.Getenv("GPG_TTY")))
	}

	out = append(out, checkAgentCache())
	return out
}

// checkAgentCache reports whether any key's passphrase is currently cached.
//
// This matters because tools spawned without a TTY -- MCP servers, editor
// extensions -- cannot prompt, so a cold cache shows up as an unrelated-looking
// startup failure elsewhere.
func checkAgentCache() result {
	out, err := output("gpg-connect-agent", "keyinfo --list", "/bye")
	if err != nil {
		return warn("gpg:cache", "could not query gpg-agent", "")
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// S KEYINFO <keygrip> D - - - <cached> P - - -
		if len(fields) > 6 && fields[0] == "S" && fields[1] == "KEYINFO" && fields[6] == "1" {
			return ok("gpg:cache", "warm")
		}
	}
	return warn("gpg:cache", "cold -- tools without a TTY cannot unlock",
		"devz secrets unlock")
}

func checkPassStore(cfg config.Config) []result {
	var out []result

	if _, found := look("pass"); !found {
		return []result{fail("pass", "not on PATH", "install pass")}
	}

	store := config.Expand(cfg.Secrets.Store)
	if store == "" {
		home, _ := os.UserHomeDir()
		store = filepath.Join(home, ".password-store")
	}
	if !exists(store) {
		return append(out, fail("pass:store", store+" does not exist",
			"pass init <gpg-key-id>"))
	}
	out = append(out, ok("pass:store", store))

	// Entry presence is checked on disk rather than by decrypting, so doctor
	// never triggers a passphrase prompt of its own.
	var missing []string
	for _, entry := range cfg.Secrets.Entries {
		if !exists(filepath.Join(store, entry+".gpg")) {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		out = append(out, fail("pass:entries",
			"missing: "+strings.Join(missing, ", "),
			"pass insert "+missing[0]))
	} else if len(cfg.Secrets.Entries) > 0 {
		out = append(out, ok("pass:entries",
			fmt.Sprintf("%d present", len(cfg.Secrets.Entries))))
	}

	// A store with no git remote is a single copy on a single disk.
	if !exists(filepath.Join(store, ".git")) {
		out = append(out, warn("pass:backup", "store is not a git repo",
			"pass git init && pass git remote add origin <private remote>"))
	} else {
		out = append(out, ok("pass:backup", "git-backed"))
	}

	return out
}

func checkClaude(cfg config.Config) []result {
	var out []result
	shared := config.Expand(cfg.Claude.SharedDir)
	if !exists(shared) {
		return []result{warn("claude:shared", shared+" not found",
			"disable with claude.enabled=false if this machine has no Claude setup")}
	}
	out = append(out, ok("claude:shared", shared))
	out = append(out, checkGitBackup("claude:shared-backup", shared))
	store := cfg.Claude.StoreDir()
	for _, host := range cfg.Claude.Memory.Hosts {
		if dir := filepath.Join(store, host); exists(dir) {
			// The store may be one repo, or one per host directory.
			out = append(out, checkGitBackup("claude:memory-backup", dir, store))
		}
	}

	known := claudeAccounts(cfg)
	if len(known) == 0 {
		return append(out, warn("claude:accounts", "no config dir is logged in", "run `claude`, then /login"))
	}
	logins := make([]string, len(known))
	for i, a := range known {
		logins[i] = a.Email + " [" + a.Alias + "]"
	}
	out = append(out, ok("claude:accounts", strings.Join(logins, ", ")))
	out = append(out, checkLaunch()...)

	cwd, _ := os.Getwd()
	acct, marker, err := resolveAccount(cfg, cwd)
	if err != nil {
		detail := "the marker here does not resolve"
		if marker != "" {
			detail = tildePath(marker) + " names an account nobody is logged into"
		}
		return append(out, warn("claude:account", detail,
			"devz claude account list, then devz claude account set <email>"))
	}
	out = append(out, ok("claude:account", acct.Email))
	return append(out, checkMemory(cfg, cwd)...)
}

// checkLaunch looks at the two places Claude Code is started from, since the
// account rule only helps where something applies it. It reads files; it
// does not start a shell or an editor.
func checkLaunch() []result {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []result

	// VS Code. The setting is machine-scoped, so one file decides for every
	// window. Only checked where VS Code keeps settings on this machine.
	for _, file := range []string{
		filepath.Join(home, ".vscode-server", "data", "Machine", "settings.json"),
		filepath.Join(home, ".config", "Code", "User", "settings.json"),
		filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json"),
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		wrapper := jsonStringValue(string(data), "claudeCode.claudeProcessWrapper")
		switch {
		case wrapper == "":
			out = append(out, warn("claude:launch", "VS Code starts Claude on the default account in every window: "+
				tildePath(file)+" sets no claudeCode.claudeProcessWrapper",
				`point it at a script containing: exec devz claude exec -- "$@"`))
		case !exists(config.Expand(wrapper)):
			out = append(out, fail("claude:launch", "VS Code's Claude wrapper does not exist: "+wrapper,
				`create it, containing: exec devz claude exec -- "$@"`))
		default:
			out = append(out, ok("claude:launch", "VS Code: "+tildePath(wrapper)))
		}
		break
	}

	// The shell. A function cannot be seen from here, so look for what
	// defines it in the rc file of the login shell.
	rc := ""
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		rc = filepath.Join(home, ".zshrc")
	case "bash":
		rc = filepath.Join(home, ".bashrc")
	}
	if data, err := os.ReadFile(rc); err == nil {
		text := string(data)
		if strings.Contains(text, "claude exec") || strings.Contains(text, "claude-account-resolve") {
			out = append(out, ok("claude:launch", "shell: "+tildePath(rc)+" applies the account rule"))
		} else {
			out = append(out, warn("claude:launch", "a bare `claude` in a terminal ignores the repo's account: nothing in "+
				tildePath(rc)+" applies the rule",
				"devz claude shell-init "+filepath.Base(os.Getenv("SHELL"))+" >> "+tildePath(rc)))
		}
	}
	return out
}

// jsonStringValue pulls one string setting out of a settings file that may
// hold comments, which VS Code allows and encoding/json does not. It is a
// read-only peek, so a line match is enough.
func jsonStringValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, `"`+key+`"`)
		if !found {
			continue
		}
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
		if !strings.HasPrefix(rest, `"`) {
			return ""
		}
		value, _, _ := strings.Cut(rest[1:], `"`)
		return value
	}
	return ""
}

// checkGitBackup reports whether dir is backed up: a git repo of its own (or
// inside one of parents) with a remote and nothing uncommitted or unpushed.
// It runs no network command, so "pushed" is measured against the upstream as
// last fetched.
func checkGitBackup(id, dir string, parents ...string) result {
	at := tildePath(dir)
	top, err := output("git", "-C", dir, "rev-parse", "--show-toplevel")
	owned := err == nil && samePath(top, dir)
	for _, parent := range parents {
		owned = owned || (err == nil && samePath(top, parent))
	}
	if !owned {
		// A repo further up that ignores this directory backs up nothing.
		return warn(id, at+" is not a git repo: one copy, one disk",
			"git -C "+at+" init, then add a private remote")
	}
	if remotes, _ := output("git", "-C", top, "remote"); remotes == "" {
		return warn(id, at+" has no remote: one copy, one disk",
			"git -C "+at+" remote add origin <private remote>")
	}
	if dirty, _ := output("git", "-C", top, "status", "--porcelain"); dirty != "" {
		return warn(id, fmt.Sprintf("%s has %d uncommitted change(s)", at, len(strings.Split(dirty, "\n"))),
			"git -C "+at+" add -A && git -C "+at+" commit")
	}
	ahead, err := output("git", "-C", top, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return warn(id, at+" is on a branch with no upstream", "git -C "+at+" push -u origin HEAD")
	}
	if ahead != "0" {
		return warn(id, fmt.Sprintf("%s has %s commit(s) not pushed", at, ahead), "git -C "+at+" push")
	}
	return ok(id, at+": committed and pushed")
}

// samePath compares two paths after resolving symlinks.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// checkAllCheckouts runs the per-repo checks over every checkout under
// claude.memory.roots, and resolves the account of each of their working
// trees. It prints one line per problem, or one summary line when there is
// none: a machine with thirty checkouts should not print ninety oks.
func checkAllCheckouts(cfg config.Config) []result {
	roots := cfg.Claude.Memory.Roots
	if len(roots) == 0 {
		return []result{skip("claude:all", "claude.memory.roots not set")}
	}
	known := claudeAccounts(cfg)
	var out []result
	checkouts, trees := 0, 0
	seen, ruleSeen := map[string]bool{}, map[string]bool{}
	for _, root := range roots {
		for _, top := range findCheckouts(config.Expand(root), 3) {
			if main := mainCheckout(top); main != "" {
				top = main
			}
			if seen[top] {
				continue
			}
			seen[top] = true
			checkouts++

			if l, err := resolveMemoryRepo(cfg, top); err == nil {
				for _, r := range memoryResults(l) {
					if r.status != statusWarn && r.status != statusFail {
						continue
					}
					if r.id == "claude:memory-rule" {
						// One host file serves every repo on the host.
						if ruleSeen[l.HostDir] {
							continue
						}
						ruleSeen[l.HostDir] = true
					} else {
						r.detail = tildePath(top) + ": " + r.detail
					}
					r.fix = "devz claude memory init " + tildePath(top)
					out = append(out, r)
				}
			}

			for _, tree := range worktreesOf(top) {
				trees++
				// With nobody logged in at all, claude:accounts has said so.
				if len(known) == 0 {
					continue
				}
				if _, _, err := resolveAmong(known, tree); err != nil {
					out = append(out, warn("claude:account", tildePath(tree)+": the marker names an account nobody is logged into",
						"cd there, then devz claude account list and devz claude account set <email>"))
				}
			}
		}
	}
	if len(out) == 0 {
		return []result{ok("claude:all", fmt.Sprintf(
			"%d checkouts, %d working trees: accounts resolve, memory shared and guarded", checkouts, trees))}
	}
	return out
}

func checkGH() result {
	if _, found := look("gh"); !found {
		return skip("gh:auth", "gh not installed")
	}
	if _, err := output("gh", "auth", "status"); err != nil {
		return warn("gh:auth", "not authenticated", "gh auth login")
	}
	user, err := output("gh", "api", "user", "--jq", ".login")
	if err != nil || user == "" {
		return ok("gh:auth", "authenticated")
	}
	return ok("gh:auth", user)
}

// checkPlugins lists the plugins on PATH, and flags any that can never run
// because a built-in command has taken the name. That happens quietly when a
// script is ported into the binary and the script is left behind.
func checkPlugins(app *cli.App) []result {
	var names []string
	var out []result
	for _, p := range cli.Plugins() {
		if app.Command(p.Name) != nil {
			out = append(out, warn("plugins", tildePath(p.Path)+" never runs: 'devz "+p.Name+"' is built in",
				"remove or rename "+tildePath(p.Path)))
			continue
		}
		if group, sub, found := strings.Cut(p.Name, "-"); found {
			if cmd := app.Command(group); cmd != nil && cmd.Group {
				if builtinSubcommand(group, sub) {
					out = append(out, warn("plugins", tildePath(p.Path)+" never runs: 'devz "+group+" "+sub+"' is built in",
						"remove or rename "+tildePath(p.Path)))
				} else {
					names = append(names, group+" "+sub)
				}
				continue
			}
		}
		names = append(names, p.Name)
	}
	if len(names) == 0 && len(out) == 0 {
		return []result{ok("plugins", "none on PATH")}
	}
	if len(names) > 0 {
		out = append([]result{ok("plugins", strings.Join(names, ", "))}, out...)
	}
	return out
}

// builtinSubcommand reports whether a group command compiles sub in, which is
// what makes a plugin of that name unreachable.
func builtinSubcommand(group, sub string) bool {
	if group != "claude" {
		return false
	}
	for _, b := range claudeBuiltins {
		if b.name == sub {
			return true
		}
	}
	return false
}

// checkDeprecated reports which old command names were used in the last 30
// days, from the log every use is written to. It is information, not a
// problem: the old names work. Nothing listed for a release cycle is what
// says a name can be removed.
func checkDeprecated(now time.Time) result {
	uses := cli.DeprecatedUses(now.AddDate(0, 0, -30))
	if len(uses) == 0 {
		return ok("devz:deprecated", "no old command names used in the last 30 days")
	}
	parts := make([]string, 0, len(uses))
	for _, u := range uses {
		parts = append(parts, fmt.Sprintf("'%s' x%d, last %s (now '%s')",
			u.Old, u.Count, u.Last.Local().Format("2006-01-02"), u.Replacement))
	}
	return ok("devz:deprecated", "old names used in the last 30 days: "+strings.Join(parts, "; "))
}

// checkCompletion flags an installed completion script that a different devz
// wrote. `go install` runs nothing after it installs, so an upgrade cannot
// refresh the file: the new subcommands exist but do not complete until it is
// regenerated.
func checkCompletion() []result {
	var out []result
	for _, sh := range []struct {
		name, script string
		files        []string
	}{
		{"zsh", zshCompletion, zshCompletionFiles()},
		{"bash", bashCompletion, bashCompletionFiles()},
	} {
		if r, found := completionResult(sh.name, sh.script, sh.files); found {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []result{skip("completion", "no installed script found; see 'devz help completion'")}
	}
	return out
}

// completionResult compares the first candidate that exists with the script
// this build emits. Only the first matters: it is the one the shell loads.
func completionResult(shell, script string, candidates []string) (result, bool) {
	id := "completion:" + shell
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if string(data) == script {
			return ok(id, tildePath(path)), true
		}
		return warn(id, tildePath(path)+" was written by a different devz version",
			"devz completion "+shell+" > "+tildePath(path)), true
	}
	return result{}, false
}

// zshCompletionFiles lists where _devz may be installed, in the order zsh
// would find it: $FPATH when the shell exports it (oh-my-zsh does), then the
// usual directories for shells that do not.
func zshCompletionFiles() []string {
	home, _ := os.UserHomeDir()
	dirs := filepath.SplitList(os.Getenv("FPATH"))
	if custom := os.Getenv("ZSH_CUSTOM"); custom != "" {
		dirs = append(dirs, filepath.Join(custom, "completions"))
	}
	if omz := os.Getenv("ZSH"); omz != "" {
		dirs = append(dirs, filepath.Join(omz, "custom", "completions"), filepath.Join(omz, "completions"))
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".oh-my-zsh", "custom", "completions"),
			filepath.Join(home, ".local", "share", "zsh", "site-functions"),
			filepath.Join(home, ".zsh", "completions"),
			filepath.Join(home, ".zfunc"))
	}
	if brew := os.Getenv("HOMEBREW_PREFIX"); brew != "" {
		dirs = append(dirs, filepath.Join(brew, "share", "zsh", "site-functions"))
	}
	dirs = append(dirs, "/usr/local/share/zsh/site-functions", "/usr/share/zsh/site-functions")
	return candidateFiles(dirs, "_devz")
}

// bashCompletionFiles lists where bash-completion looks for a script named
// after the command.
func bashCompletionFiles() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if user := os.Getenv("BASH_COMPLETION_USER_DIR"); user != "" {
		dirs = append(dirs, filepath.Join(user, "completions"))
	}
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		dirs = append(dirs, filepath.Join(data, "bash-completion", "completions"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "bash-completion", "completions"))
	}
	if brew := os.Getenv("HOMEBREW_PREFIX"); brew != "" {
		dirs = append(dirs, filepath.Join(brew, "etc", "bash_completion.d"))
	}
	dirs = append(dirs, "/usr/local/etc/bash_completion.d", "/etc/bash_completion.d",
		"/usr/share/bash-completion/completions")
	return candidateFiles(dirs, "devz")
}

// candidateFiles joins name onto each directory, dropping empty and repeated
// directories while keeping the order.
func candidateFiles(dirs []string, name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, filepath.Join(dir, name))
	}
	return out
}
