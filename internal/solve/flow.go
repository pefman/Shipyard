package solve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pefman/Shipyard/internal/githubclient"
)

// Solve runs the core solving flow: issue → AI → patch → PR.
//
//  1. fetch repo info and the issue from the GitHub API
//  2. ensure a local checkout (use Options.Workdir or clone)
//  3. build the prompt (issue + labels + file tree + included files)
//  4. call the AI endpoint
//  5. extract the unified diff (error: no usable changes)
//  6. apply it to the checkout (error: patch does not apply)
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

	// --- prompt -----------------------------------------------------
	tree, err := git.Run(ctx, workdir, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("listing files in %s: %w", workdir, err)
	}
	fileContents, err := readIncludeFiles(workdir, o.IncludeFiles)
	if err != nil {
		return nil, err
	}
	prompt := BuildPrompt(repo, issue, base, tree, fileContents)

	// --- AI call ----------------------------------------------------
	log("calling AI endpoint ...")
	response, err := d.AI.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("calling AI endpoint: %w", err)
	}
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

	// --- apply ------------------------------------------------------
	if err := ApplyPatch(ctx, git, workdir, patch, patchPath); err != nil {
		return nil, err
	}
	log("patch applied to %s", workdir)

	res := &Result{
		Workdir:      workdir,
		Base:         base,
		Branch:       branch,
		Explanation:  explanation,
		Patch:        patch,
		PatchPath:    patchPath,
		ResponsePath: responsePath,
	}
	if o.DryRun {
		stat, _ := git.Run(ctx, workdir, "diff", "--stat")
		log("dry run: nothing committed, pushed, or opened; the workdir is left dirty for you to inspect.")
		log("diff stat:\n%s", strings.TrimRight(stat, "\n"))
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
