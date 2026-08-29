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

## Docker deployment

The repo ships a multi-stage Dockerfile: a full Go toolchain builds a
static binary (`CGO_ENABLED=0`), and the runtime image is a small Alpine
base with just `git` and CA certificates — no build tools, running as the
unprivileged `shipyard` user. The image is designed to stay well under 50 MB
(`git` is the heaviest component); check `docker images shipyard` after a
build.

The image always builds the full repo, so every command and flag that is
merged into `main` (today: `solve`; once SHI-6 lands: `listen`) is included
in a rebuild.

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
  shipyard solve --repo owner/repo --issue 42
```

Or via the bundled compose file (`docker-compose.yml`), which reads the
same environment variables:

```sh
export SHIPYARD_REPO=owner/repo SHIPYARD_ISSUE=42
export SHIPYARD_GITHUB_TOKEN=... SHIPYARD_AI_ENDPOINT=... SHIPYARD_AI_KEY=...
docker compose run --rm solve
# extra flags go after `--`:
docker compose run --rm solve -- --dry-run --include-files path/to/file.go
```

For a custom (unauthenticated) endpoint like `http://localhost:8080` the
`SHIPYARD_AI_KEY` variable may be left empty; provider presets
(`--provider openai|xai`, SHI-10) use the same variables once merged.
`SHIPYARD_GITHUB_API` can point the GitHub API at a GHE instance.

### Listen mode (long-running)

Once [listen mode (SHI-6)](https://github.com/pefman/Shipyard/pull/5) is
merged into `main` and the image rebuilt, the same image can run a
long-lived listener that polls the repo's open issues and solves new ones:

```sh
# compose (state volume keeps track of processed issues across restarts)
docker compose --profile listen up -d

# or plain docker run
docker run -d --name shipyard-listen --restart unless-stopped \
  -v shipyard-state:/data \
  -e SHIPYARD_GITHUB_TOKEN=... -e SHIPYARD_AI_ENDPOINT=... \
  shipyard listen --repo owner/repo --interval 5m
```

The listener keeps its state file in `/data` (override with
`--state-file`); mount a volume there so a container restart does not
re-solve issues. Use `--label` to only solve labeled issues and
`--dry-run` to apply patches without committing or opening PRs.

## Command

### `shipyard solve`

Solves one GitHub issue in a single shot:

```sh
shipyard solve --repo owner/repo --issue 42
```

Flow:

1. Fetches repo info and issue details (title, body, labels) from the
   GitHub API.
2. Ensures a local checkout: `--workdir` if given (must be clean),
   otherwise a clone into a temp directory (the clone URL comes from the
   GitHub API and is authenticated with the GitHub token).
3. Assembles a prompt from the issue plus repo context: the file tree and
   the contents of any `--include-files`.
4. Sends the prompt to the configured AI endpoint and extracts a unified
   diff from the response.
5. Applies the diff to the checkout.
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
| Remote branch name already taken (previous run) | `remote branch … already exists; … pass --branch` |

The raw AI response is saved to a temp file on every run, so a bad model
answer is always inspectable.

## Configuration

Flags take precedence over environment variables.

| Flag              | Environment variable      | Required | Description                                          |
| ----------------- | ------------------------- | -------- | ---------------------------------------------------- |
| `--repo`          | —                         | yes      | GitHub repository, `owner/repo`                      |
| `--issue`         | —                         | yes      | Issue number to solve                                |
| `--github-token`  | `SHIPYARD_GITHUB_TOKEN`   | yes      | GitHub token (classic PAT or fine-grained)           |
| `--ai-endpoint`   | `SHIPYARD_AI_ENDPOINT`    | yes      | AI endpoint base URL, e.g. `https://api.openai.com/v1` |
| `--ai-key`        | `SHIPYARD_AI_KEY`         | yes      | API key for the AI endpoint                          |
| `--ai-model`      | —                         | no       | Model name sent to the endpoint (default `gpt-4o-mini`) |
| `--workdir`       | —                         | no       | Local checkout to build on (default: clone to temp)  |
| `--base`          | —                         | no       | Base branch (default: the repo's default branch)     |
| `--branch`        | —                         | no       | Branch for the fix (default: `shipyard/issue-<n>`)   |
| `--include-files` | —                         | no       | Comma-separated repo files to embed in the prompt    |
| `--git-url`       | —                         | no       | Git clone URL when no `--workdir` is given           |
| `--dry-run`       | —                         | no       | Stop after applying the patch: no commit/push/PR     |

`SHIPYARD_GITHUB_API` (env only) overrides the GitHub API base URL
(default `https://api.github.com`); useful against GHE or in tests.

### GitHub token

Needs `contents: read + write` and `pull-requests: write` on the target
repository (classic PAT: `repo` scope; `public_repo` is not enough to open
PRs). For private repos the token is also used to authenticate the git
clone.

### AI endpoint

Any OpenAI-compatible `/chat/completions` endpoint works —
`POST <endpoint>/chat/completions` with a `Bearer` API key, JSON request
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

shipyard solve --repo <owner>/<test-repo> --issue <n> \
  --github-token "$SHIPYARD_GITHUB_TOKEN" \
  --ai-endpoint http://127.0.0.1:8765/v1 --ai-key anything \
  --dry-run   # try dry-run first; drop it to open a real PR
```

Use a dedicated test repo + a test issue for this: the canned patch only
matches files that exist in that repo.

### 2. Real run (once the AI endpoint is configured)

```sh
export SHIPYARD_GITHUB_TOKEN=...
export SHIPYARD_AI_ENDPOINT=https://api.openai.com/v1   # or any compatible endpoint
export SHIPYARD_AI_KEY=...

# First check what the AI would do: no commit/push/PR, patch saved to a file
shipyard solve --repo owner/repo --issue 42 --dry-run \
  --include-files path/to/affected/file.py

# Then deliver for real
shipyard solve --repo owner/repo --issue 42 --include-files path/to/affected/file.py
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
tests for every error path above.

## Project layout

```
cmd/shipyard/          CLI entrypoint (solve command)
internal/config/       config resolution: flags over env vars
internal/githubclient  GitHub REST client (repo, issues, pull requests)
internal/aiclient/     OpenAI-compatible chat completions client
internal/solve/        the solving flow: prompt → AI → patch → PR
hack/mockai/           dev-only mock AI endpoint for local end-to-end runs
Dockerfile             multi-stage image: static binary on Alpine, non-root
docker-compose.yml     deployment examples: one-shot solve + long-running listen
```

## Roadmap

See the Shipyard project board: listen mode (poll for new issues) and a
small Docker image.