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

// claudeEnv gives a config with Claude support on, a shared dir holding a
// fake claude-account that records how it was called, and a PATH with only
// the system shell tools plus a plugin directory.
func claudeEnv(t *testing.T) (cfg config.Config, plugins, record string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVZ_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	plugins = filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", plugins+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	record = filepath.Join(dir, "called")
	cfg = config.Default()
	cfg.Claude.Enabled = true
	cfg.Claude.SharedDir = filepath.Join(dir, "shared")
	script(t, filepath.Join(cfg.Claude.SharedDir, "bin", "claude-account"),
		`printf '%s|via=%s\n' "$*" "$DEVZ_VIA" > "`+record+`"
[ "$1" = bad@example.com ] && exit 3
exit 0
`)
	return cfg, plugins, record
}

func TestClaudeAccountVerbsBecomeTheToolsArguments(t *testing.T) {
	cfg, _, record := claudeEnv(t)
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"show"}, ""},
		{[]string{"set", "me@example.com"}, "me@example.com"},
		{[]string{"set", "personal", "--root", "."}, "personal --root ."},
		{[]string{"pick"}, "--select"},
		{[]string{"list"}, "--list"},
		{[]string{"clear"}, "--clear"},
		// Not a verb devz names: handed over untouched.
		{[]string{"--colors"}, "--colors"},
	} {
		if err := runClaude(ctx, append([]string{"account"}, c.args...)); err != nil {
			t.Errorf("claude account %v: %v", c.args, err)
			continue
		}
		got, _ := os.ReadFile(record)
		if want := c.want + "|via=1\n"; string(got) != want {
			t.Errorf("claude account %v ran the tool with %q, want %q", c.args, got, want)
		}
	}

	if err := runClaude(ctx, []string{"account", "set"}); err == nil {
		t.Error("set with no account should be refused before the tool runs")
	}
}

func TestClaudeAccountKeepsTheToolsExitStatus(t *testing.T) {
	cfg, _, _ := claudeEnv(t)
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runClaude(ctx, []string{"account", "set", "bad@example.com"})
	var exit cli.ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Errorf("err = %v, want exit status 3 passed through", err)
	}
}

func TestClaudeFallsThroughToAPlugin(t *testing.T) {
	cfg, plugins, record := claudeEnv(t)
	script(t, filepath.Join(plugins, "devz-claude-sync"),
		`# devz: share settings across config dirs
printf '%s|via=%s\n' "$*" "$DEVZ_VIA" > "`+record+`"
[ "$1" = fail ] && exit 7
exit 0
`)
	// A plugin named like a built-in subcommand must never be reached.
	script(t, filepath.Join(plugins, "devz-claude-account"), `echo shadowed > "`+record+`"`+"\n")

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &bytes.Buffer{}}
	if err := runClaude(ctx, []string{"sync", "status", "x"}); err != nil {
		t.Fatalf("claude sync: %v", err)
	}
	if got, _ := os.ReadFile(record); string(got) != "status x|via=1\n" {
		t.Errorf("plugin was called with %q", got)
	}

	err := runClaude(ctx, []string{"sync", "fail"})
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
	if got := out.String(); !strings.Contains(got, "sync") || !strings.Contains(got, "share settings across config dirs") {
		t.Errorf("overview does not list the plugin with its description:\n%s", got)
	}
	if got := completeClaude(ctx, nil); strings.Join(got, ",") != "account,memory,account,sync" && strings.Join(got, ",") != "account,memory,sync,account" {
		// Built-ins first, then what is on PATH; the shadowed plugin is on
		// PATH too, and completing its name is harmless since the built-in runs.
		t.Errorf("completeClaude = %v", got)
	}
}

func TestCheckPluginsFlagsWhatCanNeverRun(t *testing.T) {
	_, plugins, _ := claudeEnv(t)
	for _, name := range []string{"devz-tunnel", "devz-secrets", "devz-claude-sync", "devz-claude-memory"} {
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
	if len(oks) != 1 || oks[0] != "claude sync, tunnel" {
		t.Errorf("reachable plugins = %v, want [claude sync, tunnel]", oks)
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
		{[]string{"claude"}, "account,memory"},
		{[]string{"claude", "account"}, "show,set,pick,list,clear"},
		{[]string{"claude", "memory"}, "status,init,path,list"},
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
	if got := strings.Join(complete(ctx, app, []string{"memory"}), ","); got != "status,init,path,list" {
		t.Errorf("complete([memory]) = %q", got)
	}
}
