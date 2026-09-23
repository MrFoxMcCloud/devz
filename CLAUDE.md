# CLAUDE.md

devz is a single Go binary that dispatches local dev-environment commands
(`doctor`, `secrets`, `account`, `config`, `completion`, `version`), plus any
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
  devz is for our own glue. When a tool already owns something (`pass`,
  `claude-account`), shell out to it instead of reimplementing it.
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

- Subcommand lists are **hardcoded** in the zsh and bash scripts in
  `internal/commands/completion.go`. When you add a subcommand, update both
  scripts, then run `make completions` so the checked-in `completions/` files
  match.
- When you add a subcommand or config field, also update the command's `Usage`
  text and README.md.
- Tests use only the standard `testing` package. Point `DEVZ_CONFIG` at
  `t.TempDir()` so a test never reads the real config. Tests must not call
  out to `pass` or `gpg`.
- Releases: push a `vX.Y.Z` tag, and goreleaser builds linux/darwin ×
  amd64/arm64 in CI. Semver rules are in README.md under Versioning. A breaking
  change to commands, flags, exit codes or the config format is a major bump.
- Test dev builds with `make run ARGS=...` or `./devz`. Never copy a dev build
  onto PATH (`~/bin`, `~/go/bin`); the installed devz is always a release.
  `make install` refuses to run off a tag.
