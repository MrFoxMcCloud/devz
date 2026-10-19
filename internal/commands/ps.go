package commands

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

// psUsage is the help for `devz claude ps`.
const psUsage = `usage: devz claude ps

Lists the Claude Code sessions running on this machine: pid, the account each
runs on, its email and its working directory, one per line. The account comes
from the process's CLAUDE_CONFIG_DIR; unset means the default account.

Linux only (it reads /proc). Processes it cannot read are skipped.`

func runClaudePs(ctx *cli.Context, args []string) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(ctx.Stdout, psUsage)
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("ps takes no arguments")
	}
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s", mustConfigPath())
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintf(ctx.Stdout, "devz claude ps is not supported on %s: it reads /proc, which only Linux has\n", runtime.GOOS)
		return nil
	}
	return claudePs(ctx.Stdout, "/proc", claudeAccounts(ctx.Config))
}

// claudeSession is one running Claude Code process.
type claudeSession struct {
	pid       int
	configDir string // CLAUDE_CONFIG_DIR, "" when unset
	cwd       string
}

// isClaudeCmdline reports whether argv is Claude Code: a binary named claude,
// or an interpreter running a script named claude. It is deliberately narrow:
// a missed process is better than a wrong one.
func isClaudeCmdline(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := filepath.Base(argv[0])
	if base == "claude" {
		return true
	}
	if (base == "node" || base == "bun") && len(argv) > 1 {
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "-") {
				continue // interpreter flag
			}
			return filepath.Base(a) == "claude"
		}
	}
	return false
}

// findClaudeSessions scans a /proc-shaped tree. A process that vanishes or
// cannot be read is skipped, never an error.
func findClaudeSessions(procRoot string) []claudeSession {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []claudeSession
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		if !isClaudeCmdline(argv) {
			continue
		}
		env, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(dir, "cwd"))
		if err != nil {
			continue
		}
		s := claudeSession{pid: pid, cwd: cwd}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := strings.CutPrefix(string(kv), "CLAUDE_CONFIG_DIR="); ok {
				s.configDir = v
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// claudePs prints the sessions under procRoot with the account each runs on.
func claudePs(w io.Writer, procRoot string, known []claudeAccount) error {
	sessions := findClaudeSessions(procRoot)
	if len(sessions) == 0 {
		fmt.Fprintln(w, "no Claude Code sessions running")
		return nil
	}
	for _, s := range sessions {
		alias, email := "?", "-"
		for _, a := range known {
			match := s.configDir == "" && a.Default ||
				s.configDir != "" && filepath.Clean(s.configDir) == filepath.Clean(a.ConfigDir)
			if match {
				alias, email = a.Alias, a.Email
				break
			}
		}
		if alias == "?" && s.configDir != "" {
			alias = "?(" + tildePath(s.configDir) + ")"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.pid, alias, email, s.cwd)
	}
	return nil
}
