package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Every Claude login has its own config dir, and with it its own settings,
// agents, skills and MCP servers. sync makes them one set: the copies live in
// claude.sharedDir and each config dir links to them. Logins, credentials,
// history and per-project state stay separate.
//
// MCP servers cannot be linked. Claude Code keeps them inside each account's
// .claude.json, next to the login, so sync copies that one key instead.

// sharedItems are linked from the shared dir into each config dir.
var sharedItems = []struct {
	name  string
	isDir bool
}{
	{"settings.json", false},
	{"CLAUDE.md", false},
	{"agents", true},
	{"commands", true},
	{"skills", true},
}

// mcpServersFile is the shared copy of the mcpServers key.
const mcpServersFile = "mcp-servers.json"

const syncUsage = `usage: devz claude sync [push] [--dry-run]
       devz claude sync status
       devz claude sync pull <account>

Keeps every logged-in Claude account on one set of settings, agents, commands,
skills and MCP servers. Logins, history and project state stay separate.

  push     link settings.json, CLAUDE.md, agents, commands and skills from
           claude.sharedDir into each account's config dir, and copy
           mcp-servers.json into each account's .claude.json (the default)
  status   what is linked, what has drifted, and who each dir is logged in as
  pull     take one account's current state as the shared one, then push.
           Use it after adding an MCP server in that account

It is safe to run again. A config dir that holds a real file where a link
should be is not overwritten blindly: its content becomes the shared copy if
the shared one is still empty, and is otherwise saved under
<claude.sharedDir>/backups/<time>/ before the link replaces it. Claude Code
rewrites a settings file atomically, which is how a link turns back into a
file; running sync again is the fix.`

func runClaudeSync(ctx *cli.Context, args []string) error {
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s", mustConfigPath())
	}
	sub := "push"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	shared := config.Expand(ctx.Config.Claude.SharedDir)
	accounts := claudeAccounts(ctx.Config)
	switch sub {
	case "help":
		fmt.Fprintln(ctx.Stdout, syncUsage)
		return nil
	case "status":
		if len(args) > 0 {
			return fmt.Errorf("status takes no arguments")
		}
		return syncStatus(ctx, shared, accounts)
	case "push":
		dry := false
		for _, a := range args {
			if a != "--dry-run" && a != "-n" {
				return fmt.Errorf("unknown argument %q", a)
			}
			dry = true
		}
		return syncPush(ctx, shared, accounts, dry)
	case "pull":
		if len(args) != 1 {
			return fmt.Errorf("usage: devz claude sync pull <account>")
		}
		acct, err := matchAccount(args[0], accounts)
		if err != nil {
			return unresolvedExit(ctx, err)
		}
		return syncPull(ctx, shared, accounts, acct)
	}
	return fmt.Errorf("unknown subcommand %q; expected push, status or pull", sub)
}

// linksTo reports whether path is a symlink that resolves to target.
func linksTo(path, target string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	return samePath(path, target)
}

// emptyItem reports whether a shared copy holds nothing yet: a missing or
// zero-length file, a missing or empty directory.
func emptyItem(path string, isDir bool) bool {
	if isDir {
		entries, err := os.ReadDir(path)
		return err != nil || len(entries) == 0
	}
	info, err := os.Stat(path)
	return err != nil || info.Size() == 0
}

// copyTree copies a file, a symlink or a directory tree, keeping modes.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
}

// syncer carries one run's settings.
type syncer struct {
	ctx    *cli.Context
	shared string
	dry    bool
	// backups is where a replaced file is saved, made on first use.
	backups string
}

func (s *syncer) say(format string, a ...any) { fmt.Fprintf(s.ctx.Stdout, "  "+format+"\n", a...) }

func (s *syncer) verb(v string) string {
	if s.dry {
		return "would " + v
	}
	return v
}

// backup saves path before it is replaced.
func (s *syncer) backup(path string) error {
	home, _ := os.UserHomeDir()
	name := strings.ReplaceAll(strings.TrimPrefix(path, home+string(filepath.Separator)), string(filepath.Separator), "_")
	s.say("%s %s to %s", s.verb("back up"), tildePath(path), tildePath(filepath.Join(s.backups, name)))
	if s.dry {
		return nil
	}
	if err := os.MkdirAll(s.backups, 0o755); err != nil {
		return err
	}
	return copyTree(path, filepath.Join(s.backups, name))
}

