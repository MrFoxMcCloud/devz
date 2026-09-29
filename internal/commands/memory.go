package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Memory points Claude Code's memory and plans for a repo at a shared store.
//
// Claude Code keys auto memory on the config dir *and* the checkout path, so
// two accounts and three clones of one repo make six separate memories. This
// keys it on the origin URL instead, and adds two layers above the repo that
// load through CLAUDE.md imports. It only writes Claude Code's own settings
// (autoMemoryDirectory, plansDirectory); it does not manage memory contents.
func Memory() *cli.Command {
	return &cli.Command{
		Name:  "memory",
		Short: "share Claude memory and plans per repo, org and company",
		Usage: `usage: devz memory [status] [DIR]
       devz memory init [--dry-run] [DIR | --all]

  status [DIR]  which store layers a repo loads, and whether it is set up
                (the default)
  init [DIR]    set up the repo containing DIR (default: the current one)
  init --all    set up every checkout under claude.memory.roots
  --dry-run     print what init would change, and change nothing

Only repos whose origin is on a host in claude.memory.hosts are touched. The
store, <claude.memory.store>/<host>/<org>/ (default <claude.sharedDir>/orgs),
holds three layers:

  <host>/CLAUDE.md, memory/             company: every repo on the host
  <host>/<org>/CLAUDE.md, memory/       org: every repo in the org
  <host>/<org>/plans/                   plans for the org, not in any repo
  <host>/<org>/repos/<repo>/memory/     repo: that repo's auto memory

init, for one repo:
  - writes .claude/settings.local.json: autoMemoryDirectory (the repo layer),
    plansDirectory, and permission for Claude to edit the store
  - links <org>/CLAUDE.md and <org>/plans into the directory above the repo,
    when the checkout sits at <root>/<org>/<repo>. The org CLAUDE.md imports
    the org and company memory indexes, so they load in every repo of the org
    while each repo's own memory stays separate
  - creates missing store files; an existing org-level CLAUDE.md that is not
    yet in the store is moved there and linked back
  - adds .claude/settings.local.json to your global git ignore

It is safe to run again, and never overwrites a file it did not create.`,
		Run: runMemory,
	}
}

// memoryIgnore is added to the global git excludes so the per-checkout
// settings file never shows in git status.
const memoryIgnore = ".claude/settings.local.json"

func runMemory(ctx *cli.Context, args []string) error {
	if len(ctx.Config.Claude.Memory.Hosts) == 0 {
		return fmt.Errorf("no hosts configured; add claude.memory.hosts to %s", mustConfigPath())
	}
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && (args[0] == "status" || args[0] == "init") {
		sub, args = args[0], args[1:]
	}

	var dry, all bool
	var dirs []string
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dry = true
		case "--all":
			all = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q", a)
			}
			dirs = append(dirs, a)
		}
	}
	if len(dirs) > 1 || (all && len(dirs) > 0) {
		return fmt.Errorf("give one DIR or --all, not both")
	}
	dir := "."
	if len(dirs) == 1 {
		dir = dirs[0]
	}

	switch sub {
	case "status":
		if all || dry {
			return fmt.Errorf("--all and --dry-run apply to init")
		}
		l, err := resolveMemoryRepo(ctx.Config, dir)
		if err != nil {
			return err
		}
		memoryStatus(ctx, l)
		return nil
	default: // init
		if !all {
			l, err := resolveMemoryRepo(ctx.Config, dir)
			if err != nil {
				return err
			}
			return memoryInit(ctx, l, dry)
		}
		return memoryInitAll(ctx, dry)
	}
}

// memoryLayout is where one repo's layers live.
type memoryLayout struct {
	Top, Host, Org, Repo string
	HostDir, OrgDir      string
	// Roots are the configured search roots, expanded.
	Roots []string
}

