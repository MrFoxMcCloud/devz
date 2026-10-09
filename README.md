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
  claude      Claude Code accounts and shared memory on this machine
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

Or source it from `~/.zshrc`, after `compinit`, and there is no file to keep:
`source <(devz completion zsh)`.

The script holds no lists. It asks the binary for the candidates each time, so
new commands, subcommands and plugins complete without regenerating it. When
the script itself changes, `go install` cannot refresh an installed file, since
it runs nothing once it has installed. `devz doctor` warns when the installed
script is not the one this build writes, and its fix line is the command to run.

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
`--quiet` prints only problems. `--all` also runs the per-repo Claude checks
over every checkout under `claude.memory.roots` and resolves the account of
each of their linked worktrees; it prints one line per problem, or a single
summary line. Notable checks:

| id | what it catches |
|---|---|
| `python` | `python3` resolving to a conda base env, which silently owns every `#!/usr/bin/env python3` shebang on the machine |
| `gpg:tty` | `GPG_TTY` unset — the usual reason a passphrase prompt never appears |
| `gpg:cache` | a cold agent cache, which surfaces as unrelated-looking startup failures in tools that have no TTY |
| `pass:entries` | configured secrets missing from the store (checked on disk, so doctor never triggers a prompt of its own) |
| `pass:backup` | a store with no git remote: one copy, one disk |
| `claude:accounts` | which Claude accounts are logged in on this machine |
| `claude:account` | a `.claude-account` marker that names an account nobody is logged into |
| `claude:launch` | VS Code or the shell starting Claude Code without applying the account rule, so every window or terminal lands on the default account |
| `claude:memory` | a repo on a `claude.memory.hosts` forge whose Claude memory is not shared yet |
| `claude:memory-guard` | a repo where Claude can still write company or org memory without being asked |
| `claude:memory-rule` | a host `CLAUDE.md`, or the org's where the host has no layer, without the branch rule |
| `claude:memory-layout` | a store still laid out as before 1.7; after migrating, how many old paths are still kept alive by links; and the old path reappearing as a real directory, which means a session resumed with that path in its history wrote there |
| `claude:memory-backup`, `claude:shared-backup` | the memory store, or `claude.sharedDir`, not being a git repo of its own, having no remote, or holding uncommitted or unpushed changes. No network call: "pushed" is against the upstream as last fetched |
| `gh:auth` | which GitHub account is actually active |
| `devz:build` | a work-in-progress devz installed on PATH instead of a release |
| `devz:deprecated` | which old command names were used in the last 30 days, and how often. Information, not a problem: see [Old names](#old-names) |
| `plugins` | the plugins on PATH, and any that can never run because a built-in command or subcommand has taken the name |
| `completion:zsh`, `completion:bash` | an installed completion script that an older devz wrote. `go install` runs nothing after installing, so new subcommands do not complete until the file is regenerated; the fix line is the command |

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

### `devz claude`

Everything devz does for Claude Code on a machine with more than one login:
which account a repo uses, and the memory and plans the accounts share. Needs
`claude.enabled` in the config.

```sh
devz claude                  # the subcommands, built in and plugin
devz claude account ...      # below
devz claude exec ...         # below
devz claude worktree ...     # below
devz claude memory ...       # below
devz claude <name> ...       # anything else: devz-claude-<name> on PATH
```

`claude` is a group. A subcommand it does not build in is looked up as
`devz-claude-<name>`, the same way `devz <name>` finds `devz-<name>`, so a piece
of the group can start as a script and be compiled in later under the same
name. A built-in subcommand always wins.

### `devz claude account`

```sh
devz claude account                       # which account this directory uses, and why
devz claude account set <email|alias>     # set the repo's account
devz claude account set <email> --root .  # in a worktree: that worktree alone
devz claude account pick                  # choose from the accounts logged in here
devz claude account list
devz claude account clear                 # drop the marker that applies here
devz claude account resolve [--config-dir|--alias|--info] [DIR]   # for scripts
```

Each Claude login lives in its own config dir: `~/.claude`, the default, and
`~/.claude-<name>`. Claude Code picks one through `CLAUDE_CONFIG_DIR`. Accounts
are discovered from those dirs, so logging into another one
(`CLAUDE_CONFIG_DIR=~/.claude-<name> claude`, then `/login`) makes it usable
with nothing to configure.

Which account a directory gets is decided per repo, by a `.claude-account` file
holding one word: an email, an unambiguous prefix of one, or an alias
(`personal` for the default dir, or what follows `.claude-`).

1. The marker in this worktree, searched from the directory up to the repo
   root and never above it.
2. In a linked worktree with none, the marker at the root of the repo's main
   checkout. Set the account once and every worktree follows, wherever the
   worktree sits.
3. No marker: the default account.

A marker inside one worktree overrides the repo's, which is how one repo can
be worked on two accounts at once. A marker naming an account nobody is logged
into is an error, exit status 3, never a quiet fallback to the default
account: being quietly on the wrong account is the failure this exists to
prevent.

`set` checks the name against the logged-in accounts before writing anything,
stores the canonical email, and adds `.claude-account` to your global git
ignore the first time. `resolve` prints one value for a script; with
`--config-dir` an empty line means the default account, which is reached by
unsetting the variable, not by pointing it at `~/.claude`.

### `devz claude exec`

```sh
devz claude exec [--keep-env] [--] <command> [args]
```

Runs a command, normally Claude Code, as the account the current directory's
repo uses: it sets `CLAUDE_CONFIG_DIR`, or unsets it for the default account,
and replaces itself with the command. If the marker does not resolve, nothing
runs and the exit status is 3.

This is the launch path, for the two places Claude Code is started from:

- **VS Code.** The extension's own account setting is machine-scoped and cannot
  vary per window. `claudeCode.claudeProcessWrapper` can point at a script, and
  the extension runs it in the workspace folder with the real binary as its
  arguments. That script is one line: `exec devz claude exec -- "$@"`.
- **A shell.** `devz claude shell-init zsh` prints a `claude` function for your
  rc file. It uses `--keep-env`, so a `CLAUDE_CONFIG_DIR` you set by hand still
  wins. Without the flag the marker always decides, which is right for an
  editor, whose windows inherit an environment that says nothing about the
  folder they have open.

devz prints the function and does not edit your rc file. `devz doctor`
(`claude:launch`) says whether both places apply the rule. `DEVZ_BIN` in the
printed function points one shell at another devz build.

### `devz claude worktree`

```sh
devz claude worktree add <branch> [<start-point>]   # .wt-<repo>-<topic> beside the main checkout
devz claude worktree add --name spike --account personal idea/new-thing
devz claude worktree list                           # each working tree, its branch and account
devz claude worktree rm <topic|branch|path>         # the branch is kept
```

Several branches of one repository at once, without a second clone. Run it
anywhere inside the repository.

`add` checks out a branch that exists locally, tracks one that exists only on
origin, and otherwise creates it from `<start-point>`, by default origin's
default branch. A new branch tracks nothing, so its first push cannot aim at
the branch it started from. The directory is named after the last part of the
branch; `--name` picks another.

A worktree needs no setup. It follows the repo's `.claude-account`, and Claude
Code reads the main checkout's `.claude/settings.local.json` in every worktree,
so memory and plans are already shared. `--account` puts one worktree on
another account, which is how one repo is worked on two logins at once.

It goes beside the main checkout because the folder above a checkout is where
the org's `CLAUDE.md` is linked, so a sibling loads the same shared context.
`rm` is `git worktree remove`: it refuses a worktree with uncommitted work
unless you pass `--force`.

### `devz claude memory`

```sh
devz claude memory [status] [DIR]           # which layers this repo loads, and whether it is set up
devz claude memory init [DIR]               # set up the repo containing DIR (default: here)
devz claude memory init --all [--dry-run]   # every checkout under claude.memory.roots
devz claude memory path [DIR | <host>/<org>[/<repo>]]   # where each layer lives
devz claude memory list                     # every host, org and repo in the store
devz claude memory migrate [--dry-run]      # move a pre-1.7 store to this layout
devz claude memory migrate --finish         # then remove the links it left
```

`path` prints `layer<TAB>directory` lines for the repo you are in, or for any
`<host>/<org>` or `<host>/<org>/<repo>` whether or not it is checked out here.
It is how a session finds another org's memory to read. `list` shows what the
store holds, with a count of memories for each.

**Migrating.** Before 1.7 the store was `<sharedDir>/orgs/<host>/<org>/repos/<repo>`,
in the URL's own case. That layout keeps working until you move it:

```sh
devz claude memory migrate --dry-run   # what would move
devz claude memory migrate             # move it, then update every checkout
devz claude memory migrate --finish    # later: remove the compatibility links
```

`migrate` renames `orgs` to `hosts`, lifts each repo out of `repos/`,
lower-cases names, rewrites paths spelled out in the store's Markdown files,
and reruns `init --all`, which drops rules and directories that point at the
old place. It leaves a link at every path it moves, because a Claude session
that is already running read its settings at startup and still uses the old
ones. `--finish` removes the links, and refuses while any checkout under
`claude.memory.roots` still points through one. `devz doctor`
(`claude:memory-layout`) shows where you are.

Claude Code keys auto memory on the config dir *and* the checkout path, so two
accounts and three clones of one repo make six separate memories. `devz claude
memory` keys it on the repo's origin URL instead, and adds shared layers above
it. The store mirrors the URL, in lower case:

```
<store>/<host>/CLAUDE.md, memory/         host: every repo on the host
<store>/<host>/<org>/CLAUDE.md, memory/   org: every repo in the org
<store>/<host>/<org>/plans/               plans, outside every repo
<store>/<host>/<org>/<repo>/memory/       repo: that repo's auto memory
```

`<store>` is `claude.memory.store`, by default `<claude.sharedDir>/hosts`.
Lower case, because a forge treats `Acme/Tool` and `acme/tool` as one
repository and two directories would be two memories. A repository named
`memory`, `plans` or `repos` is refused: its directory would be the org's own.

**The host layer** exists where the host is one company. On a public forge
the host means nothing and the org is the widest thing repos share, so by
default every configured host has the layer except github.com, gitlab.com,
bitbucket.org and codeberg.org. `claude.memory.hostLayer`, when set, is the
whole list instead. Without a host layer the org file carries the branch rule
itself.

**Scope stays per host.** A checkout can read its whole host directory, so a
session can be asked to consult another org's memory there. Another host's
memory is outside it, so reading that prompts.

For one repo, `init`:

- writes `autoMemoryDirectory`, `plansDirectory` and the guard (below) into
  the checkout's `.claude/settings.local.json`, keeping anything else there,
  and adds that file to your global git ignore;
- links the org's `CLAUDE.md` and `plans/` into the directory above the repo,
  when the checkout sits at `<root>/<org>/<repo>`. Claude Code loads a parent
  directory's `CLAUDE.md`, and the org one imports the org and host memory
  indexes. So in `infra` you get host + org + infra memory, and never
  `backend`'s. A checkout with no org folder above it gets the repo layer
  only, and nothing nags you about it;
- creates missing store files from templates. A hand-written org `CLAUDE.md`
  already in the org folder is moved into the store and linked back;
- appends the branch rule to the host `CLAUDE.md` when it is not there yet.

**The guard.** Shared layers are worth protecting from a session that saves
too eagerly, so `init` writes permission rules, not a blanket grant:

| Path | Rule |
|---|---|
| this repo's memory, the org's `plans/` | `allow`: written freely |
| `<host>/memory/`, `<host>/CLAUDE.md`, every `<host>/<org>/memory/` and `<host>/<org>/CLAUDE.md` | `ask`: Claude asks first |

An `ask` rule holds in accept-edits mode too, where an additional directory is
otherwise written without a prompt. The whole host directory stays readable,
so a session can consult another org's memory. It covers Claude's file-editing
tools, not a shell command that writes the file. Rerunning `init` removes the
broad `allow` on the host directory that versions before 1.4 wrote.

**The branch rule.** Every worktree of a repo shares one repo memory, so a
memory has to be true whatever branch is checked out. The host `CLAUDE.md`
says so, marked with `<!-- devz:branch-rule -->` so `init` and `doctor` can
find it however the file has been edited.

**Worktrees.** Linked git worktrees need no setup: Claude Code reads the main
checkout's `.claude/settings.local.json` in every worktree of a repo, wherever
the worktree sits. Run from inside one, `status` and `init` act on the main
checkout, and `init` never writes a settings file into a worktree.

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

A plugin gets `DEVZ_VIA=1` in its environment, so a script that is also still
callable by an older name can tell which way it was reached.

**Group plugins.** A command marked as a group, such as `claude`, extends this
one level down: `devz-claude-sync` on PATH is `devz claude sync`. It is listed
and completed under the group, not at the top level.

**A built-in always wins.** A plugin named like a built-in command or
subcommand never runs. That is what happens when a script is compiled into
devz and the script is left behind, so `devz doctor` (`plugins`) names any
plugin in that position.

## Old names

A command that is renamed keeps its old name, because removing it would break
whatever still calls it. Today:

| Old | Now |
|---|---|
| `devz account` | `devz claude account` (the old name takes the arguments the `claude-account` script took: `<email>`, `--select`, `--list`, `--clear`) |
| `devz memory` | `devz claude memory` |
| `devz secrets edit` | `devz secrets rotate` |

The old names are left out of listings and completion, and behave exactly as
before. Each use prints a one-line notice when stderr is a terminal, and
appends a line to `$XDG_STATE_HOME/devz/deprecated.log` (by default
`~/.local/state/devz/deprecated.log`): the time, the old name, the new name
and the directory. The log is what catches a script or launcher that never
shows the notice to anyone. `devz doctor` (`devz:deprecated`) reads it back.
Nothing listed over a release cycle is the signal that a name can be removed,
which is a major version.

A script that devz is replacing can append the same tab-separated line when it
is called directly, that is, without `DEVZ_VIA` set.

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
