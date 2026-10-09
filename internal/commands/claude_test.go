package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// script writes an executable shell script, skipping where there is no sh.
func script(t *testing.T, path, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// claudeEnv gives a home directory with two logged-in accounts and the
// directories that must not count as accounts, a config with Claude support
// on, and a PATH holding the system tools plus an empty plugin directory.
func claudeEnv(t *testing.T) (cfg config.Config, plugins, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell and git")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("DEVZ_CONFIG", filepath.Join(home, "devz.json"))
	t.Setenv("DEVZ_VIA", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	plugins = filepath.Join(home, "plugins")
	t.Setenv("PATH", plugins+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	for path, body := range map[string]string{
		".claude.json":                   `{"oauthAccount":{"emailAddress":"me@personal.dev","organizationName":"Mine"}}`,
		".claude-work/.claude.json":      `{"oauthAccount":{"emailAddress":"me@work.example","organizationName":"Work Inc"}}`,
		".claude-loggedout/.claude.json": `{"numStartups":3}`,
		".claude-shared/.claude.json":    `{"oauthAccount":{"emailAddress":"never@shared.example"}}`,
		".claude-notadir":                `{"oauthAccount":{"emailAddress":"never@file.example"}}`,
		"plugins/.keep":                  "",
		".claude/settings.json":          "{}",
		".claude-empty/settings.json":    "{}",
	} {
		full := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg = config.Default()
	cfg.Claude.Enabled = true
	return cfg, plugins, home
}

// repoWithWorktrees makes <home>/src/org/repo with a first commit, a sibling
// worktree and one nested where Claude Code puts its own.
func repoWithWorktrees(t *testing.T, home string) (main, sibling, nested string) {
	t.Helper()
	main = filepath.Join(home, "src", "org", "repo")
	sibling = filepath.Join(home, "src", "org", ".wt-repo-topic")
	nested = filepath.Join(main, ".claude", "worktrees", "agent-1")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, filepath.Dir(main), "init", "-q", "-b", "main", main)
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, main, "worktree", "add", "-q", sibling, "-b", "topic-a")
	gitIn(t, main, "worktree", "add", "-q", nested, "-b", "topic-b")
	return main, sibling, nested
}

func writeMarker(t *testing.T, dir, word string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, accountMarker), []byte(word+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAccountsAreDiscovered(t *testing.T) {
	cfg, _, home := claudeEnv(t)
	known := claudeAccounts(cfg)
	if len(known) != 2 {
		t.Fatalf("accounts = %+v, want the two that are logged in", known)
	}
	if a := known[0]; a.Email != "me@personal.dev" || a.Alias != "personal" || !a.Default ||
		a.ConfigDir != filepath.Join(home, ".claude") || a.configDirEnv() != "" {
		t.Errorf("default account = %+v", a)
	}
	if a := known[1]; a.Email != "me@work.example" || a.Alias != "work" || a.Org != "Work Inc" ||
		a.configDirEnv() != filepath.Join(home, ".claude-work") {
		t.Errorf("named account = %+v", a)
	}
}

func TestMatchAccount(t *testing.T) {
	cfg, _, _ := claudeEnv(t)
	known := claudeAccounts(cfg)
	for token, want := range map[string]string{
		"me@work.example": "me@work.example",
		"work":            "me@work.example",
		"personal":        "me@personal.dev",
		"me@w":            "me@work.example",
	} {
		if a, err := matchAccount(token, known); err != nil || a.Email != want {
			t.Errorf("matchAccount(%q) = %q, %v; want %q", token, a.Email, err, want)
		}
	}
	for _, token := range []string{"me@", "", "nobody@nowhere", "wor"} {
		_, err := matchAccount(token, known)
		var unresolved unresolvedError
		if !errors.As(err, &unresolved) {
			t.Errorf("matchAccount(%q) = %v, want an unresolved error", token, err)
		}
	}
}

func TestResolveAccountFollowsTheRule(t *testing.T) {
	cfg, _, home := claudeEnv(t)
	main, sibling, nested := repoWithWorktrees(t, home)
	deep := filepath.Join(main, "src", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	expect := func(when, dir, want string) {
		t.Helper()
		acct, _, err := resolveAccount(cfg, dir)
		if err != nil || acct.Email != want {
			t.Errorf("%s: %s resolves to %q, %v; want %q", when, dir, acct.Email, err, want)
		}
	}
	const personal, work = "me@personal.dev", "me@work.example"

	for _, dir := range []string{main, deep, sibling, nested} {
		expect("no marker", dir, personal)
	}

	// A marker above the repo is not the repo's: nothing is inherited.
	writeMarker(t, filepath.Join(home, "src", "org"), "work")
	writeMarker(t, filepath.Join(home, "src"), "work")
	for _, dir := range []string{main, sibling, nested} {
		expect("marker in the tree above", dir, personal)
	}
	// Outside a repo, a directory answers for itself only.
	expect("non-repo dir with a marker", filepath.Join(home, "src", "org"), work)
	plain := filepath.Join(home, "src", "org", "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	expect("non-repo dir under one with a marker", plain, personal)

	// The repo's marker covers the checkout, a deep cwd and every worktree.
	writeMarker(t, main, "work")
	for _, dir := range []string{main, deep, sibling, nested} {
		expect("marker in the main checkout", dir, work)
	}

	// A marker in one worktree overrides the repo's, for that worktree only.
	writeMarker(t, sibling, "personal")
	expect("override in the sibling", sibling, personal)
	expect("override in the sibling", nested, work)
	expect("override in the sibling", main, work)

	// A marker nobody answers to is an error, never the default account.
	writeMarker(t, main, "nobody@nowhere")
	for _, dir := range []string{main, nested} {
		_, marker, err := resolveAccount(cfg, dir)
		var unresolved unresolvedError
		if !errors.As(err, &unresolved) || marker != filepath.Join(main, accountMarker) {
			t.Errorf("bad marker from %s: marker=%q err=%v", dir, marker, err)
		}
	}
	expect("bad repo marker, overridden worktree", sibling, personal)
}

func TestAccountSetWritesTheRepoMarker(t *testing.T) {
	cfg, _, home := claudeEnv(t)
	main, sibling, nested := repoWithWorktrees(t, home)
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}

	// From inside a worktree: the repo's marker, holding the canonical email.
	t.Chdir(nested)
	if err := runClaudeAccount(ctx, []string{"set", "work"}); err != nil {
		t.Fatalf("set: %v\n%s", err, out.String())
	}
	if got := readAccountMarker(filepath.Join(main, accountMarker)); got != "me@work.example" {
		t.Errorf("main checkout marker = %q", got)
	}
	if exists(filepath.Join(nested, accountMarker)) {
		t.Error("set from a worktree wrote the marker into the worktree")
	}
	ignore, _ := os.ReadFile(filepath.Join(home, ".config", "git", "ignore"))
	if !strings.Contains(string(ignore), accountMarker) {
		t.Errorf("global ignore = %q", ignore)
	}

	// --root . : this worktree alone.
	t.Chdir(sibling)
	if err := runClaudeAccount(ctx, []string{"set", "me@p", "--root", "."}); err != nil {
		t.Fatal(err)
	}
	if got := readAccountMarker(filepath.Join(sibling, accountMarker)); got != "me@personal.dev" {
		t.Errorf("worktree marker = %q", got)
	}

	// clear drops the marker that applies here: the override first.
	out.Reset()
	if err := runClaudeAccount(ctx, []string{"clear"}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(sibling, accountMarker)) || !exists(filepath.Join(main, accountMarker)) {
		t.Errorf("clear removed the wrong marker:\n%s", out.String())
	}

	// A name nobody answers to is refused, and writes nothing.
	t.Chdir(home)
	err := runClaudeAccount(ctx, []string{"set", "nobody@nowhere"})
	var exit cli.ExitError
	if !errors.As(err, &exit) || exit.Code != exitUnresolved {
		t.Errorf("set to an unknown account: %v, want exit status 3", err)
	}
	if exists(filepath.Join(home, accountMarker)) {
		t.Error("a marker that cannot resolve was written")
	}
}

func TestAccountResolveForScripts(t *testing.T) {
	cfg, _, home := claudeEnv(t)
	main, sibling, _ := repoWithWorktrees(t, home)
	writeMarker(t, main, "work")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &bytes.Buffer{}}
		err := runClaudeAccount(ctx, append([]string{"resolve"}, args...))
		return out.String(), err
	}
	workDir := filepath.Join(home, ".claude-work")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{sibling}, "me@work.example\n"},
		{[]string{"--alias", sibling}, "work\n"},
		{[]string{"--config-dir", sibling}, workDir + "\n"},
		// The default account: an empty line, because it is reached by
		// unsetting the variable.
		{[]string{"--config-dir", home}, "\n"},
		{[]string{"--info", sibling}, "me@work.example\twork\t" + workDir + "\tWork Inc\n"},
		{[]string{"--check", "personal"}, "me@personal.dev\tpersonal\t" + filepath.Join(home, ".claude") + "\tMine\n"},
		{[]string{"--list"}, "me@personal.dev\tpersonal\t" + filepath.Join(home, ".claude") + "\tMine\n" +
			"me@work.example\twork\t" + workDir + "\tWork Inc\n"},
	} {
		if got, err := run(c.args...); err != nil || got != c.want {
			t.Errorf("resolve %v = %q, %v; want %q", c.args, got, err, c.want)
		}
	}

	writeMarker(t, main, "nobody@nowhere")
	got, err := run("--config-dir", sibling)
	var exit cli.ExitError
	if !errors.As(err, &exit) || exit.Code != exitUnresolved || got != "" {
		t.Errorf("unresolvable marker: stdout %q, err %v; want nothing printed and exit status 3", got, err)
	}
}

