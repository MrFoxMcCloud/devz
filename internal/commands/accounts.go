package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/config"
)

// The account rule. This file is the one definition of it.
//
// Each Claude Code login lives in its own config dir: ~/.claude, the default,
// and ~/.claude-<name>. Claude Code picks one through CLAUDE_CONFIG_DIR. Which
// one a directory gets is decided per repo, by a `.claude-account` file that
// holds one word, searched for in this order:
//
//  1. From the directory upward, stopping at and including the git top level.
//     In a linked worktree the top level is the worktree's own root.
//  2. In a linked worktree, when step 1 found nothing: the root of the repo's
//     main checkout. This makes the account a property of the repo. Set it
//     once and every worktree follows, wherever the worktree sits.
//  3. No marker: the default account.
//
// A marker inside one worktree therefore overrides the repo's, which is how
// one repo can be worked on two accounts at once.
//
// A repo never inherits an account from the tree it sits in: the search stops
// at the repo root. Outside a repo only the directory itself is consulted.
//
// The word is an account email, an unambiguous prefix of one, or an alias:
// `personal` for the default config dir, and whatever follows `.claude-` for
// the others. A marker naming an account that no config dir is logged into is
// an error, not a fallback to the default. Being quietly on the wrong account
// is the one failure all of this exists to prevent.
//
// Accounts are discovered, not configured: each config dir reports the email
// it is logged into, so logging into another account makes it usable with
// nothing to edit.

// accountMarker is the file that names a repo's account.
const accountMarker = ".claude-account"

// exitUnresolved is the exit status for "the account named is not logged in
// here". Launchers test for it, so it is part of the interface.
const exitUnresolved = 3

// claudeAccount is one logged-in account.
type claudeAccount struct {
	Email, Alias, Org string
	// ConfigDir is the account's config dir.
	ConfigDir string
	// Default is the account in ~/.claude. It is reached by leaving
	// CLAUDE_CONFIG_DIR unset: with the variable set to ~/.claude, Claude Code
	// keeps its state at ~/.claude/.claude.json instead of ~/.claude.json,
	// which is a different, empty login.
	Default bool
}

// configDirEnv is the value CLAUDE_CONFIG_DIR must have for this account, with
// "" meaning unset.
func (a claudeAccount) configDirEnv() string {
	if a.Default {
		return ""
	}
	return a.ConfigDir
}

// unresolvedError is a marker, or a name given on the command line, that no
// logged-in account answers to.
type unresolvedError struct{ msg string }

func (e unresolvedError) Error() string { return e.msg }

// claudeAccounts lists every logged-in account, read from each config dir's
// own metadata. The default dir comes first.
func claudeAccounts(cfg config.Config) []claudeAccount {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	type candidate struct {
		alias, dir, json string
		isDefault        bool
	}
	// The default dir keeps its JSON outside itself, at ~/.claude.json; a
	// CLAUDE_CONFIG_DIR one keeps it inside.
	candidates := []candidate{{"personal", filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), true}}

	shared := config.Expand(cfg.Claude.SharedDir)
	entries, _ := os.ReadDir(home) // sorted by name
	for _, e := range entries {
		alias, ok := strings.CutPrefix(e.Name(), ".claude-")
		dir := filepath.Join(home, e.Name())
		if !ok || alias == "" || e.Name() == ".claude-shared" || dir == shared {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		candidates = append(candidates, candidate{alias, dir, filepath.Join(dir, ".claude.json"), false})
	}

	var found []claudeAccount
	for _, c := range candidates {
		data, err := os.ReadFile(c.json)
		if err != nil {
			continue
		}
		var state struct {
			OAuth struct {
				Email string `json:"emailAddress"`
				Org   string `json:"organizationName"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(data, &state) != nil || state.OAuth.Email == "" {
			continue
		}
		found = append(found, claudeAccount{
			Email: state.OAuth.Email, Alias: c.alias, Org: state.OAuth.Org,
			ConfigDir: c.dir, Default: c.isDefault,
		})
	}
	return found
}

// findAccountMarker returns the marker that decides start's account, or "".
func findAccountMarker(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	isMarker := func(d string) bool {
		info, err := os.Stat(filepath.Join(d, accountMarker))
		return err == nil && info.Mode().IsRegular()
	}

	top, err := output("git", "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		// Not in a repo: this directory only.
		if isMarker(dir) {
			return filepath.Join(dir, accountMarker)
		}
		return ""
	}
	for d := dir; ; d = filepath.Dir(d) {
		if isMarker(d) {
			return filepath.Join(d, accountMarker)
		}
		if d == top || d == filepath.Dir(d) {
			break
		}
	}
	if main := mainCheckout(top); main != "" && main != top && isMarker(main) {
		return filepath.Join(main, accountMarker)
	}
	return ""
}

// readAccountMarker returns the first word in a marker file.
func readAccountMarker(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if words := strings.Fields(string(data)); len(words) > 0 {
		return words[0]
	}
	return ""
}

// matchAccount resolves a marker word, or a name typed by a person, to one
// account: an exact email or alias, else an unambiguous prefix of an email.
func matchAccount(token string, known []claudeAccount) (claudeAccount, error) {
	for _, a := range known {
		if token == a.Email || token == a.Alias {
			return a, nil
		}
	}
	var hits []claudeAccount
	for _, a := range known {
		if strings.HasPrefix(a.Email, token) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return claudeAccount{}, unresolvedError{fmt.Sprintf(
			"no account matches '%s': no config dir on this machine is logged into it.\nrun: devz claude account list", token)}
	}
	emails := make([]string, len(hits))
	for i, h := range hits {
		emails[i] = h.Email
	}
	return claudeAccount{}, unresolvedError{fmt.Sprintf("'%s' is ambiguous: it matches %s", token, strings.Join(emails, ", "))}
}

// resolveAccount applies the rule to dir. marker is the file that decided
// it, or "" when the default account applies.
func resolveAccount(cfg config.Config, dir string) (acct claudeAccount, marker string, err error) {
	return resolveAmong(claudeAccounts(cfg), dir)
}

// resolveAmong is resolveAccount for a caller that already has the accounts,
// such as a sweep over many directories.
func resolveAmong(known []claudeAccount, dir string) (acct claudeAccount, marker string, err error) {
	if len(known) == 0 {
		return claudeAccount{}, "", unresolvedError{"no config dir is logged in: run `claude` and /login"}
	}
	marker = findAccountMarker(dir)
	if marker == "" {
		return known[0], "", nil
	}
	acct, err = matchAccount(readAccountMarker(marker), known)
	if err != nil {
		var unresolved unresolvedError
		if errors.As(err, &unresolved) {
			err = unresolvedError{unresolved.msg + "\nmarker: " + marker}
		}
	}
	return acct, marker, err
}

// accountMarkerRoot is where a new marker goes for dir: the root of the
// repo's main checkout, so one marker covers every worktree, or dir itself
// outside a repo.
func accountMarkerRoot(dir string) string {
	if main := mainCheckout(dir); main != "" {
		return main
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}
