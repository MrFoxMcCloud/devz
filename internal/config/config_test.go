package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func useConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEVZ_CONFIG", path)
	return path
}

func TestLoadMissingFileIsDefault(t *testing.T) {
	useConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("got %+v, want Default()", cfg)
	}
}

func TestLoadMalformedIsError(t *testing.T) {
	useConfig(t, `{"profile": `)
	if _, err := Load(); err == nil {
		t.Fatal("Load of malformed config returned nil error")
	}
}

func TestLoadOverlaysDefaults(t *testing.T) {
	useConfig(t, `{"profile": "laptop", "secrets": {"envVars": {"team/api": "API_TOKEN"}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != "laptop" {
		t.Errorf("Profile = %q, want laptop", cfg.Profile)
	}
	if got := cfg.Secrets.EnvVars["team/api"]; got != "API_TOKEN" {
		t.Errorf("EnvVars[team/api] = %q, want API_TOKEN", got)
	}
	// Fields absent from the file keep their defaults.
	if cfg.Secrets.Backend != "pass" {
		t.Errorf("Backend = %q, want default pass", cfg.Secrets.Backend)
	}
	if !reflect.DeepEqual(cfg.Doctor.RequiredTools, Default().Doctor.RequiredTools) {
		t.Errorf("RequiredTools = %v, want defaults", cfg.Doctor.RequiredTools)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := useConfig(t, "")
	want := Default()
	want.Profile = "roundtrip"
	want.Doctor.Skip = []string{"python"}

	got, err := Save(want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got != path {
		t.Errorf("Save wrote %s, want %s", got, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Errorf("round trip: got %+v, want %+v", loaded, want)
	}
}

func TestPath(t *testing.T) {
	t.Setenv("DEVZ_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg", "devz", "config.json"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	t.Setenv("DEVZ_CONFIG", "/explicit.json")
	if got, _ := Path(); got != "/explicit.json" {
		t.Errorf("Path() with DEVZ_CONFIG = %q, want /explicit.json", got)
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := map[string]string{
		"~":        home,
		"~/a/b":    filepath.Join(home, "a", "b"),
		"/abs":     "/abs",
		"rel":      "rel",
		"~other/x": "~other/x",
		"":         "",
	}
	for in, want := range tests {
		if got := Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkipped(t *testing.T) {
	d := Doctor{Skip: []string{"python", "gpg:tty"}}
	for id, want := range map[string]bool{"python": true, "gpg:tty": true, "gpg": false, "": false} {
		if got := d.Skipped(id); got != want {
			t.Errorf("Skipped(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestStoreDirPrefersHostsAndFallsBackToTheOldPath(t *testing.T) {
	shared := t.TempDir()
	c := Claude{SharedDir: shared}
	if got := c.StoreDir(); got != filepath.Join(shared, "hosts") {
		t.Errorf("nothing on disk: %s, want hosts", got)
	}
	if err := os.Mkdir(filepath.Join(shared, "orgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := c.StoreDir(); got != filepath.Join(shared, "orgs") {
		t.Errorf("only the old directory exists: %s, want orgs", got)
	}
	if err := os.Mkdir(filepath.Join(shared, "hosts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := c.StoreDir(); got != filepath.Join(shared, "hosts") {
		t.Errorf("both exist: %s, want hosts", got)
	}
	c.Memory.Store = "/elsewhere"
	if got := c.StoreDir(); got != "/elsewhere" {
		t.Errorf("configured store: %s", got)
	}
}

func TestHasHostLayer(t *testing.T) {
	var m Memory
	if !m.HasHostLayer("git.example.com") || m.HasHostLayer("github.com") {
		t.Error("by default a self-hosted forge has the layer and a public one does not")
	}
	m.HostLayer = []string{"github.com"}
	if m.HasHostLayer("git.example.com") || !m.HasHostLayer("github.com") {
		t.Error("hostLayer, when set, is the whole list")
	}
	m.HostLayer = []string{}
	if m.HasHostLayer("git.example.com") {
		t.Error("an empty hostLayer means no host has the layer")
	}
}
