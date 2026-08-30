package solve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/sandbox"
)

// Solve runs the core solving flow: issue → AI → patch → PR.
//
//  1. fetch repo info and the issue from the GitHub API
//  2. ensure a local checkout (use Options.Workdir or clone)
//  3. build the prompt (issue + labels + file tree + included files)
//  4. call the AI endpoint
//  5. extract the unified diff (error: no usable changes)
//  6. run the fix step — apply the patch, then build and test it:
//     in a disposable sandbox container on live runs (never on the
//     host), natively when --dry-run or Docker is unavailable (error:
//     patch does not apply / a verification step fails)
//  7. stop if Options.DryRun
//  8. commit on a new branch, push
//  9. open a pull request that links the source issue
func Solve(ctx context.Context, d Deps, o Options) (*Result, error) {
	if d.GitHub == nil || d.AI == nil {
		return nil, fmt.Errorf("solve: Deps.GitHub and Deps.AI are required")
	}
	if o.Owner == "" || o.Repo == "" {
		return nil, fmt.Errorf("solve: owner/repo is required")
	}
	if o.IssueNumber <= 0 {
		return nil, fmt.Errorf("solve: issue number is required")
	}
	if d.Git == nil {
		d.Git = ExecGit
	}
	if d.DockerOK == nil {
		d.DockerOK = sandbox.DockerAvailable
	}
	if d.RunInSandbox == nil {
		d.RunInSandbox = sandbox.Run
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
	// Live runs execute the fix step in a disposable container; the AI
	// must know that environment before it writes the fix, so this
	// happens before the prompt is built.
	sandboxImage := ""
	if o.DryRun {
		log("sandbox: off (dry-run)")
	} else if d.DockerOK(ctx) {
		img, source := sandbox.ResolveImageWithSource(o.Image, workdir)
		sandboxImage = img
		log("sandbox: %s (source: %s)", sandboxImage, source)
	} else {
		log("sandbox: off (Docker not available: the fix step runs natively on the host)")
	}

	// --- prompt -----------------------------------------------------
	tree, err := git.Run(ctx, workdir, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("listing files in %s: %w", workdir, err)
	}
	fileContents, err := readIncludeFiles(workdir, o.IncludeFiles)
	if err != nil {
		return nil, err
	}
	prompt := BuildPrompt(repo, issue, base, tree, fileContents, sandboxImage)

	// --- AI call ----------------------------------------------------
	log("calling AI endpoint ...")
	if o.Verbose {
		// Full conversation for debugging weak or local models from the
		// log alone: everything sent and everything received. URLs with
		// embedded credentials stay redacted, as everywhere else.
		for _, line := range d.AI.VerboseRequestLines(prompt) {
			log("%s", RedactCredentials(line))
		}
	}
	completion, err := d.AI.Complete(ctx, prompt)
	if err != nil {
		if o.Verbose {
			// A failed call is exactly the case the verbose log exists
			// for (a 500 from a local endpoint, a timeout, a non-JSON
			// body): keep the diagnostics in the log, not just the raw
			// error string.
			for _, line := range d.AI.VerboseCompletionLines(completion) {
				log("%s", RedactCredentials(line))
			}
		}
		return nil, fmt.Errorf("calling AI endpoint: %w", err)
	}
	if o.Verbose {
		for _, line := range d.AI.VerboseCompletionLines(completion) {
			log("%s", RedactCredentials(line))
		}
	}
	response := completion.Content
	responsePath, err := saveToTemp(d.TempDir, "shipyard-response", ".txt", response)
	if err != nil {
		return nil, err
	}
	log("raw AI response saved to %s", responsePath)

	// --- patch extraction -------------------------------------------
	patch, explanation, err := ExtractPatch(response)
	if err != nil {
		return nil, err
	}
	patchPath, err := saveToTemp(d.TempDir, "shipyard-patch", ".patch", patch)
	if err != nil {
		return nil, err
	}
	log("extracted patch saved to %s", patchPath)

	// --- fix step -------------------------------------------------
	// Apply the patch, then build and test it: in the sandbox on live
	// runs, natively otherwise (dry runs, no Docker available).
	if sandboxImage != "" {
		commands := append([]string{ApplyPatchCommand(patch)}, sandbox.FixCommands(sandboxImage)...)
		run, err := d.RunInSandbox(ctx, sandbox.RunSpec{
			Image:    sandboxImage,
			Workdir:  workdir,
			Commands: commands,
			Log:      log,
		})
		if err != nil {
			return nil, fmt.Errorf("fix step in sandbox: %w", err)
		}
		if !run.OK {
			for i, s := range run.Steps {
				if !s.Ran {
					return nil, fmt.Errorf("fix step failed in sandbox: step %d of %d (%q) did not run — no commit, push, or PR was made", i+1, len(run.Steps), s.Command)
				}
				if s.ExitCode != 0 {
					return nil, fmt.Errorf("fix step failed in sandbox: step %d of %d (%q) exited %d — no commit, push, or PR was made", i+1, len(run.Steps), s.Command, s.ExitCode)
				}
			}
		}
		log("fix step passed in %s: patch applied to %s", sandboxImage, workdir)
	} else {
		if err := ApplyPatch(ctx, git, workdir, patch, patchPath); err != nil {
			return nil, err
		}
		log("patch applied to %s", workdir)
	}

	res := &Result{
		Workdir:      workdir,
		Base:         base,
		Branch:       branch,
		Sandbox:      sandboxImage,
		Explanation:  explanation,
		Patch:        patch,
		PatchPath:    patchPath,
		ResponsePath: responsePath,
	}
	if o.DryRun {
		stat, statErr := git.Run(ctx, workdir, "diff", "--stat")
		if statErr != nil {
			log("warning: could not show the diff stat: %v", statErr)
		}
		log("dry run: nothing committed, pushed, or opened; the workdir is left dirty for you to inspect.")
		if statErr == nil {
			log("diff stat:\n%s", strings.TrimRight(stat, "\n"))
		}
		return res, nil
	}

	// --- commit + push ------------------------------------------------
	msg := fmt.Sprintf("Fix #%d: %s\n\n%s", issue.Number, issue.Title, strings.TrimSpace(explanation))
	if _, err := git.Run(ctx, workdir, "add", "-A"); err != nil {
		return nil, fmt.Errorf("staging changes: %w", err)
	}
	commitArgs := identityArgs(ctx, git, workdir)
	commitArgs = append(commitArgs, "commit", "-m", msg)
	if _, err := git.Run(ctx, workdir, commitArgs...); err != nil {
		return nil, fmt.Errorf("committing changes: %w", err)
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
		Body:  PRBody(issue, o.Owner, o.Repo, branch, base, explanation),
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
		if strings.TrimSpace(status) != "" {
			return "", false, fmt.Errorf("%s has uncommitted changes; commit or stash them first", o.Workdir)
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

// readIncludeFiles loads the contents of the requested files relative to
// workdir. A file missing from the checkout is an error: the prompt
// would otherwise show the AI a file that does not exist.
func readIncludeFiles(workdir string, files []string) (map[string]string, error) {
	out := make(map[string]string, len(files))
	for _, f := range files {
		path := filepath.Join(workdir, filepath.FromSlash(f))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading include file %s: %w", f, err)
		}
		out[f] = string(data)
	}
	return out, nil
}

// identityArgs builds leading "-c user.name=..."/"-c user.email=..."
// flags (git only accepts -c before the subcommand) for a checkout that
// has no git identity configured.
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

// PRBody builds the pull request description. It links the source issue
// and records that Shipyard generated the change.
func PRBody(issue *githubclient.Issue, owner, repo, branch, base, explanation string) string {
	var b strings.Builder
	b.WriteString("<!-- Generated by Shipyard: an AI endpoint proposed this fix for the linked issue. Review it carefully before merging. -->\n\n")
	fmt.Fprintf(&b, "Solves [%s](%s) (#%d).\n\n", issue.Title, issue.HTMLURL, issue.Number)
	if issue.Body != "" {
		b.WriteString("<details>\n<summary>Source issue body</summary>\n\n")
		b.WriteString(issue.Body + "\n\n</details>\n\n")
	}
	if strings.TrimSpace(explanation) != "" {
		b.WriteString("## Summary\n\n")
		b.WriteString(strings.TrimSpace(explanation) + "\n\n")
	}
	fmt.Fprintf(&b, "## Changes\n\nGenerated patch applied to `%s`; full diff on branch `%s` → `%s`.\n", base, branch, base)
	fmt.Fprintf(&b, "Generated by [Shipyard](https://github.com/%s/%s). Fix #%d.", owner, repo, issue.Number)
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
