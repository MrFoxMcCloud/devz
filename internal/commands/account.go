package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Account delegates to claude-account, the existing per-repo account tool.
//
// It is a passthrough rather than a reimplementation: claude-account already
// owns the marker rule, and two implementations of one rule is how they drift.
func Account() *cli.Command {
	return &cli.Command{
		Name:  "account",
		Short: "show or set the Claude Code account for this repo",
		Usage: `usage: devz account [args...]

Passes through to claude-account, which owns the per-repo .claude-account
marker. With no arguments it reports which account the current directory uses.

  devz account              which account this directory uses, and why
  devz account --select     pick from the accounts logged in on this machine
  devz account --list       accounts available here

Requires claude.enabled and claude.sharedDir in the config.`,
		Run: runAccount,
	}
}

func runAccount(ctx *cli.Context, args []string) error {
	if !ctx.Config.Claude.Enabled {
		return fmt.Errorf("claude support is disabled; set claude.enabled=true in %s",
			mustConfigPath())
	}

	bin := "claude-account"
	if shared := config.Expand(ctx.Config.Claude.SharedDir); shared != "" {
		candidate := filepath.Join(shared, "bin", "claude-account")
		if exists(candidate) {
			bin = candidate
		}
	}
	if _, err := exec.LookPath(bin); err != nil && !exists(bin) {
		return fmt.Errorf("claude-account not found (looked in claude.sharedDir and PATH)")
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func mustConfigPath() string {
	path, err := config.Path()
	if err != nil {
		return "the devz config"
	}
	return path
}
