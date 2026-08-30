package solve

import (
	"fmt"
	"strings"

	"github.com/pefman/Shipyard/internal/githubclient"
)

// BuildTask assembles the task prompt the built-in pi agent works from.
// The agent runs inside the checkout with its own file and shell
// tools, so the task carries the issue (title, body, labels), the
// branch it is on, the environment its changes are verified in, and
// the working contract: change only what the issue requires, verify
// with the repository's own build and tests, and leave the changes in
// the working tree (Shipyard commits and reviews them).
func BuildTask(repo *githubclient.Repo, issue *githubclient.Issue, baseBranch, branch, environment string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an autonomous software engineer working in a git checkout of the repository %s: the repository root is the current working directory, checked out on branch %s (based on %s).\n\n", repo.FullName, branch, orDefault(baseBranch, "its default branch"))
	fmt.Fprintf(&b, "Task: resolve GitHub issue #%d\n", issue.Number)
	fmt.Fprintf(&b, "Title: %s\n", issue.Title)
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	if issue.Body != "" {
		fmt.Fprintf(&b, "\nIssue body:\n%s\n", strings.TrimSpace(issue.Body))
	}
	b.WriteString(`
Your job:
1. Explore the repository as needed (read the relevant files, run commands) to understand the issue.
2. Make the code changes the issue requires — and only those changes.
3. Verify the changes: run this repository's build and test commands and fix what they reveal, iterating until they pass. Suitable commands by language: Go — ` + "`go build ./...`" + ` and ` + "`go test ./...`" + `; Python — compile the affected code (e.g. ` + "`python -m compileall -q .`" + `) and run the project's tests when it has them; Node.js — ` + "`npm install`" + ` and ` + "`npm test`" + ` (when a test script exists); Rust — ` + "`cargo build`" + ` and ` + "`cargo test`" + `.
4. Keep the repository clean for the pull request: before you finish, remove anything your verification created inside it (compiled binaries, node_modules, __pycache__, target/ ...) — the pull request must contain only your intentional source changes. Prefer commands that verify without writing into the repository (e.g. ` + "`go build -o /dev/null ./...`" + `) when you have them.
5. Do not commit, push, or open pull requests. Leave your changes in the working tree: Shipyard commits them and opens a pull request for review.
6. When you are done, write a short summary: what you changed, why, and how you verified it.

Environment: your changes are built and tested in ` + environment + `; write code and commands for that environment.
`)
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
