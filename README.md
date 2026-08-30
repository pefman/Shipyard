# Shipyard

Shipyard is an AI issue solver for GitHub: it reads a GitHub issue, sends it
to a configurable AI endpoint with repository context, applies the generated
patch to a local checkout, and opens a pull request that links the source
issue. Built in Go, single binary; the host needs `git` installed (the flow
uses the git CLI for clone / apply / commit / push).

## Build

```sh
go build ./...
go build -o shipyard ./cmd/shipyard
```

## Safety

Shipyard is built to be predictable when unattended: **`listen` is
dry-run by default**. A fresh installation pointed at a repository runs
the full flow (issue → AI → patch → apply) but stops before any commit,
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
unprivileged `shipyard` user. The image is designed to stay well under 50 MB
(`git` is the heaviest component); check `docker images shipyard` after a
build.

The image always builds the full repo, so both commands (`solve` and
`listen`) and every flag that is merged into `main` are included in a
rebuild.

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
  -e SHIPYARD_REPOS=owner/repo \
  shipyard solve --repo owner/repo --issue 42
```

A live `solve` with no allowlist set is refused (see [Safety](#safety)):
pass `--repos`, `--labels`, `--dry-run`, or `--i-know-this-is-unguarded`.

Or via the bundled compose file (`docker-compose.yml`), which reads the
same environment variables:

```sh
export SHIPYARD_REPO=owner/repo SHIPYARD_ISSUE=42
export SHIPYARD_GITHUB_TOKEN=... SHIPYARD_AI_ENDPOINT=... SHIPYARD_AI_KEY=...
export SHIPYARD_REPOS=owner/repo   # guardrail: a live solve needs an allowlist
docker compose run --rm solve
# extra flags go after `--`:
docker compose run --rm solve -- --dry-run --include-files path/to/file.go
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
  -e SHIPYARD_GITHUB_TOKEN=... -e SHIPYARD_AI_ENDPOINT=... \
  shipyard listen --repo owner/repo --interval 5m