func (l memoryLayout) repoMemory() string {
	return filepath.Join(l.OrgDir, "repos", l.Repo, "memory")
}
func (l memoryLayout) plans() string      { return filepath.Join(l.OrgDir, "plans") }
func (l memoryLayout) orgClaude() string  { return filepath.Join(l.OrgDir, "CLAUDE.md") }
func (l memoryLayout) hostClaude() string { return filepath.Join(l.HostDir, "CLAUDE.md") }
func (l memoryLayout) settingsPath() string {
	return filepath.Join(l.Top, ".claude", "settings.local.json")
}

// parentIsOrg reports whether the checkout sits at <root>/<org>/<repo>, the
// only layout where the directory above it is the org's to link into. A
// search root is never the org directory, even when its name matches: a
// CLAUDE.md there would load in every repo under it, whatever its org.
func (l memoryLayout) parentIsOrg() bool {
	parent := filepath.Dir(l.Top)
	return filepath.Base(parent) == l.Org && !slices.Contains(l.Roots, parent)
}

var errNotManaged = errors.New("not managed")

// resolveMemoryRepo finds the repo containing dir and maps its origin URL to
// the store. A repo on an unconfigured host wraps errNotManaged.
func resolveMemoryRepo(cfg config.Config, dir string) (memoryLayout, error) {
	top, err := output("git", "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return memoryLayout{}, fmt.Errorf("%s is not inside a git repo", dir)
	}
	url, err := output("git", "-C", top, "remote", "get-url", "origin")
	if err != nil || url == "" {
		return memoryLayout{}, fmt.Errorf("%s: no origin remote: %w", top, errNotManaged)
	}
	host, org, repo, ok := parseRemote(url)
	if !ok {
		return memoryLayout{}, fmt.Errorf("%s: cannot read host/org/repo from origin %q", top, url)
	}
	if !slices.Contains(cfg.Claude.Memory.Hosts, host) {
		return memoryLayout{}, fmt.Errorf("%s: origin host %s is not in claude.memory.hosts: %w",
			top, host, errNotManaged)
	}
	store := cfg.Claude.StoreDir()
	return memoryLayout{
		Top: top, Host: host, Org: org, Repo: repo, Roots: expandedRoots(cfg),
		HostDir: filepath.Join(store, host),
		OrgDir:  filepath.Join(store, host, org),
	}, nil
}

func expandedRoots(cfg config.Config) []string {
	var out []string
	for _, r := range cfg.Claude.Memory.Roots {
		out = append(out, filepath.Clean(config.Expand(r)))
	}
	return out
}

// parseRemote reads host, org and repo from an https, ssh:// or scp-style
// git URL.
func parseRemote(url string) (host, org, repo string, ok bool) {
	var path string
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		host, path, ok = strings.Cut(rest, "/")
		if !ok {
			return "", "", "", false
		}
	} else if h, p, found := strings.Cut(url, ":"); found && !strings.Contains(h, "/") {
		host, path = h, p
	} else {
		return "", "", "", false
	}
	if _, h, found := strings.Cut(host, "@"); found { // user@ or user:token@
		host = h
	}
	host, _, _ = strings.Cut(host, ":") // port
	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/"), "/")
	if host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return strings.ToLower(host), parts[0], parts[1], true
}

