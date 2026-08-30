// Package solve implements Shipyard's core solving flow: fetch a GitHub
// issue, ask the AI endpoint for a fix, apply the generated patch to a
// local checkout of the repository, and open a pull request that links
// the source issue.
//
// The flow shells out to the git CLI for clone / apply / commit / push,
// so the host (or container) running Shipyard must have git installed.
package solve

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/sandbox"
)

// GitRunner runs git commands. It exists so tests can fake git.
type GitRunner interface {
	// Run runs git with args in dir ("" runs with no working directory).
	// It returns stdout; on failure the error includes git's stderr when
	// it is not empty.
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

type execGit struct{}

// ExecGit is the GitRunner that runs the real git binary.
var ExecGit GitRunner = execGit{}

func (execGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := RedactCredentials(strings.TrimSpace(errb.String()))
		if msg != "" {
			return out.String(), fmt.Errorf("git %s: %w: %s", RedactCredentials(strings.Join(args, " ")), err, msg)
		}
		return out.String(), fmt.Errorf("git %s: %w", RedactCredentials(strings.Join(args, " ")), err)
	}
	return out.String(), nil
}

// credURLRe matches the userinfo part of a URL (user:pass@ or the
// x-access-token:<token>@ prefix CloneURLWithToken embeds) so embedded
// credentials never end up in an error message or log line.
var credURLRe = regexp.MustCompile(`://[^@\s]+@`)

// RedactCredentials replaces any user:pass / x-access-token:<token>
// embedded in a URL with ***. Apply it to anything that may carry a
// git command line or git's stderr before putting it in an error or log.
func RedactCredentials(s string) string {
	return credURLRe.ReplaceAllString(s, "://***@")
}

// Options configures one solve run.
type Options struct {
	// Owner and Repo identify the GitHub repository, owner/repo.
	Owner string
	Repo  string
	// IssueNumber is the issue to solve.
	IssueNumber int

	// Workdir is a local checkout of the repo to build on. Empty means
	// Shipyard clones the repo into a temporary directory.
	Workdir string
	// GitURL overrides the git clone URL when Workdir is empty. Empty
	// means derive it from the repo info returned by the GitHub API.
	GitURL string

	// Base is the branch the fix is based on. Empty means the repo's
	// default branch.
	Base string
	// Branch is the branch name for the fix. Empty means
	// shipyard/issue-<n>.
	Branch string

	// IncludeFiles are repo-relative file paths whose contents are
	// embedded in the prompt, so the AI sees the code it is patching.
	IncludeFiles []string

	// Image names the sandbox image the fix step runs in on a live run
	// (the --image flag). Empty: resolve it from the repository (the
	// per-repo setting, once it lands, then auto-detection).
	Image string

	// DryRun stops after the patch has been applied to the workdir:
	// nothing is committed, pushed, or opened.
	DryRun bool

	// Verbose logs the full AI conversation through Deps.Log: the
	// prompt sent, the response, the thinking/reasoning block when the
	// endpoint returns one, and the call's HTTP status, latency, and
	// finish_reason (the --verbose flag, env SHIPYARD_VERBOSE). Off by
	// default: with it off the log output is exactly what it is
	// without.
	Verbose bool
}

// Deps are the collaborators Solve needs.
type Deps struct {
	// GitHub talks to the GitHub API (repo, issue, pull request).
	GitHub *githubclient.Client
	// AI is the chat-completions client.
	AI *aiclient.Client
	// Git runs git commands; nil uses ExecGit.
	Git GitRunner
	// DockerOK reports whether the fix step can run in a sandbox
	// (docker CLI on PATH, daemon answering); nil uses
	// sandbox.DockerAvailable.
	DockerOK func(ctx context.Context) bool
	// RunInSandbox executes the fix-step commands in an ephemeral
	// container; nil uses sandbox.Run.
	RunInSandbox func(ctx context.Context, spec sandbox.RunSpec) (*sandbox.RunResult, error)
	// TempDir is the directory for clones and scratch patch files;
	// "" uses os.TempDir().
	TempDir string
	// Log receives progress output; nil logs to stderr.
	Log func(format string, args ...any)
}

// Result describes what a solve run produced.
type Result struct {
	// Workdir is the checkout the patch was applied to.
	Workdir string
	// Base and Branch are the branches involved.
	Base   string
	Branch string
	// Explanation is the AI's prose explanation of the fix.
	Explanation string
	// Patch is the unified diff extracted from the AI response.
	Patch string
	// PatchPath is where the extracted patch was saved for inspection.
	PatchPath string
	// ResponsePath is where the raw AI response was saved.
	ResponsePath string
	// PR is the opened pull request (nil for --dry-run).
	PR *githubclient.PR
	// Sandbox is the image the fix step ran in; empty when the fix
	// step ran natively (dry runs, or Docker was not available).
	Sandbox string
}
