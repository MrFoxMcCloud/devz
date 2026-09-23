# devz

Local development-environment commands, as one binary.

`devz` wraps **our own glue** — the secret unlock, the per-repo Claude account,
the machine checks — not other people's tools. There is no `devz kubectl`,
`devz gh` or `devz terraform`: those are better documented and better completed
than anything we'd put in front of them, and wrapping them means shadowing flags
and lagging releases.

```
$ devz
devz v0.1.0 -- local development environment commands

usage: devz <command> [args]

commands:
  doctor      check this machine's development environment
  secrets     unlock and inspect the local secret store
  account     show or set the Claude Code account for this repo
  config      show or initialize this machine's devz config
  completion  emit a shell completion script (zsh|bash)
  version     print the devz version
```

## Install

```sh
go install github.com/MrFoxMcCloud/devz@v1     # needs $(go env GOPATH)/bin on PATH
```

`@v1` means "the newest 1.x.y". Pin tighter with `@v1.2` (newest 1.2.x) or
`@v1.2.3`. `@latest` also stays on v1: a v2 would live at a different module
path (`.../devz/v2`), so no install jumps a major version by accident.

or grab a binary from [releases](https://github.com/MrFoxMcCloud/devz/releases),
or build from source with `make build`.

Then set up completion — which is most of the point:

```sh
devz completion zsh > ~/.local/share/zsh/site-functions/_devz   # any dir on $fpath
exec zsh
```

## First run

```sh
devz config init     # writes ~/.config/devz/config.json
devz doctor          # check the machine, with a fix printed for anything off
```

`devz doctor` exits non-zero if a check fails, so it works in a shell script or
a CI step.

## Configuration

**The binary holds behavior; the config holds anything that differs between
machines** — paths, key ids, which checks apply. That split is what lets one
build serve everyone without a fork per person.

`~/.config/devz/config.json` (override with `$DEVZ_CONFIG`):

```json
{
  "profile": "laptop",
  "secrets": {
    "backend": "pass",
    "store": "~/.password-store",
    "entries": ["team/api-token"],
    "unlockEntry": "team/api-token",
    "gpgKey": "...",
    "envVars": { "team/api-token": "TEAM_API_TOKEN" }
  },
  "claude": { "enabled": false, "sharedDir": "~/.claude-shared" },
  "doctor": { "requiredTools": ["git", "gh", "uv"], "skip": [] }
}
```

Every `doctor` check has an id (the left column of its output). Put an id in
`doctor.skip` to turn it off on a machine where it does not apply — that is how
someone on a different OS silences a check instead of forking the tool.

A malformed config is an error, never a silent fallback to defaults: running
with different settings than the file says is the failure worth preventing.

## Commands

### `devz doctor`

Every check that applies to this machine, with the fix for anything off.
`--quiet` prints only problems. Notable checks:

| id | what it catches |
|---|---|
| `python` | `python3` resolving to a conda base env, which silently owns every `#!/usr/bin/env python3` shebang on the machine |
| `gpg:tty` | `GPG_TTY` unset — the usual reason a passphrase prompt never appears |
| `gpg:cache` | a cold agent cache, which surfaces as unrelated-looking startup failures in tools that have no TTY |
| `pass:entries` | configured secrets missing from the store (checked on disk, so doctor never triggers a prompt of its own) |
| `pass:backup` | a store with no git remote: one copy, one disk |
| `gh:auth` | which GitHub account is actually active |

### `devz secrets`

```sh
devz secrets cached         # exit 0 if the cache is warm, 1 if cold; no output
devz secrets env [names]    # export lines for secrets.envVars, for eval
devz secrets unlock         # warm the gpg-agent cache
devz secrets status         # cache warmth + which entries exist
devz secrets list           # the entries this machine expects
devz secrets show <entry>   # delegates to pass
devz secrets edit <entry>   # rotate; delegates to pass
```

`env` replaces exporting tokens from a shell rc file. `secrets.envVars` maps a
store entry to the variable it fills, and the value is read from the store each
time instead of sitting in plaintext on disk:

```sh
devz secrets cached && eval "$(devz secrets env)"   # skip quietly on a cold cache
```

`unlock` exists because processes spawned **without a TTY** — MCP servers,
editor extensions — cannot show a passphrase prompt, so they fail at startup on
a cold cache. Warming it once from a terminal is the fix.

### `devz account`

Passthrough to `claude-account`, which owns the per-repo `.claude-account`
marker. Not reimplemented — two implementations of one rule is how they drift.
Needs `claude.enabled` in the config.

## Plugins

Any executable named `devz-<name>` on your PATH is reachable as `devz <name>`:

```sh
$ cat ~/bin/devz-tunnel
#!/usr/bin/env bash
# devz: open the staging tunnel
...

$ devz tunnel
```

The `# devz:` comment in the first few lines becomes its description in
`devz` output; `devz help <name>` calls the plugin with `--help`.

This is the escape hatch that keeps the shared binary shared: personal or
experimental workflows live as scripts and are reachable the same way, so
nobody's one-off has to become a pull request. Completion picks them up with no
regeneration, because the completion script asks the binary for its command list
at completion time.

## Development

```sh
make build                  # ./devz, version stamped from git describe
make run ARGS="doctor"      # build and run the dev binary from the repo
make vet test fmt
make completions            # regenerate the checked-in completion scripts
goreleaser release --snapshot --clean    # dry-run a release
```

The installed `devz` is always a release; the one you are working on is
`./devz` in the repo. `make install` refuses anything but a clean checkout of a
release tag, so a work-in-progress build never becomes the devz your shell and
scripts call. If a change touches the config format, point the dev build at its
own file: `DEVZ_CONFIG=./dev-config.json ./devz doctor`.

### Versioning

[Semantic versioning](https://semver.org). For a CLI, the compatibility surface
is the commands, flags, exit codes, config format and any output that scripts
parse:

| bump | when | example |
|---|---|---|
| major | something that works today stops working | removing a command or flag, a config change that makes existing files fail to load |
| minor | something new, old usage unaffected | a new subcommand, doctor check or optional config field |
| patch | fixes | a wrong fix hint, a doc correction |

Release: `git tag v1.2.3 && git push origin v1.2.3`. CI runs goreleaser and
publishes linux/darwin × amd64/arm64 archives.

A major version needs more than a tag. Go requires the module path to end in
`/v2` (in `go.mod` and every internal import) before `v2.x.y` tags install.
While v2 is in progress, cut `release/v1` from the last v1 tag and ship v1
fixes from there. Tag v2 work as prereleases (`v2.0.0-alpha.1`), which
`@latest` ignores.

Zero third-party dependencies, deliberately — it builds offline and instantly,
and a tool people are told to install should not be a supply-chain question.