// link makes cfgDir/<item> a link to the shared copy.
func (s *syncer) link(cfgDir, name string, isDir bool) error {
	target, path := filepath.Join(s.shared, name), filepath.Join(cfgDir, name)
	if linksTo(path, target) {
		return nil
	}
	info, err := os.Lstat(path)
	isReal := err == nil && info.Mode()&os.ModeSymlink == 0
	if isReal {
		// A fresh config dir, or an atomic write that replaced the link.
		if emptyItem(target, isDir) {
			s.say("%s %s as the shared %s", s.verb("adopt"), tildePath(path), name)
			if !s.dry {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
				if err := copyTree(path, target); err != nil {
					return err
				}
			}
		} else if err := s.backup(path); err != nil {
			return err
		}
	}
	s.say("%s %s -> %s", s.verb("link"), tildePath(path), tildePath(target))
	if s.dry {
		return nil
	}
	if !exists(target) {
		if isDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		} else if err := os.WriteFile(target, nil, 0o644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

// readJSONValue parses a file as one JSON value, or returns nil.
func readJSONValue(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	return v
}

// mcpInSync reports whether an account's mcpServers equal the shared ones.
func mcpInSync(stateFile, sharedFile string) bool {
	state, _ := readJSONValue(stateFile).(map[string]any)
	want := readJSONValue(sharedFile)
	return want != nil && state != nil && reflect.DeepEqual(state["mcpServers"], want)
}

// setTopLevelKey returns doc, a JSON object, with key set to value. Every
// other key keeps its position and its exact bytes: this file is Claude
// Code's own state, with the login in it, and devz has no business
// reformatting what it was not asked to change.
func setTopLevelKey(doc []byte, key string, value []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	type pair struct {
		key string
		raw json.RawMessage
	}
	var pairs []pair
	found := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := tok.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if k == key {
			raw, found = value, true
		}
		pairs = append(pairs, pair{k, raw})
	}
	if !found {
		pairs = append(pairs, pair{key, value})
	}

	var out bytes.Buffer
	out.WriteString("{")
	for i, p := range pairs {
		if i > 0 {
			out.WriteString(",")
		}
		name, _ := json.Marshal(p.key)
		out.WriteString("\n  ")
		out.Write(name)
		out.WriteString(": ")
		out.Write(p.raw)
	}
	out.WriteString("\n}")
	if bytes.HasSuffix(doc, []byte("\n")) {
		out.WriteString("\n")
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("rewriting produced invalid JSON")
	}
	return out.Bytes(), nil
}

// pushMCP copies the shared mcpServers into one account's state file.
func (s *syncer) pushMCP(acct claudeAccount) error {
	sharedFile := filepath.Join(s.shared, mcpServersFile)
	raw, err := os.ReadFile(sharedFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s is not valid JSON", tildePath(sharedFile))
	}
	if mcpInSync(acct.StateFile, sharedFile) {
		return nil
	}
	doc, err := os.ReadFile(acct.StateFile)
	if err != nil {
		return err
	}
	var value bytes.Buffer
	if err := json.Indent(&value, bytes.TrimSpace(raw), "  ", "  "); err != nil {
		return err
	}
	updated, err := setTopLevelKey(doc, "mcpServers", value.Bytes())
	if err != nil {
		// Leave a state file we cannot read safely exactly as it is.
		s.say("!! %s: %v; leaving it alone", tildePath(acct.StateFile), err)
		return nil
	}
	s.say("%s MCP servers in %s", s.verb("update"), tildePath(acct.StateFile))
	if s.dry {
		return nil
	}
	if err := s.backup(acct.StateFile); err != nil {
		return err
	}
	// Atomically, and 0600: the file holds the account's login.
	tmp, err := os.CreateTemp(filepath.Dir(acct.StateFile), ".claude.json.")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), acct.StateFile)
}

func newSyncer(ctx *cli.Context, shared string, dry bool) *syncer {
	return &syncer{ctx: ctx, shared: shared, dry: dry,
		backups: filepath.Join(shared, "backups", time.Now().Format("20060102-150405"))}
}

func syncPush(ctx *cli.Context, shared string, accounts []claudeAccount, dry bool) error {
	if len(accounts) == 0 {
		return unresolvedExit(ctx, unresolvedError{"no config dir is logged in: run `claude` and /login"})
	}
	s := newSyncer(ctx, shared, dry)
	for _, acct := range accounts {
		fmt.Fprintf(ctx.Stdout, "%s  (%s)\n", tildePath(acct.ConfigDir), acct.Email)
		for _, item := range sharedItems {
			if err := s.link(acct.ConfigDir, item.name, item.isDir); err != nil {
				return err
			}
		}
		if err := s.pushMCP(acct); err != nil {
			return err
		}
	}
	return nil
}

