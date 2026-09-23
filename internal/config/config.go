// Package config loads devz's machine-local configuration.
//
// Behavior lives in the binary; anything that would differ between two
// people's machines -- paths, key ids, which checks apply -- lives here, so
// the same build works for everyone on the team.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the whole of devz's machine-local state.
type Config struct {
	// Profile names this machine's setup. Cosmetic; shown by `devz doctor`.
	Profile string  `json:"profile"`
	Secrets Secrets `json:"secrets"`
	Claude  Claude  `json:"claude"`
	Doctor  Doctor  `json:"doctor"`
}

// Secrets describes the local secret store. Only the "pass" backend exists
// today; the field is here so a teammate on a Mac can say "keychain" later
// without the command names changing.
type Secrets struct {
	Backend string `json:"backend"`
	// Store is the password-store directory. Empty means pass's own default.
	Store string `json:"store"`
	// Entries are the secrets this machine is expected to hold. `devz doctor`
	// reports any that are missing; `devz secrets list` prints them.
	Entries []string `json:"entries"`
	// UnlockEntry is the one read to warm the gpg-agent cache. It should be
	// the cheapest entry in Entries.
	UnlockEntry string `json:"unlockEntry"`
	// GPGKey is the key id the store is encrypted to, checked by doctor.
	GPGKey string `json:"gpgKey"`
	// EnvVars maps an entry to the environment variable it populates, for
	// `devz secrets env`. This is what replaces exporting a token from a
	// shell rc file: the value is fetched on demand instead of sitting in
	// plaintext on disk and in every process's environment.
	EnvVars map[string]string `json:"envVars"`
}

// Claude configures the two-account Claude Code setup. Disabled by default:
// it is specific to machines that have ~/.claude-shared installed.
type Claude struct {
	Enabled   bool   `json:"enabled"`
	SharedDir string `json:"sharedDir"`
}

// Doctor tunes the environment checks.
type Doctor struct {
	// RequiredTools must be on PATH.
	RequiredTools []string `json:"requiredTools"`
	// Skip names checks to omit on this machine, by check id.
	Skip []string `json:"skip"`
}

// Default is the configuration written by `devz config init` and used when no
// file exists. It is deliberately conservative: only the checks that hold for
// any developer machine are on.
func Default() Config {
	return Config{
		Profile: "default",
		Secrets: Secrets{
			Backend: "pass",
		},
		Claude: Claude{
			Enabled:   false,
			SharedDir: "~/.claude-shared",
		},
		Doctor: Doctor{
			RequiredTools: []string{"git", "gh", "uv"},
		},
	}
}

// Path returns the config file location, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	if p := os.Getenv("DEVZ_CONFIG"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "devz", "config.json"), nil
}

// Load reads the config, falling back to Default when the file is absent.
// A malformed file is an error rather than a silent fallback: quietly running
// with different settings than the file says is the failure worth avoiding.
func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg, creating the directory if needed.
func Save(cfg Config) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o600)
}

// Expand resolves a leading ~ against the home directory.
func Expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return path
}

// Skipped reports whether a doctor check id is disabled on this machine.
func (d Doctor) Skipped(id string) bool {
	for _, s := range d.Skip {
		if s == id {
			return true
		}
	}
	return false
}