// memoryInit sets up one repo. Every step reports what it did, or with dry
// what it would do.
func memoryInit(ctx *cli.Context, l memoryLayout, dry bool) error {
	verb := func(s string) string {
		if dry {
			return "would " + s
		}
		return s
	}
	say := func(format string, a ...any) { fmt.Fprintf(ctx.Stdout, "  "+format+"\n", a...) }
	fmt.Fprintf(ctx.Stdout, "%s  (%s/%s/%s)\n", l.Top, l.Host, l.Org, l.Repo)

	for _, d := range []string{
		filepath.Join(l.HostDir, "memory"), filepath.Join(l.OrgDir, "memory"), l.plans(), l.repoMemory(),
	} {
		if !exists(d) {
			say("%s %s", verb("create"), tildePath(d))
			if !dry {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
		}
	}

	orgLink := filepath.Join(filepath.Dir(l.Top), "CLAUDE.md")
	// An org CLAUDE.md written by hand before the store existed becomes the
	// store's copy, with the imports added, rather than being shadowed.
	// A store copy that is still the untouched template (seeded by a checkout
	// processed earlier) does not block this.
	adopted := false
	seeded, _ := os.ReadFile(l.orgClaude())
	storeFree := !exists(l.orgClaude()) || string(seeded) == orgClaudeTemplate(l)
	if l.parentIsOrg() && storeFree && isRegular(orgLink) {
		adopted = true
		say("%s %s into the store", verb("move"), tildePath(orgLink))
		if !dry {
			body, err := os.ReadFile(orgLink)
			if err != nil {
				return err
			}
			if err := os.WriteFile(l.orgClaude(), append([]byte(orgImports(l)+"\n"), body...), 0o644); err != nil {
				return err
			}
			if err := os.Remove(orgLink); err != nil {
				return err
			}
		}
	}

	seeds := []struct{ path, body string }{
		{l.hostClaude(), hostClaudeTemplate(l)},
		{filepath.Join(l.HostDir, "memory", "MEMORY.md"), ""},
		{l.orgClaude(), orgClaudeTemplate(l)},
		{filepath.Join(l.OrgDir, "memory", "MEMORY.md"), ""},
	}
	for _, s := range seeds {
		if !exists(s.path) {
			say("%s %s", verb("create"), tildePath(s.path))
			if !dry {
				if err := os.WriteFile(s.path, []byte(s.body), 0o644); err != nil {
					return err
				}
			}
		}
	}

	raw, err := os.ReadFile(l.settingsPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	merged, changed, err := mergeMemorySettings(raw, l)
	if err != nil {
		return fmt.Errorf("%s: %w", l.settingsPath(), err)
	}
	if changed {
		say("%s %s", verb("update"), l.settingsPath())
		if !dry {
			if err := os.MkdirAll(filepath.Dir(l.settingsPath()), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(l.settingsPath(), merged, 0o644); err != nil {
				return err
			}
		}
	}

	if l.parentIsOrg() {
		for _, link := range []struct{ at, to string }{
			{orgLink, l.orgClaude()},
			{filepath.Join(filepath.Dir(l.Top), "plans"), l.plans()},
		} {
			if dry && adopted && link.at == orgLink {
				// The move above would have cleared the way for the link.
				say("would link %s -> %s", tildePath(link.at), tildePath(link.to))
				continue
			}
			msg, err := ensureLink(link.at, link.to, dry)
			if err != nil {
				return err
			}
			if msg != "" {
				say("%s", msg)
			}
		}
	} else {
		say("skip org links: %s is not at <root>/%s/%s, so the org and company layers will not load here",
			tildePath(l.Top), l.Org, l.Repo)
	}

	ignore, err := globalExcludesFile()
	if err != nil {
		return err
	}
	added, err := ensureLine(ignore, memoryIgnore, dry)
	if err != nil {
		return err
	}
	if added {
		say("%s %s to %s", verb("add"), memoryIgnore, tildePath(ignore))
	}
	return nil
}

func memoryInitAll(ctx *cli.Context, dry bool) error {
	roots := ctx.Config.Claude.Memory.Roots
	if len(roots) == 0 {
		return fmt.Errorf("no roots configured; add claude.memory.roots to %s", mustConfigPath())
	}
	var failed int
	for _, root := range roots {
		for _, top := range findCheckouts(config.Expand(root), 3) {
			l, err := resolveMemoryRepo(ctx.Config, top)
			if errors.Is(err, errNotManaged) {
				continue
			}
			if err == nil {
				err = memoryInit(ctx, l, dry)
			}
			if err != nil {
				fmt.Fprintf(ctx.Stderr, "devz memory: %v\n", err)
				failed++
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d checkout(s) failed", failed)
	}
	return nil
}

// findCheckouts returns the git checkouts (a .git dir, or a .git file for a
// linked worktree) at most depth levels below root, without descending into
// them.
func findCheckouts(root string, depth int) []string {
	if exists(filepath.Join(root, ".git")) {
		return []string{root}
	}
	if depth == 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, findCheckouts(filepath.Join(root, e.Name()), depth-1)...)
		}
	}
	return out
}

func memoryStatus(ctx *cli.Context, l memoryLayout) {
	fmt.Fprintf(ctx.Stdout, "%s  (%s/%s/%s)\n\n", l.Top, l.Host, l.Org, l.Repo)
	layer := func(name, dir, how string) {
		lines := "missing"
		if data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md")); err == nil {
			lines = fmt.Sprintf("%d index lines", countLines(data))
		} else if exists(dir) {
			lines = "no index yet"
		}
		fmt.Fprintf(ctx.Stdout, "  %-8s %s\n           %s; %s\n", name, tildePath(dir), lines, how)
	}
	orgLink := filepath.Join(filepath.Dir(l.Top), "CLAUDE.md")
	orgHow := "loads through " + tildePath(orgLink)
	if target, _ := os.Readlink(orgLink); target != l.orgClaude() {
		orgHow = "NOT loaded: " + tildePath(orgLink) + " does not link to the store"
	}
	layer("company", filepath.Join(l.HostDir, "memory"), orgHow)
	layer("org", filepath.Join(l.OrgDir, "memory"), orgHow)

	repoHow := "auto memory"
	if !memorySettingsCurrent(l) {
		repoHow = "NOT used: " + l.settingsPath() + " does not point here"
	}
	layer("repo", l.repoMemory(), repoHow)
	fmt.Fprintf(ctx.Stdout, "  %-8s %s\n", "plans", tildePath(l.plans()))

	if !memorySettingsCurrent(l) || !strings.HasPrefix(orgHow, "loads") {
		fmt.Fprintf(ctx.Stdout, "\nrun: devz memory init %s\n", l.Top)
	}
}

// checkMemory is the doctor check for the repo in cwd.
func checkMemory(cfg config.Config, cwd string) result {
	if len(cfg.Claude.Memory.Hosts) == 0 {
		return skip("claude:memory", "claude.memory.hosts not set")
	}
	l, err := resolveMemoryRepo(cfg, cwd)
	if err != nil {
		return skip("claude:memory", "not in a repo on a configured host")
	}
	if !memorySettingsCurrent(l) {
		return warn("claude:memory", "this repo's memory is not shared", "devz memory init")
	}
	return ok("claude:memory", l.Host+"/"+l.Org+"/"+l.Repo)
}

func memorySettingsCurrent(l memoryLayout) bool {
	raw, err := os.ReadFile(l.settingsPath())
	if err != nil {
		return false
	}
	_, changed, err := mergeMemorySettings(raw, l)
	return err == nil && !changed
}

// mergeMemorySettings sets the memory keys in a settings.local.json, keeping
// everything else in it. It reports whether anything changed.
func mergeMemorySettings(raw []byte, l memoryLayout) ([]byte, bool, error) {
	settings := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, false, err
		}
	}
	changed := false
	set := func(m map[string]any, key string, v any) {
		if m[key] != v {
			m[key] = v
			changed = true
		}
	}
	set(settings, "autoMemoryDirectory", l.repoMemory())
	set(settings, "plansDirectory", l.plans())

	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		if settings["permissions"] != nil {
			return nil, false, fmt.Errorf("permissions is not an object")
		}
		perms = map[string]any{}
		settings["permissions"] = perms
	}
	appendUnique := func(key, v string) error {
		list, _ := perms[key].([]any)
		if list == nil && perms[key] != nil {
			return fmt.Errorf("permissions.%s is not a list", key)
		}
		if !slices.Contains(list, any(v)) {
			perms[key] = append(list, v)
			changed = true
		}
		return nil
	}
	// The host dir covers the company and org layers; the repo layer is
	// under it too.
	if err := appendUnique("additionalDirectories", l.HostDir); err != nil {
		return nil, false, err
	}
	if err := appendUnique("allow", "Edit("+permissionPath(l.HostDir)+"/**)"); err != nil {
		return nil, false, err
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), changed, nil
}

