package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Secrets wraps the local pass store.
//
// It deliberately does not reimplement pass -- it shells out -- because the
// value here is the *unlock* and the machine-specific entry list, not another
// way to read a file.
func Secrets() *cli.Command {
	return &cli.Command{
		Name:  "secrets",
		Short: "unlock and inspect the local secret store",
		Usage: `usage: devz secrets <unlock|status|list|show|edit> [entry]

  cached        exit 0 if the agent cache is warm, 1 if cold (no output)
  env [names]   print export lines for the configured entries, for eval
  unlock        warm the gpg-agent cache so tools without a TTY can read secrets
  status        whether the cache is warm, and which entries exist
  list          the entries this machine is expected to hold
  show <entry>  print one secret (delegates to pass)
  edit <entry>  rotate one secret (delegates to pass)

The env subcommand replaces exporting a token from a shell rc file. Instead of
plaintext on disk and in the environment of every process you start:

    eval "$(devz secrets env)"            # this shell only, from the store
    eval "$(devz secrets env CRM_API_TOKEN)"

Why unlock exists: processes spawned without a TTY -- MCP servers, editor
extensions -- cannot show a passphrase prompt, so they fail at startup if the
agent cache is cold. Warming it from a terminal once is the fix.`,
		Run: runSecrets,
	}
}

func runSecrets(ctx *cli.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected a subcommand (unlock, status, list, show, edit)")
	}
	cfg := ctx.Config
	if cfg.Secrets.Backend != "pass" {
		return fmt.Errorf("unsupported secrets backend %q", cfg.Secrets.Backend)
	}
	if _, found := look("pass"); !found {
		return fmt.Errorf("pass is not on PATH")
	}

	switch args[0] {
	case "unlock":
		return unlock(ctx)
	case "status":
		return secretsStatus(ctx)
	case "cached":
		// Exit status only: meant for shell guards like
		//   devz secrets cached && eval "$(devz secrets env FOO)"
		// so a cold cache never turns opening a terminal into a prompt.
		if checkAgentCache().status != statusOK {
			return cli.ErrSilent
		}
		return nil
	case "env":
		return secretsEnv(ctx, args[1:])
	case "list":
		for _, e := range cfg.Secrets.Entries {
			fmt.Fprintln(ctx.Stdout, e)
		}
		return nil
	case "show", "edit":
		if len(args) < 2 {
			return fmt.Errorf("%s needs an entry name", args[0])
		}
		return passthrough(cfg, args[0], args[1])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// secretsEnv prints shell export lines for the configured entry->variable
// mappings, optionally filtered to the named variables.
func secretsEnv(ctx *cli.Context, want []string) error {
	mappings := ctx.Config.Secrets.EnvVars
	if len(mappings) == 0 {
		return fmt.Errorf("no secrets.envVars configured in %s", mustConfigPath())
	}

	wanted := map[string]bool{}
	for _, name := range want {
		wanted[name] = true
	}

	// Sorted so the output is stable and diffable.
	entries := make([]string, 0, len(mappings))
	for entry := range mappings {
		entries = append(entries, entry)
	}
	sort.Strings(entries)

	found := 0
	for _, entry := range entries {
		name := mappings[entry]
		if len(wanted) > 0 && !wanted[name] {
			continue
		}
		cmd := exec.Command("pass", "show", entry)
		cmd.Stderr = ctx.Stderr
		cmd.Env = passEnv(ctx.Config)
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("reading %s: %w (is the agent cache warm? try 'devz secrets unlock')", entry, err)
		}
		value, _, _ := strings.Cut(string(out), "\n")
		fmt.Fprintf(ctx.Stdout, "export %s=%s\n", name, shellQuote(strings.TrimSpace(value)))
		found++
	}
	if found == 0 {
		return fmt.Errorf("no configured entry matches %s", strings.Join(want, ", "))
	}
	return nil
}

// shellQuote single-quotes a value safely for eval.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func unlock(ctx *cli.Context) error {
	entry := ctx.Config.Secrets.UnlockEntry
	if entry == "" {
		if len(ctx.Config.Secrets.Entries) == 0 {
			return fmt.Errorf("no secrets.unlockEntry configured")
		}
		entry = ctx.Config.Secrets.Entries[0]
	}

	store := storeDir(ctx.Config)
	if !exists(filepath.Join(store, entry+".gpg")) {
		return fmt.Errorf("entry %q not found in %s", entry, store)
	}

	// Inherit the terminal so pinentry can prompt; discard the secret itself.
	cmd := exec.Command("pass", "show", entry)
	cmd.Stdin = os.Stdin
	cmd.Stdout = nil
	cmd.Stderr = ctx.Stderr
	cmd.Env = passEnv(ctx.Config)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not unlock: %w", err)
	}
	fmt.Fprintln(ctx.Stdout, "secrets unlocked")
	return nil
}

func secretsStatus(ctx *cli.Context) error {
	cfg := ctx.Config
	store := storeDir(cfg)
	color := colorEnabled(ctx.Stdout)

	results := []result{checkAgentCache()}
	for _, entry := range cfg.Secrets.Entries {
		if exists(filepath.Join(store, entry+".gpg")) {
			results = append(results, ok(entry, "present"))
		} else {
			results = append(results, fail(entry, "missing", "pass insert "+entry))
		}
	}
	width := 0
	for _, r := range results {
		if len(r.id) > width {
			width = len(r.id)
		}
	}
	for _, r := range results {
		r.write(ctx.Stdout, width, color)
	}
	return nil
}

func passthrough(cfg config.Config, verb, entry string) error {
	cmd := exec.Command("pass", verb, entry)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = passEnv(cfg)
	return cmd.Run()
}

func storeDir(cfg config.Config) string {
	if store := config.Expand(cfg.Secrets.Store); store != "" {
		return store
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".password-store")
}

// passEnv points pass at the configured store without disturbing the rest of
// the environment.
func passEnv(cfg config.Config) []string {
	env := os.Environ()
	if store := config.Expand(cfg.Secrets.Store); store != "" {
		filtered := env[:0]
		for _, kv := range env {
			if !strings.HasPrefix(kv, "PASSWORD_STORE_DIR=") {
				filtered = append(filtered, kv)
			}
		}
		env = append(filtered, "PASSWORD_STORE_DIR="+store)
	}
	return env
}