// syncPull makes one account's current state the shared one.
func syncPull(ctx *cli.Context, shared string, accounts []claudeAccount, from claudeAccount) error {
	fmt.Fprintf(ctx.Stdout, "taking %s (%s) as the shared state\n", tildePath(from.ConfigDir), from.Email)
	state, _ := readJSONValue(from.StateFile).(map[string]any)
	if state == nil {
		return fmt.Errorf("%s is not a JSON object", tildePath(from.StateFile))
	}
	servers, ok := state["mcpServers"]
	if !ok {
		servers = map[string]any{}
	}
	out, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(shared, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(shared, mcpServersFile), append(out, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "  wrote %s\n", tildePath(filepath.Join(shared, mcpServersFile)))

	// A real file in that config dir is newer than the shared copy it
	// replaced a link to.
	for _, item := range sharedItems {
		path, target := filepath.Join(from.ConfigDir, item.name), filepath.Join(shared, item.name)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink == 0 {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := copyTree(path, target); err != nil {
				return err
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			fmt.Fprintf(ctx.Stdout, "  adopted %s from %s\n", item.name, tildePath(from.ConfigDir))
		}
	}
	return syncPush(ctx, shared, accounts, false)
}

// unlinked lists the shared items that are not links in an account's dir.
func unlinked(shared string, acct claudeAccount) []string {
	var names []string
	for _, item := range sharedItems {
		if !linksTo(filepath.Join(acct.ConfigDir, item.name), filepath.Join(shared, item.name)) {
			names = append(names, item.name)
		}
	}
	return names
}

func syncStatus(ctx *cli.Context, shared string, accounts []claudeAccount) error {
	fmt.Fprintf(ctx.Stdout, "shared: %s\n", tildePath(shared))
	if len(accounts) == 0 {
		fmt.Fprintln(ctx.Stdout, "no config dir is logged in")
		return nil
	}
	sharedMCP := filepath.Join(shared, mcpServersFile)
	for _, acct := range accounts {
		org := acct.Org
		if org == "" {
			org = "no org"
		}
		fmt.Fprintf(ctx.Stdout, "%s  (%s, %s)\n", tildePath(acct.ConfigDir), acct.Email, org)
		for _, item := range sharedItems {
			path := filepath.Join(acct.ConfigDir, item.name)
			state := "linked"
			switch {
			case linksTo(path, filepath.Join(shared, item.name)):
			case exists(path):
				state = "NOT SHARED: a local copy; run devz claude sync"
			default:
				state = "missing; run devz claude sync"
			}
			fmt.Fprintf(ctx.Stdout, "  %-14s %s\n", item.name, state)
		}
		mcp := "in sync"
		switch {
		case !exists(sharedMCP):
			mcp = "no shared " + mcpServersFile + " yet"
		case !mcpInSync(acct.StateFile, sharedMCP):
			mcp = "DRIFTED: run devz claude sync, or devz claude sync pull " + acct.Alias + " to keep this account's"
		}
		fmt.Fprintf(ctx.Stdout, "  %-14s %s\n", "MCP servers", mcp)
	}
	return nil
}

// checkSync is the doctor checks for what sync maintains.
func checkSync(cfg config.Config, accounts []claudeAccount) []result {
	shared := config.Expand(cfg.Claude.SharedDir)
	var loose []string
	for _, acct := range accounts {
		if names := unlinked(shared, acct); len(names) > 0 {
			loose = append(loose, tildePath(acct.ConfigDir)+": "+strings.Join(names, ", "))
		}
	}
	var out []result
	if len(loose) > 0 {
		out = append(out, warn("claude:links", "not shared between accounts: "+strings.Join(loose, "; "), "devz claude sync"))
	} else {
		out = append(out, ok("claude:links", fmt.Sprintf("settings, agents, commands and skills shared by %d account(s)", len(accounts))))
	}

	sharedMCP := filepath.Join(shared, mcpServersFile)
	if !exists(sharedMCP) {
		return append(out, skip("claude:mcp", "no shared "+mcpServersFile))
	}
	var drifted []string
	for _, acct := range accounts {
		if !mcpInSync(acct.StateFile, sharedMCP) {
			drifted = append(drifted, acct.Alias)
		}
	}
	if len(drifted) > 0 {
		return append(out, warn("claude:mcp", "MCP servers differ from the shared ones for: "+strings.Join(drifted, ", "),
			"devz claude sync, or devz claude sync pull <account> to keep that account's"))
	}
	return append(out, ok("claude:mcp", fmt.Sprintf("the same MCP servers in %d account(s)", len(accounts))))
}

const ejectUsage = `usage: devz claude eject [--apply]

Shows how to take this machine off the shared Claude setup, and with --apply
does the one part that is safe to automate.

Without --apply nothing changes: it prints the plan.

With --apply, every link in an account's config dir is replaced by a copy of
what it pointed at, so each account goes on working with its own settings,
agents, commands and skills. MCP servers are already copies in each account.

It never edits your shell rc file or your editor's settings, and it deletes
nothing: claude.sharedDir, the memory store and every .claude-account marker
are left where they are. It prints the lines to remove by hand.
'devz claude sync' puts the links back.`

// runClaudeEject is the way out. The setup is plain files, so leaving it is
// mostly a matter of knowing which ones: this command is that list, kept
// next to the code that creates them so it cannot go stale.
func runClaudeEject(ctx *cli.Context, args []string) error {
	apply := false
	for _, a := range args {
		switch a {
		case "--apply":
			apply = true
		case "--dry-run", "-n":
		case "help", "-h", "--help":
			fmt.Fprintln(ctx.Stdout, ejectUsage)
			return nil
		default:
			return fmt.Errorf("unknown argument %q", a)
		}
	}
	shared := config.Expand(ctx.Config.Claude.SharedDir)
	accounts := claudeAccounts(ctx.Config)
	w := ctx.Stdout
	verb := "would replace"
	if apply {
		verb = "replaced"
	}

	fmt.Fprintln(w, "1. Give each account its own copy of what is shared")
	n := 0
	for _, acct := range accounts {
		for _, item := range sharedItems {
			path, target := filepath.Join(acct.ConfigDir, item.name), filepath.Join(shared, item.name)
			if !linksTo(path, target) {
				continue
			}
			n++
			if apply {
				if err := os.Remove(path); err != nil {
					return err
				}
				if err := copyTree(target, path); err != nil {
					return err
				}
			}
			fmt.Fprintf(w, "   %s the link %s with a copy of %s\n", verb, tildePath(path), tildePath(target))
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "   nothing is linked")
	}

	fmt.Fprintln(w, "\n2. Stop applying the account rule when Claude Code starts (by hand)")
	home, _ := os.UserHomeDir()
	for _, rc := range []string{".zshrc", ".bashrc"} {
		path := filepath.Join(home, rc)
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "claude exec") {
			fmt.Fprintf(w, "   %s: remove the claude() function that calls 'devz claude exec'\n", tildePath(path))
		}
	}
	for _, file := range vscodeSettingsFiles(home) {
		if data, err := os.ReadFile(file); err == nil && jsonStringValue(string(data), "claudeCode.claudeProcessWrapper") != "" {
			fmt.Fprintf(w, "   %s: remove \"claudeCode.claudeProcessWrapper\"\n", tildePath(file))
		}
	}
	fmt.Fprintln(w, "   after that every session starts on the default account, ~/.claude;")
	fmt.Fprintln(w, "   use CLAUDE_CONFIG_DIR=~/.claude-<name> claude for another one")

	fmt.Fprintln(w, "\n3. What can stay (harmless once nothing reads it)")
	fmt.Fprintln(w, "   .claude-account markers in repos: one-word files, globally git-ignored")
	fmt.Fprintf(w, "   %s: memory and plans; each repo's .claude/settings.local.json still\n", tildePath(ctx.Config.Claude.StoreDir()))
	fmt.Fprintln(w, "   points there, so shared memory keeps working. To end that too, delete")
	fmt.Fprintln(w, "   autoMemoryDirectory and plansDirectory from those files")
	fmt.Fprintf(w, "   %s: after steps 1 and 2 only the memory store inside it is still used\n", tildePath(shared))
	fmt.Fprintf(w, "   set claude.enabled to false in %s to silence the doctor checks\n", mustConfigPath())

	if !apply {
		fmt.Fprintln(w, "\nnothing was changed. 'devz claude eject --apply' does step 1; 'devz claude sync' undoes it.")
	} else {
		fmt.Fprintln(w, "\nstep 1 is done. 'devz claude sync' puts the links back.")
	}
	return nil
}
