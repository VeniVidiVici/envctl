# envctl

Envctl is an experimental, read-first macOS environment manager. It is intended
to provide:

- declarative package and configuration profiles;
- machine inventory and drift detection;
- reviewable plans before changes;
- a CLI for bootstrap and remote use;
- an interactive TUI;
- local SQLite history without putting machine state in Git.

Its package apply slice is intentionally narrow: it can install missing
Homebrew formulae and casks, declared Mise runtimes, and declared Bun global
tools on config-declared local and SSH machines.

Build the committed checkout into the user-local PATH:

```sh
mkdir -p ~/.local/bin
go build -trimpath -o ~/.local/bin/envctl ./cmd/envctl
chmod 0755 ~/.local/bin/envctl
```

Validate a native configuration without collecting or changing machine state:

```sh
envctl config validate --config /path/to/env-config --json
```

Identify and register a Mac interactively:

```sh
envctl onboard --config /path/to/env-config
```

Onboarding hashes the platform UUID before it leaves the local identity
collector. It can add that fingerprint to a clean existing machine file or
create a proposed machine overlay, but only after an explicit two-key
confirmation. A new Mac first asks the user to accept or replace its suggested
machine ID, then select profiles. It does not commit or push. Use `--json` for
a read-only, scriptable result.

Pass `--setup` to continue directly into the guided setup TUI after registration:

```sh
envctl onboard --config /path/to/env-config --setup
```

For an already registered Mac, this proceeds directly to setup. `--local` is
accepted only when the current Mac's hardware fingerprint matches the requested
machine; it cannot be used to bypass machine selection.

The clean-Mac bootstrap adds `--auto`. That mode runs executable phases in
dependency order without asking for a separate confirmation for each phase.
It stops on the first blocker or failure, and review-only or explicitly manual
items retain their existing safety boundaries. When Homebrew work is pending,
automatic setup requests sudo authorization once before entering the
full-screen interface and refreshes that authorization while package
installation is running:

```sh
envctl onboard --config /path/to/env-config --setup --auto
```

Run or resume first-run convergence with:

```sh
envctl setup \
  --config /path/to/env-config \
  --machine example-mac \
  --local
```

Setup presents credential recovery, portable links, Homebrew, Mise, Bun,
fixed-registry custom tools, Mac App Store review, and explicitly manual tools
as ordered phases. Each executable phase launches the existing scoped envctl
transaction in a child process, so it replans against live state, asks for
confirmation, journals mutations, and verifies the result. `--json` prints the
same phase plan without changing state.

For clean-account child processes, envctl defaults `XDG_CONFIG_HOME` to
`~/.config` when it is otherwise unset. An explicit caller value is preserved.
This makes portable XDG configuration, including Homebrew's trust policy,
available before the user's first new login shell.

Portable links also have a separately scoped transaction:

```sh
envctl links apply \
  --config /path/to/env-config \
  --machine example-mac \
  --local \
  --dry-run \
  --json
```

The transaction blocks before mutation if any source digest, occupied target,
or real-directory parent check fails. Explicit `--yes` moves an existing
symlink into a timestamped machine-local backup, creates all links as one
transaction, verifies every resulting target, and rolls earlier operations back
if a later operation fails. It never replaces a regular file or directory.

Credential recovery has its own backup-first transaction:

```sh
envctl recovery apply \
  --config /path/to/env-config \
  --machine example-mac \
  --local \
  --dry-run \
  --json
```

Recovery declarations use fixed SOPS-file, age-archive, or GPG-keyring
drivers—never configuration-supplied commands. The planner pins SOPS and age to
the bootstrap-installed local age identity, decrypts only into bounded in-memory
hashes or archive readers, and reports status without emitting plaintext or
secret digests. It detects missing tools and sources, unsafe symlinks, occupied
targets, permission drift, content drift, missing archive members, and an
unexpected GPG fingerprint.

`--dry-run` remains read-only and does not open SQLite. Explicit `--yes`
decrypts and validates every requested payload in a mode-0700 machine-local
staging directory before changing any target. Existing SOPS files and differing
archive members move to timestamped backups; the whole transaction is verified
and rolled back if a later action fails. An absent GPG home is built and
fingerprint-checked in staging before one atomic rename. Envctl will only repair
the mode of an existing GPG home that already contains the expected secret key;
it blocks an existing keyring missing that key for manual review. SQLite records
paths, statuses, and action history, but never plaintext or secret content
digests.

For a clean Mac, `scripts/bootstrap-macos` is the versioned bootstrap
foundation. It expects the age identity and encrypted read-only `env-config`
deploy key in iCloud's `Env Secrets` directory. It installs only the tools
needed to clone both repositories and build envctl, verifies the encrypted
config, and records an initial read-only audit. In an interactive terminal, the
same command asks for the machine ID and profiles, registers the machine
locally, and continues directly into ordered guided setup. Selecting the machine
and profiles authorizes executable setup phases; the workflow stops on the
first blocker or failure. In a noninteractive terminal, it prints the exact
interactive command to run later. A rerun preserves an
onboarding-created machine file when the private checkout has not advanced
upstream.

Then launch the fleet review TUI with:

```sh
envctl tui \
  --config ~/Documents/env-config \
  --inventory-dir ~/.local/state/envctl/migration-20260728
```

## Current commands

