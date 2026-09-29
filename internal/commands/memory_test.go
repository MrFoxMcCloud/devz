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
	if got["autoMemoryDirectory"] != "/s/git.example.com/acme/repos/backend/memory" {
		t.Errorf("autoMemoryDirectory = %v", got["autoMemoryDirectory"])
	}
	allow := got["permissions"].(map[string]any)["allow"].([]any)
	if len(allow) != 2 || allow[0] != "Bash(ls)" || allow[1] != "Edit(//s/git.example.com/**)" {
		t.Errorf("allow = %v", allow)
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

	store := filepath.Join(home, "shared", "orgs", "git.example.com")
	for _, p := range []string{
		"CLAUDE.md", "memory/MEMORY.md", "acme/CLAUDE.md", "acme/memory/MEMORY.md",
		"acme/plans", "acme/repos/backend/memory",
	} {
		if !exists(filepath.Join(store, p)) {
			t.Errorf("missing %s", p)
		}
	}

	orgClaude, _ := os.ReadFile(filepath.Join(store, "acme", "CLAUDE.md"))
	if !strings.HasPrefix(string(orgClaude), "@~/shared/orgs/git.example.com/CLAUDE.md\n") ||
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
	if r := checkMemory(cfg, top); r.status != statusOK {
		t.Errorf("doctor after init: %+v", r)
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
	if r := checkMemory(cfg, top); r.status != statusWarn {
		t.Errorf("doctor before init: %+v", r)
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
		HostDir: filepath.Join(home, "shared", "orgs", "git.example.com"),
		OrgDir:  filepath.Join(home, "shared", "orgs", "git.example.com", "acme"),
	}) {
		t.Error("flat checkout did not get its repo memory")
	}
}

func TestMemoryAdoptsOverAnUntouchedSeed(t *testing.T) {
	home, top, cfg := memoryEnv(t)
	orgDir := filepath.Join(home, "src", "acme")
	ctx := &cli.Context{Config: cfg, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	// An earlier checkout seeded the store before this one was seen.
	store := filepath.Join(home, "shared", "orgs", "git.example.com", "acme")
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
