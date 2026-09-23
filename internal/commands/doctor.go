package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Doctor checks that this machine is set up the way devz expects.
//
// On a shared tool this is the highest-value command: "is my machine right"
// is the question that otherwise becomes a direct message to whoever wrote it.
func Doctor() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Short: "check this machine's development environment",
		Usage: `usage: devz doctor [--quiet]

Runs every check that applies to this machine and prints a fix for anything
that is off. Exits non-zero if any check fails, so it is usable in a script.

  --quiet    print only problems

Checks are skipped per-machine via "doctor.skip" in the config, which is how a
teammate on a different OS turns off what does not apply to them. See
'devz config show'.`,
		Run: runDoctor,
	}
}

func runDoctor(ctx *cli.Context, args []string) error {
	quiet := false
	for _, a := range args {
		switch a {
		case "--quiet", "-q":
			quiet = true
		default:
			return fmt.Errorf("unknown flag %q", a)
		}
	}

	cfg := ctx.Config
	var results []result
	add := func(rs ...result) {
		for _, r := range rs {
			if cfg.Doctor.Skipped(r.id) {
				r = skip(r.id, "skipped by config")
			}
			results = append(results, r)
		}
	}

	add(checkBuild(ctx.Version))
	add(checkConfig(cfg))
	add(checkTools(cfg)...)
	add(checkPython())
	if cfg.Secrets.Backend == "pass" {
		add(checkGPG(cfg)...)
		add(checkPassStore(cfg)...)
	}
	if cfg.Claude.Enabled {
		add(checkClaude(cfg)...)
	}
	add(checkGH())
	add(checkPlugins())

	width := 0
	for _, r := range results {
		if len(r.id) > width {
			width = len(r.id)
		}
	}
	color := colorEnabled(ctx.Stdout)

	failures, warnings := 0, 0
	for _, r := range results {
		switch r.status {
		case statusFail:
			failures++
		case statusWarn:
			warnings++
		}
		if quiet && (r.status == statusOK || r.status == statusSkip) {
			continue
		}
		r.write(ctx.Stdout, width, color)
	}

	fmt.Fprintf(ctx.Stdout, "\n%d checks, %d failed, %d warnings (profile: %s)\n",
		len(results), failures, warnings, cfg.Profile)
	if failures > 0 {
		return fmt.Errorf("%d check(s) failed", failures)
	}
	return nil
}

// checkBuild flags a work-in-progress devz on PATH. Installing one to test it
// is normal; forgetting it is there is how a stale, unreleased build ends up
// answering every call for weeks.
func checkBuild(version string) result {
	if isRelease(version) {
		return ok("devz:build", "release "+version)
	}
	return warn("devz:build", "dev build "+version,
		"go install github.com/MrFoxMcCloud/devz@v1 to return to a release")
}

func checkConfig(cfg config.Config) result {
	path, err := config.Path()
	if err != nil {
		return fail("config", err.Error(), "")
	}
	if !exists(path) {
		return warn("config", "no config file; using defaults",
			"devz config init")
	}
	return ok("config", path)
}

func checkTools(cfg config.Config) []result {
	var out []result
	for _, tool := range cfg.Doctor.RequiredTools {
		id := "tool:" + tool
		if path, found := look(tool); found {
			out = append(out, ok(id, path))
		} else {
			out = append(out, fail(id, "not on PATH", "install "+tool))
		}
	}
	return out
}

// checkPython exists because a conda base environment that auto-activates
// silently owns `python3` for every script with a `#!/usr/bin/env python3`
// shebang -- including ones that have nothing to do with conda.
func checkPython() result {
	path, found := look("python3")
	if !found {
		return warn("python", "no python3 on PATH", "")
	}
	if strings.Contains(path, "conda") || strings.Contains(path, "miniconda") {
		return warn("python", "python3 resolves to conda: "+path,
			"conda config --set auto_activate_base false (then open a new shell)")
	}
	return ok("python", path)
}

func checkGPG(cfg config.Config) []result {
	var out []result

	if _, found := look("gpg"); !found {
		return []result{fail("gpg", "not on PATH", "install gnupg")}
	}

	if cfg.Secrets.GPGKey != "" {
		if _, err := output("gpg", "--list-secret-keys", cfg.Secrets.GPGKey); err != nil {
			out = append(out, fail("gpg:key",
				"secret key "+cfg.Secrets.GPGKey+" not in this keyring",
				"import the key, or fix secrets.gpgKey in the config"))
		} else {
			out = append(out, ok("gpg:key", cfg.Secrets.GPGKey))
		}
	}

	// pinentry-curses cannot prompt without GPG_TTY. This is the single most
	// common reason a passphrase prompt never appears.
	if os.Getenv("GPG_TTY") == "" {
		out = append(out, warn("gpg:tty", "GPG_TTY is unset",
			`add: export GPG_TTY="$TTY"  (zsh) to your shell rc`))
	} else {
		out = append(out, ok("gpg:tty", os.Getenv("GPG_TTY")))
	}

	out = append(out, checkAgentCache())
	return out
}

