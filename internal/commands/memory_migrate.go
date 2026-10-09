package commands

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// Before 1.7 the store was <sharedDir>/orgs/<host>/<org>/repos/<repo>, with
// org and repo in whatever case the origin URL used. It now mirrors the URL:
// <sharedDir>/hosts/<host>/<org>/<repo>, in lower case.
//
// Sessions that are running, and settings files not yet rewritten, still
// name the old paths. So a migration leaves a link behind at every path it
// moves, and --finish removes the links once no settings file under
// claude.memory.roots points through one.

// memoryMigrate moves a store to the current layout.
func memoryMigrate(ctx *cli.Context, args []string) error {
	dry, finish := false, false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dry = true
		case "--finish":
			finish = true
		default:
			return fmt.Errorf("unknown argument %q", a)
		}
	}
	if finish {
		return memoryMigrateFinish(ctx, dry)
	}

	cfg := ctx.Config
	verb := func(s string) string {
		if dry {
			return "would " + s
		}
		return s
	}
	say := func(format string, a ...any) { fmt.Fprintf(ctx.Stdout, format+"\n", a...) }

	store := cfg.Claude.StoreDir()
	if !legacyStore(cfg) {
		say("%s already has the current layout", tildePath(store))
		return nil
	}

	// 1. The store itself: orgs -> hosts, when it is at the old default.
	target := store
	legacyDir := cfg.Claude.LegacyStoreDir()
	if cfg.Claude.Memory.Store == "" && store == legacyDir {
		target = filepath.Join(filepath.Dir(legacyDir), "hosts")
		say("%s %s -> %s, leaving a link at the old path", verb("move"), tildePath(legacyDir), tildePath(target))
		if !dry {
			if err := os.Rename(legacyDir, target); err != nil {
				return err
			}
			if err := os.Symlink(filepath.Base(target), legacyDir); err != nil {
				return err
			}
		}
	}
	// In a dry run nothing has moved, so the walk reads the old place.
	walk := target
	if dry {
		walk = store
	}

	// 2. Each org: lower-case its name, and lift its repos out of repos/.
	for _, host := range realDirs(walk) {
		hostDir := filepath.Join(walk, host)
		for _, org := range realDirs(hostDir) {
			if org == "memory" {
				continue
			}
			orgDir := filepath.Join(hostDir, org)
			for _, repo := range realDirs(filepath.Join(orgDir, "repos")) {
				lower := strings.ToLower(repo)
				to := filepath.Join(orgDir, lower)
				if exists(to) {
					return fmt.Errorf("cannot move %s: %s is already there", tildePath(filepath.Join(orgDir, "repos", repo)), tildePath(to))
				}
				say("%s %s/%s/repos/%s -> %s/%s/%s", verb("move"), host, org, repo, host, strings.ToLower(org), lower)
				if !dry {
					from := filepath.Join(orgDir, "repos", repo)
					if err := os.Rename(from, to); err != nil {
						return err
					}
					if err := os.Symlink(filepath.Join("..", lower), from); err != nil {
						return err
					}
				}
			}
			if lower := strings.ToLower(org); lower != org {
				to := filepath.Join(hostDir, lower)
				if exists(to) {
					return fmt.Errorf("cannot rename %s: %s is already there", tildePath(orgDir), tildePath(to))
				}
				say("%s %s/%s -> %s/%s", verb("rename"), host, org, host, lower)
				if !dry {
					if err := os.Rename(orgDir, to); err != nil {
						return err
					}
					if err := os.Symlink(lower, orgDir); err != nil {
						return err
					}
				}
			}
		}
	}

	// 3. The store's own text: imports and pointers that spell out a path.
	rewritten, err := rewriteStorePaths(walk, legacyDir, target, dry)
	if err != nil {
		return err
	}
	for _, file := range rewritten {
		say("%s old paths in %s", verb("rewrite"), tildePath(file))
	}

	// 4. Every checkout: settings, permission rules and org links.
	if dry {
		say("would then update each checkout under claude.memory.roots, as 'devz claude memory init --all' does")
		return nil
	}
	say("")
	fresh := *ctx
	if err := memoryInitAll(&fresh, false); err != nil {
		return err
	}
	say("\nold paths still resolve through links. Restart Claude sessions, then run:\n  devz claude memory migrate --finish")
	return nil
}

