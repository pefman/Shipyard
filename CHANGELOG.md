# Changelog

Notable changes to Shipyard, newest first.

## Unreleased

### Fixed (SHI-48)

- **Host-local AI endpoints are reachable from the sandbox**: a
  sandbox run with `--ai-endpoint` on a host-loopback address
  (`localhost`, `127.0.0.1` / `127.0.0.0/8`, `::1`) now remaps the
  endpoint to `host.docker.internal` for the container run (a log line
  announces it), every sandbox container starts with
  `--add-host=host.docker.internal:host-gateway` so the name resolves
  on all Docker platforms, and when an endpoint was remapped the
  container run starts with a short-timeout TCP probe **from inside
  the container** to that address — the same address the agent's model
  calls use, so the check and the run agree by construction. When the
  host-local model server is unreachable (e.g. it bound `127.0.0.1`
  only), the run fails fast before the agent's first model call, with
  an error naming the exact address used and the non-loopback bind
  requirement (`--host 0.0.0.0`) instead of every model call ending in
  `Connection error` and the run ending `the repository is unchanged`.
  Non-loopback endpoints (public URLs, LAN addresses, explicit
  `host.docker.internal`) pass through unchanged; native (no-Docker)
  runs use the configured endpoint as-is, where `localhost` is the
  host. The probe's failure message also names the rootless-podman
  alternatives (`host.containers.internal`, `--net=host`).
- Docs: a new README section, [Local model endpoints](README.md),
  documents the host-reachability requirement; the failure-modes table
  and the `--ai-endpoint` help text note the remap.

### Fixed (SHI-47, follow-up to the review of PR #21)

- **Data race in the native (no-Docker) agent run**: the run's stdout/
  stderr pumps are now joined before the exit code is returned, so the
  event sink's state (turn count, final summary, budget flags) is fully
  settled before the flow reads it — a budget stop can no longer land a
  beat late and turn a budget-exhausted run into a "successful" one.
  `go test -race ./...` is now green.
- **Parent cancellation is no longer reported as budget exhaustion**:
  stopping the listener (`SIGINT`/`SIGTERM`) surfaces the cancellation
  itself (the in-flight issue is left for the restart) instead of
  claiming the wall-clock budget ran out.
- **Test log buffers are synchronized again** (the container path logs
  from several pumps at once); the production loggers were already
  safe.
- Docs: the README's host-requirements claim is scoped to sandboxed
  runs (native runs need `pi` on the host), the failure-mode table
  quotes the actual `the agent made no usable changes` message, the
  wrapper-image build is described as logged via the run's own
  `agent: running …` line, and the `--agent-timeout` clock is noted to
  include the first run's wrapper-image build on a cold host.

### Added (SHI-47, follow-up to the review of PR #21)

- `--agent-context-window` (on both `solve` and `listen`, env
  `SHIPYARD_AGENT_CONTEXT_WINDOW`, default 128000): the context window
  (tokens) declared for the model in the agent's `models.json`. The
  agent compacts its context off this *declared* size, so a local model
  with a smaller real window (e.g. a 32k local inference model) must
  declare it — otherwise a big repo or issue overflows the model's real
  context before compaction ever kicks in. The default suits large
  models; set the flag to your local model's real window.

### Changed (SHI-47)

- **The solving engine is now the built-in pi coding agent** — no more
  one-shot "prompt → diff" completion. Shipyard writes the issue (title,
  labels, body, branch, environment) and the model configuration into
  the checkout's agent directory (`.shipyard-pi/`, git-excluded) and
  runs pi against the configured OpenAI-compatible endpoint. The agent
  reads and edits files itself, runs the repository's build and test
  commands, and iterates until they pass; Shipyard then commits the
  agent's work, pushes, and opens the pull request. The pi runtime is
  **built into Shipyard** (vendored offline in the repo) and runs in
  the sandbox container via a wrapper image over the language image
  (`shipyard-sandbox/<base>:pi-<version>`, auto-built on first use);
  without Docker the agent runs natively, which needs the `pi` binary
  on the host.
