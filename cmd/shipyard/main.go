// Command shipyard is an AI issue solver: it reads a GitHub issue, sends
// it to a configurable AI endpoint with repository context, applies the
// generated patch to a local checkout, and opens a pull request that
// links the source issue.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pefman/Shipyard/internal/config"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/guardrails"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/repo"
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
	case "login":
		if err := runLogin(args); err != nil {
			fmt.Fprintln(os.Stderr, "shipyard:", err)
			os.Exit(1)
		}
	case "whoami":
		if err := runWhoami(args); err != nil {
			fmt.Fprintln(os.Stderr, "shipyard:", err)
			os.Exit(1)
		}
	case "logout":
		if err := runLogout(); err != nil {
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
  shipyard solve --repo <repo> --issue <n> [flags]
  shipyard listen --repo <repo> [flags]
  shipyard login [flags]
  shipyard whoami [flags]
  shipyard logout

Solve flags:
  --repo <repo>          GitHub repository (required): owner/repo, or
                         https://github.com/owner/repo, git@github.com:owner/repo,
                         ssh://git@github.com/owner/repo, or github.com/owner/repo
                         (a trailing .git is stripped)
  --issue <n>            Issue number to solve (required)
  --github-token <t>     GitHub token (env SHIPYARD_GITHUB_TOKEN, else the
                         token stored by "shipyard login")
  --provider <name>      AI provider: openai, xai, or custom (env SHIPYARD_AI_PROVIDER;
                         default custom: --ai-endpoint required, key optional)
  --ai-endpoint <url>    AI endpoint base URL (env SHIPYARD_AI_ENDPOINT; defaults per provider)
  --ai-key <k>           AI API key (env SHIPYARD_AI_KEY; also SHIPYARD_OPENAI_KEY / SHIPYARD_XAI_KEY)
  --ai-model <m>         Model the built-in agent uses, served by the endpoint
                         (env SHIPYARD_AI_MODEL; defaults: openai gpt-5.6-sol,
                         xai grok-4.6)
  --agent-max-turns <n>  Cap the agent's assistant turns for the issue
                         (env SHIPYARD_AGENT_MAX_TURNS; default 30)
  --agent-timeout <dur>  Cap the agent run's wall clock per issue
                         (env SHIPYARD_AGENT_TIMEOUT; default 30m)
  --workdir <dir>        Local checkout to build on (default: clone to a temp dir)
  --base <branch>        Base branch (default: the repo's default branch)
  --branch <name>        Branch for the fix (default: shipyard/issue-<n>)
  --git-url <url>        Git clone URL (with --workdir unset; default from the API)
  --image <image>        Language image the sandbox container is built on (default:
                         auto-detected from the repository — see the README);
                         the built-in pi agent runtime is added by the wrapper image
  --dry-run              Stop after the agent's work: no commit, push, or PR
  --verbose              Log the agent's raw event lines in addition to the
                         rendered progress (env SHIPYARD_VERBOSE=1). Off by default

Guardrails (solve and listen):
  --repos <list>         Comma-separated repository allowlist, owner/repo entries
                         (env SHIPYARD_REPOS). When set, only issues in these
                         repos are solved.
  --labels <list>        Comma-separated label allowlist (env SHIPYARD_LABELS).
                         When set, only issues carrying an allowed label are
                         solved. (listen: the repeatable --label flag is an
                         equivalent label allowlist)
  --max-prs <n>          Stop the run after this many pull requests have been
                         opened (env SHIPYARD_MAX_PRS; default 3). A live run
                         must be able to open at least one: --max-prs 0 is a
                         dry-run setting, not a live one.
  --all                  Run with no repo/label allowlist, on purpose: marks
                         the allowlist axis as explicitly unrestricted, so a
                         live run without one is not refused. Conflicts with a
                         --repos/--labels allowlist. (The hidden flag
                         --i-know-this-is-unguarded is a compatible alias.)

Listen flags:
  --repo <repo>          GitHub repository to watch (required; accepted forms
                         like solve's --repo)
  --interval <dur>       Delay between poll passes (default 1m)
  --live                 Live mode: commit fixes, push, and open pull
                         requests (env SHIPYARD_MODE=live). Without --live,
                         listen runs in dry-run mode — the default: it runs
                         the full flow but commits nothing and opens no pull
                         requests
  --label <name>         Only solve issues carrying this label (repeatable)
  --state-file <path>    File tracking processed issues (default: shipyard-listen-state.json)
  --github-token <t>     GitHub token (env SHIPYARD_GITHUB_TOKEN, else the
                         token stored by "shipyard login")
  --provider <name>      AI provider: openai, xai, or custom (env SHIPYARD_AI_PROVIDER;
                         default custom: --ai-endpoint required, key optional)
  --ai-endpoint <url>    AI endpoint base URL (env SHIPYARD_AI_ENDPOINT; defaults per provider)
  --ai-key <k>           AI API key (env SHIPYARD_AI_KEY; also SHIPYARD_OPENAI_KEY / SHIPYARD_XAI_KEY)
  --ai-model <m>         Model the built-in agent uses, served by the endpoint
                         (env SHIPYARD_AI_MODEL; defaults: openai gpt-5.6-sol,
                         xai grok-4.6)
  --base <branch>        Base branch (default: the repo's default branch)
  --git-url <url>        Git clone URL for the per-issue checkout
  --image <image>        Language image the sandbox container is built on
                         (default: auto-detected from the repository)
  --agent-max-turns <n>  Cap the agent's assistant turns per issue
                         (env SHIPYARD_AGENT_MAX_TURNS; default 30)
  --agent-timeout <dur>  Cap the agent run's wall clock per issue
                         (env SHIPYARD_AGENT_TIMEOUT; default 30m)
  --dry-run              Dry-run mode (the default for listen): the agent's work
                         is kept but nothing is committed and no pull requests
                         are opened. Takes precedence over SHIPYARD_MODE;
                         conflicts with --live
  --verbose              Log the agent's raw event lines per issue in addition
                         to the rendered progress, prefixed like all output
                         (env SHIPYARD_VERBOSE=1). Off by default

Login flags:
  --github-client-id <id> GitHub OAuth App client ID (env SHIPYARD_GITHUB_CLIENT_ID;
                          default: the built-in pre-registered app — login works with zero config)
  --force                Redo the device flow even if a valid stored token exists

Whoami flags:
  --github-token <t>     Verify this token via GET /user instead of the stored login
                         (env SHIPYARD_GITHUB_TOKEN)
  --github-api <url>     GitHub API root (env SHIPYARD_GITHUB_API; default
                         https://api.github.com)

The device flow prints a verification URI and a one-time code; after you
authorize at the URI, the access token is verified via GET /user and stored
at $XDG_CONFIG_HOME/shipyard/credentials.json (default
~/.config/shipyard/credentials.json) with 0600 permissions. Re-running
the command while a valid token is stored just verifies it and exits.
"shipyard whoami" shows which identity the token precedence (flag, env,
stored login) resolves to; "shipyard logout" removes the stored
credentials file.
`)
}

func runSolve(args []string) error {
	fs := flag.NewFlagSet("solve", flag.ExitOnError)
	repoFlag := fs.String("repo", "", "GitHub repository (owner/repo or a github.com URL); see usage")
	issue := fs.Int("issue", 0, "issue number to solve")
	githubToken := fs.String("github-token", "", "GitHub token (env SHIPYARD_GITHUB_TOKEN, else the token stored by shipyard login)")
	aiProvider := fs.String("provider", "", "AI provider: openai, xai, or custom")
	aiEndpoint := fs.String("ai-endpoint", "", "AI endpoint base URL")
	aiKey := fs.String("ai-key", "", "AI API key")
	aiModel := fs.String("ai-model", "", "model name for the AI endpoint")
	workdir := fs.String("workdir", "", "local checkout to build on (default: clone)")
	base := fs.String("base", "", "base branch (default: repo default branch)")
	branch := fs.String("branch", "", "branch for the fix (default: shipyard/issue-<n>)")
	agentMaxTurns := fs.Int("agent-max-turns", -1, "cap the agent's assistant turns for the issue (env SHIPYARD_AGENT_MAX_TURNS; default 30)")
	agentTimeout := fs.Duration("agent-timeout", 0, "cap the agent run's wall clock (env SHIPYARD_AGENT_TIMEOUT; default 30m)")
	gitURL := fs.String("git-url", "", "git clone URL (with --workdir unset)")
	dryRun := fs.Bool("dry-run", false, "stop after the agent's work: no commit, push, or PR")
	verbose := fs.Bool("verbose", verboseFromEnv(), "log the agent's raw event lines in addition to the rendered progress (env SHIPYARD_VERBOSE=1)")
	image := fs.String("image", "", "language image the sandbox container is built on (default: auto-detect)")
	repos := fs.String("repos", "", "repository allowlist, comma-separated owner/repo (env SHIPYARD_REPOS)")
	labels := fs.String("labels", "", "label allowlist, comma-separated (env SHIPYARD_LABELS)")
	maxPRs := fs.Int("max-prs", -1, "stop after opening this many pull requests (env SHIPYARD_MAX_PRS; default 3)")
	all := fs.Bool("all", false, "run with no repo/label allowlist, on purpose (explicitly unrestricted)")
	unguarded := fs.Bool("i-know-this-is-unguarded", false, "hidden alias of --all: proceed even with no repo/label allowlist set")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *repoFlag == "" {
		return fmt.Errorf("--repo is required: owner/repo, a https://github.com/… URL, or git@github.com:owner/repo")
	}
	if *issue <= 0 {
		return fmt.Errorf("--issue <n> is required")
	}
	owner, name, err := repo.Normalize(*repoFlag)
	if err != nil {
		return err
	}
	allow, _, err := applyGuardrails(guardrailInput{
		reposFlag:  *repos,
		labelsFlag: *labels,
		maxPRsFlag: *maxPRs,
		all:        *all,
		unguarded:  *unguarded,
		owner:      owner,
		repo:       name,
		issue:      *issue,
		dryRun:     *dryRun,
	})
	if err != nil {
		return err
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

	maxTurns, err := resolveMaxTurns(*agentMaxTurns, os.Getenv(envAgentMaxTurns))
	if err != nil {
		return err
	}
	timeout, err := resolveAgentTimeout(*agentTimeout, os.Getenv(envAgentTimeout))
	if err != nil {
		return err
	}

	agentConfig := piagent.Config{
		Provider: cfg.AIProvider,
		Endpoint: cfg.AIEndpoint,
		APIKey:   cfg.AIKey,
		Model:    cfg.AIModel,
	}
	if err := agentConfig.Validate(); err != nil {
		return err
	}
	gh := githubclient.NewClient(cfg.GitHubAPIRoot, cfg.GitHubToken)
	// With a label allowlist, the target issue must carry one of the
	// allowed labels; check it before spending an AI call on it.
	if len(allow.Labels) > 0 {
		iss, err := gh.GetIssue(context.Background(), owner, name, *issue)
		if err != nil {
			return fmt.Errorf("checking the labels of issue #%d: %w", *issue, err)
		}
		if !allow.LabelsAllowed(iss.Labels) {
			return fmt.Errorf("issue #%d carries no allowed label (allowed: %s)", *issue, strings.Join(allow.Labels, ", "))
		}
	}

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: gh,
	}, solve.Options{
		Owner:         owner,
		Repo:          name,
		IssueNumber:   *issue,
		Workdir:       *workdir,
		GitURL:        *gitURL,
		Base:          *base,
		Branch:        *branch,
		Image:         *image,
		AgentConfig:   agentConfig,
		AgentMaxTurns: maxTurns,
		AgentTimeout:  timeout,
		DryRun:        *dryRun,
		Verbose:       *verbose,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: branch %s on %s\n", res.Branch, res.Workdir)
	fmt.Fprintln(os.Stderr, "changes: "+res.PatchPath)
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

// guardrailInput bundles the guardrail settings of one command run: the
// flag values (an empty flag falls back to the environment), the target
// repo and issue, and the run mode.
type guardrailInput struct {
	reposFlag  string // SHIPYARD_REPOS
	labelsFlag string // SHIPYARD_LABELS
	maxPRsFlag int    // SHIPYARD_MAX_PRS; negative means unset
	all        bool   // --all: explicitly no allowlist, on purpose
	unguarded  bool   // --i-know-this-is-unguarded (hidden alias of --all)
	owner      string
	repo       string
	issue      int // issue to solve (solve); unused by listen
	dryRun     bool
	quiet      bool // don't print the audit line (listen logs its own)
}

const (
	envRepos     = "SHIPYARD_REPOS"
	envLabels    = "SHIPYARD_LABELS"
	envMaxPRs    = "SHIPYARD_MAX_PRS"
	envVerbose   = "SHIPYARD_VERBOSE"
	envAgentMaxTurns = "SHIPYARD_AGENT_MAX_TURNS"
	envAgentTimeout  = "SHIPYARD_AGENT_TIMEOUT"
)

// resolveMaxTurns resolves the agent turn budget: the flag wins over the
// environment; both unset selects piagent.DefaultMaxTurns (returned as
// 0 for the solve flow to apply).
func resolveMaxTurns(flag int, env string) (int, error) {
	if flag > 0 {
		return flag, nil
	}
	if env == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(env)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s value %q (want a positive number)", envAgentMaxTurns, env)
	}
	return n, nil
}

// resolveAgentTimeout resolves the agent wall-clock budget the same way
// (flag over environment; both unset selects piagent.DefaultTimeout).
func resolveAgentTimeout(flag time.Duration, env string) (time.Duration, error) {
	if flag > 0 {
		return flag, nil
	}
	if env == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(env)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid %s value %q (want a duration like 45m)", envAgentTimeout, env)
	}
	return d, nil
}

// verboseFromEnv reports whether SHIPYARD_VERBOSE asks for the full AI
// conversation to be logged (1/true/yes/on; anything else is off). It
// only seeds the --verbose flag's default, so an explicit
// --verbose[=false] on the command line wins.
func verboseFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envVerbose))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// applyGuardrails resolves the allowlists and the pull-request budget
// (flag wins over environment), refuses a live run that is unguarded —
// no repo or label allowlist without --all or its hidden alias
// --i-know-this-is-unguarded (dry runs are safe without one: they open
// nothing) — checks the target repo against the repo allowlist, and
// treats --all combined with a set allowlist as a configuration error.
// It prints the audit line (or the unguarded warning) to stderr and
// returns the parsed allowlist plus the resolved pull-request budget.
func applyGuardrails(g guardrailInput) (*guardrails.Allow, int, error) {
	repos := g.reposFlag
	if repos == "" {
		repos = os.Getenv(envRepos)
	}
	labels := g.labelsFlag
	if labels == "" {
		labels = os.Getenv(envLabels)
	}
	allow, err := guardrails.NewAllow(guardrails.ParseList(repos), guardrails.ParseList(labels))
	if err != nil {
		return nil, 0, err
	}
	if g.all && allow.Configured() {
		return nil, 0, fmt.Errorf("conflict: --all (no allowlist on the repo/label axis, on purpose) is set together with an allowlist (%s) — drop --all or drop the allowlist", allow.Summary())
	}

	maxPRs := g.maxPRsFlag
	if maxPRs < 0 {
		if env := os.Getenv(envMaxPRs); env != "" {
			if maxPRs, err = guardrails.ParseMaxPRs(env); err != nil {
				return nil, 0, err
			}
		}
	}
	if maxPRs < 0 {
		maxPRs = guardrails.DefaultMaxPRs
	}
	if maxPRs == 0 && !g.dryRun {
		return nil, 0, errors.New("--max-prs is 0: a live run would never open a pull request; run in dry-run mode (or drop --live) or raise the cap")
	}

	if !allow.RepoAllowed(g.owner, g.repo) {
		return nil, 0, fmt.Errorf("repo %s/%s is not in the repo allowlist (%s)", g.owner, g.repo, strings.Join(allow.Repos, ", "))
	}
	// The unguarded gate applies to live runs only: a dry run commits
	// nothing and opens no pull requests, so it is safe without an
	// allowlist.
	if !g.dryRun {
		if err := allow.Gate(g.all || g.unguarded); err != nil {
			return nil, 0, err
		}
	}
	if g.quiet {
		return allow, maxPRs, nil
	}
	if allow.Configured() {
		fmt.Fprintf(os.Stderr, "shipyard: guardrails: %s; max-prs: %d\n", allow.Summary(), maxPRs)
	} else if g.dryRun {
		fmt.Fprintln(os.Stderr, "shipyard: note: no repo or label allowlist is set — this dry run may act on any issue in the repository, but it opens no pull requests.")
	} else if g.all {
		fmt.Fprintf(os.Stderr, "shipyard: guardrails: %s; max-prs: %d\n", guardrails.SummaryExplicitAll, maxPRs)
	} else {
		fmt.Fprintln(os.Stderr, "shipyard: WARNING: no repo or label allowlist is set — this live run is UNGUARDED (acknowledged with --all or the hidden --i-know-this-is-unguarded) and may act on any issue in the repository.")
	}
	return allow, maxPRs, nil
}
