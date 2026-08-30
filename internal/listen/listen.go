// Package listen implements Shipyard's listen mode: it polls a GitHub
// repository for open issues and runs the core solving flow
// (internal/solve) on every issue that is new — or has no pull request
// yet — skipping ones that have already been processed. It is designed
// to run unattended (e.g. in a container): start it once and it keeps
// watching the repo, opening a PR per issue.
package listen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/guardrails"
	"github.com/pefman/Shipyard/internal/sandbox"
	"github.com/pefman/Shipyard/internal/solve"
)

// DefaultInterval is the poll interval used when Options.Interval is zero.
const DefaultInterval = time.Minute

// Options configures a listen run.
type Options struct {
	// Owner and Repo identify the GitHub repository to watch.
	Owner string
	Repo  string

	// StateFile is where processed issues are persisted so restarts do
	// not re-solve them. Empty means DefaultStateFile.
	StateFile string

	// Interval is the delay between poll passes. Zero means
	// DefaultInterval.
	Interval time.Duration

	// Labels restricts a pass to issues carrying at least one of these
	// labels (e.g. "shipyard"). Empty means every open issue. Matching
	// is case-insensitive, the shared guardrails rule, so solve and
	// listen agree on what an allowed label means.
	Labels []string

	// Base is the branch fixes are based on, passed through to the
	// solving flow per issue. Empty means the repo's default branch.
	Base string

	// GitURL overrides the clone URL for the per-issue checkout. Empty
	// means derive it from the GitHub API (authenticated with the
	// GitHub token).
	GitURL string

	// IncludeFiles are repo-relative files embedded in every prompt.
	IncludeFiles []string

	// Image names the sandbox image the fix step runs in (live runs
	// only; the --image flag). Empty: auto-detect from the repository.
	Image string

	// DryRun stops each solve after the patch is applied: nothing is
	// committed, pushed, or opened.
	DryRun bool

	// Repos is the repository allowlist (owner/repo entries, the
	// --repos flag or SHIPYARD_REPOS). When non-empty, Run refuses to
	// start unless the watched repo is in the list.
	Repos []string

	// MaxPRs is the per-run budget of pull requests to open
	// (--max-prs / SHIPYARD_MAX_PRS). A value of zero or less selects
	// guardrails.DefaultMaxPRs; a run that may open no pull requests
	// at all installs guardrails.NewPRCap(0) on PRCap instead.
	MaxPRs int

	// Unguarded reports that the operator acknowledged an unguarded
	// run (--i-know-this-is-unguarded): with no repo or label
	// allowlist set, Run is refused unless this is true.
	Unguarded bool

	// PRCap tracks how many pull requests this run has opened; Run
	// installs one from MaxPRs before the first pass. When nil,
	// RunOnce applies no cap (for direct API use).
	PRCap *guardrails.PRCap
}

// Deps are the collaborators listen needs.
type Deps struct {
	// GitHub talks to the GitHub API (issues, pull requests, repos).
	GitHub *githubclient.Client
	// AI is the chat-completions client used by the solving flow.
	AI *aiclient.Client
	// Git runs git commands; nil uses solve.ExecGit.
	Git solve.GitRunner
	// DockerOK reports whether the fix step can run in a sandbox;
	// nil uses sandbox.DockerAvailable.
	DockerOK func(ctx context.Context) bool
	// RunInSandbox executes the fix step in an ephemeral container;
	// nil uses sandbox.Run.
	RunInSandbox func(ctx context.Context, spec sandbox.RunSpec) (*sandbox.RunResult, error)
	// Log receives per-run and per-issue progress output; nil logs to
	// stderr.
	Log func(format string, args ...any)
}

// PassOutcome counts what one poll pass did.
type PassOutcome struct {
	// Seen is the number of open issues matching the label filter.
	Seen int
	// New is the number of issues handed to the solving flow.
	New int
	// Skipped is the number of issues already processed (state file or
	// an existing pull request for the fix branch).
	Skipped int
	// Failed is the number of issues the solving flow rejected; they
	// are retried on a later pass.
	Failed int
	// PRsOpened is how many pull requests this pass opened (always
	// zero for dry runs).
	PRsOpened int
	// CapReached is true when the run's pull-request cap
	// (Options.PRCap) was reached during the pass: the pass stopped
	// and Run exits cleanly without picking up further issues.
	CapReached bool
}

