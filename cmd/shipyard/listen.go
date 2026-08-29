package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/config"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/listen"
)

// stringFlag collects a repeatable string flag (--label a --label b).
type stringFlag []string

func (s *stringFlag) String() string { return strings.Join(*s, ",") }
func (s *stringFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub repository to watch (owner/repo)")
	interval := fs.Duration("interval", listen.DefaultInterval, "delay between poll passes")
	labels := &stringFlag{}
	fs.Var(labels, "label", "only solve issues carrying this label (repeatable)")
	stateFile := fs.String("state-file", listen.DefaultStateFile, "file tracking processed issues")
	githubToken := fs.String("github-token", "", "GitHub token")
	aiProvider := fs.String("provider", "", "AI provider: openai, xai, or custom")
	aiEndpoint := fs.String("ai-endpoint", "", "AI endpoint base URL")
	aiKey := fs.String("ai-key", "", "AI API key")
	aiModel := fs.String("ai-model", "", "model name sent to the endpoint")
	base := fs.String("base", "", "base branch for fixes (default: the repo's default branch)")
	gitURL := fs.String("git-url", "", "git clone URL for the per-issue checkout (default from the API)")
	includeFiles := fs.String("include-files", "", "comma-separated files to embed in the prompt")
	dryRun := fs.Bool("dry-run", false, "apply patches but commit nothing and open no pull requests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *repo == "" {
		return fmt.Errorf("--repo owner/repo is required")
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

	// Graceful shutdown: SIGINT/SIGTERM cancel the context, the current
	// issue finishes (or is aborted mid-solve), and Run returns.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "shipyard: ", log.LstdFlags|log.Lmsgprefix)
	if err := (&listen.Deps{
		GitHub: githubclient.NewClient(cfg.GitHubAPIRoot, cfg.GitHubToken),
		AI:     ai,
		Log:    logger.Printf,
	}).Run(ctx, listen.Options{
		Owner:        owner,
		Repo:         name,
		StateFile:    *stateFile,
		Interval:     *interval,
		Labels:       *labels,
		Base:         *base,
		GitURL:       *gitURL,
		IncludeFiles: files,
		DryRun:       *dryRun,
	}); err != nil {
		return err
	}
	return nil
}