```sh
go run ./cmd/envctl audit --json
go run ./cmd/envctl setup \
  --config ../env-config --machine example-mac --local
go run ./cmd/envctl import-legacy --input ../env/apps-config.json
go run ./cmd/envctl plan --legacy ../env/apps-config.json --json
go run ./cmd/envctl apply \
  --config ../env-config --machine example-mac --dry-run --json
go run ./cmd/envctl apply \
  --config ../env-config --machine build-mac --dry-run --json
go run ./cmd/envctl apply \
  --config ../env-config --machine example-mac --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine laptop --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine remote-mac --manager bun --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine remote-mac --manager mise --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine remote-mac --manager mas --dry-run --json
go run ./cmd/envctl links apply \
  --config ../env-config --machine example-mac --local --dry-run --json
go run ./cmd/envctl recovery plan \
  --config ../env-config --machine example-mac --local --json
go run ./cmd/envctl recovery apply \
  --config ../env-config --machine example-mac --local --dry-run --json
go run ./cmd/envctl config resolve \
  --config ./examples/env-config --machine example-mac --json
go run ./cmd/envctl history --json
go run ./cmd/envctl tui \
  --config ../env-config \
  --inventory-dir ~/.local/state/envctl/migration-20260728
go run ./cmd/envctl fleet refresh \
  --config ../env-config \
  --inventory-dir ~/.local/state/envctl/migration-20260728 \
  --json
go run ./cmd/envctl fleet export-decisions \
  --config ../env-config \
  --json
go test ./...
```

`audit` currently collects installed Homebrew formulae and casks, active Mise
runtimes and their configured version requests, Mac App Store applications,
Bun global packages, and the declared custom tools Claude, gh-dash, and
OpenCode. Custom tools use fixed version-only probes with
five-second timeouts; arbitrary probe commands are never loaded from
configuration. A failing custom probe becomes a per-tool issue and missing
finding without hiding healthy custom tools. Package-manager collector failures
are also isolated so one unavailable manager does not hide the rest of the
inventory. A fixed read-only state-boundary registry also checks that OpenCode's
machine-local data path and its parents are real directories, never symlinks.
The path may be absent on a machine where OpenCode has not created state yet.
Boundary violations are named inventory diagnostics; envctl does not repair
them automatically.

Native profiles can also declare portable file or directory links. Each
catalog entry names a real source inside the config checkout and a
home-relative target. Directory trees reject symlinks and special files.
Source content is included in the configuration digest. Agentless audits hash
the source on each machine and inspect the target without following it; plans
distinguish missing targets, occupied targets, wrong symlinks, missing sources,
and stale source content. Link changes use the separately confirmed
`links apply` transaction described above; they never enter package-manager
execution.
`import-legacy` converts the old package configuration into a reviewable JSON
draft; it never modifies the input.
`plan` compares that draft with the live Homebrew inventory and reports
satisfied, missing, drifted, extra, and not-yet-checked packages. It does not
execute its proposed actions. Missing legacy Homebrew entries are resolved
against formula/cask metadata before an install action is described.
Use `--inventory PATH` to plan centrally from a saved remote audit.

`apply` always creates a new live inventory. `--dry-run` supports both local and
SSH machines, validates and prints the exact argv for supported low-risk
Homebrew, Mise, Bun, and fixed-registry custom-tool installs, and reports every
unsupported action as a blocker. Remote dry-runs use the same agentless
temporary-binary transport as fleet refresh and do not open the state database.
`--yes` explicitly authorizes execution on a config-declared local or SSH
machine and refuses the entire plan if any selected action falls outside the
supported subset.

`--manager brew`, `--manager mise`, `--manager bun`, `--manager custom`, or
`--manager mas` explicitly limits an apply transaction.
Actions for other managers remain visible as deferred actions and are not
written into that run's executable plan. Bun execution accepts only
`bun add --global --ignore-scripts --no-progress --no-summary PACKAGE`.
Mise execution accepts only `mise install --yes TOOL@VERSION` for a declared
tool and validated version request. Custom-tool execution accepts only three
compiled-in identities: the official native Claude Code installer, the
`dlvhdr/gh-dash` GitHub CLI extension, and the official native OpenCode
installer. No installer command or URL is loaded from configuration.

MAS is preflight-only. `--manager mas --dry-run` performs read-only storefront
lookups on the target and reports mas version, storefront, macOS compatibility,
price, noninteractive sudo readiness, unknown Apple Account state, and an
informational candidate command for each app. `--manager mas --yes` is rejected
before config access or SSH. This prevents headless runs from hanging on sudo,
Touch ID, Apple Account, purchase, or App Store GUI prompts.

The executor fails fast, journals every action locally in SQLite, performs a
fresh inventory on the target, and only marks the run complete when each
applied package has reached its declared type, source, and configured Mise
version request. Remote commands use strict, noninteractive, independent SSH
connections and accept only the exact validated package-manager argv.
Homebrew upgrades, removals, source repair, Mac App Store installation, Bun
updates/removals, unregistered custom tools, and privileged actions are not
supported by this slice.

Audits and plans are recorded in `~/.local/state/envctl/state.db` by default.
Use `--no-record` for an entirely ephemeral run or `--state PATH` to select a
different database.

The fleet TUI is review-only. It can record `adopt`, `keep`, `ignore`, and
`remove` decisions for extra installed packages, but it cannot install or
delete anything.

`fleet refresh` is agentless. For SSH machines it checks that the remote
platform matches the current binary, copies envctl to a random `/tmp` path,
runs a read-only audit, removes the temporary binary, and atomically replaces
the saved inventory. A failed refresh retains the last good snapshot. A
targeted `--machines` refresh preserves status rows for machines it did not
visit.

`fleet export-decisions` writes the latest decision for each reviewed package
to `reviews/fleet-decisions.yaml` in the private config checkout. The result is
a deterministic Git-reviewable handoff; exporting does not apply a decision.

## Safety boundary

Git-backed configuration will be the desired state. SQLite will contain only
local inventory, plans, and run history. Secret values, key material, and
application session state must never be written to the database or logs.

No executor may widen the current apply boundary without new validation,
journaling, verification, and tests.