# or plain docker run, live with an allowlist
docker run -d --name shipyard-listen --restart unless-stopped \
  -v shipyard-state:/data \
  -e SHIPYARD_GITHUB_TOKEN=... -e SHIPYARD_AI_ENDPOINT=... \
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
fall back to the native fix step, which then executes *inside the
Shipyard container* — fine for most repos, but keep `--dry-run` in mind
if the mounted checkout is precious.

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
3. Assembles a prompt from the issue plus repo context: the file tree and
   the contents of any `--include-files`. On live runs it also tells the
   AI which sandbox image its fix will be built and tested in (see
   [Sandbox](#sandbox)).
4. Sends the prompt to the configured AI endpoint and extracts a unified
   diff from the response.
5. Runs the fix step — applies the diff, then builds and tests it —
   inside a disposable sandbox container on live runs, or natively with
   `--dry-run` (see [Sandbox](#sandbox)).
6. Commits on a new branch (`shipyard/issue-<n>` by default), pushes it,
   and opens a pull request via the GitHub API whose body links the source
   issue.

With `--dry-run`, step 6 stops after the patch is applied: nothing is
committed, pushed, or opened, the workdir is left dirty for inspection,
and the patch plus the raw AI response are saved to temp files whose paths
are printed.

On success stdout prints the pull request URL (or the patch path for
`--dry-run`), so runs are scriptable.

### Failure modes

Shipyard fails fast with actionable errors for the expected failure modes:

| Situation | Error contains |
| --------- | -------------- |
| AI response has no unified diff | `the AI returned no usable changes` |
| Generated patch does not apply | `the generated patch does not apply to <dir>` (patch saved for inspection) |
| Token cannot read/push the repo | `check that the GitHub token can read/push the repo` |
| 401 from the GitHub API | `check that the GitHub token is valid and not expired` |
| 403 from the GitHub API (e.g. opening the PR) | `this token is missing the permissions GitHub needs here (…)` |
| `--workdir` has uncommitted changes | `has uncommitted changes; commit or stash them first` |
| Live run (solve or listen) with no repo/label allowlist set | `no repo or label allowlist is set: this run would be unguarded…` (set an allowlist or pass `--i-know-this-is-unguarded`) |
| Remote branch name already taken (previous run) | `remote branch … already exists; … pass --branch` |
| A build/test step fails in the sandbox (live run) | `fix step failed in sandbox: step … — no commit, push, or PR was made` |
| Docker not installed / daemon down (live run) | `sandbox: off (Docker not available: the fix step runs natively on the host)` |

The raw AI response is saved to a temp file on every run, so a bad model
answer is always inspectable.

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
4. Runs the solving flow (clone → prompt → AI → patch → PR) for each
   remaining issue, each on its own throwaway clone, and records it in
   the state file as soon as it is done.

Behavior notes:

- One issue failing does not stop the pass; it is logged and retried
  on a later pass.
- `SIGINT`/`SIGTERM` shut down gracefully: the in-flight issue finishes
  (or is aborted) and no further issue is picked up.
- The default mode is dry-run: patches are applied but nothing is
  committed and no pull requests are opened; state still records the
  issues (with no PR URL). Pass `--live` to deliver.
- In live mode the first log line is the guardrails audit line
  (allowlists, pull-request budget) so a container's first line is an
  audit line (see [Safety](#safety)).
- Every log line is prefixed with the issue number, so a long-running
  listener is easy to follow (`journalctl`, `docker logs`, …).

Useful flags: `--interval 5m` (default `1m`), `--live` / `--dry-run`
(mode; dry-run is the default), `--repos`, `--labels` / `--label
shipyard`, `--max-prs`, `--i-know-this-is-unguarded`, `--state-file`,
`--base main`, `--git-url`, `--include-files`, `--image` —
plus the same `--provider` / `--ai-endpoint` / `--ai-key` / `--ai-model`
configuration as `solve` (the default `custom` provider needs no key,
so a keyless local endpoint works out of the box).

## Sandbox

Live runs of `solve` and `listen` execute the *fix step* — applying the
AI's patch, then building and testing the result — inside a disposable
Docker container: the checkout is mounted read-write, the steps run in
an ephemeral `docker run`, and the container is discarded when they
finish. Everything else — cloning, the AI call, commit, push, opening
the pull request — runs natively on the host, and `--dry-run` never
touches Docker at all: it applies the patch natively and stops. So
AI-generated code only ever executes inside the throwaway container,
never on your machine.

### Which image

Resolution order:

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
   | none of the above | `ubuntu:24.04` | the patch applies only (the image must provide `git`) |

Unknown images (custom `--image` values) run the patch-apply step only.
The image is announced to the AI in the prompt, so the generated code
targets that toolchain. If a verification step fails, the run stops
before commit, push, and pull request: a patch that does not build or
test cleanly never opens a PR.

### Docker requirement

Docker (CLI plus a running daemon) is required **only for live runs**;
`--dry-run` works with zero dependencies. A live run that cannot reach
Docker falls back to the native fix step exactly as before. Every run
prints an audit line up front:

- `sandbox: <image> (source: flag|auto|config)` — live run, fix step
  runs in the container;
- `sandbox: off (Docker not available: …)` — live run, native fallback;
- `sandbox: off (dry-run)` — dry run, nothing runs.

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
| `--ai-endpoint`   | `SHIPYARD_AI_ENDPOINT`    | yes for `custom` | AI endpoint base URL; `openai`/`xai` use their preset base URLs |
| `--ai-key`        | `SHIPYARD_AI_KEY` (also `SHIPYARD_OPENAI_KEY` / `SHIPYARD_XAI_KEY`) | yes for `openai`/`xai` | API key for the AI endpoint |
| `--ai-model`      | `SHIPYARD_AI_MODEL`       | no       | Model name (defaults: `gpt-5.6-sol` for `openai`, `grok-4.6` for `xai`) |
| `--workdir`       | —                         | no       | Local checkout to build on (default: clone to temp)  |
| `--base`          | —                         | no       | Base branch (default: the repo's default branch)     |
| `--branch`        | —                         | no       | Branch for the fix (default: `shipyard/issue-<n>`)   |
| `--include-files` | —                         | no       | Comma-separated repo files to embed in the prompt    |
| `--git-url`       | —                         | no       | Git clone URL when no `--workdir` is given           |
| `--dry-run`       | —                         | no       | `solve`: stop after applying the patch (no commit/push/PR); `listen`: dry-run mode — the default for `listen` |
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
  endpoint such as `http://localhost:8080` needs none.

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

Any OpenAI-compatible `/chat/completions` endpoint works —
`POST <endpoint>/chat/completions` with a `Bearer` API key when a key is
configured (a keyless `custom` endpoint sends no Authorization header),
JSON request
`{"model": ..., "messages": [{"role": "user", "content": ...}]}`, and the
first choice's `message.content` is used as the response text. The response
should contain a unified diff in a fenced code block; anything else is
extracted best-effort and, if no diff is found, the run fails with a clear
error.

## End-to-end test runs

### 1. Without an AI key (mock endpoint, real GitHub)

The repo ships a tiny mock of an OpenAI-compatible endpoint
(`hack/mockai`) that answers with a canned response:

```sh
# canned response: explanation + one ```diff block
cat > /tmp/canned.txt <<'EOF'
Guarded the empty-input case in greet().

```diff
diff --git a/hello.py b/hello.py
--- a/hello.py
+++ b/hello.py
@@ -1,2 +1,4 @@
 def greet(name):
+    if not name:
+        return "Hello, stranger"
     return "Hello, " + name
```
EOF

go run ./hack/mockai --port 8765 --response-file /tmp/canned.txt &

# `custom` providers need no key: the mock is just another OpenAI-compatible
# server
shipyard solve --repo <owner>/<test-repo> --issue <n> \
  --github-token "$SHIPYARD_GITHUB_TOKEN" \
  --provider custom --ai-endpoint http://127.0.0.1:8765/v1 \
  --dry-run   # try dry-run first; to open a real PR drop it and add a
              # guardrail (e.g. --repos <owner>/<test-repo>)
```

Use a dedicated test repo + a test issue for this: the canned patch only
matches files that exist in that repo.

### 2. Real run (once an AI provider is configured)

```sh
export SHIPYARD_GITHUB_TOKEN=...
export SHIPYARD_OPENAI_KEY=...   # or SHIPYARD_XAI_KEY for Grok

# The preset pins the endpoint and default model. The generic variables
# still work for any OpenAI-compatible server: SHIPYARD_AI_ENDPOINT +
# SHIPYARD_AI_KEY

# First check what the AI would do: no commit/push/PR, patch saved to a file
shipyard solve --repo owner/repo --issue 42 --provider openai --dry-run \
  --include-files path/to/affected/file.py

# Then deliver for real (override the preset's model if you like); a
# live run needs a guardrail, so the repo allowlist goes along:
shipyard solve --repo owner/repo --issue 42 --provider openai \
  --repos owner/repo \
  --include-files path/to/affected/file.py
```

The printed pull request URL is where the result is reviewed.

## Development

```sh
go vet ./...
go test ./...
```

The `internal/solve` package includes end-to-end tests that run the whole
flow (clone → prompt → mock AI → `git apply` → commit → push → mock PR
creation) with real git against a local bare "remote", plus failure-mode
tests for every error path above. The sandbox wiring is covered by
end-to-end tests in `internal/listen` that stub the `docker` binary on
PATH, so `go test` needs neither a Docker daemon nor a container
runtime.

## Project layout

```
cmd/shipyard/          CLI entrypoint (solve and listen commands)
internal/config/       config resolution: flags over env vars
internal/githubclient  GitHub REST client (repo, issues, pull requests)
internal/aiclient/     OpenAI-compatible chat completions client
internal/solve/        the solving flow: prompt → AI → patch → fix step → PR
internal/sandbox/      ephemeral Docker execution of the fix step (live runs)
internal/guardrails/   safety contract: allowlists, PR cap, run-mode resolution
internal/listen/       listen mode: poll open issues, solve new ones
hack/mockai/           dev-only mock AI endpoint for local end-to-end runs
Dockerfile             multi-stage image: static binary on Alpine, non-root
docker-compose.yml     deployment examples: one-shot solve + long-running listen
```

## Roadmap

See the Shipyard project board: a small Docker image.