// permissionPath writes an absolute path the way Claude Code permission rules
// expect: ~/ for the home directory, // for any other absolute path.
func permissionPath(p string) string {
	if t := tildePath(p); t != p {
		return t
	}
	return "/" + p
}

// tildePath abbreviates the home directory to ~.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rel
	}
	return p
}

// ensureLink makes at a symlink to to. It replaces only a missing path or a
// symlink pointing elsewhere; a real file or directory is left alone.
func ensureLink(at, to string, dry bool) (string, error) {
	target, err := os.Readlink(at)
	switch {
	case err == nil && target == to:
		return "", nil
	case err == nil:
		if dry {
			return fmt.Sprintf("would relink %s -> %s (was %s)", tildePath(at), tildePath(to), target), nil
		}
		if err := os.Remove(at); err != nil {
			return "", err
		}
	case exists(at):
		return fmt.Sprintf("skip %s: a real file or directory is there; move its contents to %s and rerun",
			tildePath(at), tildePath(to)), nil
	}
	if dry {
		return fmt.Sprintf("would link %s -> %s", tildePath(at), tildePath(to)), nil
	}
	return fmt.Sprintf("link %s -> %s", tildePath(at), tildePath(to)), os.Symlink(to, at)
}

// globalExcludesFile is git's core.excludesfile, or its XDG default.
func globalExcludesFile() (string, error) {
	if p, err := output("git", "config", "--global", "--get", "core.excludesfile"); err == nil && p != "" {
		return config.Expand(p), nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "git", "ignore"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "git", "ignore"), nil
}

