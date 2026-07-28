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
Homebrew formulae and casks plus declared Bun global tools on config-declared
local and SSH machines.

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
confirmation. It does not commit, push, install packages, or apply links.
Use `--json` for a read-only, scriptable result.

Once the identity is registered, onboarding includes a live local plan and
prints the matching `apply --local --dry-run` command. `--local` is accepted
only when the current Mac's hardware fingerprint matches the requested machine;
it cannot be used to bypass machine selection.

Portable links have a separately scoped transaction:

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

For a clean Mac, `scripts/bootstrap-macos` is the versioned bootstrap
foundation. It expects the age identity and encrypted read-only `env-config`
deploy key in iCloud's `Env Secrets` directory. It installs only the tools
needed to clone both repositories and build envctl, verifies the encrypted
config, and records an initial read-only audit. Desired-state onboarding and
link application are intentionally not automatic. In an interactive terminal,
the script continues into the onboarding TUI; otherwise it prints the exact
command to run later.

Then launch the fleet review TUI with:

```sh
envctl tui \
  --config ~/Documents/env-config \
  --inventory-dir ~/.local/state/envctl/migration-20260728
```

## Current commands

```sh
go run ./cmd/envctl audit --json
go run ./cmd/envctl import-legacy --input ../env/apps-config.json
go run ./cmd/envctl plan --legacy ../env/apps-config.json --json
go run ./cmd/envctl apply \
  --config ../env-config --machine ai --dry-run --json
go run ./cmd/envctl apply \
  --config ../env-config --machine mac-studio --dry-run --json
go run ./cmd/envctl apply \
  --config ../env-config --machine ai --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine macbook-pro --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine matilda --manager bun --yes --json
go run ./cmd/envctl apply \
  --config ../env-config --machine matilda --manager mas --dry-run --json
go run ./cmd/envctl links apply \
  --config ../env-config --machine ai --local --dry-run --json
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

`audit` currently collects installed Homebrew formulae and casks, Mac App Store
applications, Bun global packages, and the declared custom tools Claude,
gh-dash, and OpenCode. Custom tools use fixed version-only probes with
five-second timeouts; arbitrary probe commands are never loaded from
configuration. A failing custom probe becomes a per-tool issue and missing
finding without hiding healthy custom tools. Package-manager collector failures
are also isolated so one unavailable manager does not hide the rest of the
inventory. A fixed read-only state-boundary registry also checks that OpenCode's
machine-local data path and its parents are real directories, never symlinks.
The path may be absent on a machine where OpenCode has not created state yet.
Boundary violations are named inventory diagnostics; envctl does not repair
them automatically.

Native profiles can also declare portable file links. Each catalog entry names
a regular source file inside the config checkout and a home-relative target.
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
Homebrew and Bun global installs, and reports every unsupported action as a
blocker. Remote dry-runs use the same agentless temporary-binary transport as
fleet refresh and do not open the state database. `--yes` explicitly authorizes
execution on a config-declared local or SSH machine and refuses the entire plan
if any selected action falls outside the supported subset.

`--manager brew`, `--manager bun`, or `--manager mas` explicitly limits an
apply transaction.
Actions for other managers remain visible as deferred actions and are not
written into that run's executable plan. Bun execution accepts only
`bun add --global --ignore-scripts --no-progress --no-summary PACKAGE`.

MAS is preflight-only. `--manager mas --dry-run` performs read-only storefront
lookups on the target and reports mas version, storefront, macOS compatibility,
price, noninteractive sudo readiness, unknown Apple Account state, and an
informational candidate command for each app. `--manager mas --yes` is rejected
before config access or SSH. This prevents headless runs from hanging on sudo,
Touch ID, Apple Account, purchase, or App Store GUI prompts.

The executor fails fast, journals every action locally in SQLite, performs a
fresh inventory on the target, and only marks the run complete when each
applied package has reached its declared type and source. Remote commands use
strict, noninteractive, independent SSH connections and accept only the exact
validated package-manager argv. Upgrades, removals, source repair, Mac App Store
installation, Bun updates/removals, custom tools, and privileged actions are
not supported by this slice.

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
