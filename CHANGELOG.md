# Changelog

Notable changes to Shipyard, newest first.

## Unreleased

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