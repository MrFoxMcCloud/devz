# CLAUDE.md

devz is a single Go binary that dispatches local dev-environment commands
(`doctor`, `secrets`, `claude`, `config`, `completion`, `version`), plus any
`devz-<name>` executable on PATH as a plugin. See README.md for user-facing docs.

## Workflow rules

- Use the `gh` CLI for all GitHub operations: PRs, issues, releases, CI runs,
  API calls. Don't use the web UI or raw `curl` against the GitHub API.
- Never put the Claude noreply email (`noreply@anthropic.com`) in a commit
  message or PR description. That means no `Co-Authored-By: Claude ...` trailer
  on commits. PR descriptions may end with the
  `🤖 Generated with [Claude Code](https://claude.com/claude-code)` footer, just
  without the email.
- Commit messages follow the existing style: `area: imperative summary`
  (e.g. `secrets: add devz secrets cached for shell guards`), then a body
  explaining *why*.

## Commands

```sh
make build        # ./devz, version stamped from git describe
make vet test fmt # CI also runs these and fails if gofmt -l prints anything
make completions  # regenerate completions/ from the built binary
```

Run `make fmt vet test build` before calling a change done.

## Layout

- `main.go`: registers the built-in commands; `version` is set with `-ldflags`.
- `internal/cli/`: the dispatcher: the command registry, `devz-*` plugin
  discovery, help, and "did you mean". `cli.ErrSilent` exits 1 with no output.
- `internal/commands/`: one file per built-in command. Each returns a
  `*cli.Command`. `check.go` holds the shared helpers for doctor results
  (`ok/warn/fail/skip`, `look`, `output`).
- `internal/config/`: machine-local config at `~/.config/devz/config.json`
  (`$DEVZ_CONFIG` overrides it).

## Design constraints (don't break these)

- **Zero third-party dependencies.** Standard library only. Don't add modules
  to go.mod.
- **Don't wrap other people's tools.** No `devz kubectl`/`gh`/`terraform`.
  devz is for our own glue. When a tool already owns something, as `pass`
  owns the secret store, shell out to it instead of reimplementing it.
- **Behavior goes in the binary; anything that differs per machine goes in the
  config** (paths, key ids, which checks apply). New per-machine knobs go into
  `config.Config` with a doc comment and a sensible value in `Default()`.
- **A malformed config is an error**, never a silent fallback to defaults.
- **Personal or one-off workflows should be `devz-<name>` plugins**, not new
  built-ins.
- Doctor checks each have a stable id (users list ids in `doctor.skip`), give a
  concrete fix for every warn or fail, and must never trigger a passphrase
  prompt. Check on disk or check the agent cache instead of reading secrets.

## Gotchas

- The completion scripts hold no lists. Each command's `Complete` func
  returns the candidates for the next word, and the scripts ask for them with
  `devz completion --complete <words>`. When you add a subcommand, add it to
  that command's `Complete`. Only touch the scripts in `completion.go` for a
  change to the scripts themselves, then run `make completions`; every
  installed copy then shows as stale in `devz doctor` until regenerated.
- Renaming a command: keep the old name registered with `Deprecated` set to the
  new one and `Hidden: true`. The dispatcher logs each use
  (`internal/cli/deprecated.go`). For a renamed subcommand, call
  `cli.NoteDeprecated` where the old spelling is handled, as `secrets edit`
  does. Never remove an old name inside v1.
- `claude` is a `Group`: an unknown `devz claude <sub>` runs
  `devz-claude-<sub>` from PATH. When you compile such a plugin in, add it to
  `claudeBuiltins` so `devz doctor` flags the script left behind.
- **The Claude account rule has one definition:** the comment and code at the
  top of `internal/commands/accounts.go`. It used to be a script outside this
  repo; it was ported behind a comparison of both over every checkout and
  worktree on a real machine. `devz claude exec` runs on every Claude Code
  launch, in the editor and the shell, so a bug there stops every session or,
  worse, starts one on the wrong account. Keep it failing closed: a marker
  that does not resolve is exit status 3 and nothing runs. Never fall back to
  the default account. The default account means CLAUDE_CONFIG_DIR *unset*,
  never set to `~/.claude`.
- A command that hands off to another program returns `cli.ExitError` with
  the child's exit status, so the status survives and nothing is printed
  twice.
- When you add a subcommand or config field, also update the command's `Usage`
  text and README.md.
- `memory` resolves a linked worktree to its main checkout
  (`resolveMemoryRepo`), because Claude Code reads the main checkout's
  `.claude/settings.local.json` in every worktree. Never write a settings file
  into a worktree. The permission rules it writes are in `memoryLayout.guard`;
  `ask` rules there were tested to hold in accept-edits mode, and `*` to match
  one path segment.
- `memory` tests build real git repos in `t.TempDir()`. They set `HOME`,
  `XDG_CONFIG_HOME` and `GIT_CONFIG_GLOBAL` there (see `memoryEnv`) so they
  never touch the real global git ignore or `~/.claude-shared`.
- Tests use only the standard `testing` package. Point `DEVZ_CONFIG` at
  `t.TempDir()` so a test never reads the real config. Tests must not call
  out to `pass` or `gpg`.
- Releases: push a `vX.Y.Z` tag, and goreleaser builds linux/darwin ×
  amd64/arm64 in CI. Semver rules are in README.md under Versioning. A breaking
  change to commands, flags, exit codes or the config format is a major bump.
- `make install` builds the working tree to `./devz` in the checkout, never
  onto PATH: the devz on PATH is the release that MCP launchers call. Run dev
  builds as `./devz` or by full path. Never `cp` or `go build -o` a binary onto
  PATH; releases get there only through `go install ...@v1`. `devz doctor`'s
  `devz:build` check and `devz version` flag a dev build; `isRelease` in
  `version.go` decides, and its tests list the version shapes it must reject.
  Never name a dev binary `devz-*`: that makes it a plugin.
