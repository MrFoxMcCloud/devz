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