// storePathPattern matches a store path that still has the repos/ level.
func storePathPattern(store string) *regexp.Regexp {
	forms := []string{regexp.QuoteMeta(store), regexp.QuoteMeta(tildePath(store))}
	return regexp.MustCompile(`((?:` + strings.Join(forms, "|") + `)/[^/\s]+/[^/\s]+/)repos/`)
}

// rewriteStorePaths updates every Markdown file in the store that spells out
// a path into the old layout, and returns the files it changed. It replaces
// exact path prefixes only; nothing else in a file is touched.
func rewriteStorePaths(walk, legacyDir, target string, dry bool) ([]string, error) {
	var changed []string
	replace := func(body []byte) []byte {
		for _, pair := range [][2]string{
			{legacyDir + "/", target + "/"},
			{tildePath(legacyDir) + "/", tildePath(target) + "/"},
		} {
			if pair[0] != pair[1] {
				body = bytes.ReplaceAll(body, []byte(pair[0]), []byte(pair[1]))
			}
		}
		return storePathPattern(target).ReplaceAll(body, []byte("$1"))
	}
	err := filepath.WalkDir(walk, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		updated := replace(body)
		if bytes.Equal(body, updated) {
			return nil
		}
		changed = append(changed, path)
		if dry {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(path, updated, info.Mode().Perm())
	})
	return changed, err
}

// compatLinks lists the links a migration left behind: the old store path,
// an org under its old capitalization, and each repos/<repo>.
func compatLinks(store, legacyDir string) []string {
	var links []string
	isLink := func(path string) bool {
		info, err := os.Lstat(path)
		return err == nil && info.Mode()&os.ModeSymlink != 0
	}
	if legacyDir != store && isLink(legacyDir) {
		links = append(links, legacyDir)
	}
	for _, host := range realDirs(store) {
		hostDir := filepath.Join(store, host)
		entries, _ := os.ReadDir(hostDir)
		for _, e := range entries {
			path := filepath.Join(hostDir, e.Name())
			if isLink(path) {
				links = append(links, path)
				continue
			}
			if !e.IsDir() {
				continue
			}
			repos, _ := os.ReadDir(filepath.Join(path, "repos"))
			for _, r := range repos {
				if link := filepath.Join(path, "repos", r.Name()); isLink(link) {
					links = append(links, link)
				}
			}
		}
	}
	return links
}

// settingsUsingOldPaths lists the checkouts under claude.memory.roots whose
// settings file still names a path only a compatibility link keeps alive.
func settingsUsingOldPaths(ctx *cli.Context) []string {
	cfg := ctx.Config
	var users []string
	seen := map[string]bool{}
	for _, root := range expandedRoots(cfg) {
		for _, top := range findCheckouts(root, 3) {
			l, err := resolveMemoryRepo(cfg, top)
			if err != nil || seen[l.Top] {
				continue
			}
			seen[l.Top] = true
			raw, err := os.ReadFile(l.settingsPath())
			if err != nil {
				continue
			}
			if _, changed, err := mergeMemorySettings(raw, l); err == nil && changed {
				users = append(users, l.Top)
			}
		}
	}
	return users
}

// memoryMigrateFinish removes the links a migration left, once nothing that
// devz can see still goes through them.
func memoryMigrateFinish(ctx *cli.Context, dry bool) error {
	cfg := ctx.Config
	store := cfg.Claude.StoreDir()
	if legacyStore(cfg) {
		return fmt.Errorf("%s has not been migrated yet; run 'devz claude memory migrate' first", tildePath(store))
	}
	links := compatLinks(store, cfg.Claude.LegacyStoreDir())
	if len(links) == 0 {
		fmt.Fprintln(ctx.Stdout, "no compatibility links left")
		return nil
	}
	if users := settingsUsingOldPaths(ctx); len(users) > 0 {
		for _, top := range users {
			fmt.Fprintf(ctx.Stdout, "still on old paths: %s\n", tildePath(top))
		}
		return fmt.Errorf("%d checkout(s) still point at the old paths; run 'devz claude memory init --all' first", len(users))
	}
	for _, link := range links {
		if dry {
			fmt.Fprintf(ctx.Stdout, "would remove %s\n", tildePath(link))
			continue
		}
		if err := os.Remove(link); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "removed %s\n", tildePath(link))
		// An org's repos/ directory that held only links is now empty.
		if dir := filepath.Dir(link); filepath.Base(dir) == "repos" {
			os.Remove(dir)
		}
	}
	if !dry {
		fmt.Fprintln(ctx.Stdout, "a Claude session started before the migration still uses the old paths: restart any that are still open")
	}
	return nil
}
