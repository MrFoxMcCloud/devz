package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// An old command name keeps working after it is renamed, because removing it
// would be a breaking change. What tells us when it can finally go is whether
// anything still calls it -- including the scripts and launchers that never
// see a notice. So every use is written to a small log that `devz doctor`
// reads back.

// DeprecatedLogPath is where uses of old names are recorded, one line each.
// It honors XDG_STATE_HOME. The scripts devz replaces append the same lines.
func DeprecatedLogPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "devz", "deprecated.log")
}

// NoteDeprecated records that old was used where replacement is the current
// name, and says so on a terminal. It never fails the command it is noting:
// a log that cannot be written is not a reason to refuse to run.
func NoteDeprecated(ctx *Context, old, replacement string) {
	// A script that was itself called by an old name has already noted that,
	// and reaches devz with DEVZ_VIA set. One use, one line.
	if os.Getenv("DEVZ_VIA") != "" {
		return
	}
	// Only to a terminal: a script that captures stderr should not find a
	// notice in what it captured.
	if f, ok := ctx.Stderr.(*os.File); ok && isTerminal(f) {
		fmt.Fprintf(ctx.Stderr, "devz: '%s' is now '%s'; the old name still works\n", old, replacement)
	}
	path := DeprecatedLogPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	cwd, _ := os.Getwd()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), old, replacement, cwd)
}

// DeprecatedUse summarizes the logged uses of one old name.
type DeprecatedUse struct {
	Old, Replacement string
	Count            int
	Last             time.Time
}

// DeprecatedUses reads the log and returns the old names used since the
// given time, most recently used first. A missing log means none.
func DeprecatedUses(since time.Time) []DeprecatedUse {
	f, err := os.Open(DeprecatedLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()

	byName := map[string]*DeprecatedUse{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 3 {
			continue
		}
		when, err := time.Parse(time.RFC3339, fields[0])
		if err != nil || when.Before(since) {
			continue
		}
		use := byName[fields[1]]
		if use == nil {
			use = &DeprecatedUse{Old: fields[1]}
			byName[fields[1]] = use
		}
		use.Count++
		if when.After(use.Last) {
			use.Last, use.Replacement = when, fields[2]
		}
	}
	out := make([]DeprecatedUse, 0, len(byName))
	for _, use := range byName {
		out = append(out, *use)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Old < out[j].Old
	})
	return out
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