func TestLegacyAccountArguments(t *testing.T) {
	cfg, _, home := claudeEnv(t)
	main, _, _ := repoWithWorktrees(t, home)
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}

	if err := runLegacyAccount(ctx, []string{"me@work.example", "--root", main}); err != nil {
		t.Fatalf("<email> --root: %v", err)
	}
	if got := readAccountMarker(filepath.Join(main, accountMarker)); got != "me@work.example" {
		t.Errorf("marker = %q", got)
	}
	out.Reset()
	if err := runLegacyAccount(ctx, []string{"--list"}); err != nil || !strings.Contains(out.String(), "[work]") {
		t.Errorf("--list: %v\n%s", err, out.String())
	}
	t.Chdir(main)
	out.Reset()
	if err := runLegacyAccount(ctx, nil); err != nil || !strings.Contains(out.String(), "me@work.example   (from ") {
		t.Errorf("no arguments: %v\n%s", err, out.String())
	}
	if err := runLegacyAccount(ctx, []string{"--clear"}); err != nil || exists(filepath.Join(main, accountMarker)) {
		t.Errorf("--clear: %v", err)
	}
	if err := runLegacyAccount(ctx, []string{"--bogus"}); err == nil {
		t.Error("an unknown option was accepted")
	}
}

func TestWithConfigDir(t *testing.T) {
	env := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/stale", "CLAUDE_CONFIG_DIRX=keep"}
	if got := strings.Join(withConfigDir(env, "/new"), " "); got != "PATH=/bin CLAUDE_CONFIG_DIRX=keep CLAUDE_CONFIG_DIR=/new" {
		t.Errorf("set: %s", got)
	}
	// The default account is reached by removing the variable, not by
	// pointing it at ~/.claude.
	if got := strings.Join(withConfigDir(env, ""), " "); got != "PATH=/bin CLAUDE_CONFIG_DIRX=keep" {
		t.Errorf("unset: %s", got)
	}
}