- Progress streams into shipyard's log per issue (`issue #42: agent: …`):
  turns, tool calls, the model's text, endpoint trouble (with pi's
  automatic retries), and compaction events. `--verbose` now adds the
  agent's raw event lines (replacing the one-shot conversation dump).
- **Agent budgets**: `--agent-max-turns` (env `SHIPYARD_AGENT_MAX_TURNS`,
  default 30) caps the agent's assistant turns per issue and
  `--agent-timeout` (env `SHIPYARD_AGENT_TIMEOUT`, default 30m) caps
  its wall clock; hitting either stops the run before any commit, push,
  or PR.
- **Dry runs now run the agent in the sandbox whenever Docker is
  available** (dry runs included: the agent executes code, so the
  container is the safe choice). A dry run still commits, pushes, and
  opens nothing.
- Failure modes changed with the engine: `the agent returned no usable
  changes` (agent left the tree untouched) and `agent stopped: budget
  exhausted` replace the patch-extraction failures; a failing
  verification step reports `verify step N of M … exited C`.
- `hack/mockai` now streams (SSE) like a real model, shaped for the
  agent's streaming client; its canned response is agent instructions,
  not a diff.
- Docs: README rewritten around the built-in agent (sandbox section now
  covers the wrapper image and native runs; the failure-mode table and
  the end-to-end test-run recipe are updated).

### Removed (SHI-47)

- `--include-files`: the agent reads the repository itself; there is no
  file tree to attach to a prompt.
- The one-shot AI call and its client (`internal/aiclient`): the
  OpenAI-compatible endpoint is now consumed by the built-in agent,
  not by a bespoke completions client.

### Added (SHI-47)

- `internal/piagent`: the built-in agent runner — task/config
  preparation, the pi invocation, JSON event parsing and rendering,
  turn/wall-clock budgets, and the container path (wrapper image over
  the language image, artifact-free post-verification steps). The
  vendored offline pi bundle (package, lockfile, npm cache, launcher,
  Dockerfile) ships with the module.
- `internal/testpi`: test doubles for the agent flow — a stub `pi`
  binary that emits pi's JSON event stream (modes: fix/noop/fail, turn
  and sleep knobs) and the stub `docker` binary the sandbox e2e tests
  already used. `go test ./...` still needs no Docker daemon, no
  container runtime, and no live model.

### Added (SHI-46)

- `--verbose` (on both `solve` and `listen`, env `SHIPYARD_VERBOSE=1`):
  log the full AI conversation, so a weak or local model can be debugged
  from the log alone. Per AI call: the model name and the full prompt
  sent, the full response content, the `reasoning_content`/thinking block
  when the endpoint returns one, and the call's HTTP status, latency, and
  `finish_reason` — a `length` finish is announced explicitly as
  *response truncated by token limit*. Off by default (no behavior change
  when off); secret redaction still applies; extremely long content (over
  256 KiB) is rendered as size plus first/last lines, omission announced,
  never silent.

### Fixed (SHI-46, follow-up to the review of #19)

- Verbose diagnostics are now logged for a **failed** AI call too
  (HTTP 500 from a local endpoint, a non-JSON body, …): the
  `AI response: HTTP 500 in 2.3s; …` line lands in the log before the
  run aborts. A transport-level failure (no response at all) announces
  itself instead of printing `HTTP 0 in 0s`.
- The verbose rendering of an oversized payload with too few lines to
  summarize line-wise (a wall of newline-free prose — exactly what weak
  local models emit) now falls back to a byte-based head/tail slice
  (first/last 2 KiB, omission announced) instead of dropping the
  content entirely.

### Added (SHI-44)

- `--all` (on both `solve` and `listen`): "no allowlist on this axis,
  on purpose." It marks the repo/label allowlist axis as explicitly
  unrestricted, so a live run with no `--repos`/`--labels` allowlist
  starts without the `--i-know-this-is-unguarded` sentence-flag, and
  the startup audit line says so —
  `live mode: guardrails: NONE (explicit --all); max-prs: N` — so a
  long-running container's first log line never hides that the run is
  unguarded. `--all` combined with a set `--repos`/`--labels` allowlist
  is a configuration error (same treatment as `--live` + `--dry-run`),
  and `--i-know-this-is-unguarded` remains as a hidden alias of `--all`.

