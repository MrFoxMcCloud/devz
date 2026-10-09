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
		Name:       "memory",
		Short:      "share Claude memory and plans per repo, org and company",
		Deprecated: "claude memory",
		Hidden:     true,
		Usage:      memoryUsage,
		Run:        runMemory,
		Complete:   completeMemory,
	}
}

// memoryUsage is the help for `devz claude memory`, and for `devz memory`,
// its older name.
const memoryUsage = `usage: devz claude memory [status] [DIR]
       devz claude memory init [--dry-run] [DIR | --all]
       devz claude memory path [DIR | <host>/<org>[/<repo>]]
       devz claude memory list
       devz claude memory migrate [--dry-run | --finish]

  status [DIR]  which store layers a repo loads, and whether it is set up
                (the default)
  init [DIR]    set up the repo containing DIR (default: the current one)
  init --all    set up every checkout under claude.memory.roots
  --dry-run     print what init would change, and change nothing
  path          where each layer's memory lives, as 'layer<TAB>directory'
                lines: for the repo you are in, or for any <host>/<org> or
                <host>/<org>/<repo>, checked out here or not
  list          every host, org and repo that has a place in the store, with
                how many memories each holds

  migrate       move a store laid out before 1.7 to the current layout.
                Old paths keep resolving through links afterwards; --finish
                removes those once nothing uses them

'devz memory' is the older name for this command and still works.

Only repos whose origin is on a host in claude.memory.hosts are touched. The
store mirrors the repo's URL, <claude.memory.store>/<host>/<org>/<repo>/
(default <claude.sharedDir>/hosts), in lower case, and holds three layers:

  <host>/CLAUDE.md, memory/         host: every repo on the host, where the
                                    host is one company (claude.memory.hostLayer)
  <host>/<org>/CLAUDE.md, memory/   org: every repo in the org
  <host>/<org>/plans/               plans for the org, not in any repo
  <host>/<org>/<repo>/memory/       repo: that repo's auto memory

A repo cannot be named memory, plans or repos: its directory would be the
org's own.

init, for one repo:
  - writes .claude/settings.local.json: autoMemoryDirectory (the repo layer),
    plansDirectory, and the guard: Claude may edit this repo's memory and the
    org's plans freely, and is asked before it edits company or org memory
  - links <org>/CLAUDE.md and <org>/plans into the directory above the repo,
    when the checkout sits at <root>/<org>/<repo>. The org CLAUDE.md imports
    the org and company memory indexes, so they load in every repo of the org
    while each repo's own memory stays separate
  - creates missing store files; an existing org-level CLAUDE.md that is not
    yet in the store is moved there and linked back
  - appends the branch rule to the host CLAUDE.md if it is not there yet
  - adds .claude/settings.local.json to your global git ignore

Linked git worktrees need nothing. Claude Code reads the main checkout's
.claude/settings.local.json in every worktree of a repo, so they share its
memory and plans. Run from a worktree, status and init act on the main checkout.

It is safe to run again, and never overwrites a file it did not create.`

// memoryIgnore is added to the global git excludes so the per-checkout
// settings file never shows in git status.
const memoryIgnore = ".claude/settings.local.json"