// RunOnce performs a single poll pass: list the open issues, skip the
// ones already processed, and run the solving flow on the rest. The
// state file is saved after each issue, so an interrupted pass loses at
// most the issue it was mid-way through.
//
// A per-issue failure never aborts the pass: it is logged and the pass
// continues with the next issue, and the failed issue is retried on a
// later pass. A failure that takes the whole pass down (e.g. the issue
// listing itself fails) is returned as an error.
func (d *Deps) RunOnce(ctx context.Context, o Options) (*PassOutcome, error) {
	if d.GitHub == nil || d.AI == nil {
		return nil, fmt.Errorf("listen: Deps.GitHub and Deps.AI are required")
	}
	if o.Owner == "" || o.Repo == "" {
		return nil, fmt.Errorf("listen: owner/repo is required")
	}

	log := d.log()
	// The label filter is the label allowlist itself; matching is the
	// shared, case-insensitive guardrails rule so solve and listen
	// agree on what an allowed label means.
	labelAllow, err := guardrails.NewAllow(nil, o.Labels)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(stateFile(o.StateFile))
	if err != nil {
		return nil, err
	}
	cap := o.PRCap
	issues, err := d.GitHub.ListIssues(ctx, o.Owner, o.Repo, "open")
	if err != nil {
		return nil, fmt.Errorf("listing open issues in %s/%s: %w", o.Owner, o.Repo, err)
	}

	out := &PassOutcome{}
	for _, issue := range issues {
		if ctx.Err() != nil {
			break // shutting down: stop picking up further issues
		}
		if cap != nil && !cap.CanOpen() {
			out.CapReached = true
			log("pull request cap reached (%d of %d): no more issues are picked up this pass", cap.Count(), cap.Max())
			break
		}
		if !labelAllow.LabelsAllowed(issue.Labels) {
			continue
		}
		out.Seen++

		if prURL, ok := state.IsProcessed(issue.Number); ok {
			out.Skipped++
			log("issue #%d: already processed (%s): skipping", issue.Number, orNone(prURL))
			continue
		}
		// Seed the state from pull requests that already exist for the
		// issue's fix branch, so a lost state file does not re-solve an
		// issue Shipyard already delivered a PR for.
		if pr, found := d.existingFixPR(ctx, o, issue.Number, log); found {
			out.Skipped++
			state.Remember(issue.Number, pr.HTMLURL)
			d.saveState(o, state, log)
			log("issue #%d: pull request %s already exists: skipping", issue.Number, pr.HTMLURL)
			continue
		}

		out.New++
		log("issue #%d: %s", issue.Number, issue.Title)
		res, err := d.solveOne(ctx, o, issue)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break // shutdown raced the solve; leave it for the restart
			}
			out.Failed++
			log("issue #%d: failed: %v (will retry on a later pass)", issue.Number, err)
			continue
		}
		if res.PR != nil {
			out.PRsOpened++
			if cap != nil {
				cap.Opened()
			}
			state.Remember(issue.Number, res.PR.HTMLURL)
			log("issue #%d: pull request opened: %s", issue.Number, res.PR.HTMLURL)
		} else {
			state.Remember(issue.Number, "")
			log("issue #%d: dry run, patch saved to %s", issue.Number, res.PatchPath)
		}
		d.saveState(o, state, log)
		if cap != nil && cap.Exhausted() {
			out.CapReached = true
			break
		}
	}
	return out, nil
}

// Run performs a poll pass immediately, then passes again every
// Options.Interval until ctx is canceled. A failing pass (e.g. a
// transient GitHub API hiccup) is logged and does not stop the loop:
// the listener retries on the next tick. Run returns nil after the
// shutdown.
func (d *Deps) Run(ctx context.Context, o Options) error {
	if d.GitHub == nil || d.AI == nil {
		return fmt.Errorf("listen: Deps.GitHub and Deps.AI are required")
	}
	if o.Owner == "" || o.Repo == "" {
		return fmt.Errorf("listen: owner/repo is required")
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}

	// Guardrails: a run with no repo or label allowlist is refused
	// unless the operator acknowledged it, and the watched repo must
	// be on the repo allowlist when one is set.
	allow, err := guardrails.NewAllow(o.Repos, o.Labels)
	if err != nil {
		return err
	}
	if err := allow.Gate(o.Unguarded); err != nil {
		return err
	}
	if len(o.Repos) > 0 && !allow.RepoAllowed(o.Owner, o.Repo) {
		return fmt.Errorf("repo %s/%s is not in the repo allowlist (%s): this listener would never solve anything here — point --repo at an allowed repository or remove it from --repos", o.Owner, o.Repo, strings.Join(o.Repos, ", "))
	}

	log := d.log()
	if allow.Configured() {
		log("guardrails: %s", allow.Summary())
	} else {
		log("WARNING: no repo or label allowlist is set — this run is UNGUARDED (acknowledged with --i-know-this-is-unguarded) and may act on any issue in %s/%s", o.Owner, o.Repo)
	}
	log("listening on %s/%s: every %s, state in %s, max %d pull request(s) per run", o.Owner, o.Repo, o.Interval, stateFile(o.StateFile), maxPRsForRun(o.MaxPRs))
	if len(o.Labels) > 0 {
		log("label filter: %s", strings.Join(o.Labels, ", "))
	}
	if o.DryRun {
		log("dry run: patches are applied but nothing is committed, pushed, or opened")
		log("sandbox: off (dry-run)")
	} else {
		// Startup audit line: whether live fix steps run in a sandbox.
		// The exact image is auto-detected per checkout, so it is
		// reported per issue by the solving flow.
		if !d.dockerOK()(ctx) {
			log("sandbox: off (Docker not available: the fix step runs natively on the host)")
		} else if o.Image != "" {
			log("sandbox: %s (source: flag)", o.Image)
		} else {
			log("sandbox: on (image: auto-detected per repository)")
		}
	}

	// The per-run pull-request budget. Options.MaxPRs <= 0 selects the
	// default (a zero value from a direct API call must not mean
	// "open nothing"); an explicit zero cap is still available via a
	// PRCap installed on Options.PRCap.
	cap := guardrails.NewPRCap(maxPRsForRun(o.MaxPRs))
	o.PRCap = cap

	ticker := time.NewTicker(o.Interval)
	defer ticker.Stop()
	for {
		out, err := d.RunOnce(ctx, o)
		if out != nil && out.CapReached {
			log("run stopped: pull request cap reached (%d of %d opened this run) — remaining issues stay open for the next run", cap.Count(), cap.Max())
			return nil
		}
		if err != nil && ctx.Err() == nil {
			log("pass failed: %v (retrying in %s)", err, o.Interval)
		} else if out != nil && err == nil {
			log("pass done: %d issue(s) seen, %d solved, %d skipped, %d failed",
				out.Seen, out.New, out.Skipped, out.Failed)
		}
		select {
		case <-ctx.Done():
			log("shutting down: %v", ctx.Err())
			return nil
		case <-ticker.C:
		}
	}
}

