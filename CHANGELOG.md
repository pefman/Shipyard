# Changelog

Notable changes to Shipyard, newest first.

## Unreleased

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