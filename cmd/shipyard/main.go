// Command shipyard is an AI issue solver: it reads a GitHub issue, sends
// it to a configurable AI endpoint with repository context, applies the
// generated patch to a local checkout, and opens a pull request that
// links the source issue.
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
	"github.com/pefman/Shipyard/internal/solve"
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
	case "listen":
		if err := runListen(args); err != nil {
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
  shipyard listen --repo owner/repo [flags]

Solve flags:
  --repo owner/repo      GitHub repository (required)
  --issue <n>            Issue number to solve (required)
  --github-token <t>     GitHub token (env SHIPYARD_GITHUB_TOKEN)
  --provider <name>      AI provider: openai, xai, or custom (env SHIPYARD_AI_PROVIDER;
                         default custom: --ai-endpoint required, key optional)
  --ai-endpoint <url>    AI endpoint base URL (env SHIPYARD_AI_ENDPOINT; defaults per provider)
  --ai-key <k>           AI API key (env SHIPYARD_AI_KEY; also SHIPYARD_OPENAI_KEY / SHIPYARD_XAI_KEY)
  --ai-model <m>         Model name sent to the endpoint (env SHIPYARD_AI_MODEL;
                         defaults: openai gpt-5.6-sol, xai grok-4.6)
  --workdir <dir>        Local checkout to build on (default: clone to a temp dir)
  --base <branch>        Base branch (default: the repo's default branch)
  --branch <name>        Branch for the fix (default: shipyard/issue-<n>)
  --include-files <list> Comma-separated files to embed in the prompt
  --git-url <url>        Git clone URL (with --workdir unset; default from the API)
  --dry-run              Stop after applying the patch: no commit, push, or PR

Listen flags:
  --repo owner/repo      GitHub repository to watch (required)
  --interval <dur>       Delay between poll passes (default 1m)
  --label <name>         Only solve issues carrying this label (repeatable)
  --state-file <path>    File tracking processed issues (default: shipyard-listen-state.json)
  --github-token <t>     GitHub token (env SHIPYARD_GITHUB_TOKEN)
  --provider <name>      AI provider: openai, xai, or custom (env SHIPYARD_AI_PROVIDER;
                         default custom: --ai-endpoint required, key optional)
  --ai-endpoint <url>    AI endpoint base URL (env SHIPYARD_AI_ENDPOINT; defaults per provider)
  --ai-key <k>           AI API key (env SHIPYARD_AI_KEY; also SHIPYARD_OPENAI_KEY / SHIPYARD_XAI_KEY)
  --ai-model <m>         Model name sent to the endpoint (env SHIPYARD_AI_MODEL;
                         defaults: openai gpt-5.6-sol, xai grok-4.6)
  --base <branch>        Base branch (default: the repo's default branch)
  --git-url <url>        Git clone URL for the per-issue checkout
  --include-files <list> Comma-separated files to embed in the prompt
  --dry-run              Apply patches but commit nothing and open no pull requests
`)
}

func runSolve(args []string) error {
	fs := flag.NewFlagSet("solve", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub repository (owner/repo)")
	issue := fs.Int("issue", 0, "issue number to solve")
	githubToken := fs.String("github-token", "", "GitHub token")
	aiProvider := fs.String("provider", "", "AI provider: openai, xai, or custom")
	aiEndpoint := fs.String("ai-endpoint", "", "AI endpoint base URL")
	aiKey := fs.String("ai-key", "", "AI API key")
	aiModel := fs.String("ai-model", "", "model name for the AI endpoint")
	workdir := fs.String("workdir", "", "local checkout to build on (default: clone)")
	base := fs.String("base", "", "base branch (default: repo default branch)")
	branch := fs.String("branch", "", "branch for the fix (default: shipyard/issue-<n>)")
	includeFiles := fs.String("include-files", "", "comma-separated files to embed in the prompt")
	gitURL := fs.String("git-url", "", "git clone URL (with --workdir unset)")
	dryRun := fs.Bool("dry-run", false, "stop after applying the patch: no commit, push, or PR")
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
		Provider:    *aiProvider,
		AIEndpoint:  *aiEndpoint,
		AIKey:       *aiKey,
		AIModel:     *aiModel,
	})
	if err != nil {
		return err
	}

	var files []string
	for _, f := range strings.Split(*includeFiles, ",") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}

	ai := aiclient.NewClient(cfg.AIEndpoint, cfg.AIKey, cfg.AIModel)

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(cfg.GitHubAPIRoot, cfg.GitHubToken),
		AI:     ai,
	}, solve.Options{
		Owner:        owner,
		Repo:         name,
		IssueNumber:  *issue,
		Workdir:      *workdir,
		GitURL:       *gitURL,
		Base:         *base,
		Branch:       *branch,
		IncludeFiles: files,
		DryRun:       *dryRun,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: branch %s on %s\n", res.Branch, res.Workdir)
	fmt.Fprintln(os.Stderr, "patch: "+res.PatchPath)
	fmt.Fprintln(os.Stderr, "AI response: "+res.ResponsePath)
	if res.PR != nil {
		fmt.Printf("\nPull request opened: %s (#%d)\n", res.PR.HTMLURL, res.PR.Number)
	}
	// Also print to stdout so the result is machine-usable: the PR URL
	// (real runs) or the patch path (dry runs).
	if res.PR != nil {
		fmt.Println(res.PR.HTMLURL)
	} else {
		fmt.Println(res.PatchPath)
	}
	return nil
}