// checkAgentCache reports whether any key's passphrase is currently cached.
//
// This matters because tools spawned without a TTY -- MCP servers, editor
// extensions -- cannot prompt, so a cold cache shows up as an unrelated-looking
// startup failure elsewhere.
func checkAgentCache() result {
	out, err := output("gpg-connect-agent", "keyinfo --list", "/bye")
	if err != nil {
		return warn("gpg:cache", "could not query gpg-agent", "")
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// S KEYINFO <keygrip> D - - - <cached> P - - -
		if len(fields) > 6 && fields[0] == "S" && fields[1] == "KEYINFO" && fields[6] == "1" {
			return ok("gpg:cache", "warm")
		}
	}
	return warn("gpg:cache", "cold -- tools without a TTY cannot unlock",
		"devz secrets unlock")
}

func checkPassStore(cfg config.Config) []result {
	var out []result

	if _, found := look("pass"); !found {
		return []result{fail("pass", "not on PATH", "install pass")}
	}

	store := config.Expand(cfg.Secrets.Store)
	if store == "" {
		home, _ := os.UserHomeDir()
		store = filepath.Join(home, ".password-store")
	}
	if !exists(store) {
		return append(out, fail("pass:store", store+" does not exist",
			"pass init <gpg-key-id>"))
	}
	out = append(out, ok("pass:store", store))

	// Entry presence is checked on disk rather than by decrypting, so doctor
	// never triggers a passphrase prompt of its own.
	var missing []string
	for _, entry := range cfg.Secrets.Entries {
		if !exists(filepath.Join(store, entry+".gpg")) {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		out = append(out, fail("pass:entries",
			"missing: "+strings.Join(missing, ", "),
			"pass insert "+missing[0]))
	} else if len(cfg.Secrets.Entries) > 0 {
		out = append(out, ok("pass:entries",
			fmt.Sprintf("%d present", len(cfg.Secrets.Entries))))
	}

	// A store with no git remote is a single copy on a single disk.
	if !exists(filepath.Join(store, ".git")) {
		out = append(out, warn("pass:backup", "store is not a git repo",
			"pass git init && pass git remote add origin <private remote>"))
	} else {
		out = append(out, ok("pass:backup", "git-backed"))
	}

	return out
}

func checkClaude(cfg config.Config) []result {
	var out []result
	shared := config.Expand(cfg.Claude.SharedDir)
	if !exists(shared) {
		return []result{warn("claude:shared", shared+" not found",
			"disable with claude.enabled=false if this machine has no Claude setup")}
	}
	out = append(out, ok("claude:shared", shared))

	resolve := filepath.Join(shared, "bin", "claude-account-resolve")
	if !exists(resolve) {
		return append(out, warn("claude:account", "claude-account-resolve not found", ""))
	}
	cwd, _ := os.Getwd()
	email, err := output(resolve, "--info", cwd)
	if err != nil {
		return append(out, warn("claude:account", "marker does not resolve here",
			"claude-account --list, then claude-account <email>"))
	}
	// --info prints tab-separated fields; the first is the account email.
	email, _, _ = strings.Cut(strings.ReplaceAll(email, "\n", "\t"), "\t")
	if email == "" {
		email = "(default account)"
	}
	return append(out, ok("claude:account", email))
}

func checkGH() result {
	if _, found := look("gh"); !found {
		return skip("gh:auth", "gh not installed")
	}
	if _, err := output("gh", "auth", "status"); err != nil {
		return warn("gh:auth", "not authenticated", "gh auth login")
	}
	user, err := output("gh", "api", "user", "--jq", ".login")
	if err != nil || user == "" {
		return ok("gh:auth", "authenticated")
	}
	return ok("gh:auth", user)
}

func checkPlugins() result {
	plugins := cli.Plugins()
	if len(plugins) == 0 {
		return ok("plugins", "none on PATH")
	}
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.Name)
	}
	return ok("plugins", strings.Join(names, ", "))
}
