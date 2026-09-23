package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// isolate points devz at an empty config and a PATH holding only dir, so a
// test never sees the real config or plugins installed on the machine.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVZ_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("PATH", dir)
	return dir
}

func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("plugins are shell scripts")
	}
	path := filepath.Join(dir, "devz-"+name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunDispatch(t *testing.T) {
	isolate(t)
	var got []string
	app := New("test",
		&Command{Name: "ok", Run: func(_ *Context, args []string) error { got = args; return nil }},
		&Command{Name: "boom", Run: func(*Context, []string) error { return errors.New("boom") }},
		&Command{Name: "quiet", Run: func(*Context, []string) error { return ErrSilent }},
	)

	if code := app.Run([]string{"ok", "a", "b"}); code != 0 {
		t.Errorf("ok: exit %d, want 0", code)
	}
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("ok: args = %v, want [a b]", got)
	}
	if code := app.Run([]string{"boom"}); code != 1 {
		t.Errorf("boom: exit %d, want 1", code)
	}
	if code := app.Run([]string{"quiet"}); code != 1 {
		t.Errorf("quiet: exit %d, want 1", code)
	}
	if code := app.Run([]string{"nope"}); code != 127 {
		t.Errorf("unknown: exit %d, want 127", code)
	}
}

func TestRunPluginExitCode(t *testing.T) {
	dir := isolate(t)
	writePlugin(t, dir, "seven", "#!/bin/sh\nexit 7\n")
	if code := New("test").Run([]string{"seven"}); code != 7 {
		t.Errorf("exit %d, want the plugin's 7", code)
	}
}

func TestBuiltinShadowsPlugin(t *testing.T) {
	dir := isolate(t)
	writePlugin(t, dir, "doctor", "#!/bin/sh\nexit 9\n")
	ran := false
	app := New("test", &Command{Name: "doctor", Run: func(*Context, []string) error { ran = true; return nil }})
	if code := app.Run([]string{"doctor"}); code != 0 || !ran {
		t.Errorf("exit %d, ran=%v; want the built-in to win", code, ran)
	}
}

func TestPluginsFirstOnPathWins(t *testing.T) {
	isolate(t)
	first, second := t.TempDir(), t.TempDir()
	writePlugin(t, first, "tool", "#!/bin/sh\n")
	writePlugin(t, second, "tool", "#!/bin/sh\n")
	writePlugin(t, second, "other", "#!/bin/sh\n")
	// Not executable: must be ignored.
	if err := os.WriteFile(filepath.Join(second, "devz-noexec"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", first+string(os.PathListSeparator)+second)

	want := []Plugin{
		{Name: "other", Path: filepath.Join(second, "devz-other")},
		{Name: "tool", Path: filepath.Join(first, "devz-tool")},
	}
	if got := Plugins(); !reflect.DeepEqual(got, want) {
		t.Errorf("Plugins() = %v, want %v", got, want)
	}
}

func TestDescribePlugin(t *testing.T) {
	dir := isolate(t)
	writePlugin(t, dir, "tunnel", "#!/usr/bin/env bash\n# devz: open the staging tunnel\necho hi\n")
	writePlugin(t, dir, "bare", "#!/bin/sh\necho devz: not a comment\n")
	if got := describePlugin(filepath.Join(dir, "devz-tunnel")); got != "open the staging tunnel" {
		t.Errorf("tunnel: %q", got)
	}
	if got := describePlugin(filepath.Join(dir, "devz-bare")); got != "" {
		t.Errorf("bare: %q, want empty", got)
	}
}

func TestSuggest(t *testing.T) {
	isolate(t)
	app := New("test", &Command{Name: "doctor"}, &Command{Name: "secrets"})
	for in, want := range map[string]string{
		"docter":  "doctor",
		"secret":  "secrets",
		"hlep":    "help",
		"zzzzzzz": "",
	} {
		if got := app.suggest(in); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"doctor", "doctor", 0},
		{"docter", "doctor", 1},
	}
	for _, tt := range tests {
		if got := distance(tt.a, tt.b); got != tt.want {
			t.Errorf("distance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
