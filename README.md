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
  "claude": {
    "enabled": false,
    "sharedDir": "~/.claude-shared",
    "memory": {
      "hosts": ["git.example.com"],
      "roots": ["~/src/work"]
    }
  },
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
| `claude:memory` | a repo on a `claude.memory.hosts` forge whose Claude memory is not shared yet |
| `gh:auth` | which GitHub account is actually active |
| `devz:build` | a work-in-progress devz installed on PATH instead of a release |

### `devz secrets`

```sh
devz secrets cached         # exit 0 if the cache is warm, 1 if cold; no output
devz secrets env [names]    # export lines for secrets.envVars, for eval
devz secrets exec [names] -- <cmd> [args]  # run cmd with those secrets in its env
devz secrets unlock         # warm the gpg-agent cache
devz secrets status         # cache warmth + which entries exist
devz secrets list           # the entries this machine expects
devz secrets show <entry>   # delegates to pass
devz secrets rotate <entry> # change the value; delegates to pass edit
devz secrets add <entry> [--env NAME]  # pass insert, then record it in the config
devz secrets map            # which variable each entry is exported as
devz secrets map <entry> NAME      # set or change that variable
devz secrets map <entry> --clear   # stop exporting the entry
```

`env` replaces exporting tokens from a shell rc file. `secrets.envVars` maps a
store entry to the variable it fills, and the value is read from the store each
time instead of sitting in plaintext on disk:

```sh
devz secrets cached && eval "$(devz secrets env)"   # skip quietly on a cold cache
```

`exec` is for launchers, such as an MCP server wrapper that Claude spawns
without a TTY. The values go straight into the command's environment, never
through stdout or `eval`, and devz is replaced by the command, so stdio,
signals and the exit status pass through. Unlike `env`, every name given must
be in `secrets.envVars`:

```sh
exec devz secrets exec CRM_API_TOKEN -- uvx dasnuve-crm
```

`add` stores a new secret with `pass insert` and appends it to
`secrets.entries`, so `status` and `doctor` start tracking it. With
`--env NAME` it also maps the entry to `NAME` in `secrets.envVars`, so the next
`devz secrets env` exports it. `add` refuses an entry already in the store
(change its value with `rotate`). Pipe the value in to skip the prompt:
`echo "$TOKEN" | devz secrets add team/api-token --env TEAM_API_TOKEN`.

`map` is for the secret that is already stored but exported under the wrong
name, or not at all, such as one added without `--env`. It changes only
`secrets.envVars` in the config: the secret is not read, so it works on a cold
cache. It refuses an entry the store does not hold, and a variable name another
entry already uses. An entry put there with plain `pass insert` is added to
`secrets.entries` as well. With no arguments it lists every entry and its
variable.

`rotate` changes a secret's value. `edit` is the older name for it and still
works.

`unlock` exists because processes spawned **without a TTY** — MCP servers,
editor extensions — cannot show a passphrase prompt, so they fail at startup on
a cold cache. Warming it once from a terminal is the fix.

### `devz account`

Passthrough to `claude-account`, which owns the per-repo `.claude-account`
marker. Not reimplemented — two implementations of one rule is how they drift.
Needs `claude.enabled` in the config.

### `devz memory`

```sh
devz memory [status] [DIR]           # which layers this repo loads, and whether it is set up
devz memory init [DIR]               # set up the repo containing DIR (default: here)
devz memory init --all [--dry-run]   # every checkout under claude.memory.roots
```

Claude Code keys auto memory on the config dir *and* the checkout path, so two
accounts and three clones of one repo make six separate memories. `devz memory`
keys it on the repo's origin URL instead, and adds two shared layers above it:

```
<store>/<host>/CLAUDE.md, memory/           company: every repo on the host
<store>/<host>/<org>/CLAUDE.md, memory/     org: every repo in the org
<store>/<host>/<org>/plans/                 plans, outside every repo
<store>/<host>/<org>/repos/<repo>/memory/   repo: that repo's auto memory
```

`<store>` is `claude.memory.store`, by default `<claude.sharedDir>/orgs`.

For one repo, `init`:

- writes `autoMemoryDirectory`, `plansDirectory` and permission to edit the
  store into the checkout's `.claude/settings.local.json`, keeping anything
  else there, and adds that file to your global git ignore;
- links the org's `CLAUDE.md` and `plans/` into the directory above the repo,
  when the checkout sits at `<root>/<org>/<repo>`. Claude Code loads a parent
  directory's `CLAUDE.md`, and the org one imports the org and company memory
  indexes. So in `infra` you get company + org + infra memory, and never
  `backend`'s;
- creates missing store files from templates. A hand-written org `CLAUDE.md`
  already in the org folder is moved into the store and linked back.

Only repos whose origin host is in `claude.memory.hosts` are touched. `init`
never overwrites a file it did not create, never links into a search root
itself, and is safe to rerun. Restart Claude sessions afterwards: settings are
read at startup.

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
make build           # ./devz, version stamped from git describe
make install         # the working tree as ./devz; the devz on PATH is untouched
make vet test fmt
make completions     # regenerate the checked-in completion scripts
goreleaser release --snapshot --clean    # dry-run a release
```

`make install` puts the working tree at `./devz` in the checkout and never
touches the devz on PATH. That one is the release, and launchers such as the
MCP server wrappers call it, so work in progress must not replace it. Try a
change by path:

```sh
make install
./devz version                                  # "..., dev build"
~/src/devz/devz memory                          # from another repo, where per-repo commands look
```

Completion calls `devz` by name, so it keeps completing the release until a new
one is tagged and installed with `go install github.com/MrFoxMcCloud/devz@v1`.
`devz doctor` warns (`devz:build`) when the binary running it is a dev build.
`make install BIN=devz2` names the output `./devz2`; never `devz-something`,
which would be picked up as a plugin. If a change touches the config format,
give the dev build its own file with `DEVZ_CONFIG=./dev-config.json`.

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

## License

[MIT](LICENSE)
