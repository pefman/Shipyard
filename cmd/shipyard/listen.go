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

	"github.com/pefman/Shipyard/internal/config"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/guardrails"
	"github.com/pefman/Shipyard/internal/listen"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/repo"
)

// stringFlag collects a repeatable string flag (--label a --label b).
type stringFlag []string

func (s *stringFlag) String() string { return strings.Join(*s, ",") }
func (s *stringFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// listenRun is the result of prepareListen: the resolved collaborators
// and loop options, ready to hand to listen.Deps.Run.
type listenRun struct {
	GitHub  *githubclient.Client
	Options listen.Options
}

func runListen(args []string) error {
	prepare, err := prepareListen(args)
	if err != nil {
		return err
	}

	// Graceful shutdown: SIGINT/SIGTERM cancel the context, the current
	// issue finishes (or is aborted mid-solve), and Run returns.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "shipyard: ", log.LstdFlags|log.Lmsgprefix)
	return (&listen.Deps{
		GitHub: prepare.GitHub,
		Log:    logger.Printf,
	}).Run(ctx, prepare.Options)
}

// prepareListen parses the listen flags and resolves configuration and
// guardrails without touching the network, so the mapping from flags and
// environment to listen.Options is directly testable: whatever ends up
// in Options is exactly what the loop's own guardrail gate sees.
func prepareListen(args []string) (*listenRun, error) {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	repoFlag := fs.String("repo", "", "GitHub repository to watch (owner/repo or a github.com URL); see usage")
	interval := fs.Duration("interval", listen.DefaultInterval, "delay between poll passes")
	label := &stringFlag{}
	fs.Var(label, "label", "only solve issues carrying this label (repeatable)")
	stateFile := fs.String("state-file", listen.DefaultStateFile, "file tracking processed issues")
	githubToken := fs.String("github-token", "", "GitHub token (env SHIPYARD_GITHUB_TOKEN, else the token stored by shipyard login)")
	aiProvider := fs.String("provider", "", "AI provider: openai, xai, or custom")
	aiEndpoint := fs.String("ai-endpoint", "", "AI endpoint base URL")
	aiKey := fs.String("ai-key", "", "AI API key")
	aiModel := fs.String("ai-model", "", "model name for the AI endpoint")
	base := fs.String("base", "", "base branch for fixes (default: the repo's default branch)")
	gitURL := fs.String("git-url", "", "git clone URL for the per-issue checkout (default from the API)")
	image := fs.String("image", "", "language image the sandbox container is built on (default: auto-detect)")
	agentMaxTurns := fs.Int("agent-max-turns", -1, "cap the agent's assistant turns per issue (env SHIPYARD_AGENT_MAX_TURNS; default 30)")
	agentTimeout := fs.Duration("agent-timeout", 0, "cap the agent run's wall clock per issue (env SHIPYARD_AGENT_TIMEOUT; default 30m)")
	agentContextWindow := fs.Int("agent-context-window", 0, "context window (tokens) to declare for the model (env SHIPYARD_AGENT_CONTEXT_WINDOW; default 128000 — set it for local models with a smaller real window)")
	live := fs.Bool("live", false, "live mode: commit the agent's work, push, and open pull requests (env SHIPYARD_MODE=live)")
	dryRun := fs.Bool("dry-run", false, "dry-run mode (the default for listen): the agent's work is kept but nothing is committed and no pull requests are opened")
	repos := fs.String("repos", "", "repository allowlist, comma-separated owner/repo (env SHIPYARD_REPOS)")
	labelsStr := fs.String("labels", "", "label allowlist, comma-separated (env SHIPYARD_LABELS; --label is an equivalent flag)")
	maxPRs := fs.Int("max-prs", -1, "stop after opening this many pull requests (env SHIPYARD_MAX_PRS; default 3)")
	verbose := fs.Bool("verbose", verboseFromEnv(), "log the agent's raw event lines per issue in addition to the rendered progress (env SHIPYARD_VERBOSE=1)")
	all := fs.Bool("all", false, "run with no repo/label allowlist, on purpose (explicitly unrestricted)")
	unguarded := fs.Bool("i-know-this-is-unguarded", false, "hidden alias of --all: proceed even with no repo/label allowlist set (live runs)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Listen is dry-run by default: a fresh installation pointed at a
	// repo must not start opening pull requests until the operator
	// deliberately goes live with --live or SHIPYARD_MODE=live.
	mode, err := guardrails.ResolveMode(*live, *dryRun, os.Getenv(guardrails.EnvMode), guardrails.ModeDryRun)
	if err != nil {
		return nil, err
	}
	dry := mode == guardrails.ModeDryRun

	if *repoFlag == "" {
		return nil, fmt.Errorf("--repo is required: owner/repo, a https://github.com/… URL, or git@github.com:owner/repo")
	}
	owner, name, err := repo.Normalize(*repoFlag)
	if err != nil {
		return nil, err
	}

	// The label allowlist is the union of --labels/SHIPYARD_LABELS and
	// the repeatable --label flag; it both guards the run and filters
	// it, so both must reach the guardrail gate as one list.
	labelValue := *labelsStr
	if labelValue == "" {
		labelValue = os.Getenv(envLabels)
	}
	if joined := strings.Join(*label, ","); joined != "" {
		if labelValue != "" {
			labelValue += "," + joined
		} else {
			labelValue = joined
		}
	}

	allow, runMaxPRs, err := applyGuardrails(guardrailInput{
		reposFlag:  *repos,
		labelsFlag: labelValue,
		maxPRsFlag: *maxPRs,
		all:        *all,
		unguarded:  *unguarded,
		owner:      owner,
		repo:       name,
		dryRun:     dry,
		quiet:      true,
	})
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(config.Raw{
		GitHubToken: *githubToken,
		Provider:    *aiProvider,
		AIEndpoint:  *aiEndpoint,
		AIKey:       *aiKey,
		AIModel:     *aiModel,
	})
	if err != nil {
		return nil, err
	}

	agentConfig := piagent.Config{
		Provider: cfg.AIProvider,
		Endpoint: cfg.AIEndpoint,
		APIKey:   cfg.AIKey,
		Model:    cfg.AIModel,
	}
	contextWindow, err := resolveAgentContextWindow(*agentContextWindow, os.Getenv(envAgentContextWindow))
	if err != nil {
		return nil, err
	}
	agentConfig.ContextWindow = contextWindow
	if err := agentConfig.Validate(); err != nil {
		return nil, err
	}
	maxTurns, err := resolveMaxTurns(*agentMaxTurns, os.Getenv(envAgentMaxTurns))
	if err != nil {
		return nil, err
	}
	agentBudget, err := resolveAgentTimeout(*agentTimeout, os.Getenv(envAgentTimeout))
	if err != nil {
		return nil, err
	}

	return &listenRun{
		GitHub: githubclient.NewClient(cfg.GitHubAPIRoot, cfg.GitHubToken),
		Options: listen.Options{
			Owner:         owner,
			Repo:          name,
			StateFile:     *stateFile,
			Interval:      *interval,
			Labels:        allow.Labels,
			Repos:         allow.Repos,
			MaxPRs:        runMaxPRs,
			Unguarded:     *unguarded,
			All:           *all,
			Base:          *base,
			GitURL:        *gitURL,
			Image:         *image,
			AgentConfig:   agentConfig,
			AgentMaxTurns: maxTurns,
			AgentTimeout:  agentBudget,
			DryRun:        dry,
			Verbose:       *verbose,
		},
	}, nil
}
