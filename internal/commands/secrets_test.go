package commands

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

func TestShellQuoteRoundTrips(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, v := range []string{"", "plain", "has space", "it's", `$HOME`, "`id`", `a\b"c`, "''"} {
		out, err := exec.Command(sh, "-c", "printf %s "+shellQuote(v)).Output()
		if err != nil {
			t.Fatalf("sh for %q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("shellQuote(%q) evaluated to %q", v, out)
		}
	}
}

func TestPassEnvOverridesStore(t *testing.T) {
	t.Setenv("PASSWORD_STORE_DIR", "/old")
	cfg := config.Default()
	cfg.Secrets.Store = "/new/store"

	var dirs []string
	for _, kv := range passEnv(cfg) {
		if v, ok := strings.CutPrefix(kv, "PASSWORD_STORE_DIR="); ok {
			dirs = append(dirs, v)
		}
	}
	if len(dirs) != 1 || dirs[0] != "/new/store" {
		t.Errorf("PASSWORD_STORE_DIR entries = %v, want [/new/store]", dirs)
	}
}

func TestPassEnvLeavesStoreWhenUnset(t *testing.T) {
	t.Setenv("PASSWORD_STORE_DIR", "/old")
	for _, kv := range passEnv(config.Default()) {
		if kv == "PASSWORD_STORE_DIR=/old" {
			return
		}
	}
	t.Error("existing PASSWORD_STORE_DIR was dropped")
}

func TestSecretsEnvWithoutMappings(t *testing.T) {
	t.Setenv("DEVZ_CONFIG", t.TempDir()+"/config.json")
	var out bytes.Buffer
	ctx := &cli.Context{Config: config.Default(), Stdout: &out, Stderr: os.Stderr}
	err := secretsEnv(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "secrets.envVars") {
		t.Errorf("err = %v, want one naming secrets.envVars", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q before failing", out.String())
	}
}

func TestParseAddArgs(t *testing.T) {
	cases := []struct {
		args          []string
		entry, envVar string
		wantErr       bool
	}{
		{args: []string{"team/token"}, entry: "team/token"},
		{args: []string{"team/token", "--env", "TEAM_TOKEN"}, entry: "team/token", envVar: "TEAM_TOKEN"},
		{args: []string{"--env=TEAM_TOKEN", "team/token"}, entry: "team/token", envVar: "TEAM_TOKEN"},
		{args: nil, wantErr: true},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"team/token", "--env"}, wantErr: true},
		{args: []string{"team/token", "--force"}, wantErr: true},
		{args: []string{"team/token", "--env", "1BAD"}, wantErr: true},
		{args: []string{"team/token", "--env", "X;rm -rf ~"}, wantErr: true},
		{args: []string{"../outside"}, wantErr: true},
		{args: []string{"/abs"}, wantErr: true},
		{args: []string{"team/"}, wantErr: true},
		{args: []string{"team/token.gpg"}, wantErr: true},
	}
	for _, c := range cases {
		entry, envVar, err := parseAddArgs(c.args)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseAddArgs(%q) = %q, %q; want an error", c.args, entry, envVar)
			}
			continue
		}
		if err != nil || entry != c.entry || envVar != c.envVar {
			t.Errorf("parseAddArgs(%q) = %q, %q, %v; want %q, %q", c.args, entry, envVar, err, c.entry, c.envVar)
		}
	}
}

func TestRegisterSecret(t *testing.T) {
	cfg := config.Default()
	if err := registerSecret(&cfg, "team/token", "TEAM_TOKEN"); err != nil {
		t.Fatal(err)
	}
	// Registering again is a no-op, not a duplicate entry.
	if err := registerSecret(&cfg, "team/token", "TEAM_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := registerSecret(&cfg, "team/other", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Secrets.Entries, ","); got != "team/token,team/other" {
		t.Errorf("entries = %s", got)
	}
	if got := cfg.Secrets.EnvVars; len(got) != 1 || got["team/token"] != "TEAM_TOKEN" {
		t.Errorf("envVars = %v", got)
	}
}

func TestRegisterSecretRejectsTakenVariable(t *testing.T) {
	cfg := config.Default()
	cfg.Secrets.EnvVars = map[string]string{"team/token": "TEAM_TOKEN"}
	if err := registerSecret(&cfg, "team/new", "TEAM_TOKEN"); err == nil {
		t.Error("two entries exporting one variable should be an error")
	}
	if len(cfg.Secrets.Entries) != 0 {
		t.Errorf("entries changed on error: %v", cfg.Secrets.Entries)
	}
}

func TestStoreDirFollowsPass(t *testing.T) {
	t.Setenv("PASSWORD_STORE_DIR", "/from/env")
	cfg := config.Default()
	if got := storeDir(cfg); got != "/from/env" {
		t.Errorf("unconfigured storeDir = %s, want $PASSWORD_STORE_DIR", got)
	}
	cfg.Secrets.Store = "/from/config"
	if got := storeDir(cfg); got != "/from/config" {
		t.Errorf("configured storeDir = %s, want the config value", got)
	}
}

func TestSecretsExecRejectsBeforeReading(t *testing.T) {
	t.Setenv("DEVZ_CONFIG", t.TempDir()+"/config.json")
	cfg := config.Default()
	cfg.Secrets.EnvVars = map[string]string{"team/token": "TEAM_TOKEN"}
	ctx := &cli.Context{Config: cfg, Stdout: os.Stdout, Stderr: os.Stderr}
	// Each of these must fail before pass is ever run.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"TEAM_TOKEN", "sh"}, "usage"},
		{[]string{"TEAM_TOKEN", "--"}, "usage"},
		{[]string{"TEAM_TOKEN", "OTHER", "--", "sh"}, "not in secrets.envVars: OTHER"},
		{[]string{"--", "devz-no-such-command-anywhere"}, "not found"},
	} {
		err := secretsExec(ctx, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("secretsExec(%q) = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

func TestWithEnvReplacesExisting(t *testing.T) {
	got := withEnv([]string{"PATH=/bin", "TOKEN=stale", "TOKENX=keep"},
		[]envSecret{{"TOKEN", "fresh=value"}})
	want := []string{"PATH=/bin", "TOKENX=keep", "TOKEN=fresh=value"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("withEnv = %q, want %q", got, want)
	}
}
