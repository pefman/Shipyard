package solve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/sandbox"
)

// Solve runs the core solving flow: issue → agent → changes → PR.
//
//  1. fetch repo info and the issue from the GitHub API
//  2. ensure a local checkout (use Options.Workdir or clone)
//  3. prepare the agent run: the task prompt (issue + instructions)
//     and the endpoint/model configuration, written into the checkout
//  4. run the built-in pi coding agent on the checkout — inside a
//     disposable sandbox container when Docker is available (the
//     wrapper image adds the built-in pi runtime to the resolved
//     language image; the agent's build/test verification steps run
//     in the same container, right after it), natively on the host
//     when Docker is unavailable
//  5. fail when the agent made no changes (nothing usable to review)
//  6. stop if Options.DryRun
//  7. commit on a new branch, push
//  8. open a pull request that links the source issue
func Solve(ctx context.Context, d Deps, o Options) (*Result, error) {
	if d.GitHub == nil {
		return nil, fmt.Errorf("solve: Deps.GitHub is required")
	}
	if o.Owner == "" || o.Repo == "" {
		return nil, fmt.Errorf("solve: owner/repo is required")
	}
	if o.IssueNumber <= 0 {
		return nil, fmt.Errorf("solve: issue number is required")
	}
	if d.Agent == nil {
		d.Agent = piagent.DefaultRunner
	}
	if d.Git == nil {
		d.Git = ExecGit
	}
	if d.DockerOK == nil {
		d.DockerOK = sandbox.DockerAvailable
	}
	if d.Log == nil {
		d.Log = func(format string, args ...any) { fmt.Fprintf(os.Stderr, "shipyard: "+format+"\n", args...) }
	}
	log := d.Log
	git := d.Git

	repo, err := d.GitHub.GetRepo(ctx, o.Owner, o.Repo)
	if err != nil {
		return nil, fmt.Errorf("fetching repo %s/%s: %w (check the GitHub token and that the repo exists)", o.Owner, o.Repo, err)
	}
	issue, err := d.GitHub.GetIssue(ctx, o.Owner, o.Repo, o.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("fetching issue #%d from %s/%s: %w (check the GitHub token and the issue number)", o.IssueNumber, o.Owner, o.Repo, err)
	}
	log("solving %s (issue #%d: %s)", repo.FullName, issue.Number, issue.Title)
	log("engine: pi-agent (built-in, pi %s): model %s via %s", piagent.Version, o.AgentConfig.Model, RedactCredentials(o.AgentConfig.Endpoint))

	base := firstNonEmpty(o.Base, repo.DefaultBranch)
	if base == "" {
		return nil, fmt.Errorf("could not determine the base branch for %s/%s; pass --base explicitly", o.Owner, o.Repo)
	}
	branch := firstNonEmpty(o.Branch, fmt.Sprintf("shipyard/issue-%d", o.IssueNumber))

	// --- checkout ---------------------------------------------------
	workdir, scratch, err := ensureCheckout(ctx, d, o, repo, base, log)
	if err != nil {
		return nil, err
	}
	if scratch {
		defer os.RemoveAll(workdir)
	}
	if err := newBranch(ctx, git, workdir, branch, base); err != nil {
		return nil, err
	}
	log("working on branch %s (base %s)", branch, base)

	// --- sandbox decision ---------------------------------------------
	// The agent runs inside a disposable container whenever Docker is
	// available (live and dry runs alike: the agent executes code, so
	// the container is the safe choice); without Docker it runs
	// natively on the host. The language image is resolved before the
	// task prompt is built, because the agent must know the
	// environment its changes are built and tested in.
	baseImage := ""
	var verifyCommands []string
	environment := "the host machine it runs on"
	if d.DockerOK(ctx) {
		img, source := sandbox.ResolveImageWithSource(o.Image, workdir)
		baseImage = img
		verifyCommands = sandbox.FixCommands(baseImage)
		environment = "a disposable container running the " + baseImage + " image"
		log("sandbox: %s (source: %s)", baseImage, source)
	} else {
		log("sandbox: off (Docker not available: the agent runs natively on the host)")
	}

	// --- agent run ----------------------------------------------------
	task := BuildTask(repo, issue, base, branch, environment)
	outcome, err := d.Agent.RunAgent(ctx, piagent.RunSpec{
		Workdir:        workdir,
		Task:           task,
		Config:         o.AgentConfig,
		BaseImage:      baseImage,
		VerifyCommands: verifyCommands,
		MaxTurns:       o.AgentMaxTurns,
		Timeout:        o.AgentTimeout,
		Verbose:        o.Verbose,
		Log:            log,
	})
	if err != nil {
		if o.DryRun {
			// A dry run opens nothing: the agent's partial or absent
			// work stays in the workdir for inspection instead of
			// failing the run — the point of a dry run is to see what
			// the agent does.
			log("dry run: the agent run ended without a clean result (%v); the workdir is left for inspection.", err)
		}
		return nil, fmt.Errorf("agent run: %w", err)
	}
	if outcome.StoppedByBudget {
		return nil, fmt.Errorf("agent stopped: budget exhausted (no commit, push, or PR was made)")
	}
	log("agent finished in %d turn%s; its changes are in %s", outcome.Turns, pluralize(outcome.Turns), workdir)

	// --- changes ------------------------------------------------------
	// Stage everything (the agent may have added files) and take the
	// full diff against the base branch.
	if _, err := git.Run(ctx, workdir, "add", "-A"); err != nil {
		return nil, fmt.Errorf("staging the agent's changes: %w", err)
	}
	patch, err := git.Run(ctx, workdir, "diff", "--cached")
	if err != nil {
		return nil, fmt.Errorf("reading the agent's changes: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		return nil, fmt.Errorf("%w (its final message: %s)", ErrNoUsableChanges, oneLine(outcome.Summary, 200))
	}
	patchPath, err := saveToTemp(d.TempDir, "shipyard-changes", ".patch", patch)
	if err != nil {
		return nil, err
	}
	log("agent's changes saved to %s", patchPath)

	res := &Result{
		Workdir:   workdir,
		Base:      base,
		Branch:    branch,
		Sandbox:   outcome.Image,
		Summary:   outcome.Summary,
		Patch:     patch,
		PatchPath: patchPath,
	}
	if o.DryRun {
		stat, statErr := git.Run(ctx, workdir, "diff", "--cached", "--stat")
		if statErr != nil {
			log("warning: could not show the diff stat: %v", statErr)
		} else {
			log("diff stat:\n%s", strings.TrimRight(stat, "\n"))
		}
		log("dry run: nothing committed, pushed, or opened; the workdir is left dirty for you to inspect.")
		return res, nil
	}

	// --- commit + push ------------------------------------------------
	msg := fmt.Sprintf("Fix #%d: %s\n\n%s", issue.Number, issue.Title, strings.TrimSpace(outcome.Summary))
	commitArgs := append(identityArgs(ctx, git, workdir), "commit", "-m", msg)
	if _, err := git.Run(ctx, workdir, commitArgs...); err != nil {
		return nil, fmt.Errorf("committing the agent's changes: %w", err)
	}
	log("committed on %s", branch)
	if refs, _ := git.Run(ctx, workdir, "ls-remote", "--heads", "origin", branch); strings.TrimSpace(refs) != "" {
		return nil, fmt.Errorf("remote branch %s already exists (probably from a previous run); delete it or pass --branch to use a different name", branch)
	}
	if _, err := git.Run(ctx, workdir, "push", "-u", "origin", branch); err != nil {
		return nil, fmt.Errorf("pushing branch %s: %w (check that the GitHub token can push to %s/%s)", branch, err, o.Owner, o.Repo)
	}

	// --- pull request --------------------------------------------------
	title := fmt.Sprintf("Fix #%d: %s", issue.Number, issue.Title)
	pr, err := d.GitHub.CreatePR(ctx, o.Owner, o.Repo, githubclient.PRRequest{
		Title: title,
		Head:  branch,
		Base:  base,
		Body:  PRBody(issue, o.Owner, o.Repo, branch, base, outcome.Summary),
	})
	if err != nil {
		return nil, fmt.Errorf("opening pull request: %w", err)
	}
	res.PR = pr
	log("opened pull request: %s", pr.HTMLURL)
	return res, nil
}

// ensureCheckout returns the workdir to run in. scratch is true when
// Shipyard made a throwaway clone the caller must clean up.
func ensureCheckout(ctx context.Context, d Deps, o Options, repo *githubclient.Repo, base string, log func(string, ...any)) (string, bool, error) {
	git := d.Git
	if o.Workdir != "" {
		status, err := git.Run(ctx, o.Workdir, "status", "--porcelain")
		if err != nil {
			return "", false, fmt.Errorf("%s is not a git working tree: %w", o.Workdir, err)
		}
		if status := strings.TrimSpace(status); status != "" {
			if strings.Contains(status, piagent.DirName) {
				// A previous run left the agent's config directory
				// behind; it is git-excluded, so ignore only its
				// entries (a clean checkout otherwise stays required).
				rest := ""
				for _, line := range strings.Split(status, "\n") {
					if !strings.Contains(line, piagent.DirName) {
						rest += line + "\n"
					}
				}
				if strings.TrimSpace(rest) != "" {
					return "", false, fmt.Errorf("%s has uncommitted changes; commit or stash them first", o.Workdir)
				}
			} else {
				return "", false, fmt.Errorf("%s has uncommitted changes; commit or stash them first", o.Workdir)
			}
		}
		if _, err := git.Run(ctx, o.Workdir, "fetch", "--quiet", "origin", base); err != nil {
			log("warning: could not refresh origin/%s (continuing with the local copy): %v", base, err)
		}
		return o.Workdir, false, nil
	}

	gitURL := o.GitURL
	if gitURL == "" {
		if repo.CloneURL == "" {
			return "", false, fmt.Errorf("the GitHub API returned no clone URL for %s/%s; pass --workdir or --git-url", o.Owner, o.Repo)
		}
		gitURL = CloneURLWithToken(repo.CloneURL, d.GitHub.Token)
	}
	dir, err := os.MkdirTemp(d.TempDir, "shipyard-"+strconv.Itoa(o.IssueNumber))
	if err != nil {
		return "", false, fmt.Errorf("making a temp directory: %w", err)
	}
	log("cloning %s (base branch %s) ...", repo.FullName, base)
	if _, err := git.Run(ctx, "", "clone", "--quiet", "--single-branch", "--branch", base, gitURL, dir); err != nil {
		os.RemoveAll(dir)
		return "", false, fmt.Errorf("cloning %s/%s: %w (check that the GitHub token can read the repo)", o.Owner, o.Repo, err)
	}
	return dir, true, nil
}

// newBranch points branch at origin/base (falling back to the local
// base ref) inside dir.
func newBranch(ctx context.Context, git GitRunner, dir, branch, base string) error {
	if _, err := git.Run(ctx, dir, "checkout", "-B", branch, "origin/"+base); err != nil {
		if _, err2 := git.Run(ctx, dir, "checkout", "-B", branch, base); err2 != nil {
			return fmt.Errorf("preparing branch %s from %s: %w", branch, base, err)
		}
	}
	return nil
}

// identityArgs returns leading "-c user.name=..."/"-c user.email=..."
// flags (git only accepts -c before the subcommand) for a checkout
// that has no git identity configured.
func identityArgs(ctx context.Context, git GitRunner, dir string) []string {
	name, _ := git.Run(ctx, dir, "config", "user.name")
	email, _ := git.Run(ctx, dir, "config", "user.email")
	var args []string
	if strings.TrimSpace(name) == "" {
		args = append(args, "-c", "user.name=Shipyard")
	}
	if strings.TrimSpace(email) == "" {
		args = append(args, "-c", "user.email=shipyard@localhost")
	}
	return args
}

// PRBody builds the pull request description. It links the source
// issue and records that Shipyard's built-in agent generated the
// change.
func PRBody(issue *githubclient.Issue, owner, repo, branch, base, summary string) string {
	var b strings.Builder
	b.WriteString("<!-- Generated by Shipyard: its built-in pi coding agent made this change for the linked issue. Review it carefully before merging. -->\n\n")
	fmt.Fprintf(&b, "Solves [%s](%s) (#%d).\n\n", issue.Title, issue.HTMLURL, issue.Number)
	if issue.Body != "" {
		b.WriteString("<details>\n<summary>Source issue body</summary>\n\n")
		b.WriteString(issue.Body + "\n\n</details>\n\n")
	}
	if strings.TrimSpace(summary) != "" {
		b.WriteString("## Summary\n\n")
		b.WriteString(strings.TrimSpace(summary) + "\n\n")
	}
	fmt.Fprintf(&b, "## Changes\n\nThe agent's changes to `%s`; full diff on branch `%s` → `%s`.\n", base, branch, base)
	fmt.Fprintf(&b, "Generated by [Shipyard](https://github.com/%s/%s) (built-in pi agent). Fix #%d.", owner, repo, issue.Number)
	return b.String()
}

func saveToTemp(dir, prefix, suffix, content string) (string, error) {
	f, err := os.CreateTemp(dir, prefix+suffix)
	if err != nil {
		return "", fmt.Errorf("saving to a temp file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("saving to a temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("saving to a temp file: %w", err)
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ErrNoUsableChanges is returned when the agent's run left the
// repository unchanged: there is nothing to commit or review.
var ErrNoUsableChanges = errors.New("the agent made no usable changes: the repository is unchanged after the agent run")

// oneLine collapses s to a single line capped at n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
