# Shipyard

Shipyard is an AI issue solver for GitHub: it reads a GitHub issue, sends it
to a configurable AI endpoint with repository context, and produces a proposed
fix. Built in Go, single binary, no external runtime dependencies.

## Build

```sh
go build ./...
go build -o shipyard ./cmd/shipyard
```

## Command

### `shipyard solve`

Solves one GitHub issue in a single shot:

```sh
shipyard solve --repo owner/repo --issue 42
```

Flow:

1. Fetches repo info and issue details (title, body, labels) from the
   GitHub API.
2. Assembles a prompt from the issue plus repo context.
3. Sends the prompt to the configured AI endpoint and prints the response
   (the proposed fix) to stdout.

## Configuration

Flags take precedence over environment variables.

| Flag             | Environment variable      | Required | Description                                      |
| ---------------- | ------------------------- | -------- | ------------------------------------------------ |
| `--repo`         | —                         | yes      | GitHub repository, `owner/repo`                  |
| `--issue`        | —                         | yes      | Issue number to solve                            |
| `--github-token` | `SHIPYARD_GITHUB_TOKEN`   | yes      | GitHub token (classic PAT or fine-grained)       |
| `--ai-endpoint`  | `SHIPYARD_AI_ENDPOINT`    | yes      | AI endpoint base URL, e.g. `https://api.openai.com/v1` |
| `--ai-key`       | `SHIPYARD_AI_KEY`         | yes      | API key for the AI endpoint                      |
| `--ai-model`     | —                         | no       | Model name sent to the endpoint (default `gpt-4o-mini`) |

`SHIPYARD_GITHUB_API` (env only) overrides the GitHub API base URL
(default `https://api.github.com`); useful against GHE or in tests.

### GitHub token

Needs at least `public_repo` scope (or repo read on the specific
repository); `repo` scope for private repositories.

### AI endpoint

Any OpenAI-compatible `/chat/completions` endpoint works —
`POST <endpoint>/chat/completions` with a `Bearer` API key, JSON request
`{"model": ..., "messages": [{"role": "user", "content": ...}]}`, and the
first choice's `message.content` is used as the response text.

## Development

```sh
go vet ./...
go test ./...
```

## Project layout

```
cmd/shipyard/        CLI entrypoint (solve command)
internal/config/     config resolution: flags over env vars
internal/githubclient  GitHub REST client (issues, repo info)
internal/aiclient/     OpenAI-compatible chat completions client
```

## Roadmap

See the Shipyard project board: applying generated fixes and opening PRs,
listen mode (poll for new issues), and a small Docker image.