// solveOne runs the core solving flow for one issue, prefixing its
// progress lines with the issue number.
func (d *Deps) solveOne(ctx context.Context, o Options, issue *githubclient.Issue) (*solve.Result, error) {
	git := d.Git
	if git == nil {
		git = solve.ExecGit
	}
	return solve.Solve(ctx, solve.Deps{
		GitHub:       d.GitHub,
		AI:           d.AI,
		Git:          git,
		DockerOK:     d.DockerOK,
		RunInSandbox: d.RunInSandbox,
		Log:          d.issueLog(issue.Number),
	}, solve.Options{
		Owner:        o.Owner,
		Repo:         o.Repo,
		IssueNumber:  issue.Number,
		GitURL:       o.GitURL,
		Base:         o.Base,
		IncludeFiles: o.IncludeFiles,
		Image:        o.Image,
		DryRun:       o.DryRun,
	})
}

// dockerOK returns Deps.DockerOK, or sandbox.DockerAvailable when it is
// nil.
func (d *Deps) dockerOK() func(context.Context) bool {
	if d.DockerOK != nil {
		return d.DockerOK
	}
	return sandbox.DockerAvailable
}

// existingFixPR returns the pull request on the issue's default fix
// branch (shipyard/issue-<n>, matching the solve flow's naming), if any.
// A GitHub API error is logged and treated as "not found": the state
// file still protects against re-solving within a run.
func (d *Deps) existingFixPR(ctx context.Context, o Options, number int, log func(format string, args ...any)) (*githubclient.PR, bool) {
	head := fmt.Sprintf("%s:shipyard/issue-%d", o.Owner, number)
	prs, err := d.GitHub.ListPRs(ctx, o.Owner, o.Repo, head)
	if err != nil {
		log("warning: could not check for an existing pull request on %s: %v", head, solve.RedactCredentials(err.Error()))
		return nil, false
	}
	if len(prs) > 0 {
		return prs[0], true
	}
	return nil, false
}

// log returns Deps.Log, or a stderr logger when it is nil.
func (d *Deps) log() func(format string, args ...any) {
	if d.Log != nil {
		return d.Log
	}
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "shipyard listen: "+format+"\n", args...)
	}
}

// issueLog wraps the run logger with an "issue #n:" prefix.
func (d *Deps) issueLog(number int) func(format string, args ...any) {
	log := d.log()
	return func(format string, args ...any) {
		args = append([]any{number}, args...)
		log("issue #%d: "+format, args...)
	}
}

func (d *Deps) saveState(o Options, state *State, log func(format string, args ...any)) {
	if err := state.Save(stateFile(o.StateFile)); err != nil {
		log("warning: could not save the state file: %v", err)
	}
}

func orNone(s string) string {
	if s == "" {
		return "no pull request (dry run)"
	}
	return s
}

// maxPRsForRun maps the configured budget to a concrete cap: a value
// of zero or less selects guardrails.DefaultMaxPRs.
func maxPRsForRun(maxPRs int) int {
	if maxPRs <= 0 {
		return guardrails.DefaultMaxPRs
	}
	return maxPRs
}
