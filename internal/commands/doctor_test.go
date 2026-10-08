package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// completionHome points every directory the completion check consults at a
// temp dir, so a test never reads the scripts installed on the real machine.
func completionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"FPATH", "ZSH", "ZSH_CUSTOM", "HOMEBREW_PREFIX",
		"BASH_COMPLETION_USER_DIR", "XDG_DATA_HOME"} {
		t.Setenv(name, "")
	}
	return home
}

func writeScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionResult(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current", "_devz")
	stale := filepath.Join(dir, "stale", "_devz")
	missing := filepath.Join(dir, "missing", "_devz")
	writeScript(t, current, zshCompletion)
	writeScript(t, stale, "#compdef devz\n# from an older release\n")

	if r, found := completionResult("zsh", zshCompletion, []string{missing, current}); !found || r.status != statusOK {
		t.Errorf("current script: found=%v result=%+v, want ok", found, r)
	}

	r, found := completionResult("zsh", zshCompletion, []string{missing, stale})
	if !found || r.status != statusWarn || r.id != "completion:zsh" {
		t.Fatalf("stale script: found=%v result=%+v, want a completion:zsh warning", found, r)
	}
	if !strings.HasPrefix(r.fix, "devz completion zsh > ") || !strings.HasSuffix(r.fix, "_devz") {
		t.Errorf("fix = %q, want the command that rewrites the file", r.fix)
	}

	// The first file found is the one the shell loads; a current copy further
	// down the search path does not make a stale one harmless.
	if r, _ := completionResult("zsh", zshCompletion, []string{stale, current}); r.status != statusWarn {
		t.Errorf("stale script first: %+v, want a warning", r)
	}
	if r, _ := completionResult("zsh", zshCompletion, []string{current, stale}); r.status != statusOK {
		t.Errorf("current script first: %+v, want ok", r)
	}

	if _, found := completionResult("zsh", zshCompletion, []string{missing}); found {
		t.Error("no file on disk should report nothing")
	}
}

func TestCheckCompletionFindsFPATHFirst(t *testing.T) {
	home := completionHome(t)
	onFpath := filepath.Join(home, "fpath-dir")
	t.Setenv("FPATH", strings.Join([]string{filepath.Join(home, "empty"), onFpath}, string(os.PathListSeparator)))
	writeScript(t, filepath.Join(onFpath, "_devz"), "old")
	// A current copy in a fallback directory must not hide the stale one that
	// zsh actually loads.
	writeScript(t, filepath.Join(home, ".local", "share", "zsh", "site-functions", "_devz"), zshCompletion)

	results := checkCompletion()
	if len(results) != 1 || results[0].id != "completion:zsh" || results[0].status != statusWarn {
		t.Fatalf("results = %+v, want one completion:zsh warning", results)
	}
	if !strings.Contains(results[0].detail, "fpath-dir") {
		t.Errorf("detail = %q, want the file on FPATH named", results[0].detail)
	}
}

func TestCheckCompletionBashAndNothingInstalled(t *testing.T) {
	home := completionHome(t)
	if results := checkCompletion(); len(results) != 1 || results[0].status != statusSkip || results[0].id != "completion" {
		t.Fatalf("nothing installed: %+v, want one skip", results)
	}

	writeScript(t, filepath.Join(home, ".local", "share", "bash-completion", "completions", "devz"), bashCompletion)
	results := checkCompletion()
	if len(results) != 1 || results[0].id != "completion:bash" || results[0].status != statusOK {
		t.Fatalf("current bash script: %+v, want one completion:bash ok", results)
	}
}

func TestCandidateFilesKeepsOrderAndDropsRepeats(t *testing.T) {
	got := candidateFiles([]string{"/a", "", "/b", "/a"}, "_devz")
	want := []string{"/a/_devz", "/b/_devz"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("candidateFiles = %v, want %v", got, want)
	}
}

// gitIn runs git in dir with an identity, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCheckGitBackup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, ".gitconfig"))

	expect := func(dir, want string, status status, parents ...string) {
		t.Helper()
		r := checkGitBackup("x:backup", dir, parents...)
		if r.status != status || !strings.Contains(r.detail, want) {
			t.Errorf("checkGitBackup(%s) = %+v, want status %d and detail containing %q", dir, r, status, want)
		}
	}

	// A directory inside some other repo that ignores it is not backed up,
	// even though git answers from there.
	outer := filepath.Join(base, "outer")
	store := filepath.Join(outer, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, base, "init", "-q", "-b", "main", outer)
	expect(store, "not a git repo", statusWarn)

	gitIn(t, base, "init", "-q", "-b", "main", store)
	expect(store, "no remote", statusWarn)

	remote := filepath.Join(base, "remote.git")
	gitIn(t, base, "init", "-q", "--bare", "-b", "main", remote)
	gitIn(t, store, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(store, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect(store, "1 uncommitted", statusWarn)

	gitIn(t, store, "add", "-A")
	gitIn(t, store, "commit", "-q", "-m", "one")
	expect(store, "no upstream", statusWarn)

	gitIn(t, store, "push", "-q", "-u", "origin", "main")
	expect(store, "committed and pushed", statusOK)

	if err := os.WriteFile(filepath.Join(store, "b.md"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, store, "add", "-A")
	gitIn(t, store, "commit", "-q", "-m", "two")
	expect(store, "1 commit(s) not pushed", statusWarn)
	gitIn(t, store, "push", "-q")

	// A host directory inside a store that is one repo counts as backed up by
	// that repo, when the store is named as an acceptable parent.
	host := filepath.Join(store, "git.example.com")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	expect(host, "not a git repo", statusWarn)
	expect(host, "committed and pushed", statusOK, store)
}

func TestCheckAllCheckouts(t *testing.T) {
	_, top, cfg := memoryEnv(t)
	gitCommit(t, top)
	wt := filepath.Join(filepath.Dir(top), "backend-topic")
	gitIn(t, top, "worktree", "add", "-q", wt, "-b", "topic")

	results := checkAllCheckouts(cfg)
	if len(results) != 1 || results[0].id != "claude:memory" || results[0].status != statusWarn {
		t.Fatalf("before init: %+v, want one claude:memory warning", results)
	}
	// The worktree sits in the tree, but it is the same repo: one warning,
	// naming the main checkout, with a fix that can be pasted.
	if !strings.Contains(results[0].detail, "backend:") || !strings.HasSuffix(results[0].fix, "backend") {
		t.Errorf("warning = %+v", results[0])
	}

	ctx := &cli.Context{Config: cfg, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}}
	if err := runMemory(ctx, []string{"init", "--all"}); err != nil {
		t.Fatal(err)
	}
	results = checkAllCheckouts(cfg)
	if len(results) != 1 || results[0].id != "claude:all" || results[0].status != statusOK {
		t.Fatalf("after init: %+v, want one summary line", results)
	}
	if !strings.Contains(results[0].detail, "1 checkouts, 2 working trees") {
		t.Errorf("summary = %q", results[0].detail)
	}
}