### Changed (SHI-19)

- **`listen` now starts in dry-run mode by default** — the safe default
  for unattended operation: it runs the full solving flow but commits
  nothing and opens no pull requests. Pass `--live` (or
  `SHIPYARD_MODE=live`) to deliver; `--dry-run` explicitly forces
  dry-run, takes precedence over `SHIPYARD_MODE`, and conflicts with
  `--live`. The one-shot `solve` command is unaffected by this: it runs
  live unless you pass `--dry-run` (the guardrail gate from SHI-18
  still applies to it).
- A live `listen` prints a startup audit line — a long-running
  container's first log line — confirming the active guardrails
  (allowlists, pull-request budget), so the deployed configuration is
  visible at a glance.
- Docs pass (SHI-20): README "Safety" section finalized; the deploy
  examples in `docker-compose.yml` (including the new `listen-live`
  profile) and the Docker run snippets now show the safe default
  configuration — a dry-run listener, and an allowlist set explicitly
  for live runs.

### Added (SHI-18)

- Guardrails for safe unattended operation, on both `solve` and
  `listen`:
  - `--repos` (env `SHIPYARD_REPOS`), a comma-separated `owner/repo`
    allowlist, and `--labels` (env `SHIPYARD_LABELS`), a label
    allowlist. When either is set, only issues in allowed repos
    carrying an allowed label are solved; `listen` additionally
    refuses to start on a repo that is not on the list. On `listen`,
    the repeatable `--label` flag is an equivalent label allowlist.
  - `--max-prs <n>` (env `SHIPYARD_MAX_PRS`, default 3): a hard
    per-run budget. When the budget is spent, `listen` stops the run
    with a clean exit and a summary; the issues it did not get to stay
    open for a later run. `--max-prs 0` is a dry-run setting, not a
    live one.
  - `--i-know-this-is-unguarded`: a run with neither allowlist set is
    unguarded — it may act on any issue in the repository — and is
    refused unless this flag acknowledges the risk. Guarded runs print
    a `shipyard: guardrails: …; max-prs: N` audit line at startup; a
    dry run never counts against the pull-request budget.

### Added (SHI-33)

- `solve` and `listen` now run the fix step — applying the AI's patch,
  then building and testing it — inside a disposable Docker container
  on live runs: AI-generated code no longer executes on the host. The
  rest of the flow (clone, AI call, commit, push, PR) stays native, and
  `--dry-run` never touches Docker. A live run without Docker available
  falls back to the native path, exactly as before.
- New `--image <image>` flag on `solve` and `listen` to pick the sandbox
  image. Default: auto-detection from the repository contents
  (`go.mod` → `golang:1.22`, `pyproject.toml`/`requirements.txt` →
  `python:3.12`, `package.json` → `node:20`, `Cargo.toml` → `rust:1.79`,
  otherwise `ubuntu:24.04` with patch-apply only). The per-repo setting
  will slot in here when `shipyard.yaml` lands — no rework needed.
- The AI prompt now names the image the fix will be built and tested in
  (live runs), so generated code targets that toolchain.
- Every run prints an audit line: `sandbox: <image> (source:
  flag|auto|config)` (live, sandboxed), `sandbox: off (Docker not
  available: …)` (live, native fallback), or `sandbox: off (dry-run)`.
- A failed build/test step in the sandbox stops the run before commit,
  push, and pull request: a patch that does not pass verification never
  opens a PR.
### Added (SHI-27)

- Stored login is now used across every command: the GitHub token resolves
  as `--github-token` flag > `SHIPYARD_GITHUB_TOKEN` env > the token
  stored by `shipyard login` (first non-empty wins). With no token
  anywhere, the "missing required configuration" error now points at
  `shipyard login`.
- `shipyard whoami`: shows which identity the token precedence resolves to
  (`@login` — the stored username, or `GET /user` for flag/env tokens) and
  the stored refresh-token expiry when that metadata exists. Clear
  "not logged in" error when no token is available.
- `shipyard logout`: removes the stored credentials file (clear error when
  none is stored).

