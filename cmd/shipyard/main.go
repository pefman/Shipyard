// Command shipyard is an AI issue solver: it reads a GitHub issue, sends it
// to a configurable AI endpoint with repository context, and prints the
// AI's response (the proposed fix).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/config"
	"github.com/pefman/Shipyard/internal/githubclient"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "solve":
		if err := runSolve(args); err != nil {
			fmt.Fprintln(os.Stderr, "shipyard:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `shipyard - AI issue solver for GitHub

Usage:
  shipyard solve --repo owner/repo --issue <n> [flags]

Solve flags:
  --repo owner/repo      GitHub repository (required)
  --issue <n>            Issue number to solve (required)
  --github-token <t>     GitHub token (env SHIPYARD_GITHUB_TOKEN)
  --ai-endpoint <url>    AI endpoint base URL (env SHIPYARD_AI_ENDPOINT)
  --ai-key <k>           AI API key (env SHIPYARD_AI_KEY)
  --ai-model <m>         Model name sent to the endpoint
`)
}

func runSolve(args []string) error {
	fs := flag.NewFlagSet("solve", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub repository (owner/repo)")
	issue := fs.Int("issue", 0, "issue number to solve")
	githubToken := fs.String("github-token", "", "GitHub token")
	aiEndpoint := fs.String("ai-endpoint", "", "AI endpoint base URL")
	aiKey := fs.String("ai-key", "", "AI API key")
	aiModel := fs.String("ai-model", "", "model name for the AI endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *repo == "" {
		return fmt.Errorf("--repo owner/repo is required")
	}
	if *issue <= 0 {
		return fmt.Errorf("--issue <n> is required")
	}
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("invalid --repo %q: expected owner/repo", *repo)
	}

	cfg, err := config.Load(config.Raw{
		GitHubToken: *githubToken,
		AIEndpoint:  *aiEndpoint,
		AIKey:       *aiKey,
	})
	if err != nil {
		return err
	}

	ctx := context.Background()

	github := githubclient.NewClient(cfg.GitHubAPIRoot, cfg.GitHubToken)
	repoInfo, err := github.GetRepo(ctx, owner, name)
	if err != nil {
		return fmt.Errorf("fetching repo: %w", err)
	}
	issueInfo, err := github.GetIssue(ctx, owner, name, *issue)
	if err != nil {
		return fmt.Errorf("fetching issue: %w", err)
	}

	ai := aiclient.NewClient(cfg.AIEndpoint, cfg.AIKey)
	if *aiModel != "" {
		ai.Model = *aiModel
	}

	fmt.Fprintf(os.Stderr, "solving %s (issue #%d: %s)\n", repoInfo.FullName, issueInfo.Number, issueInfo.Title)
	response, err := ai.Complete(ctx, buildPrompt(repoInfo, issueInfo))
	if err != nil {
		return fmt.Errorf("calling AI endpoint: %w", err)
	}
	fmt.Println(response)
	return nil
}

// buildPrompt assembles the prompt sent to the AI endpoint: the issue
// details plus the repository context the solving flow has so far.
func buildPrompt(repo *githubclient.Repo, issue *githubclient.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an autonomous engineer solving a GitHub issue.\n\n")
	fmt.Fprintf(&b, "Repository: %s\n", repo.FullName)
	fmt.Fprintf(&b, "Issue #%d: %s\n", issue.Number, issue.Title)
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	fmt.Fprintf(&b, "\nIssue body:\n%s\n", issue.Body)
	fmt.Fprintf(&b, "\nRespond with the concrete changes that resolve this issue,")
	fmt.Fprintf(&b, " as a unified diff where possible, plus a short explanation.")
	return b.String()
}
