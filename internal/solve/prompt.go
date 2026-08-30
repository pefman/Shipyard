package solve

import (
	"fmt"
	"strings"

	"github.com/pefman/Shipyard/internal/githubclient"
)

const (
	// maxTreeLines caps how many files from git ls-files go into the
	// prompt, so huge repos cannot blow up the context window.
	maxTreeLines = 500
)

// BuildPrompt assembles the prompt sent to the AI endpoint: the issue
// (title, body, labels) plus the repository context (file tree and the
// contents of explicitly included files), the environment the fix will
// be verified in (the sandbox image, on live runs), and the response
// contract.
func BuildPrompt(repo *githubclient.Repo, issue *githubclient.Issue, baseBranch, fileTree string, fileContents map[string]string, sandboxImage string) string {
	var b strings.Builder
	b.WriteString("You are an autonomous engineer solving a GitHub issue in this repository.\n\n")
	fmt.Fprintf(&b, "Repository: %s\n", repo.FullName)
	if baseBranch != "" {
		fmt.Fprintf(&b, "Base branch: %s\n", baseBranch)
	}
	fmt.Fprintf(&b, "Issue #%d: %s\n", issue.Number, issue.Title)
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	if issue.Body != "" {
		fmt.Fprintf(&b, "\nIssue body:\n%s\n", issue.Body)
	}
	tree := capLines(fileTree, maxTreeLines)
	if tree != "" {
		fmt.Fprintf(&b, "\nFiles in the repository (as of the base branch):\n%s\n", tree)
	}
	for _, path := range sortedKeys(fileContents) {
		fmt.Fprintf(&b, "\nFile: %s\n```\n%s\n```\n", path, fileContents[path])
	}
	if sandboxImage != "" {
		fmt.Fprintf(&b, "\nEnvironment: your fix will be built and tested in a disposable container running the %s image; write code and any commands for that toolchain.\n", sandboxImage)
	}
	b.WriteString(`
Your response:
1. A brief explanation of the fix (2-5 sentences).
2. ONE unified diff (git diff format) with all the changes that resolve
   the issue, inside a fenced code block tagged "diff". The diff must
   apply cleanly to the repository as of the base branch and must not
   include unrelated changes. If you need to add a new file, include it
   in the same diff (with /dev/null as the source path).
`)
	return b.String()
}

func capLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n... (%d more files)", len(lines)-n)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort: the map is small (user-provided list).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