func runMemory(ctx *cli.Context, args []string) error {
	if len(ctx.Config.Claude.Memory.Hosts) == 0 {
		return fmt.Errorf("no hosts configured; add claude.memory.hosts to %s", mustConfigPath())
	}
	sub := "status"
	if len(args) > 0 && slices.Contains(memorySubcommands, args[0]) {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "help":
		fmt.Fprintln(ctx.Stdout, memoryUsage)
		return nil
	case "list":
		if len(args) > 0 {
			return fmt.Errorf("list takes no arguments")
		}
		return memoryList(ctx)
	case "migrate":
		return memoryMigrate(ctx, args)
	case "path":
		if len(args) > 1 {
			return fmt.Errorf("path takes one DIR or <host>/<org>[/<repo>]")
		}
		l, err := memoryLayoutFor(ctx.Config, strings.Join(args, ""))
		if err != nil {
			return err
		}
		memoryPaths(ctx, l)
		return nil
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

var memorySubcommands = []string{"status", "init", "path", "list", "migrate", "help"}

func completeMemory(_ *cli.Context, args []string) []string {
	if len(args) == 0 {
		return []string{"status", "init", "path", "list", "migrate"}
	}
	switch args[0] {
	case "init":
		return []string{"--all", "--dry-run"}
	case "migrate":
		return []string{"--dry-run", "--finish"}
	}
	return nil
}

// memoryLayoutFor resolves what `path` was given: a directory inside a repo,
// or <host>/<org>[/<repo>] naming a place in the store. The second form is
// what lets a session look up another org's memory without a checkout of it.
func memoryLayoutFor(cfg config.Config, arg string) (memoryLayout, error) {
	if arg == "" {
		return resolveMemoryRepo(cfg, ".")
	}
	if exists(arg) {
		return resolveMemoryRepo(cfg, arg)
	}
	parts := strings.Split(strings.Trim(arg, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || slices.Contains(parts, "") || slices.Contains(parts, "..") {
		return memoryLayout{}, fmt.Errorf("%s is neither a directory nor <host>/<org>[/<repo>]", arg)
	}
	host := strings.ToLower(parts[0])
	if !slices.Contains(cfg.Claude.Memory.Hosts, host) {
		return memoryLayout{}, fmt.Errorf("%s is not in claude.memory.hosts: %w", host, errNotManaged)
	}
	repo := ""
	if len(parts) == 3 {
		repo = parts[2]
	}
	return newMemoryLayout(cfg, "", host, parts[1], repo)
}

// memoryPaths prints one layer per line, tab-separated, so the output can be
// read by a person or cut by a script.
func memoryPaths(ctx *cli.Context, l memoryLayout) {
	if l.HostLayer {
		fmt.Fprintf(ctx.Stdout, "host\t%s\n", filepath.Join(l.HostDir, "memory"))
	}
	fmt.Fprintf(ctx.Stdout, "org\t%s\n", filepath.Join(l.OrgDir, "memory"))
	if l.Repo != "" {
		fmt.Fprintf(ctx.Stdout, "repo\t%s\n", l.repoMemory())
	}
	fmt.Fprintf(ctx.Stdout, "plans\t%s\n", l.plans())
}

// memoryList walks the store and prints every place that can hold memories.
func memoryList(ctx *cli.Context) error {
	cfg := ctx.Config
	store := cfg.Claude.StoreDir()
	legacy := legacyStore(cfg)
	type row struct {
		name  string
		count int
	}
	var rows []row
	for _, host := range cfg.Claude.Memory.Hosts {
		hostDir := filepath.Join(store, host)
		if !exists(hostDir) {
			continue
		}
		if cfg.Claude.Memory.HasHostLayer(host) {
			rows = append(rows, row{host, countMemories(filepath.Join(hostDir, "memory"))})
		}
		for _, org := range realDirs(hostDir) {
			// The host's own memory directory sits beside the orgs.
			if org == "memory" {
				continue
			}
			orgDir := filepath.Join(hostDir, org)
			rows = append(rows, row{host + "/" + org, countMemories(filepath.Join(orgDir, "memory"))})
			repoParent := orgDir
			if legacy {
				repoParent = filepath.Join(orgDir, "repos")
			}
			for _, repo := range realDirs(repoParent) {
				if !legacy && slices.Contains(reservedRepoNames, repo) {
					continue
				}
				rows = append(rows, row{host + "/" + org + "/" + repo,
					countMemories(filepath.Join(repoParent, repo, "memory"))})
			}
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("nothing in %s yet; run 'devz claude memory init' in a repo", tildePath(store))
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r.name))
	}
	for _, r := range rows {
		fmt.Fprintf(ctx.Stdout, "%-*s  %d\n", width, r.name, r.count)
	}
	return nil
}

// realDirs lists the directories in dir that are not hidden and not links.
// The links left by a migration point at directories listed in their own
// right, so following them would count everything twice.
func realDirs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// countMemories counts the memory files in a directory: every .md except the
// index.
func countMemories(dir string) int {
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "MEMORY.md" {
			n++
		}
	}
	return n
}

// memoryLayout is where one repo's layers live.
type memoryLayout struct {
	// Top is the repo's main checkout, where its settings file lives.
	Top, Host, Org, Repo string
	HostDir, OrgDir      string
	// Worktree is the linked worktree the question was asked from, if any.
	Worktree string
	// Roots are the configured search roots, expanded.
	Roots []string
	// HostLayer says the host's own directory is a shared layer.
	HostLayer bool
	// Legacy says the store is still laid out as it was before 1.7:
	// <org>/repos/<repo>, names in their original case.
	Legacy bool
	// StaleRoots are places the store used to be. Rules and directories in a
	// settings file that point into one are dropped when it is rewritten.
	StaleRoots []string
}

// reservedRepoNames are the directories an org keeps for itself. A repo with
// one of these names would have no directory of its own.
var reservedRepoNames = []string{"memory", "plans", "repos"}

// newMemoryLayout places host/org/repo in the store. The store mirrors the
// URL in lower case, since both a forge and a person treat MrFox/Devz and
// mrfox/devz as one repository, and two directories for it would be two
// memories. repo may be empty, for an org on its own.
func newMemoryLayout(cfg config.Config, top, host, org, repo string) (memoryLayout, error) {
	store := cfg.Claude.StoreDir()
	l := memoryLayout{
		Top: top, Host: host, Org: org, Repo: repo, Roots: expandedRoots(cfg),
		HostLayer: cfg.Claude.Memory.HasHostLayer(host),
		Legacy:    legacyStore(cfg),
	}
	orgDir := org
	if !l.Legacy {
		orgDir = strings.ToLower(org)
		if orgDir == "memory" {
			return memoryLayout{}, fmt.Errorf("%s/%s: an org named %q would share a directory with the host's own memory", host, org, org)
		}
		if slices.Contains(reservedRepoNames, strings.ToLower(repo)) {
			return memoryLayout{}, fmt.Errorf("%s/%s/%s: a repository named %q cannot have a place in the store, "+
				"because <org>/%s is the org's own directory", host, org, repo, repo, strings.ToLower(repo))
		}
		if legacyDir := cfg.Claude.LegacyStoreDir(); legacyDir != store {
			l.StaleRoots = []string{legacyDir}
		}
	}
	l.HostDir = filepath.Join(store, host)
	l.OrgDir = filepath.Join(store, host, orgDir)
	return l, nil
}

// legacyStore reports whether the store still has the layout from before
// 1.7: it is at the old default path, or an org in it still keeps real
// directories under repos/.
func legacyStore(cfg config.Config) bool {
	store := cfg.Claude.StoreDir()
	if cfg.Claude.Memory.Store == "" && store == cfg.Claude.LegacyStoreDir() {
		return true
	}
	for _, host := range realDirs(store) {
		for _, org := range realDirs(filepath.Join(store, host)) {
			if len(realDirs(filepath.Join(store, host, org, "repos"))) > 0 {
				return true
			}
		}
	}
	return false
}

func (l memoryLayout) repoMemory() string {
	if l.Legacy {
		return filepath.Join(l.OrgDir, "repos", l.Repo, "memory")
	}
	return filepath.Join(l.OrgDir, strings.ToLower(l.Repo), "memory")
}

// ruleFile is the shared file that carries the branch rule: the host's where
// the host is a layer, else the org's.
func (l memoryLayout) ruleFile() string {
	if l.HostLayer {
		return l.hostClaude()
	}
	return l.orgClaude()
}

// staleRule reports whether a permission rule or directory in a settings
// file points at somewhere the store, or this repo's place in it, used to be.
func (l memoryLayout) staleRule(entry string) bool {
	if l.Legacy {
		return false
	}
	for _, root := range l.StaleRoots {
		for _, form := range []string{root, tildePath(root), permissionPath(root)} {
			if entry == form || strings.Contains(entry, form+"/") {
				return true
			}
		}
	}
	for _, form := range []string{l.OrgDir, tildePath(l.OrgDir), permissionPath(l.OrgDir)} {
		if strings.Contains(entry, form+"/repos/") {
			return true
		}
	}
	return false
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
	return strings.EqualFold(filepath.Base(parent), l.Org) && !slices.Contains(l.Roots, parent)
}

var errNotManaged = errors.New("not managed")

// resolveMemoryRepo finds the repo containing dir and maps its origin URL to
// the store. A repo on an unconfigured host wraps errNotManaged.
func resolveMemoryRepo(cfg config.Config, dir string) (memoryLayout, error) {
	top, err := output("git", "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return memoryLayout{}, fmt.Errorf("%s is not inside a git repo", dir)
	}
	// Claude Code reads the main checkout's .claude/settings.local.json in
	// every linked worktree of a repo. So the main checkout is where settings
	// and the org links belong, wherever the question is asked from.
	worktree := ""
	if main := mainCheckout(top); main != "" && main != top {
		worktree, top = top, main
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
	l, err := newMemoryLayout(cfg, top, host, org, repo)
	l.Worktree = worktree
	return l, err
}

// worktreesOf lists every working tree of the repo containing dir, the main
// checkout first. A bare repository's own directory is left out.
func worktreesOf(dir string) []string {
	out, err := output("git", "-C", dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var trees []string
	for _, block := range strings.Split(out, "\n\n") {
		lines := strings.Split(block, "\n")
		if slices.Contains(lines, "bare") {
			continue
		}
		for _, line := range lines {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				trees = append(trees, path)
			}
		}
	}
	return trees
}

// mainCheckout is the root of the main working tree of the repo containing
// dir, or "" when there is none (not a repo, or a bare one). git lists the
// main working tree first, wherever a linked one was put.
func mainCheckout(dir string) string {
	out, err := output("git", "-C", dir, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(out, "\n\n")
	lines := strings.Split(first, "\n")
	if slices.Contains(lines, "bare") {
		return ""
	}
	for _, line := range lines {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			return path
		}
	}
	return ""
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

	dirs := []string{filepath.Join(l.OrgDir, "memory"), l.plans(), l.repoMemory()}
	if l.HostLayer {
		dirs = append([]string{filepath.Join(l.HostDir, "memory")}, dirs...)
	}
	for _, d := range dirs {
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
		{l.orgClaude(), orgClaudeTemplate(l)},
		{filepath.Join(l.OrgDir, "memory", "MEMORY.md"), ""},
	}
	if l.HostLayer {
		seeds = append([]struct{ path, body string }{
			{l.hostClaude(), hostClaudeTemplate(l)},
			{filepath.Join(l.HostDir, "memory", "MEMORY.md"), ""},
		}, seeds...)
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

	// A shared file from before the branch rule existed gets the rule
	// appended. Nothing else in the file is touched.
	if body, err := os.ReadFile(l.ruleFile()); err == nil && !bytes.Contains(body, []byte(branchRuleMarker)) {
		say("%s the branch rule to %s", verb("append"), tildePath(l.ruleFile()))
		if !dry {
			sep := "\n"
			if !bytes.HasSuffix(body, []byte("\n")) {
				sep = "\n\n"
			}
			if err := os.WriteFile(l.ruleFile(), append(body, []byte(sep+branchRuleSection)...), 0o644); err != nil {
				return err
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
		say("no org links: %s is not at <root>/%s/%s, so only the repo's own memory loads here",
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
	// A linked worktree found in the tree resolves to its main checkout, which
	// the walk reaches on its own.
	done := map[string]bool{}
	for _, root := range roots {
		for _, top := range findCheckouts(config.Expand(root), 3) {
			l, err := resolveMemoryRepo(ctx.Config, top)
			if errors.Is(err, errNotManaged) {
				continue
			}
			if err == nil {
				if done[l.Top] {
					continue
				}
				done[l.Top] = true
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
	fmt.Fprintf(ctx.Stdout, "%s  (%s/%s/%s)\n", l.Top, l.Host, l.Org, l.Repo)
	if l.Worktree != "" {
		fmt.Fprintf(ctx.Stdout, "  asked from the linked worktree %s, which uses the main checkout's settings\n",
			tildePath(l.Worktree))
	}
	fmt.Fprintln(ctx.Stdout)
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
	orgHow, orgOK := "loads through "+tildePath(orgLink), true
	if !l.parentIsOrg() {
		// Nothing to fix: this checkout has no org folder above it to link into.
		orgHow = fmt.Sprintf("not loaded here: the checkout is not at <root>/%s/%s", l.Org, l.Repo)
	} else if target, _ := os.Readlink(orgLink); target != l.orgClaude() {
		orgHow, orgOK = "NOT loaded: "+tildePath(orgLink)+" does not link to the store", false
	}
	if l.HostLayer {
		layer("host", filepath.Join(l.HostDir, "memory"), orgHow)
	}
	layer("org", filepath.Join(l.OrgDir, "memory"), orgHow)

	shared, guarded := memorySettingsState(l)
	repoHow := "auto memory"
	if !shared {
		repoHow = "NOT used: " + l.settingsPath() + " does not point here"
	}
	layer("repo", l.repoMemory(), repoHow)
	fmt.Fprintf(ctx.Stdout, "  %-8s %s\n", "plans", tildePath(l.plans()))
	guardHow := "writes to company and org memory prompt first"
	if !guarded {
		guardHow = "NOT in place: company and org memory can be written without a prompt"
	}
	fmt.Fprintf(ctx.Stdout, "  %-8s %s\n", "guard", guardHow)

	if l.Legacy {
		fmt.Fprintln(ctx.Stdout, "\nthe store uses the layout from before 1.7; run: devz claude memory migrate --dry-run")
	}
	if !memorySettingsCurrent(l) || !orgOK {
		fmt.Fprintf(ctx.Stdout, "\nrun: devz claude memory init %s\n", l.Top)
	}
}

// checkMemory is the doctor checks for the repo in cwd.
func checkMemory(cfg config.Config, cwd string) []result {
	if len(cfg.Claude.Memory.Hosts) == 0 {
		return []result{skip("claude:memory", "claude.memory.hosts not set")}
	}
	l, err := resolveMemoryRepo(cfg, cwd)
	if err != nil {
		return []result{skip("claude:memory", "not in a repo on a configured host")}
	}
	return memoryResults(l)
}

// memoryResults checks one repo: that its memory is shared, that the shared
// layers are guarded, and that the host file carries the branch rule. The
// last two only mean something once the first holds.
func memoryResults(l memoryLayout) []result {
	shared, guarded := memorySettingsState(l)
	if !shared {
		return []result{warn("claude:memory", "this repo's memory is not shared", "devz claude memory init")}
	}
	name := l.Host + "/" + l.Org + "/" + l.Repo
	if l.Worktree != "" {
		name += " (worktree of " + tildePath(l.Top) + ")"
	}
	out := []result{ok("claude:memory", name)}

	if guarded {
		out = append(out, ok("claude:memory-guard", "writes to company and org memory prompt first"))
	} else {
		out = append(out, warn("claude:memory-guard",
			"company and org memory can be written without a prompt", "devz claude memory init"))
	}

	body, err := os.ReadFile(l.ruleFile())
	switch {
	case err == nil && bytes.Contains(body, []byte(branchRuleMarker)) && !l.HostLayer && !l.parentIsOrg():
		// The rule is written down, but no shared file loads in a checkout
		// with no org folder above it.
		out = append(out, skip("claude:memory-rule", "no shared layer loads here, so the branch rule does not either"))
	case err == nil && bytes.Contains(body, []byte(branchRuleMarker)):
		out = append(out, ok("claude:memory-rule", "branch rule is in "+tildePath(l.ruleFile())))
	default:
		out = append(out, warn("claude:memory-rule",
			"the branch rule is missing from "+tildePath(l.ruleFile()), "devz claude memory init"))
	}
	return out
}

// memorySettingsState reads the settings Claude Code will use for l: whether
// auto memory and plans point at the store, and whether the guard is in place.
func memorySettingsState(l memoryLayout) (shared, guarded bool) {
	raw, err := os.ReadFile(l.settingsPath())
	if err != nil {
		return false, false
	}
	var settings struct {
		AutoMemoryDirectory string `json:"autoMemoryDirectory"`
		PlansDirectory      string `json:"plansDirectory"`
		Permissions         struct {
			Allow []string `json:"allow"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, false
	}
	shared = settings.AutoMemoryDirectory == l.repoMemory() && settings.PlansDirectory == l.plans()
	g := l.guard()
	guarded = !slices.Contains(settings.Permissions.Allow, g.legacy)
	for _, rule := range g.ask {
		if !slices.Contains(settings.Permissions.Ask, rule) {
			guarded = false
		}
	}
	return shared, guarded
}

// memoryGuard is the permission rules that keep the shared layers from being
// written without a prompt.
type memoryGuard struct {
	// allow: this repo's own memory and the org's plans, written freely.
	allow []string
	// ask: the company and org layers, in every org on the host. An ask rule
	// holds even in accept-edits mode, where an additional directory is
	// otherwise written without a prompt.
	ask []string
	// legacy is the rule earlier versions wrote, allowing the whole host
	// directory. It is removed.
	legacy string
}

func (l memoryLayout) guard() memoryGuard {
	host := permissionPath(l.HostDir)
	return memoryGuard{
		allow: []string{
			"Edit(" + permissionPath(l.repoMemory()) + "/**)",
			"Edit(" + permissionPath(l.plans()) + "/**)",
		},
		// * matches one path segment, so */memory is an org's memory and
		// never a repo's, which sits deeper.
		ask: []string{
			"Edit(" + host + "/memory/**)",
			"Edit(" + host + "/CLAUDE.md)",
			"Edit(" + host + "/*/memory/**)",
			"Edit(" + host + "/*/CLAUDE.md)",
		},
		legacy: "Edit(" + host + "/**)",
	}
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
	// The host dir stays readable from every repo on it, so a session can
	// consult another org's memory. Writing is narrower: see memoryGuard.
	if err := appendUnique("additionalDirectories", l.HostDir); err != nil {
		return nil, false, err
	}
	// A settings file written before the store moved still names the old
	// place. Those entries would keep a dead path readable and its rules live.
	for _, key := range []string{"additionalDirectories", "allow", "ask"} {
		if list, _ := perms[key].([]any); slices.ContainsFunc(list, func(v any) bool {
			entry, _ := v.(string)
			return l.staleRule(entry)
		}) {
			perms[key] = slices.DeleteFunc(slices.Clone(list), func(v any) bool {
				entry, _ := v.(string)
				return l.staleRule(entry)
			})
			changed = true
		}
	}
	g := l.guard()
	for _, rule := range g.allow {
		if err := appendUnique("allow", rule); err != nil {
			return nil, false, err
		}
	}
	for _, rule := range g.ask {
		if err := appendUnique("ask", rule); err != nil {
			return nil, false, err
		}
	}
	if list, _ := perms["allow"].([]any); slices.Contains(list, any(g.legacy)) {
		perms["allow"] = slices.DeleteFunc(slices.Clone(list), func(v any) bool { return v == any(g.legacy) })
		changed = true
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
	org := "@" + tildePath(filepath.Join(l.OrgDir, "memory", "MEMORY.md")) + "\n"
	if !l.HostLayer {
		return org
	}
	return "@" + tildePath(l.hostClaude()) + "\n" + org
}

// findingMemory tells a session how the store is laid out, so it can be asked
// to read what another repo or org has learned. Writing stays narrower: see
// memoryGuard.
func findingMemory(l memoryLayout) string {
	store := tildePath(filepath.Dir(l.HostDir))
	return fmt.Sprintf(`## Finding memory

The store mirrors a repository's URL, in lower case:

- a repository: `+"`%[1]s/<host>/<org>/<repo>/memory/`"+`
- an org: `+"`%[1]s/<host>/<org>/memory/`"+`, and its plans in `+"`plans/`"+` beside it

`+"`devz claude memory path <host>/<org>[/<repo>]`"+` prints the directories and
`+"`devz claude memory list`"+` shows what exists. Read another repository's or
org's memory when it helps. Write only to your own repository's.
`, store)
}

// branchRuleMarker lets init and doctor tell whether a host CLAUDE.md already
// carries the rule, however the surrounding text has been edited.
const branchRuleMarker = "<!-- devz:branch-rule -->"

// branchRuleSection is the price of sharing one repo memory between every
// worktree of a repo: what is saved has to be true on all of them.
const branchRuleSection = `## What not to save

` + branchRuleMarker + `
Save a memory only if it holds whatever branch is checked out. Every worktree
of a repository shares one repo memory, so a fact that is true on one branch
only, such as "the locations route is half migrated", loads in all the others,
where it is false. In-progress and branch-specific state goes in the plan file,
the pull request or the task, not in memory.
`

func hostClaudeTemplate(l memoryLayout) string {
	mem := tildePath(filepath.Join(l.HostDir, "memory"))
	return fmt.Sprintf(`# %[1]s

Shared context for every repository on %[1]s, loaded through each org's
CLAUDE.md. Created by `+"`devz claude memory init`"+`; edit it freely.

## Memory layers

Save each memory at the narrowest layer where it is always true.

| Layer | Directory | Loaded by |
|---|---|---|
| Host: every repo on %[1]s | `+"`%[2]s/`"+` | this file |
| Org: every repo in one org | `+"`%[3]s/<org>/memory/`"+` | the org CLAUDE.md |
| Repo: one repository | your auto memory directory | auto memory |

Auto memory saves to the repo layer on its own. For the host or org layer,
write the memory file in that directory and add its one-line pointer to the
MEMORY.md there. When a repo memory turns out to hold for other repos too,
move it up a layer and move its index line with it.

`+branchRuleSection+`
`+findingMemory(l)+`
Host memory index (`+"`%[2]s/MEMORY.md`"+`):

@%[2]s/MEMORY.md
`, l.Host, mem, tildePath(l.HostDir))
}

func orgClaudeTemplate(l memoryLayout) string {
	loads := "The line above loads this org's memory index"
	if l.HostLayer {
		loads = "The two lines above load the host's shared context and this org's memory index"
	}
	body := orgImports(l) + fmt.Sprintf(`
# %[1]s

Shared guidance for every repository in %[2]s/%[1]s. Each repository's own
CLAUDE.md still governs its code. %[5]s
(`+"`%[3]s/`"+`).

## Plans

Write plans to `+"`%[4]s/`"+`, never to a repository's docs/.
`, l.Org, l.Host, tildePath(filepath.Join(l.OrgDir, "memory")), tildePath(l.plans()), loads)
	if !l.HostLayer {
		// With no host file above it, the org file is the widest shared one,
		// so it carries what the host file would.
		body += "\n" + branchRuleSection + "\n" + findingMemory(l)
	}
	return body
}
