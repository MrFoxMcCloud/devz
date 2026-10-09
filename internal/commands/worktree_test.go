package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

func TestWorktreeTopic(t *testing.T) {
	for branch, want := range map[string]string{
		"ops-core/1.11-live-board": "1.11-live-board",
		"fix/Some Thing!":          "some-thing",
		"main":                     "main",
		"a/b/c":                    "c",
		"task/--":                  "",
	} {
		if got := worktreeTopic(branch); got != want {
			t.Errorf("worktreeTopic(%q) = %q, want %q", branch, got, want)
		}
	}
}

// worktreeRepo is a repo at <home>/src/org/repo with one commit, an origin
// that holds main and a branch only the origin has, and the working
// directory set inside it.
func worktreeRepo(t *testing.T) (ctx *cli.Context, out *bytes.Buffer, home, main string) {
	t.Helper()
	cfg, _, home := claudeEnv(t)
	main = filepath.Join(home, "src", "org", "repo")
	origin := filepath.Join(home, "origin.git")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, home, "init", "-q", "-b", "main", main)
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, home, "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, main, "remote", "add", "origin", origin)
	gitIn(t, main, "push", "-q", "-u", "origin", "main")
	gitIn(t, main, "branch", "local-only")
	gitIn(t, main, "push", "-q", "origin", "main:refs/heads/team/on-origin")
	gitIn(t, main, "fetch", "-q", "origin")
	gitIn(t, main, "remote", "set-head", "origin", "main")
	t.Chdir(main)
	out = &bytes.Buffer{}
	return &cli.Context{Config: cfg, Stdout: out, Stderr: &bytes.Buffer{}}, out, home, main
}

func branchOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := output("git", "-C", dir, "branch", "--show-current")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWorktreeAdd(t *testing.T) {
	ctx, out, home, main := worktreeRepo(t)
	writeMarker(t, main, "work")
	sibling := func(topic string) string { return filepath.Join(home, "src", "org", ".wt-repo-"+topic) }

	// A branch that exists locally is checked out as it is.
	if err := runClaude(ctx, []string{"worktree", "add", "local-only"}); err != nil {
		t.Fatalf("existing branch: %v", err)
	}
	if got := branchOf(t, sibling("local-only")); got != "local-only" {
		t.Errorf("branch = %q", got)
	}
	if !strings.Contains(out.String(), "me@work.example (the repo's)") {
		t.Errorf("the repo's account should apply and be shown:\n%s", out.String())
	}

	// A branch only origin has is tracked, under a name taken from its tail.
	out.Reset()
	if err := runClaude(ctx, []string{"worktree", "add", "team/on-origin"}); err != nil {
		t.Fatalf("origin branch: %v", err)
	}
	if up, _ := output("git", "-C", sibling("on-origin"), "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/team/on-origin" {
		t.Errorf("upstream = %q, want origin/team/on-origin", up)
	}

	// A new branch starts from origin's default branch and tracks nothing,
	// so its first push cannot aim at main.
	out.Reset()
	if err := runClaude(ctx, []string{"worktree", "add", "--name", "Spike", "--account", "personal", "idea/new-thing"}); err != nil {
		t.Fatalf("new branch: %v", err)
	}
	spike := sibling("spike")
	if got := branchOf(t, spike); got != "idea/new-thing" {
		t.Errorf("branch = %q", got)
	}
	if _, err := output("git", "-C", spike, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Error("a new branch should have no upstream")
	}
	if !strings.Contains(out.String(), "new branch from origin/main") {
		t.Errorf("output should say what it started from:\n%s", out.String())
	}
	// --account puts this worktree, and only this one, on another account.
	if got := readAccountMarker(filepath.Join(spike, accountMarker)); got != "me@personal.dev" {
		t.Errorf("worktree marker = %q", got)
	}
	if acct, _, _ := resolveAccount(ctx.Config, sibling("local-only")); acct.Email != "me@work.example" {
		t.Errorf("another worktree was moved to %s", acct.Email)
	}

	// Refusals: a taken name, an account nobody is logged into (nothing made).
	if err := runClaude(ctx, []string{"worktree", "add", "--name", "spike", "other"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("taken name: %v", err)
	}
	err := runClaude(ctx, []string{"worktree", "add", "--account", "nobody@nowhere", "ghost"})
	var exit cli.ExitError
	if !errors.As(err, &exit) || exit.Code != exitUnresolved || exists(sibling("ghost")) {
		t.Errorf("unknown account: err=%v, worktree made=%v", err, exists(sibling("ghost")))
	}
}

func TestWorktreeListAndRemove(t *testing.T) {
	ctx, out, home, main := worktreeRepo(t)
	writeMarker(t, main, "work")
	for _, args := range [][]string{{"add", "local-only"}, {"add", "--account", "personal", "idea/own-account"}} {
		if err := runClaude(ctx, append([]string{"worktree"}, args...)); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	// Listing works from inside a worktree too.
	t.Chdir(filepath.Join(home, "src", "org", ".wt-repo-local-only"))
	if err := runClaude(ctx, []string{"worktree", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("list:\n%s", out.String())
	}
	for i, want := range [][]string{
		{"src/org/repo", "main", "me@work.example", "main checkout"},
		{".wt-repo-local-only", "local-only", "me@work.example"},
		{".wt-repo-own-account", "idea/own-account", "me@personal.dev (this worktree only)"},
	} {
		for _, part := range want {
			if !strings.Contains(lines[i], part) {
				t.Errorf("list line %d = %q, want it to contain %q", i, lines[i], part)
			}
		}
	}

	t.Chdir(main)
	if err := runClaude(ctx, []string{"worktree", "rm", "main"}); err == nil || !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("removing the main checkout: %v", err)
	}
	if err := runClaude(ctx, []string{"worktree", "rm", "nope"}); err == nil {
		t.Error("an unknown worktree was accepted")
	}

	// Uncommitted work stops the removal unless --force is given.
	own := filepath.Join(home, "src", "org", ".wt-repo-own-account")
	if err := os.WriteFile(filepath.Join(own, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runClaude(ctx, []string{"worktree", "rm", "idea/own-account"}); err == nil || !exists(own) {
		t.Errorf("a worktree with uncommitted work was removed: %v", err)
	}
	if err := runClaude(ctx, []string{"worktree", "rm", "--force", "own-account"}); err != nil || exists(own) {
		t.Errorf("rm --force: %v", err)
	}
	// By topic, and the branch survives.
	if err := runClaude(ctx, []string{"worktree", "rm", "local-only"}); err != nil {
		t.Fatal(err)
	}
	if _, err := output("git", "-C", main, "rev-parse", "-q", "--verify", "refs/heads/local-only"); err != nil {
		t.Error("the branch was deleted with its worktree")
	}
}

func TestStrayOldStoreDirectoryIsFlagged(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatal(err)
	}
	if rs := checkMemoryLayout(cfg); len(rs) != 1 || rs[0].status != statusOK {
		t.Fatalf("clean store: %+v", rs)
	}
	// A resumed session writes a plan to where the store used to be.
	stray := filepath.Join(home, "shared", "orgs", "git.example.com", "acme", "plans")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	rs := checkMemoryLayout(cfg)
	if len(rs) != 1 || rs[0].status != statusWarn || !strings.Contains(rs[0].detail, "exists again") {
		t.Errorf("stray directory: %+v", rs)
	}
	// And the store in use does not flip back to the old place.
	if got := cfg.Claude.StoreDir(); got != filepath.Join(home, "shared", "hosts") {
		t.Errorf("store = %s", got)
	}
}