### Docs (SHI-27)

- README: new "Authentication" section covering `login` / `whoami` /
  `logout` and the flag > env > stored token precedence; configuration
  table notes that `--github-token` is only required when not logged in.

### Added (SHI-30)

- `--repo` (on `solve` and `listen`) now accepts every common GitHub
  repository spelling, normalized by one shared `repo.Normalize`: bare
  `owner/repo`, `https://github.com/owner/repo`, `git@github.com:owner/repo`,
  `ssh://git@github.com/owner/repo`, and `github.com/owner/repo`. A trailing
  `.git` is stripped and the host is matched case-insensitively. Unrecognized
  input errors out with the accepted forms listed; hosts other than
  `github.com` are rejected.

### Added (SHI-29)

- Built-in default GitHub OAuth App client ID (`Iv23lipRhtA8srclwbp3`) for `shipyard
  login`: login now works with zero configuration, exactly like
  `gh auth login`. Precedence: `--github-client-id` flag >
  `SHIPYARD_GITHUB_CLIENT_ID` env > built-in default, so anyone with their
  own OAuth App can still override.

### Added (SHI-26)

- `shipyard login`: GitHub OAuth device flow. Passes the verification URI
  and one-time code to the terminal, polls for authorization (honoring
  `slow_down` backoff and the device-code expiry), verifies the token via
  `GET /user`, and stores it at
  `$XDG_CONFIG_HOME/shipyard/credentials.json` (default
  `~/.config/shipyard/credentials.json`) with 0600 permissions, written
  atomically. Re-running `login` with a valid stored token just verifies
  it and exits; `--force` (or an invalid stored token) re-does the flow.
- OAuth App client ID: `--github-client-id` / `SHIPYARD_GITHUB_CLIENT_ID`
  override the built-in pre-registered app (added by SHI-29).

### Fixed (SHI-26)

- Device-code deadline now reads GitHub's real `expires_in` field (the
  original code decoded an `expiration` field GitHub never sends, so the
  deadline collapsed to "now" and login could never succeed against real
  GitHub); the wire contract is pinned by a test that feeds the literal
  GitHub-shaped response body.
- A corrupt/unreadable stored credentials file now prints a one-line
  warning before falling back to the device flow, instead of doing so
  silently.
- The refresh-token expiry metadata is no longer invented: GitHub reports
  no refresh-token expiry in the device flow, so the field is left unset.
- Token polling now accepts the poll outcome in GitHub's real OAuth-spec
  `error` field (previously only the documented `code` field was
  recognized, so a live `authorization_pending` response was treated as
  fatal and the flow terminated before the user could authorize;
  `authorization_pending` and `slow_down` are non-terminal again,
  whatever field they arrive in).
- Ctrl-C / SIGTERM during the wait now exits cleanly with
  "shipyard: login interrupted" instead of a token-poll error.

### Added (SHI-10)

- Provider presets for the AI endpoint: `--provider` / `SHIPYARD_AI_PROVIDER`
  with `openai` (ChatGPT, base `https://api.openai.com/v1`, default model
  `gpt-5.6-sol`), `xai` (Grok, base `https://api.x.ai/v1`, default model
  `grok-4.6`), and `custom` (default; an explicit `--ai-endpoint` is
  required, no key needed).
- Model selection: `--ai-model` / `SHIPYARD_AI_MODEL` overrides the
  preset's default model; the client no longer hardcodes a single model.
- Provider key env vars: `SHIPYARD_OPENAI_KEY` (for `openai`) and
  `SHIPYARD_XAI_KEY` (for `xai`). The generic `--ai-key` /
  `SHIPYARD_AI_KEY` still works for every provider, and `custom` providers
  need no key at all (e.g. a local `http://localhost:8080` endpoint).

### Docs (SHI-11)

- README: "AI providers" section with the three presets, key resolution
  order, and copy-pasteable examples per provider (including the keyless
  `custom` case); configuration table updated — `--ai-endpoint` is only
  required for `custom`, and no key is required there; stale references to
  the hardcoded model default and the mandatory-key rule removed.
- This changelog.