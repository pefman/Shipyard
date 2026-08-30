// Package solve implements Shipyard's core solving flow: fetch a
// GitHub issue, let the built-in pi coding agent work on a checkout of
// the repository (inside a disposable sandbox container when Docker is
// available, natively otherwise), and — on live runs — commit the
// agent's changes on a new branch, push it, and open a pull request
// that links the source issue.
//
// The agent (not a one-shot prompt) is the solving engine: it reads
// the repository, edits it, runs the build and tests, and iterates.
// Shipyard prepares the run (task prompt + endpoint/model
// configuration, see internal/piagent), streams the agent's progress
// to the log, enforces the per-issue budgets, and then applies the
// same contract as before: no commit/push/PR unless the run succeeded.
package solve

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/piagent"
)

// GitRunner runs git commands. It exists so tests can fake git.
type GitRunner interface {
	// Run runs git with args in dir ("" runs with no working
	// directory). It returns stdout; on failure the error includes
	// git's stderr when it is not empty.
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
// git command line, a git stderr, or an endpoint URL before putting it
// in an error or log.
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

	// Image names the language image the sandbox container is built on
	// (the --image flag). Empty: resolve it from the repository
	// (auto-detection from the repository contents). The built-in pi
	// runtime is added to it by the wrapper image.
	Image string

	// AgentConfig is the agent's endpoint/model configuration, mapped
	// from the provider flags (see internal/config).
	AgentConfig piagent.Config
	// AgentMaxTurns caps the agent's assistant turns for the issue;
	// <= 0 selects piagent.DefaultMaxTurns.
	AgentMaxTurns int
	// AgentTimeout caps the agent run's wall clock; <= 0 selects
	// piagent.DefaultTimeout.
	AgentTimeout time.Duration

	// DryRun stops after the agent's work: nothing is committed,
	// pushed, or opened (the changes stay in the workdir).
	DryRun bool

	// Verbose logs the agent's raw event lines in addition to the
	// rendered progress lines (the --verbose flag, env
	// SHIPYARD_VERBOSE). Off by default.
	Verbose bool
}

// Deps are the collaborators Solve needs.
type Deps struct {
	// GitHub talks to the GitHub API (repo, issue, pull request).
	GitHub *githubclient.Client
	// Agent runs the built-in pi coding agent on the checkout (the
	// solving engine). Nil uses piagent.DefaultRunner (the built-in
	// pi runtime: wrapper image in the sandbox container, pi on PATH
	// for native runs).
	Agent piagent.Runner
	// Git runs git commands; nil uses ExecGit.
	Git GitRunner
	// DockerOK reports whether the fix step can run in a sandbox
	// (docker CLI on PATH, daemon answering); nil uses
	// sandbox.DockerAvailable.
	DockerOK func(ctx context.Context) bool
	// TempDir is the directory for clones and scratch patch files;
	// "" uses os.TempDir().
	TempDir string
	// Log receives progress output; nil logs to stderr.
	Log func(format string, args ...any)
}

// Result describes what a solve run produced.
type Result struct {
	// Workdir is the checkout the agent worked in.
	Workdir string
	// Base and Branch are the branches involved.
	Base   string
	Branch string
	// Summary is the agent's final summary of the fix.
	Summary string
	// Patch is the unified diff of the agent's changes against base.
	Patch string
	// PatchPath is where the diff was saved for inspection.
	PatchPath string
	// PR is the opened pull request (nil for --dry-run).
	PR *githubclient.PR
	// Sandbox is the image the agent ran in (the built-in wrapper
	// image); empty when the agent ran natively on the host.
	Sandbox string
}