// ensureLine appends line to path unless it is already there.
func ensureLine(path, line string, dry bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return false, nil
		}
	}
	if dry {
		return true, nil
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(data, line+"\n"...), 0o644)
}

func isRegular(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

func countLines(b []byte) int {
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// orgImports are the lines that load the company and org layers. They use
// ~/ paths so the one file reads the same from every checkout it is linked
// into.
func orgImports(l memoryLayout) string {
	return fmt.Sprintf("@%s\n@%s\n",
		tildePath(l.hostClaude()), tildePath(filepath.Join(l.OrgDir, "memory", "MEMORY.md")))
}

func hostClaudeTemplate(l memoryLayout) string {
	mem := tildePath(filepath.Join(l.HostDir, "memory"))
	return fmt.Sprintf(`# %[1]s

Shared context for every repository on %[1]s, loaded through each org's
CLAUDE.md. Created by `+"`devz memory init`"+`; edit it freely.

## Memory layers

Save each memory at the narrowest layer where it is always true.

| Layer | Directory | Loaded by |
|---|---|---|
| Company: every repo on %[1]s | `+"`%[2]s/`"+` | this file |
| Org: every repo in one org | `+"`%[3]s/<org>/memory/`"+` | the org CLAUDE.md |
| Repo: one repository | your auto memory directory | auto memory |

Auto memory saves to the repo layer on its own. For the company or org layer,
write the memory file in that directory and add its one-line pointer to the
MEMORY.md there. When a repo memory turns out to hold for other repos too,
move it up a layer and move its index line with it.

Company memory index (`+"`%[2]s/MEMORY.md`"+`):

@%[2]s/MEMORY.md
`, l.Host, mem, tildePath(l.HostDir))
}

func orgClaudeTemplate(l memoryLayout) string {
	return orgImports(l) + fmt.Sprintf(`
# %[1]s

Shared guidance for every repository in %[2]s/%[1]s. Each repository's own
CLAUDE.md still governs its code. The two lines above load the company
context and this org's memory index (`+"`%[3]s/`"+`).

## Plans

Write plans to `+"`%[4]s/`"+`, never to a repository's docs/.
`, l.Org, l.Host, tildePath(filepath.Join(l.OrgDir, "memory")), tildePath(l.plans()))
}
