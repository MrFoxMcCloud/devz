package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
