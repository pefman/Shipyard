# Shipyard

Shipyard is an AI issue solver for GitHub: it reads a GitHub issue and runs
the **built-in pi coding agent** on a checkout of the repository — the agent
explores the code, makes the changes the issue requires, and builds and tests
them (against the AI model you configure: any OpenAI-compatible endpoint).
Shipyard then commits the agent's work on a branch, pushes it, and opens a
pull request that links the source issue. Built in Go, single binary, and
the agent runtime is built into Shipyard: sandboxed runs preinstall pi in
the sandbox image from Shipyard's own vendor bundle (nothing is downloaded
at run time). Native (no-Docker) runs instead need a `pi` binary on the
host — see [Native agent runs](#native-agent-runs). The host needs `git`
(the flow uses the git CLI for clone / commit / push).

## Build

```sh
go build ./...
go build -o shipyard ./cmd/shipyard
```

## Safety

Shipyard is built to be predictable when unattended: **`listen` is
dry-run by default**. A fresh installation pointed at a repository runs
the full flow (issue → agent → changes) but stops before any commit,
push, or pull request, printing what it would do. Nothing reaches
GitHub until you deliberately go live:

```sh
# dry run (the default): watch what it would do
shipyard listen --repo owner/repo

# live: deliver commits, pushes, and pull requests
shipyard listen --repo owner/repo --live      # or SHIPYARD_MODE=live
```

Live runs are bounded by allowlists and a per-run pull-request budget:

| Control | Flag | Environment | Effect |
| ------- | ---- | ----------- | ------ |
| Repo allowlist | `--repos owner/repo,…` | `SHIPYARD_REPOS` | Only issues in these repositories are solved; `listen` refuses to start on a repo outside the list |
| Label allowlist | `--labels a,b` (on `listen` the repeatable `--label` is equivalent) | `SHIPYARD_LABELS` | Only issues carrying at least one allowed label are solved |
| Pull-request budget | `--max-prs 5` | `SHIPYARD_MAX_PRS` | Hard per-run cap (default 3): after N pull requests the run exits cleanly with a summary, and issues it did not get to stay open for a later run. `--max-prs 0` is a dry-run setting, not a live one |
| Agent budgets | `--agent-max-turns 30` / `--agent-timeout 30m` | `SHIPYARD_AGENT_MAX_TURNS` / `SHIPYARD_AGENT_TIMEOUT` | Per-issue caps on the agent's assistant turns and wall clock; a budget-exhausted run makes no commit, push, or PR |
| Model context window | `--agent-context-window 32768` | `SHIPYARD_AGENT_CONTEXT_WINDOW` | Context window (tokens) declared for the model — the agent compacts its context off this size (default 128000). Set it to your local model's real window, or a big repo/issues will overflow the model before compaction kicks in |
| Unguarded acknowledgment | `--all` | — | A live run with **no repo or label allowlist set is refused** unless this flag marks the allowlist axis as explicitly unrestricted — "no allowlist, on purpose" (the audit line then reads `guardrails: NONE (explicit --all)`, and `--all` conflicts with a set `--repos`/`--labels` allowlist). The hidden flag `--i-know-this-is-unguarded` remains a compatible alias |

The gate applies to both commands: `listen` in live mode, and `solve`,
which runs live unless you pass `--dry-run`. Dry runs need no allowlist
and no acknowledgment because they commit nothing and open no pull
requests. Guarded runs print an audit line at startup
(`shipyard: guardrails: repos: …; labels: …; max-prs: N`), and for a
live `listen` that audit line is the container's first log line — so a
long-running deployment's first line is the one that tells you which
guardrails are active.

Copy-pasteable examples:

```sh
# live listener restricted to one label, budget of 5 pull requests per run
shipyard listen --repo owner/repo --live --labels shipyard --max-prs 5

# the same, configured via environment (what a docker compose file sets)
SHIPYARD_MODE=live SHIPYARD_LABELS=shipyard SHIPYARD_MAX_PRS=5 \
  shipyard listen --repo owner/repo

# one-shot solve, guarded by the repo allowlist (alternatives: --dry-run
# or --i-know-this-is-unguarded — see the gate above)
shipyard solve --repo owner/repo --issue 42 --repos owner/repo
```

The one-shot `solve` command stays explicit: it always targets the issue
you name and opens at most one pull request. Compose deployments that
want the listener live must pass `--live` (or `SHIPYARD_MODE=live`)
plus an allowlist on purpose — see
[Listen mode (long-running)](#listen-mode-long-running).

## Build (Docker)

The repo ships a multi-stage Dockerfile: a full Go toolchain builds a
static binary (`CGO_ENABLED=0`), and the runtime image is a small Alpine
base with just `git` and CA certificates — no build tools, running as the
unprivileged `shipyard` user. The image is designed to stay well under
50 MB (`git` is the heaviest component); check `docker images shipyard`
after a build.

The image always builds the full repo, so both commands (`solve` and
`listen`) and every flag that is merged into `main` are included in a
rebuild.

Note: the Shipyard *binary* image is not the sandbox image. When Docker
is available the agent runs in a per-repository sandbox image (see
[Sandbox](#sandbox)); when it is not, the agent runs natively — which
requires the `pi` binary on the host (see [Native agent
runs](#native-agent-runs)).

### Build

```sh
docker build -t shipyard .
# or
docker compose build
```

### One-shot solve

Plain `docker run`:

```sh
docker run --rm \
  -e SHIPYARD_GITHUB_TOKEN="$SHIPYARD_GITHUB_TOKEN" \
  -e SHIPYARD_AI_ENDPOINT=http://localhost:8080 \
  -e SHIPYARD_AI_KEY="${SHIPYARD_AI_KEY:-}" \
  -e SHIPYARD_AI_MODEL=gpt-5.6-sol \
  -e SHIPYARD_REPOS=owner/repo \
  shipyard solve --repo owner/repo --issue 42
```

A live `solve` with no allowlist set is refused (see [Safety](#safety)):
pass `--repos`, `--labels`, `--dry-run`, or `--i-know-this-is-unguarded`.

Or via the bundled compose file (`docker-compose.yml`), which reads the
same environment variables:

```sh
export SHIPYARD_REPO=owner/repo SHIPYARD_ISSUE=42
export SHIPYARD_GITHUB_TOKEN=... SHIPYARD_AI_ENDPOINT=... SHIPYARD_AI_KEY=... SHIPYARD_AI_MODEL=...
export SHIPYARD_REPOS=owner/repo   # guardrail: a live solve needs an allowlist
docker compose run --rm solve
# extra flags go after `--`:
docker compose run --rm solve -- --dry-run
```

For a custom (unauthenticated) endpoint like `http://localhost:8080` the
`SHIPYARD_AI_KEY` variable may be left empty; provider presets
(`--provider openai|xai`) use the same variables.
`SHIPYARD_GITHUB_API` can point the GitHub API at a GHE instance.

### Listen mode (long-running)

The same image can run a long-lived listener that polls the repo's open
issues and solves new ones:

```sh
# compose, dry-run (the default; a state volume keeps track of processed
# issues across restarts)
docker compose --profile listen up -d

# …or the same, live with an allowlist, once a dry run has shown what to
# expect (see Safety)
SHIPYARD_REPOS=owner/repo SHIPYARD_LABELS=shipyard \
  docker compose --profile listen-live up -d

# or plain docker run (dry run, the default)
docker run -d --name shipyard-listen --restart unless-stopped \
  -v shipyard-state:/data \
  -e SHIPYARD_GITHUB_TOKEN=... -e SHIPYARD_AI_ENDPOINT=... -e SHIPYARD_AI_MODEL=... \
  shipyard listen --repo owner/repo --interval 5m

# or plain docker run, live with an allowlist
docker run -d --name shipyard-listen --restart unless-stopped \
  -v shipyard-state:/data \
  -e SHIPYARD_GITHUB_TOKEN=... -e SHIPYARD_AI_ENDPOINT=... -e SHIPYARD_AI_MODEL=... \
  -e SHIPYARD_LABELS=shipyard -e SHIPYARD_MAX_PRS=3 \
  shipyard listen --repo owner/repo --interval 5m --live
```

The listener keeps its state file in `/data` (override with
`--state-file`); mount a volume there so a container restart does not
re-solve issues. The listener starts in dry-run mode: add `--live` (and
an allowlist — see [Safety](#safety)) once you have watched a dry run
do what you expect.

Note for live runs while Shipyard itself is in a container: the sandbox
needs to start containers from inside (see [Sandbox](#sandbox)), so
give the Shipyard container access to the Docker socket
(`-v /var/run/docker.sock:/var/run/docker.sock`). Without it, live runs
run the agent natively — see [Native agent runs](#native-agent-runs).

## Command

### `shipyard login`

Authenticates with GitHub via the OAuth device flow. Works out of the box —
shipyard ships a pre-registered GitHub OAuth App (client ID `Iv23lipRhtA8srclwbp3`), so
no flags or environment variables are needed:

```sh
shipyard login
```

To use your own GitHub OAuth App instead, override the client ID with
`--github-client-id` or `SHIPYARD_GITHUB_CLIENT_ID` (flag wins over the
environment variable).

The flow prints a verification URI and a one-time code; after you authorize
at the URI, the access token is verified via `GET /user` and stored at
`$XDG_CONFIG_HOME/shipyard/credentials.json` (default
`~/.config/shipyard/credentials.json`) with `0600` permissions. Re-running
`login` while a valid token is stored just verifies it and exits;
`--force` re-does the device flow.

### `shipyard solve`

Solves one GitHub issue in a single shot:

```sh
shipyard solve --repo owner/repo --issue 42 --repos owner/repo
```

A live `solve` is guarded like `listen` (see [Safety](#safety)): with
no allowlist set it is refused, so pass `--repos` or `--labels`, run
`--dry-run`, or pass `--i-know-this-is-unguarded`.

Flow:

1. Fetches repo info and issue details (title, body, labels) from
   the GitHub API.
2. Ensures a local checkout: `--workdir` if given (must be clean),
   otherwise a clone into a temp directory (the clone URL comes from the
   GitHub API and is authenticated with the GitHub token).
3. Decides where the agent runs: in a disposable sandbox container
   whenever Docker is available (dry runs included — the agent executes
   code, so the container is the safe choice), natively otherwise (see
   [Sandbox](#sandbox)). The environment is part of the task, so the
   agent knows its changes are built and tested in that environment.
4. Runs the **built-in pi coding agent** on the checkout: Shipyard
   writes the agent's task (issue title, labels, body, branch,
   environment) and its model configuration into the checkout
   (`.shipyard-pi/`, git-excluded), then runs pi against the endpoint
   you configured. The agent reads and edits files, runs the
   repository's build and test commands, and iterates until they pass —
   its progress streams into shipyard's log (`issue #42: agent: …`).
5. Re-verifies the agent's final tree (on container runs, the image's
   artifact-free build/test steps run in the same container), then
   commits everything the agent left behind on a new branch
   (`shipyard/issue-<n>` by default), pushes it, and opens a pull
   request via the GitHub API whose body links the source issue.

With `--dry-run`, step 5 stops after the changes are in the workdir:
nothing is committed, pushed, or opened, the workdir is left dirty for
inspection, and the agent's changes are saved to a patch file whose
path is printed.

On success stdout prints the pull request URL (or the patch path for
`--dry-run`), so runs are scriptable.

The agent is bounded per issue by `--agent-max-turns` (default 30) and
`--agent-timeout` (default 30m); hitting either stops the run before
any commit, push, or PR. (The wall-clock clock starts before the run:
on a cold host the first run's wrapper-image build counts against it,
so leave headroom for a first run on a new machine.)

### Failure modes

Shipyard fails fast with actionable errors for the expected failure modes:

| Situation | Error contains |
| --------- | -------------- |
| The agent leaves the repository unchanged | `the agent made no usable changes` |
| The agent's tree fails the build/test verification | `verify step … exited … — no commit, push, or PR was made` |
| The agent hits its turn or wall-clock budget | `agent stopped: budget exhausted (no commit, push, or PR was made)` |
| The agent run fails (endpoint down after retries, non-zero exit) | `agent run: … (will be retried on a later pass)` on `listen` |
| Token cannot read/push the repo | `check that the GitHub token can read/push the repo` |
| 401 from the GitHub API | `check that the GitHub token is valid and not expired` |
| 403 from the GitHub API (e.g. opening the PR) | `this token is missing the permissions GitHub needs here (…)` |
| `--workdir` has uncommitted changes | `has uncommitted changes; commit or stash them first` |
| Live run (solve or listen) with no repo/label allowlist set | `no repo or label allowlist is set: this run would be unguarded…` (set an allowlist or pass `--i-know-this-is-unguarded`) |
| Remote branch name already taken (previous run) | `remote branch … already exists; … pass --branch` |
| Docker not installed / daemon down (live run) | `sandbox: off (Docker not available: the agent runs natively on the host)` (native runs need the `pi` binary; see [Native agent runs](#native-agent-runs)) |
| No AI endpoint configured | `no AI endpoint configured (set --ai-endpoint or SHIPYARD_AI_ENDPOINT)` |
| Local model server unreachable from the sandbox (loopback endpoint, server bound to `127.0.0.1` only) | `agent: cannot reach your local AI endpoint from the sandbox: nothing answers at host.docker.internal:<port>` + the bind requirement (see [Local model endpoints](#local-model-endpoints)) |

The agent's changes are saved to a patch file on every run, so a bad
agent session is always inspectable.

### `shipyard listen`

Listens on a repository: it polls the open issues, runs the same
solving flow as `solve` on every issue that has not been processed
yet, and skips the rest. Designed to run unattended (e.g. in a
container), one long-lived process per repository. It starts in
dry-run mode (see [Safety](#safety)): `--live` or
`SHIPYARD_MODE=live` enables commits, pushes, and pull requests.

```sh
shipyard listen --repo owner/repo
```

How a pass works:

1. Lists the repo's open issues (pull requests are not issues and are
   ignored); with `--label` only issues carrying at least one of the
   given labels are considered.
2. Skips issues already recorded in the state file (default
   `shipyard-listen-state.json` next to where the process runs,
   override with `--state-file` and mount that path in a container to
   survive restarts).
3. As a safety net for a lost state file, checks the GitHub API for an
   existing pull request on the issue's fix branch
   (`shipyard/issue-<n>`) and skips those too.
4. Runs the solving flow (clone → agent → commit → push → PR) for each
   remaining issue, each on its own throwaway clone, and records it in
   the state file as soon as it is done.

Behavior notes:

- One issue failing does not stop the pass; it is logged and retried
  on a later pass.
- `SIGINT`/`SIGTERM` shut down gracefully: the in-flight issue finishes
  (or is aborted) and no further issue is picked up.
- The default mode is dry-run: the agent's changes are kept in the
  workdir but nothing is committed and no pull requests are opened;
  state still records the issues (with no PR URL). Pass `--live` to
  deliver.
- In live mode the first log line is the guardrails audit line
  (allowlists, pull-request budget) so a container's first line is an
  audit line (see [Safety](#safety)).
- Every log line is prefixed with the issue number, so a long-running
  listener is easy to follow (`journalctl`, `docker logs`, …).

Useful flags: `--interval 5m` (default `1m`), `--live` / `--dry-run`
(mode; dry-run is the default), `--repos`, `--labels` / `--label
shipyard`, `--max-prs`, `--agent-max-turns`, `--agent-timeout`,
`--i-know-this-is-unguarded`, `--state-file`, `--base main`,
`--git-url`, `--image`, `--verbose` —
plus the same `--provider` / `--ai-endpoint` / `--ai-key` / `--ai-model`
configuration as `solve` (the default `custom` provider needs no key,
so a keyless local endpoint works out of the box).

## Sandbox

`solve` and `listen` run the agent — which explores the repository,
edits code, and **executes the repository's build and test commands** —
inside a disposable Docker container whenever Docker is available:
the checkout is mounted read-write at `/work`, the agent and the
artifact-free verification steps run in an ephemeral `docker run`, and
the container is discarded when they finish. Everything else — cloning,
commit, push, opening the pull request — runs natively on the host. So
agent-generated code only ever executes inside the throwaway container,
never on your machine.

The agent runs in a **wrapper image** built on the language image:
`shipyard-sandbox/<base>:pi-<version>` = the language image plus the
built-in pi runtime (Node.js + the pi coding agent, vendored offline in
this repository). Shipyard builds it automatically on first use
(`docker build`, logged as `sandbox: building …`); afterwards it is
reused.

### Which image

The *base* image resolution order:

1. `--image <image>` — explicit override (both commands).
2. The per-repository setting, once the planned `shipyard.yaml` lands
   (the lookup point already exists; nothing to rewire).
3. Auto-detection from the repository contents — first match wins:

   | Marker file | Image | Verification steps run in it |
   | ----------- | ----- | ---------------------------- |
   | `go.mod` | `golang:1.22` | `go build -o /dev/null ./…` (no binary is written), `go test ./…` |
   | `pyproject.toml` / `requirements.txt` | `python:3.12` | bytecode compile of all `.py` files |
   | `package.json` | `node:20` | `npm install`, `npm test` (skipped when the package defines no test script) |
   | `Cargo.toml` | `rust:1.79` | `cargo build`, `cargo test` |
   | none of the above | `ubuntu:24.04` | nothing (the image just runs the agent) |

Unknown images (custom `--image` values) get no verification steps.
The image is announced to the agent in its task, so the changes target
that toolchain. If a verification step fails, the run stops before
commit, push, and pull request: a change set that does not build or
test cleanly never opens a PR.

The agent is also instructed to keep the repository clean for the pull
request (no compiled binaries or `node_modules` committed), and the
verification steps are artifact-free, so the pushed branch contains
only source changes.

### Native agent runs

Without Docker (or on hosts where you deliberately skip the sandbox),
the agent runs natively on the host — which executes whatever the agent
runs, in the checkout. Two things make a native run possible:

- the `pi` binary on the host (`npm install -g
  @earendil-works/pi-coding-agent`, or any `pi` on `PATH`), and
- if Shipyard itself runs in a container without the Docker socket, the
  native agent runs *inside the Shipyard container* — the bundled
  Alpine image carries no agent runtime, so install pi in that image
  (or run shipyard on the host) before expecting native runs to work.

Every run prints an audit line up front:

- `sandbox: <image> (source: flag|auto)` — the agent runs in the
  wrapper container over that image;
- `sandbox: off (Docker not available: the agent runs natively on the
  host)` — native fallback.

In `listen` mode the per-issue line is prefixed like all other output
(`issue #42: sandbox: …`), and the listener prints one summary line at
startup.

## Authentication

Every command that talks to GitHub (`solve`, `listen`, …) resolves the
token in this order — first non-empty wins:

1. `--github-token` flag
2. `SHIPYARD_GITHUB_TOKEN` environment variable
3. the token stored by `shipyard login`
   (`$XDG_CONFIG_HOME/shipyard/credentials.json`, default
   `~/.config/shipyard/credentials.json`)

If none of the three is present, the run stops with the usual "missing
required configuration" error. The small account commands:

```sh
shipyard whoami    # shows which identity the precedence resolves to (@login),
                   # plus the stored token's expiry when that metadata exists;
                   # --github-token / --github-api override token and API root
shipyard logout    # removes the stored credentials file (clear error if none)
```

In Docker runs the credentials file lives on the host, so pass the token
via `SHIPYARD_GITHUB_TOKEN` (or bind-mount the config directory).

## Configuration

Flags take precedence over environment variables.

| Flag              | Environment variable      | Required | Description                                          |
| ----------------- | ------------------------- | -------- | ---------------------------------------------------- |
| `--repo`          | —                         | yes      | GitHub repository: `owner/repo` or a github.com URL (https/ssh/scp forms) |
| `--issue`         | —                         | yes      | Issue number to solve                                |
| `--github-token`  | `SHIPYARD_GITHUB_TOKEN`   | unless logged in | GitHub token (or the token stored by `shipyard login`; see Authentication) |
| `--provider`      | `SHIPYARD_AI_PROVIDER`    | no       | AI provider preset: `openai`, `xai`, or `custom` (default `custom`) |
| `--ai-endpoint`   | `SHIPYARD_AI_ENDPOINT`    | yes for `custom` | AI endpoint base URL the agent's model calls use; `openai`/`xai` use their preset base URLs |
| `--ai-key`        | `SHIPYARD_AI_KEY` (also `SHIPYARD_OPENAI_KEY` / `SHIPYARD_XAI_KEY`) | yes for `openai`/`xai` | API key for the AI endpoint |
| `--ai-model`      | `SHIPYARD_AI_MODEL`       | no       | Model the agent uses (defaults: `gpt-5.6-sol` for `openai`, `grok-4.6` for `xai`) |
| `--agent-max-turns` | `SHIPYARD_AGENT_MAX_TURNS` | no    | Cap the agent's assistant turns per issue (default 30) |
| `--agent-timeout` | `SHIPYARD_AGENT_TIMEOUT`  | no       | Cap the agent run's wall clock per issue (default 30m) |
| `--agent-context-window` | `SHIPYARD_AGENT_CONTEXT_WINDOW` | no | Context window (tokens) declared for the model, driving the agent's built-in context compaction (default 128000; set it to your local model's real window) |
| `--workdir`       | —                         | no       | Local checkout to build on (default: clone to temp)  |
| `--base`          | —                         | no       | Base branch (default: the repo's default branch)     |
| `--branch`        | —                         | no       | Branch for the fix (default: `shipyard/issue-<n>`)   |
| `--git-url`       | —                         | no       | Git clone URL when no `--workdir` is given           |
| `--image`         | —                         | no       | Base image the sandbox container is built on (default: auto-detected) |
| `--dry-run`       | —                         | no       | `solve`: stop after the agent's work (no commit/push/PR); `listen`: dry-run mode — the default for `listen` |
| `--verbose`       | `SHIPYARD_VERBOSE`        | no       | Log the agent's raw event lines in addition to the rendered progress; off by default (see [Debugging the agent](#debugging-the-agent)) |
| `--live`          | `SHIPYARD_MODE`           | no       | `listen` only: commit, push, and open pull requests (the default for `listen` is dry-run; `solve` is always live by default) |
| `--repos`         | `SHIPYARD_REPOS`          | no       | Comma-separated `owner/repo` allowlist (solve + listen; see [Safety](#safety)) |
| `--labels`        | `SHIPYARD_LABELS`         | no       | Comma-separated label allowlist (solve + listen; on `listen` the repeatable `--label` is an equivalent flag) |
| `--max-prs`       | `SHIPYARD_MAX_PRS`        | no       | Stop the run after this many pull requests (default 3; `0` = open none — a dry-run setting) |
| `--all`             | —                         | no       | Run with no `--repos`/`--labels` allowlist, on purpose: the axis is explicitly unrestricted (hidden alias `--i-know-this-is-unguarded`; see [Safety](#safety)) |

`SHIPYARD_GITHUB_API` (env only) overrides the GitHub API base URL
(default `https://api.github.com`); useful against GHE or in tests.

### AI providers

`--provider` selects a preset that pins the endpoint base URL and default
model, so provider switches are one flag apart:

- `openai` — ChatGPT, base `https://api.openai.com/v1`, default model
  `gpt-5.6-sol`, key required (`SHIPYARD_OPENAI_KEY` or `SHIPYARD_AI_KEY`).
- `xai` — Grok, base `https://api.x.ai/v1`, default model `grok-4.6`, key
  required (`SHIPYARD_XAI_KEY` or `SHIPYARD_AI_KEY`).
- `custom` (default) — any OpenAI-compatible endpoint via
  `--ai-endpoint` / `SHIPYARD_AI_ENDPOINT`; the key is optional, so a local
  endpoint such as `http://localhost:8080` needs none (sandboxed runs
  reach host-local servers via `host.docker.internal` — see
  [Local model endpoints](#local-model-endpoints)).

`--ai-model` / `SHIPYARD_AI_MODEL` overrides the preset's default model in
all cases. The API key is resolved in this order: `--ai-key` flag, then the
provider's own variable (`SHIPYARD_OPENAI_KEY` / `SHIPYARD_XAI_KEY`), then
the generic `SHIPYARD_AI_KEY`. The examples below are live runs, so as with
every live run each needs a guardrail — `--repos owner/repo` (or an
allowlist env var), `--i-know-this-is-unguarded`, or a `--dry-run` —
see [Safety](#safety):

```sh
# ChatGPT (OpenAI) — preset pins the base URL and default model
export SHIPYARD_OPENAI_KEY=...
shipyard solve --repo owner/repo --issue 42 --provider openai

# Grok (xAI)
export SHIPYARD_XAI_KEY=...
shipyard solve --repo owner/repo --issue 42 --provider xai

# A different model on the same provider
shipyard solve --repo owner/repo --issue 42 --provider openai --ai-model <model-name>

# Local/self-hosted OpenAI-compatible endpoint, no key needed
shipyard solve --repo owner/repo --issue 42 \
  --provider custom --ai-endpoint http://localhost:8080
```

### GitHub token

Needs `contents: read + write` and `pull-requests: write` on the target
repository (classic PAT: `repo` scope; `public_repo` is not enough to open
PRs). For private repos the token is also used to authenticate the git
clone.

### AI endpoint

Any OpenAI-compatible `/chat/completions` endpoint works: the built-in
agent registers it as its model provider (Shipyard writes a
`models.json` into the checkout's agent directory) and talks to
`POST <endpoint>/chat/completions` with a `Bearer` API key when a key is
configured (a keyless `custom` endpoint sends a placeholder key instead
of an Authorization header). The endpoint should stream (SSE) — as
OpenAI, xAI, and most local servers do; non-streaming endpoints are not
used. What the model returns is up to the model and the agent: the agent
asks it for help as an LLM in a coding session, and decides what to do
with the answers (edit files, run commands, finish).

### Local model endpoints

Sandboxed runs (Docker available) execute the agent **inside a
container**, where `localhost` is the container itself — not your
machine. A model server on the host (ninfer, ollama, vLLM, …) is
therefore not reachable from the sandbox at
`--ai-endpoint http://localhost:<port>/v1`, even though `curl`ing the
same address from the host works. Shipyard handles this case:

- **Remap.** A host-loopback endpoint (`localhost`, `127.0.0.1` /
  `127.0.0.0/8`, `::1`) is remapped to `host.docker.internal` for the
  sandbox run (a log line announces it), so the address the agent's
  model calls use points at the host from inside the container.
- **Host gateway mapping.** Every sandbox container starts with
  `--add-host=host.docker.internal:host-gateway`, which resolves the
  name on all Docker platforms (Linux does not define it by default;
  on Docker Desktop it is redundant but harmless). This also makes an
  endpoint you already configured as `http://host.docker.internal:…`
  work on Linux.
- **Pre-flight probe.** When an endpoint was remapped, the container
  run starts with a short-timeout TCP connect **from inside the
  container** to `host.docker.internal:<port>` — the same address the
  agent's model calls use, checked in the same container run, so a
  run can never pass the check and then fail to reach the server. If
  nothing answers, the run stops before the agent's first model call
  with an error naming the exact address and the bind requirement.

The one requirement that is genuinely yours: **the model server must
bind a non-loopback interface** — start it with `--host 0.0.0.0` (or
the host's LAN address), not `127.0.0.1`. A server listening on
loopback only is reachable from the host, which is exactly why the
failure looks like "the server is fine but every model call is a
Connection error". If it already binds `0.0.0.0` and the probe still
fails, the block is usually a **host firewall** (ufw/nftables) that
forbids the docker bridge network (`172.17.0.0/16`) from reaching that
port — allow that range for the port, or point `--ai-endpoint` at an
address the sandbox can reach (e.g. the model server's own container
IP on the shared bridge, when it runs in a container too). Non-loopback
endpoints (public URLs, LAN addresses, `host.docker.internal`) pass
through unchanged. Native (no-Docker) runs use the endpoint exactly as
configured — on the host, `localhost` is the host. On podman the
host-gateway mapping is best-effort; the probe's failure message names
the podman alternatives (`host.containers.internal`, `--net=host`).

### Debugging the agent

`--verbose` (or `SHIPYARD_VERBOSE=1`) on `solve` and `listen` logs the
agent's raw event lines (pi's JSON event stream) in addition to the
rendered progress lines, so a weak or local model can be debugged from
the log alone. It is off by default and changes nothing else: the
rendered lines already show each turn, tool call, the model's text,
endpoint trouble (with pi's automatic retries), and compaction events,
and with `listen` the lines carry the usual per-issue prefix. The
diagnostics are logged for failed runs too — an endpoint's
`HTTP 500` surfaced as a model error, or a budget exhaustion:

```sh
shipyard solve --repo owner/repo --issue 42 --verbose
shipyard listen --repo owner/repo --verbose
```

```sh
# what the lines look like (listen output, rendered)
issue #42: agent: turn 1 done
issue #42: agent: bash go test ./...
issue #42: agent: endpoint trouble (retry 1/3 in 2s): 500: internal error
issue #42: agent: finished (2 turns)
issue #42: agent finished in 2 turns; its changes are in /tmp/shipyard-42
```

## End-to-end test runs

### 1. Without an AI key (mock endpoint, real GitHub)

The repo ships a tiny mock of an OpenAI-compatible endpoint
(`hack/mockai`) that streams a canned response, shaped like a real
model answer:

```sh
# canned response: instructions the agent acts on (it edits the repo
# itself — no diff in the model's answer)
cat > /tmp/canned.txt <<'EOF'
Fix the greet() function in hello.py so that an empty name returns the
fallback greeting "Hello, stranger" instead of "Hello, ". Verify with
python3 -m py_compile hello.py.
EOF

go run ./hack/mockai --port 8765 --response-file /tmp/canned.txt &

# `custom` providers need no key: the mock is just another OpenAI-compatible
# server; the model name is arbitrary for the mock
shipyard solve --repo <owner>/<test-repo> --issue <n> \
  --github-token "$SHIPYARD_GITHUB_TOKEN" \
  --provider custom --ai-endpoint http://127.0.0.1:8765/v1 \
  --ai-model mock-model \
  --dry-run   # try dry-run first; to open a real PR drop it and add a
              # guardrail (e.g. --repos <owner>/<test-repo>)
```

Use a dedicated test repo + a test issue for this: the canned
instructions only make sense against a repo that has the files they
mention.

### 2. Real run (once an AI provider is configured)

```sh
export SHIPYARD_GITHUB_TOKEN=...
export SHIPYARD_OPENAI_KEY=...   # or SHIPYARD_XAI_KEY for Grok

# The preset pins the endpoint and default model. The generic variables
# still work for any OpenAI-compatible server: SHIPYARD_AI_ENDPOINT +
# SHIPYARD_AI_KEY

# First check what the agent would do: no commit/push/PR, changes saved
# to a patch file
shipyard solve --repo owner/repo --issue 42 --provider openai --dry-run

# Then deliver for real (override the preset's model if you like); a
# live run needs a guardrail, so the repo allowlist goes along:
shipyard solve --repo owner/repo --issue 42 --provider openai \
  --repos owner/repo
```

The printed pull request URL is where the result is reviewed.

## Development

```sh
go vet ./...
go test ./...
```

`go test` needs no Docker daemon, no container runtime, and no live
model: the tests stub the `pi` binary (a shell script that emits pi's
JSON event stream and behaves per test knobs — `internal/testpi`) and,
for the sandbox wiring, stub the `docker` binary the same way.

The `internal/solve` package includes end-to-end tests that run the
whole flow (clone → agent → commit → push → mock PR creation) with real
git against a local bare "remote", plus failure-mode tests for every
error path above (no usable changes, failed agent run, budget
exhaustion, dirty workdir, …). The sandbox wiring is covered by
end-to-end tests in `internal/listen` that point the flow at the stub
docker binary, and the agent runner itself is covered in
`internal/piagent` (event parsing, budgets, the container path through
the stub docker).

## Project layout

```
cmd/shipyard/          CLI entrypoint (solve and listen commands)
internal/config/       config resolution: flags over env vars
internal/githubclient  GitHub REST client (repo, issues, pull requests)
internal/piagent/      the built-in pi coding agent: runner, event parsing,
                       budgets, the vendored offline agent bundle + Dockerfile
internal/solve/        the solving flow: task → agent → verify → commit → push → PR
internal/sandbox/      ephemeral Docker execution of the agent run (live runs)
internal/guardrails/   safety contract: allowlists, PR cap, run-mode resolution
internal/listen/       listen mode: poll open issues, solve new ones
internal/testpi/       test doubles: stub pi binary and stub docker binary
hack/mockai/           dev-only mock AI endpoint (streaming) for local runs
Dockerfile             multi-stage image: static binary on Alpine, non-root
docker-compose.yml     deployment examples: one-shot solve + long-running listen
```

## Roadmap

See the Shipyard project board: a small Docker image.