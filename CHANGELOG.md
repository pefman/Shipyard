# Changelog

Notable changes to Shipyard, newest first.

## Unreleased

### Added (SHI-26)

- `shipyard login`: GitHub OAuth device flow. Passes the verification URI
  and one-time code to the terminal, polls for authorization (honoring
  `slow_down` backoff and the device-code expiry), verifies the token via
  `GET /user`, and stores it at
  `$XDG_CONFIG_HOME/shipyard/credentials.json` (default
  `~/.config/shipyard/credentials.json`) with 0600 permissions, written
  atomically. Re-running `login` with a valid stored token just verifies
  it and exits; `--force` (or an invalid stored token) re-does the flow.
- OAuth App client ID comes from `--github-client-id` /
  `SHIPYARD_GITHUB_CLIENT_ID` — the client ID is never hardcoded in the
  repo.

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