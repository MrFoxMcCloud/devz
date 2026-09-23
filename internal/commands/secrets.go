package commands

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

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
		Usage: `usage: devz secrets <cached|env|exec|unlock|status|list|show|edit|add> [args]

  cached        exit 0 if the agent cache is warm, 1 if cold (no output)
  env [names]   print export lines for the configured entries, for eval
  exec [names] -- <command> [args]
                run a command with the configured entries (or just the named
                variables) in its environment
  unlock        warm the gpg-agent cache so tools without a TTY can read secrets
  status        whether the cache is warm, and which entries exist
  list          the entries this machine is expected to hold
  show <entry>  print one secret (delegates to pass)
  edit <entry>  rotate one secret (delegates to pass)
  add <entry> [--env NAME]
                store a new secret (via pass insert) and add it to this
                machine's config: secrets.entries, plus secrets.envVars when
                --env names the variable 'devz secrets env' should export

The env subcommand replaces exporting a token from a shell rc file. Instead of
plaintext on disk and in the environment of every process you start:

    eval "$(devz secrets env)"            # this shell only, from the store
    eval "$(devz secrets env CRM_API_TOKEN)"

exec is for launchers such as MCP server wrappers: the values go straight into
the command's environment, never through stdout or eval, and every name given
must be configured:

    exec devz secrets exec CRM_API_TOKEN -- uvx dasnuve-crm

To add a token and have env and exec provide it from then on:

    devz secrets add team/api-token --env TEAM_API_TOKEN

Why unlock exists: processes spawned without a TTY -- MCP servers, editor
extensions -- cannot show a passphrase prompt, so they fail at startup if the
agent cache is cold. Warming it from a terminal once is the fix.`,
		Run: runSecrets,
	}
}

