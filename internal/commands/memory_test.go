package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

func TestParseRemote(t *testing.T) {
	cases := []struct{ url, host, org, repo string }{
		{"https://git.example.com/acme/backend", "git.example.com", "acme", "backend"},
		{"https://git.example.com/acme/backend.git", "git.example.com", "acme", "backend"},
		{"https://user:tok@Git.Example.com:3000/acme/backend/", "git.example.com", "acme", "backend"},
		{"git@git.example.com:acme/backend.git", "git.example.com", "acme", "backend"},
		{"ssh://git@git.example.com:2222/acme/backend.git", "git.example.com", "acme", "backend"},
	}
	for _, c := range cases {
		host, org, repo, ok := parseRemote(c.url)
		if !ok || host != c.host || org != c.org || repo != c.repo {
			t.Errorf("parseRemote(%q) = %q %q %q %v", c.url, host, org, repo, ok)
		}
	}
	for _, bad := range []string{"", "/local/path", "https://git.example.com/onlyone", "https://h/a/b/c"} {
		if _, _, _, ok := parseRemote(bad); ok {
			t.Errorf("parseRemote(%q) accepted", bad)
		}
	}
}

func TestMergeMemorySettingsKeepsOtherKeys(t *testing.T) {
	l := memoryLayout{Repo: "backend", HostDir: "/s/git.example.com", OrgDir: "/s/git.example.com/acme"}
	in := []byte(`{"model":"opus","permissions":{"allow":["Bash(ls)"]}}`)

	out, changed, err := mergeMemorySettings(in, l)
	if err != nil || !changed {
		t.Fatalf("first merge: changed=%v err=%v", changed, err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "opus" {
		t.Errorf("model dropped: %v", got)
	}
	if got["autoMemoryDirectory"] != "/s/git.example.com/acme/backend/memory" {
		t.Errorf("autoMemoryDirectory = %v", got["autoMemoryDirectory"])
	}
	perms := got["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if len(allow) != 3 || allow[0] != "Bash(ls)" ||
		allow[1] != "Edit(//s/git.example.com/acme/backend/memory/**)" ||
		allow[2] != "Edit(//s/git.example.com/acme/plans/**)" {
		t.Errorf("allow = %v", allow)
	}
	ask := perms["ask"].([]any)
	wantAsk := []any{
		"Edit(//s/git.example.com/memory/**)", "Edit(//s/git.example.com/CLAUDE.md)",
		"Edit(//s/git.example.com/*/memory/**)", "Edit(//s/git.example.com/*/CLAUDE.md)",
	}
	if len(ask) != len(wantAsk) {
		t.Fatalf("ask = %v", ask)
	}
	for i := range wantAsk {
		if ask[i] != wantAsk[i] {
			t.Errorf("ask[%d] = %v, want %v", i, ask[i], wantAsk[i])
		}
	}

	if _, changed, err := mergeMemorySettings(out, l); err != nil || changed {
		t.Errorf("second merge should be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestMergeMemorySettingsRejectsWrongShapes(t *testing.T) {
	l := memoryLayout{Repo: "r", HostDir: "/h", OrgDir: "/h/o"}
	for _, in := range []string{`[]`, `{"permissions":[]}`, `{"permissions":{"allow":"x"}}`} {
		if _, _, err := mergeMemorySettings([]byte(in), l); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
}

// memoryEnv isolates HOME, git's global config and the devz config, and
// returns a checkout at <home>/src/acme/backend with an origin on
// git.example.com.
func memoryEnv(t *testing.T) (home, top string, cfg config.Config) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	// Resolved, because git reports the real path (macOS's /var is a link).
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("DEVZ_CONFIG", filepath.Join(home, "devz.json"))

	top = filepath.Join(home, "src", "acme", "backend")
	for _, args := range [][]string{
		{"init", "-q", top},
		{"-C", top, "remote", "add", "origin", "https://git.example.com/acme/backend.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cfg = config.Default()
	cfg.Claude.SharedDir = filepath.Join(home, "shared")
	cfg.Claude.Memory.Hosts = []string{"git.example.com"}
	cfg.Claude.Memory.Roots = []string{filepath.Join(home, "src")}
	return home, top, cfg
}

func TestMemoryInitLaysOutStoreAndIsIdempotent(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	orgDir := filepath.Join(home, "src", "acme")
	// A hand-written org CLAUDE.md from before the store existed.
	if err := os.WriteFile(filepath.Join(orgDir, "CLAUDE.md"), []byte("# hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}

	store := filepath.Join(home, "shared", "hosts", "git.example.com")
	for _, p := range []string{
		"CLAUDE.md", "memory/MEMORY.md", "acme/CLAUDE.md", "acme/memory/MEMORY.md",
		"acme/plans", "acme/backend/memory",
	} {
		if !exists(filepath.Join(store, p)) {
			t.Errorf("missing %s", p)
		}
	}

	orgClaude, _ := os.ReadFile(filepath.Join(store, "acme", "CLAUDE.md"))
	if !strings.HasPrefix(string(orgClaude), "@~/shared/hosts/git.example.com/CLAUDE.md\n") ||
		!strings.Contains(string(orgClaude), "# hand written") {
		t.Errorf("org CLAUDE.md was not adopted with imports:\n%s", orgClaude)
	}
	if target, _ := os.Readlink(filepath.Join(orgDir, "CLAUDE.md")); target != filepath.Join(store, "acme", "CLAUDE.md") {
		t.Errorf("org CLAUDE.md link -> %q", target)
	}
	if target, _ := os.Readlink(filepath.Join(orgDir, "plans")); target != filepath.Join(store, "acme", "plans") {
		t.Errorf("plans link -> %q", target)
	}

	ignore, _ := os.ReadFile(filepath.Join(home, ".config", "git", "ignore"))
	if !strings.Contains(string(ignore), memoryIgnore) {
		t.Errorf("global ignore = %q", ignore)
	}
	if st, _ := exec.Command("git", "-C", top, "status", "--porcelain").Output(); len(st) != 0 {
		t.Errorf("init left the repo dirty:\n%s", st)
	}
	results := checkMemory(cfg, top)
	if len(results) != 3 {
		t.Errorf("doctor after init: %d results, want memory, guard and rule", len(results))
	}
	for _, r := range results {
		if r.status != statusOK {
			t.Errorf("doctor after init: %+v", r)
		}
	}

	out.Reset()
	if err := runMemory(ctx, []string{"init", "--all"}); err != nil {
		t.Fatalf("init --all: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 {
		t.Errorf("second run changed things:\n%s", out.String())
	}
}

func TestMemoryInitDryRunWritesNothing(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", "--dry-run", top}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(home, "shared")) || exists(filepath.Join(top, ".claude")) {
		t.Error("dry run wrote files")
	}
	if !strings.Contains(out.String(), "would update") {
		t.Errorf("dry run output:\n%s", out.String())
	}
	if rs := checkMemory(cfg, top); len(rs) != 1 || rs[0].status != statusWarn || rs[0].id != "claude:memory" {
		t.Errorf("doctor before init: %+v", rs)
	}
}

func TestMemoryLeavesOtherHostsAlone(t *testing.T) {
	_, top, cfg := memoryEnv(t)
	cfg.Claude.Memory.Hosts = []string{"elsewhere.example.com"}
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := runMemory(ctx, []string{"init", top}); err == nil {
		t.Error("init accepted a repo on an unconfigured host")
	}
	if err := runMemory(ctx, []string{"init", "--all"}); err != nil {
		t.Errorf("init --all should skip unmanaged repos: %v", err)
	}
	if exists(filepath.Join(top, ".claude")) {
		t.Error("unmanaged repo was written to")
	}
}

func TestMemoryNeverLinksIntoARoot(t *testing.T) {
	home, _, cfg := memoryEnv(t)
	// A flat checkout directly under a root that shares the org's name.
	root := filepath.Join(home, "acme")
	top := filepath.Join(root, "tool")
	for _, args := range [][]string{
		{"init", "-q", top},
		{"-C", top, "remote", "add", "origin", "git@git.example.com:acme/tool.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cfg.Claude.Memory.Roots = []string{"~/acme"}
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", "--all"}); err != nil {
		t.Fatalf("init --all: %v\n%s", err, out.String())
	}
	if _, err := os.Lstat(filepath.Join(root, "CLAUDE.md")); err == nil {
		t.Error("linked an org CLAUDE.md into a search root")
	}
	if !memorySettingsCurrent(memoryLayout{
		Top: top, Repo: "tool",
		HostDir: filepath.Join(home, "shared", "hosts", "git.example.com"),
		OrgDir:  filepath.Join(home, "shared", "hosts", "git.example.com", "acme"),
	}) {
		t.Error("flat checkout did not get its repo memory")
	}
}

func TestMemoryAdoptsOverAnUntouchedSeed(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	orgDir := filepath.Join(home, "src", "acme")
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	// An earlier checkout seeded the store before this one was seen.
	store := filepath.Join(home, "shared", "hosts", "git.example.com", "acme")
	l, err := resolveMemoryRepo(cfg, top)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "CLAUDE.md"), []byte(orgClaudeTemplate(l)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgDir, "CLAUDE.md"), []byte("# hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(store, "CLAUDE.md"))
	if !strings.Contains(string(got), "# hand written") {
		t.Errorf("hand-written CLAUDE.md was not adopted:\n%s", got)
	}

	// Once edited, the store copy wins and a stray file is left alone.
	if err := os.Remove(filepath.Join(orgDir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgDir, "CLAUDE.md"), []byte("# stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatal(err)
	}
	if got2, _ := os.ReadFile(filepath.Join(store, "CLAUDE.md")); string(got2) != string(got) {
		t.Error("an edited store CLAUDE.md was overwritten")
	}
}

// The rule earlier versions wrote let Claude edit the whole host directory.
// It is what the guard exists to replace, so a rerun has to take it out.
func TestMergeMemorySettingsReplacesTheBroadAllow(t *testing.T) {
	l := memoryLayout{Repo: "backend", HostDir: "/s/git.example.com", OrgDir: "/s/git.example.com/acme"}
	in := []byte(`{"autoMemoryDirectory":"/s/git.example.com/acme/backend/memory",
		"plansDirectory":"/s/git.example.com/acme/plans",
		"permissions":{"additionalDirectories":["/s/git.example.com"],
		"allow":["Bash(ls)","Edit(//s/git.example.com/**)"]}}`)

	out, changed, err := mergeMemorySettings(in, l)
	if err != nil || !changed {
		t.Fatalf("merge: changed=%v err=%v", changed, err)
	}
	if strings.Contains(string(out), `"Edit(//s/git.example.com/**)"`) {
		t.Errorf("the broad allow survived:\n%s", out)
	}
	if !strings.Contains(string(out), `"Bash(ls)"`) {
		t.Errorf("an unrelated allow rule was dropped:\n%s", out)
	}
	if _, changed, err := mergeMemorySettings(out, l); err != nil || changed {
		t.Errorf("second merge should be a no-op: changed=%v err=%v", changed, err)
	}
}

func TestMemorySettingsState(t *testing.T) {
	_, top, cfg := memoryEnv(t)
	l, err := resolveMemoryRepo(cfg, top)
	if err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(l.settingsPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l.settingsPath(), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if shared, guarded := memorySettingsState(l); shared || guarded {
		t.Errorf("no settings file: shared=%v guarded=%v", shared, guarded)
	}

	// What devz wrote before the guard: shared, and wide open.
	g := l.guard()
	old, _ := json.Marshal(map[string]any{
		"autoMemoryDirectory": l.repoMemory(), "plansDirectory": l.plans(),
		"permissions": map[string]any{"allow": []string{g.legacy}},
	})
	write(string(old))
	if shared, guarded := memorySettingsState(l); !shared || guarded {
		t.Errorf("pre-guard settings: shared=%v guarded=%v, want shared and unguarded", shared, guarded)
	}

	merged, _, err := mergeMemorySettings(old, l)
	if err != nil {
		t.Fatal(err)
	}
	write(string(merged))
	if shared, guarded := memorySettingsState(l); !shared || !guarded {
		t.Errorf("after merge: shared=%v guarded=%v", shared, guarded)
	}
}

// gitCommit gives a test repo its first commit, which a worktree needs.
func gitCommit(t *testing.T, top string) {
	t.Helper()
	cmd := exec.Command("git", "-C", top, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-q", "--allow-empty", "-m", "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// Claude Code reads the main checkout's settings in every linked worktree, so
// that is where devz looks and writes, wherever it is run from.
func TestMemoryInALinkedWorktreeUsesTheMainCheckout(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	gitCommit(t, top)
	sibling := filepath.Join(home, "src", "acme", ".wt-backend-topic")
	nested := filepath.Join(top, ".claude", "worktrees", "agent-1")
	for i, wt := range []string{sibling, nested} {
		branch := "topic-" + string(rune('a'+i))
		if out, err := exec.Command("git", "-C", top, "worktree", "add", "-q", wt, "-b", branch).CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v\n%s", err, out)
		}
	}

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	for _, wt := range []string{sibling, nested} {
		l, err := resolveMemoryRepo(cfg, wt)
		if err != nil {
			t.Fatal(err)
		}
		if l.Top != top || l.Worktree != wt || l.Repo != "backend" {
			t.Errorf("from %s: Top=%s Worktree=%s Repo=%s", wt, l.Top, l.Worktree, l.Repo)
		}
		if !l.parentIsOrg() {
			t.Errorf("from %s: the org links should still be placed beside the main checkout", wt)
		}
	}

	// init from inside a worktree sets up the repo, not the worktree.
	if err := runMemory(ctx, []string{"init", nested}); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	if !exists(filepath.Join(top, ".claude", "settings.local.json")) {
		t.Error("the main checkout got no settings file")
	}
	for _, wt := range []string{sibling, nested} {
		if exists(filepath.Join(wt, ".claude", "settings.local.json")) {
			t.Errorf("a settings file was written into the worktree %s", wt)
		}
		for _, r := range checkMemory(cfg, wt) {
			if r.status != statusOK {
				t.Errorf("doctor in %s: %+v", wt, r)
			}
		}
	}

	out.Reset()
	if err := runMemory(ctx, []string{"status", sibling}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "NOT") || !strings.Contains(got, "linked worktree") {
		t.Errorf("status from a worktree:\n%s", got)
	}
}

func TestMemoryInitAppendsTheBranchRuleOnce(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	hostClaude := filepath.Join(home, "shared", "hosts", "git.example.com", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(hostClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	// A host file from before the rule existed, edited by hand since.
	if err := os.WriteFile(hostClaude, []byte("# git.example.com\n\nOur own notes."), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	for range 2 {
		if err := runMemory(ctx, []string{"init", top}); err != nil {
			t.Fatalf("init: %v\n%s", err, out.String())
		}
	}
	body, _ := os.ReadFile(hostClaude)
	if !strings.HasPrefix(string(body), "# git.example.com\n\nOur own notes.\n\n## What not to save") {
		t.Errorf("the existing text was not kept ahead of the rule:\n%s", body)
	}
	if n := strings.Count(string(body), branchRuleMarker); n != 1 {
		t.Errorf("the rule is in the file %d times, want 1", n)
	}
	if n := strings.Count(out.String(), "append the branch rule"); n != 1 {
		t.Errorf("init reported the append %d times, want 1:\n%s", n, out.String())
	}
}

func TestMemoryPathAndList(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(home, "shared", "hosts", "git.example.com")
	for _, f := range []string{"memory/a.md", "memory/MEMORY.md", "acme/backend/memory/b.md", "acme/backend/memory/c.md"} {
		if err := os.WriteFile(filepath.Join(store, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out.Reset()
	if err := runMemory(ctx, []string{"path", top}); err != nil {
		t.Fatal(err)
	}
	want := "host\t" + store + "/memory\n" +
		"org\t" + store + "/acme/memory\n" +
		"repo\t" + store + "/acme/backend/memory\n" +
		"plans\t" + store + "/acme/plans\n"
	if out.String() != want {
		t.Errorf("path for a checkout =\n%swant\n%s", out.String(), want)
	}

	// A repo that is not checked out here, and an org on its own.
	out.Reset()
	if err := runMemory(ctx, []string{"path", "git.example.com/other/thing"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "repo\t"+store+"/other/thing/memory\n") {
		t.Errorf("path by name =\n%s", out.String())
	}
	out.Reset()
	if err := runMemory(ctx, []string{"path", "git.example.com/acme"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "repo\t") || !strings.Contains(out.String(), "plans\t"+store+"/acme/plans\n") {
		t.Errorf("path for an org =\n%s", out.String())
	}

	for _, bad := range []string{"github.com/someone/else", "git.example.com", "git.example.com/a/b/c", "git.example.com/../x"} {
		if err := runMemory(ctx, []string{"path", bad}); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}

	out.Reset()
	if err := runMemory(ctx, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	wantList := "git.example.com               1\n" +
		"git.example.com/acme          0\n" +
		"git.example.com/acme/backend  2\n"
	if out.String() != wantList {
		t.Errorf("list =\n%swant\n%s", out.String(), wantList)
	}
}

// checkoutAt makes a repo at dir whose origin is url.
func checkoutAt(t *testing.T, dir, url string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "remote", "add", "origin", url},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestStoreMirrorsTheURLInLowerCase(t *testing.T) {
	home, _, cfg := memoryEnv(t)
	top := filepath.Join(home, "src", "acme", "Shouty")
	checkoutAt(t, top, "https://git.example.com/ACME/Shouty.git")

	l, err := resolveMemoryRepo(cfg, top)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "shared", "hosts", "git.example.com", "acme", "shouty", "memory")
	if l.repoMemory() != want {
		t.Errorf("repo memory = %s, want %s", l.repoMemory(), want)
	}
	// The folder above the checkout is the org's whatever its capitals.
	if !l.parentIsOrg() {
		t.Error("src/acme should count as the org folder for origin ACME")
	}
}

func TestReservedNamesHaveNoPlaceInTheStore(t *testing.T) {
	home, _, cfg := memoryEnv(t)
	for _, name := range []string{"plans", "Memory", "repos"} {
		top := filepath.Join(home, "src", "acme", name)
		checkoutAt(t, top, "https://git.example.com/acme/"+name+".git")
		if _, err := resolveMemoryRepo(cfg, top); err == nil || !strings.Contains(err.Error(), "cannot have a place") {
			t.Errorf("a repository named %s: err = %v", name, err)
		}
	}
	if _, err := memoryLayoutFor(cfg, "git.example.com/memory/thing"); err == nil {
		t.Error("an org named memory was accepted")
	}
}

// On a public forge the host means nothing, so there is no host layer: the
// org file is the widest shared one and carries the branch rule itself.
func TestAHostWithoutAHostLayer(t *testing.T) {
	home, _, cfg := memoryEnv(t)
	cfg.Claude.Memory.Hosts = append(cfg.Claude.Memory.Hosts, "github.com")
	top := filepath.Join(home, "src", "someone", "tool")
	checkoutAt(t, top, "https://github.com/Someone/tool.git")

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	host := filepath.Join(home, "shared", "hosts", "github.com")
	if exists(filepath.Join(host, "CLAUDE.md")) || exists(filepath.Join(host, "memory")) {
		t.Error("a host layer was created for a public forge")
	}
	org, _ := os.ReadFile(filepath.Join(host, "someone", "CLAUDE.md"))
	if strings.Contains(string(org), "github.com/CLAUDE.md") {
		t.Errorf("the org file imports a host file that does not exist:\n%s", org)
	}
	if !strings.Contains(string(org), branchRuleMarker) || !strings.Contains(string(org), "## Finding memory") {
		t.Errorf("the org file should carry the branch rule and the layout:\n%s", org)
	}
	for _, r := range checkMemory(cfg, top) {
		if r.status != statusOK {
			t.Errorf("doctor: %+v", r)
		}
	}
	out.Reset()
	if err := runMemory(ctx, []string{"path", top}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "host\t") || !strings.Contains(out.String(), "repo\t"+host+"/someone/tool/memory\n") {
		t.Errorf("path =\n%s", out.String())
	}

	// hostLayer, when set, is the whole answer: here it gives the forge a
	// layer and takes the company host's away.
	cfg.Claude.Memory.HostLayer = []string{"github.com"}
	if l, _ := resolveMemoryRepo(cfg, top); !l.HostLayer {
		t.Error("claude.memory.hostLayer did not turn the layer on")
	}
}

// A checkout with no org folder above it gets the repo layer and nothing to
// fix: status must not tell you to run init for ever.
func TestAFlatCheckoutIsNotNagged(t *testing.T) {
	home, _, cfg := memoryEnv(t)
	cfg.Claude.Memory.Hosts = []string{"github.com"}
	top := filepath.Join(home, "src", "tool")
	checkoutAt(t, top, "https://github.com/someone/tool.git")
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"init", top}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runMemory(ctx, []string{"status", top}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "run: devz") || strings.Contains(got, "NOT") || !strings.Contains(got, "auto memory") {
		t.Errorf("status for a flat checkout:\n%s", got)
	}
	var skipped bool
	for _, r := range checkMemory(cfg, top) {
		if r.status == statusWarn || r.status == statusFail {
			t.Errorf("doctor: %+v", r)
		}
		skipped = skipped || (r.id == "claude:memory-rule" && r.status == statusSkip)
	}
	if !skipped {
		t.Error("the rule check should be skipped where no shared file loads")
	}
}

func TestMemoryMigrate(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	shared := filepath.Join(home, "shared")
	old := filepath.Join(shared, "orgs", "git.example.com")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A store as 1.6 left it, one org in mixed case with no checkout here.
	write(filepath.Join(old, "CLAUDE.md"), "# host\n"+branchRuleSection+"\n@~/shared/orgs/git.example.com/memory/MEMORY.md\n")
	write(filepath.Join(old, "memory", "MEMORY.md"), "")
	write(filepath.Join(old, "acme", "CLAUDE.md"),
		"@~/shared/orgs/git.example.com/CLAUDE.md\n@~/shared/orgs/git.example.com/acme/memory/MEMORY.md\n\n# acme\n")
	write(filepath.Join(old, "acme", "memory", "MEMORY.md"), "")
	write(filepath.Join(old, "acme", "plans", "p.md"),
		"see "+shared+"/orgs/git.example.com/acme/repos/backend/memory/a.md and ~/shared/orgs/git.example.com/acme/plans/\n")
	write(filepath.Join(old, "acme", "repos", "backend", "memory", "a.md"), "a memory\n")
	write(filepath.Join(old, "Mixed", "repos", "Thing", "memory", "b.md"), "b\n")
	write(filepath.Join(old, "Mixed", "memory", "MEMORY.md"), "")
	// The checkout's settings as 1.6 wrote them, plus a rule of the user's.
	oldMem := filepath.Join(old, "acme", "repos", "backend", "memory")
	write(filepath.Join(top, ".claude", "settings.local.json"), `{
  "autoMemoryDirectory": "`+oldMem+`",
  "plansDirectory": "`+filepath.Join(old, "acme", "plans")+`",
  "permissions": {
    "additionalDirectories": ["`+old+`"],
    "allow": ["Bash(ls)", "Edit(~/shared/orgs/git.example.com/acme/repos/backend/memory/**)", "Edit(~/shared/orgs/git.example.com/acme/plans/**)"],
    "ask": ["Edit(~/shared/orgs/git.example.com/memory/**)", "Edit(~/shared/orgs/git.example.com/*/CLAUDE.md)"]
  }
}`)
	if err := os.Symlink(filepath.Join(old, "acme", "CLAUDE.md"), filepath.Join(home, "src", "acme", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	if !legacyStore(cfg) {
		t.Fatal("a store at <sharedDir>/orgs should read as the old layout")
	}
	if rs := checkMemoryLayout(cfg); len(rs) != 1 || rs[0].status != statusWarn {
		t.Errorf("doctor before: %+v", rs)
	}
	// Until it is migrated, the old layout keeps working where it is.
	if l, _ := resolveMemoryRepo(cfg, top); l.repoMemory() != oldMem {
		t.Errorf("before migrating, repo memory = %s, want the old path", l.repoMemory())
	}

	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"migrate", "--dry-run"}); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if exists(filepath.Join(shared, "hosts")) || !exists(oldMem) {
		t.Fatalf("the dry run moved something:\n%s", out.String())
	}
	for _, want := range []string{"would move", "git.example.com/Mixed/repos/Thing -> git.example.com/mixed/thing", "would rewrite old paths"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := runMemory(ctx, []string{"migrate"}); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	store := filepath.Join(shared, "hosts", "git.example.com")
	for _, p := range []string{"acme/backend/memory/a.md", "mixed/thing/memory/b.md", "acme/plans/p.md", "CLAUDE.md"} {
		if !exists(filepath.Join(store, p)) {
			t.Errorf("missing after migrate: %s", p)
		}
	}
	// Every old path still answers, for sessions that are running.
	for _, p := range []string{oldMem + "/a.md", filepath.Join(old, "Mixed", "repos", "Thing", "memory", "b.md"), filepath.Join(old, "acme", "plans", "p.md")} {
		if _, err := os.ReadFile(p); err != nil {
			t.Errorf("old path no longer resolves: %s", p)
		}
	}
	if legacyStore(cfg) {
		t.Error("still reads as the old layout after migrating")
	}

	host, _ := os.ReadFile(filepath.Join(store, "CLAUDE.md"))
	orgFile, _ := os.ReadFile(filepath.Join(store, "acme", "CLAUDE.md"))
	plan, _ := os.ReadFile(filepath.Join(store, "acme", "plans", "p.md"))
	for name, body := range map[string][]byte{"host": host, "org": orgFile, "plan": plan} {
		if strings.Contains(string(body), "/orgs/") || strings.Contains(string(body), "/repos/") {
			t.Errorf("%s file still spells out an old path:\n%s", name, body)
		}
	}
	if !strings.Contains(string(plan), shared+"/hosts/git.example.com/acme/backend/memory/a.md") {
		t.Errorf("plan = %s", plan)
	}

	settings, _ := os.ReadFile(filepath.Join(top, ".claude", "settings.local.json"))
	if strings.Contains(string(settings), "/orgs/") || strings.Contains(string(settings), "/repos/") {
		t.Errorf("settings still name old paths:\n%s", settings)
	}
	if !strings.Contains(string(settings), `"Bash(ls)"`) ||
		!strings.Contains(string(settings), `"autoMemoryDirectory": "`+store+`/acme/backend/memory"`) {
		t.Errorf("settings after migrate:\n%s", settings)
	}
	if target, _ := os.Readlink(filepath.Join(home, "src", "acme", "CLAUDE.md")); target != filepath.Join(store, "acme", "CLAUDE.md") {
		t.Errorf("org link -> %s", target)
	}
	for _, r := range checkMemory(cfg, top) {
		if r.status != statusOK {
			t.Errorf("doctor for the checkout: %+v", r)
		}
	}
	if rs := checkMemoryLayout(cfg); len(rs) != 1 || rs[0].status != statusOK || !strings.Contains(rs[0].detail, "4 old path(s)") {
		t.Errorf("doctor after: %+v", rs)
	}

	// A second run finds nothing to do.
	out.Reset()
	if err := runMemory(ctx, []string{"migrate"}); err != nil || !strings.Contains(out.String(), "already has the current layout") {
		t.Errorf("second migrate: %v\n%s", err, out.String())
	}

	out.Reset()
	if err := runMemory(ctx, []string{"migrate", "--finish"}); err != nil {
		t.Fatalf("finish: %v\n%s", err, out.String())
	}
	if exists(filepath.Join(shared, "orgs")) || exists(filepath.Join(store, "acme", "repos")) || exists(filepath.Join(store, "Mixed")) {
		t.Errorf("links left after --finish:\n%s", out.String())
	}
	if !exists(filepath.Join(store, "acme", "backend", "memory", "a.md")) {
		t.Error("--finish removed more than links")
	}
	if rs := checkMemoryLayout(cfg); len(rs) != 1 || strings.Contains(rs[0].detail, "old path") {
		t.Errorf("doctor after finish: %+v", rs)
	}
}

// --finish must not pull the links out from under a checkout that has not
// been rewritten yet.
func TestMemoryMigrateFinishWaitsForCheckouts(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	old := filepath.Join(home, "shared", "orgs", "git.example.com", "acme", "repos", "backend", "memory")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx := &cli.Context{Config: cfg, Stdout: &out, Stderr: &out}
	if err := runMemory(ctx, []string{"migrate"}); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out.String())
	}
	// Someone puts the old settings back, as a restored backup would.
	stale := `{"autoMemoryDirectory": "` + old + `", "plansDirectory": "x", "permissions": {"additionalDirectories": ["` +
		filepath.Join(home, "shared", "orgs", "git.example.com") + `"]}}`
	if err := os.WriteFile(filepath.Join(top, ".claude", "settings.local.json"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := runMemory(ctx, []string{"migrate", "--finish"})
	if err == nil || !strings.Contains(out.String(), "still on old paths") {
		t.Errorf("finish with a stale checkout: %v\n%s", err, out.String())
	}
	if !exists(filepath.Join(home, "shared", "orgs")) {
		t.Error("the links were removed anyway")
	}
}