func TestCheckLaunch(t *testing.T) {
	_, _, home := claudeEnv(t)
	t.Setenv("SHELL", "/usr/bin/zsh")
	settings := filepath.Join(home, ".vscode-server", "data", "Machine", "settings.json")
	wrapper := filepath.Join(home, "bin", "wrapper")
	status := func() string {
		var parts []string
		for _, r := range checkLaunch() {
			parts = append(parts, r.status.label(false))
		}
		return strings.Join(parts, "")
	}
	if got := status(); got != "" {
		t.Errorf("no editor settings, no rc file: %q, want nothing to report", got)
	}

	// Comments are legal in a VS Code settings file.
	script(t, settings, "")
	if err := os.WriteFile(settings, []byte("{\n  // machine settings\n  \"other\": 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias ll=ls\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "[warn][warn]" {
		t.Errorf("no wrapper set, plain rc file: %q, want two warnings", got)
	}

	body := "{\n  // machine settings\n  \"claudeCode.claudeProcessWrapper\": \"" + wrapper + "\",\n}\n"
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "[FAIL][warn]" {
		t.Errorf("wrapper set but missing: %q", got)
	}

	script(t, wrapper, "exec devz claude exec -- \"$@\"\n")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(shellInit), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "[ ok ][ ok ]" {
		t.Errorf("both in place: %q", got)
	}
}

func TestClaudeFallsThroughToAPlugin(t *testing.T) {
	cfg, plugins, home := claudeEnv(t)
	record := filepath.Join(home, "called")
	script(t, filepath.Join(plugins, "devz-claude-hello"),
		`# devz: share settings across config dirs
printf '%s|via=%s\n' "$*" "$DEVZ_VIA" > "`+record+`"
[ "$1" = fail ] && exit 7
exit 0
`)
	// A plugin named like a built-in subcommand must never be reached.
	script(t, filepath.Join(plugins, "devz-claude-account"), `echo shadowed > "`+record+`"`+"\n")

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &bytes.Buffer{}}
	if err := runClaude(ctx, []string{"hello", "status", "x"}); err != nil {
		t.Fatalf("claude hello: %v", err)
	}
	if got, _ := os.ReadFile(record); string(got) != "status x|via=1\n" {
		t.Errorf("plugin was called with %q", got)
	}

	err := runClaude(ctx, []string{"hello", "fail"})
	var exit cli.ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Errorf("err = %v, want the plugin's exit status 7", err)
	}

	if err := runClaude(ctx, []string{"account", "list"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(record); strings.Contains(string(got), "shadowed") {
		t.Error("a plugin took over a built-in subcommand")
	}

	if err := runClaude(ctx, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("unknown subcommand: %v", err)
	}

	out.Reset()
	if err := runClaude(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "hello") || !strings.Contains(got, "share settings across config dirs") {
		t.Errorf("overview does not list the plugin with its description:\n%s", got)
	}
	// Built-ins first, then what is on PATH. The shadowed plugin is on PATH
	// too; completing its name is harmless, since the built-in is what runs.
	if got := strings.Join(completeClaude(ctx, nil), ","); got != "account,eject,exec,memory,shell-init,sync,worktree,account,hello" {
		t.Errorf("completeClaude = %s", got)
	}
}

func TestCheckPluginsFlagsWhatCanNeverRun(t *testing.T) {
	_, plugins, _ := claudeEnv(t)
	for _, name := range []string{"devz-tunnel", "devz-secrets", "devz-claude-hello", "devz-claude-memory"} {
		script(t, filepath.Join(plugins, name), "exit 0\n")
	}
	app := cli.New("test", Secrets(), Claude())

	var oks, warns []string
	for _, r := range checkPlugins(app) {
		if r.status == statusWarn {
			warns = append(warns, r.detail)
		} else {
			oks = append(oks, r.detail)
		}
	}
	if len(oks) != 1 || oks[0] != "claude hello, tunnel" {
		t.Errorf("reachable plugins = %v, want [claude hello, tunnel]", oks)
	}
	if len(warns) != 2 || !strings.Contains(strings.Join(warns, "\n"), "'devz secrets' is built in") ||
		!strings.Contains(strings.Join(warns, "\n"), "'devz claude memory' is built in") {
		t.Errorf("shadowed plugins = %v", warns)
	}
}

func TestCheckDeprecated(t *testing.T) {
	claudeEnv(t)
	now := time.Now()
	if r := checkDeprecated(now); r.status != statusOK || !strings.Contains(r.detail, "no old command names") {
		t.Errorf("empty log: %+v", r)
	}
	ctx := &cli.Context{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	cli.NoteDeprecated(ctx, "devz account", "devz claude account")
	cli.NoteDeprecated(ctx, "devz account", "devz claude account")
	r := checkDeprecated(now.Add(time.Minute))
	if r.status != statusOK || !strings.Contains(r.detail, "'devz account' x2") || !strings.Contains(r.detail, "'devz claude account'") {
		t.Errorf("after two uses: %+v", r)
	}
	// Outside the window it is no longer reported.
	if r := checkDeprecated(now.AddDate(0, 0, 31)); !strings.Contains(r.detail, "no old command names") {
		t.Errorf("31 days later: %+v", r)
	}
}

func TestCompleteAsksTheCommand(t *testing.T) {
	cfg, _, _ := claudeEnv(t)
	cfg.Secrets.Entries = []string{"team/token"}
	cfg.Secrets.EnvVars = map[string]string{"team/token": "TEAM_TOKEN"}
	ctx := &cli.Context{Config: cfg}
	app := cli.New("test", Secrets(), Claude(), Memory(), Config())

	for _, c := range []struct {
		words []string
		want  string
	}{
		{[]string{"secrets", "map"}, "team/token"},
		{[]string{"secrets", "exec"}, "TEAM_TOKEN"},
		{[]string{"claude"}, "account,eject,exec,memory,shell-init,sync,worktree"},
		{[]string{"claude", "account"}, "show,set,pick,list,clear,resolve"},
		{[]string{"claude", "account", "set"}, "me@personal.dev,personal,me@work.example,work"},
		{[]string{"claude", "memory"}, "status,init,path,list,migrate"},
		{[]string{"claude", "memory", "init"}, "--all,--dry-run"},
		{[]string{"config"}, "show,path,init,edit"},
		{[]string{"nope"}, ""},
		{[]string{"secrets", "status"}, ""},
	} {
		if got := strings.Join(complete(ctx, app, c.words), ","); got != c.want {
			t.Errorf("complete(%v) = %q, want %q", c.words, got, c.want)
		}
	}
	// The old names are hidden from the first word, but still complete.
	if names := strings.Join(complete(ctx, app, nil), ","); strings.Contains(names, "memory") {
		t.Errorf("first word offers a hidden alias: %s", names)
	}
	if got := strings.Join(complete(ctx, app, []string{"memory"}), ","); got != "status,init,path,list,migrate" {
		t.Errorf("complete([memory]) = %q", got)
	}
}