func runSecrets(ctx *cli.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected a subcommand (cached, env, exec, unlock, status, list, show, edit, add)")
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
	case "exec":
		return secretsExec(ctx, args[1:])
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
	case "add":
		return secretsAdd(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// secretsEnv prints shell export lines for the configured entry->variable
// mappings, optionally filtered to the named variables.
func secretsEnv(ctx *cli.Context, want []string) error {
	vars, err := readEnvSecrets(ctx, want)
	if err != nil {
		return err
	}
	if len(vars) == 0 {
		return fmt.Errorf("no configured entry matches %s", strings.Join(want, ", "))
	}
	for _, v := range vars {
		fmt.Fprintf(ctx.Stdout, "export %s=%s\n", v.name, shellQuote(v.value))
	}
	return nil
}

// secretsExec replaces devz with a command whose environment holds the
// configured secrets. Unlike env it is strict: a launcher that asks for a
// variable nothing provides should fail loudly, not start half-configured.
func secretsExec(ctx *cli.Context, args []string) error {
	sep := slices.Index(args, "--")
	if sep < 0 || sep == len(args)-1 {
		return fmt.Errorf("usage: devz secrets exec [names] -- <command> [args]")
	}
	want, command := args[:sep], args[sep+1:]

	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	configured := slices.Collect(maps.Values(ctx.Config.Secrets.EnvVars))
	var missing []string
	for _, name := range want {
		if !slices.Contains(configured, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not in secrets.envVars: %s", strings.Join(missing, ", "))
	}
	vars, err := readEnvSecrets(ctx, want)
	if err != nil {
		return err
	}
	// Replace devz rather than run a child, so stdio, signals and the exit
	// status pass through untouched, which is what a stdio MCP server needs.
	return syscall.Exec(path, command, withEnv(os.Environ(), vars))
}

// withEnv sets vars in env, dropping any existing values for those names:
// with duplicates, which one a program sees depends on its libc.
func withEnv(env []string, vars []envSecret) []string {
	out := make([]string, 0, len(env)+len(vars))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(vars, func(v envSecret) bool { return v.name == name }) {
			out = append(out, kv)
		}
	}
	for _, v := range vars {
		out = append(out, v.name+"="+v.value)
	}
	return out
}

type envSecret struct{ name, value string }

// readEnvSecrets reads the secrets.envVars entries, sorted by entry so output
// is stable, keeping only the named variables when any are given.
func readEnvSecrets(ctx *cli.Context, want []string) ([]envSecret, error) {
	mappings := ctx.Config.Secrets.EnvVars
	if len(mappings) == 0 {
		return nil, fmt.Errorf("no secrets.envVars configured in %s", mustConfigPath())
	}
	entries := make([]string, 0, len(mappings))
	for entry := range mappings {
		entries = append(entries, entry)
	}
	sort.Strings(entries)

	var vars []envSecret
	for _, entry := range entries {
		name := mappings[entry]
		if len(want) > 0 && !slices.Contains(want, name) {
			continue
		}
		cmd := exec.Command("pass", "show", entry)
		cmd.Stderr = ctx.Stderr
		cmd.Env = passEnv(ctx.Config)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w (is the agent cache warm? try 'devz secrets unlock')", entry, err)
		}
		value, _, _ := strings.Cut(string(out), "\n")
		vars = append(vars, envSecret{name, strings.TrimSpace(value)})
	}
	return vars, nil
}

// secretsAdd stores a new secret with pass insert, then records it in the
// config so status, doctor and env know about it. Everything is validated
// before pass runs, so a bad argument never leaves a secret in the store that
// the config does not mention.
func secretsAdd(ctx *cli.Context, args []string) error {
	entry, envVar, err := parseAddArgs(args)
	if err != nil {
		return err
	}
	cfg := ctx.Config
	if err := registerSecret(&cfg, entry, envVar); err != nil {
		return err
	}
	// add is for new secrets only: overwriting one is what edit is for, and
	// pass would replace it without asking when the value is piped in.
	if exists(filepath.Join(storeDir(cfg), entry+".gpg")) {
		return fmt.Errorf("%s is already in the store; use 'devz secrets edit %s' to change it", entry, entry)
	}

	// On a terminal pass prompts twice with echo off. From a pipe, --echo
	// makes it read a single line instead of expecting the value twice.
	passArgs := []string{"insert", entry}
	if !isTerminal(os.Stdin) {
		passArgs = []string{"insert", "--echo", entry}
	}
	cmd := exec.Command("pass", passArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, ctx.Stdout, ctx.Stderr
	cmd.Env = passEnv(cfg)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pass insert %s: %w", entry, err)
	}

	path, err := config.Save(cfg)
	if err != nil {
		return fmt.Errorf("stored %s, but could not update the config: %w", entry, err)
	}
	fmt.Fprintf(ctx.Stdout, "added %s to %s\n", entry, path)
	return nil
}

func parseAddArgs(args []string) (entry, envVar string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--env":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--env needs a variable name")
			}
			i++
			envVar = args[i]
		case strings.HasPrefix(a, "--env="):
			envVar = strings.TrimPrefix(a, "--env=")
		case strings.HasPrefix(a, "-"):
			return "", "", fmt.Errorf("unknown flag %q", a)
		case entry != "":
			return "", "", fmt.Errorf("add takes one entry, got %q and %q", entry, a)
		default:
			entry = a
		}
	}
	if entry == "" {
		return "", "", fmt.Errorf("add needs an entry name")
	}
	if strings.HasPrefix(entry, "/") || strings.HasSuffix(entry, "/") ||
		strings.HasSuffix(entry, ".gpg") || slices.Contains(strings.Split(entry, "/"), "..") {
		return "", "", fmt.Errorf("invalid entry name %q", entry)
	}
	if envVar != "" && !validEnvName(envVar) {
		// The name goes unquoted into an export line meant for eval.
		return "", "", fmt.Errorf("invalid environment variable name %q", envVar)
	}
	return entry, envVar, nil
}

// registerSecret adds entry (and its env var mapping, if any) to cfg.
func registerSecret(cfg *config.Config, entry, envVar string) error {
	s := &cfg.Secrets
	if envVar != "" {
		for e, name := range s.EnvVars {
			if name == envVar && e != entry {
				return fmt.Errorf("%s is already exported from %s", envVar, e)
			}
		}
		if s.EnvVars == nil {
			s.EnvVars = map[string]string{}
		}
		s.EnvVars[entry] = envVar
	}
	if !slices.Contains(s.Entries, entry) {
		s.Entries = append(s.Entries, entry)
	}
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
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

// storeDir is the directory pass will use: the configured store, else pass's
// own default, which honors $PASSWORD_STORE_DIR.
func storeDir(cfg config.Config) string {
	if store := config.Expand(cfg.Secrets.Store); store != "" {
		return store
	}
	if store := os.Getenv("PASSWORD_STORE_DIR"); store != "" {
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
