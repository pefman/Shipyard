package listen

// End-to-end tests for the sandbox wiring (SHI-33): a live solve run
// executes the fix step (apply the AI's patch, then build and test) in
// an ephemeral container. A stub docker binary on PATH stands in for
// the daemon: it answers `info` and executes the container's /bin/sh
// script in the directory the mount points at, so the "container"
// behaves like the mounted workdir and the tests need no daemon.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/solve"
)

const stubDocker = `#!/bin/sh
if [ -n "${STUB_DOCKER_LOG:-}" ]; then
  printf 'docker %s\n' "$*" >> "$STUB_DOCKER_LOG"
fi
case "$1" in
  info)
    exit 0
    ;;
  run)
    shift
    v=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --name|--entrypoint) shift 2 ;;
        -v) v="$2"; shift 2 ;;
        -w|-e) shift 2 ;;
        --rm) shift ;;
        -*) shift ;;
        *) break ;;
      esac
    done
    shift
    [ "$1" = "-c" ] && shift
    script="$1"
    cd "${v%%:*}" || exit 127
    exec /bin/sh -c "$script"
    ;;
  rm)
    exit 0
    ;;
  *)
    exit 125
    ;;
esac
`

// installDockerStub puts the stub docker binary first on PATH and logs
// every invocation to a file the test can assert on.
func installDockerStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stubDocker), 0o755); err != nil {
		t.Fatalf("writing stub docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubLog := filepath.Join(dir, "docker-stub.log")
	t.Setenv("STUB_DOCKER_LOG", stubLog)
	return stubLog
}

func readStubLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // docker was never invoked
	}
	return string(data)
}

type solveLogBuf struct {
	mu    sync.Mutex
	lines []string
}

func (b *solveLogBuf) logf(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}

func (b *solveLogBuf) has(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func newSolveLog() *solveLogBuf { return &solveLogBuf{} }

// TestSolveLiveFixStepInSandbox is the acceptance test for the wiring:
// a live solve runs the fix step (patch apply, then the image's
// verification steps) in the stubbed ephemeral container; the patch
// lands through the container, the commit/push/PR then proceed natively.
func TestSolveLiveFixStepInSandbox(t *testing.T) {
	gitAvailable(t)
	stubLogPath := installDockerStub(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:     ai,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image: "ubuntu:24.04", // explicit → source: flag, patch-apply only
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR == nil {
		t.Fatal("live run opened no pull request")
	}
	if res.Sandbox != "ubuntu:24.04" {
		t.Errorf("Result.Sandbox = %q, want ubuntu:24.04", res.Sandbox)
	}
	if !log.has("sandbox: ubuntu:24.04 (source: flag)") {
		t.Errorf("missing the sandbox audit line")
	}
	if !log.has("fix step passed") {
		t.Errorf("missing the fix-step pass line")
	}
	// The fix reached the "remote" through the container ...
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
	if show := runE2EGit(t, bare, "show", "shipyard/issue-9:hello.py"); !strings.Contains(show, "Hello, stranger") {
		t.Errorf("remote branch does not contain the fix:\n%s", show)
	}
	// ... and the container really ran (info, run, rm).
	stubLog := readStubLog(t, stubLogPath)
	for _, want := range []string{"docker info", "docker run", "docker rm"} {
		if !strings.Contains(stubLog, want) {
			t.Errorf("stub docker never saw %q:\n%s", want, stubLog)
		}
	}
}

// TestSolveLiveSandboxVerificationFailure: a verification step that
// fails in the sandbox (here: `go build` in a repo with no go.mod)
// must stop the run before commit, push, and PR.
func TestSolveLiveSandboxVerificationFailure(t *testing.T) {
	gitAvailable(t)
	installDockerStub(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	log := newSolveLog()

	_, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:     ai,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image: "golang:1.22", // its `go build ./...` step fails: no go.mod
	})
	if err == nil {
		t.Fatal("Solve succeeded although a verification step failed")
	}
	if !strings.Contains(err.Error(), "fix step failed in sandbox") {
		t.Errorf("error = %v, want a sandbox fix-step failure", err)
	}
	if !log.has("sandbox: golang:1.22 (source: flag)") {
		t.Errorf("missing the sandbox audit line")
	}
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); strings.TrimSpace(refs) != "" {
		t.Errorf("branch pushed although the fix step failed: %q", refs)
	}
}

// TestSolveLiveNoDockerFallsBackNative: a live run without Docker must
// run the native path exactly as before (apply natively, commit, push,
// PR) and say so in the audit line.
func TestSolveLiveNoDockerFallsBackNative(t *testing.T) {
	gitAvailable(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub:   githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:       ai,
		Git:      solve.ExecGit,
		DockerOK: func(context.Context) bool { return false },
		Log:      log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image: "golang:1.22", // irrelevant: no sandbox without Docker
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR == nil {
		t.Fatal("live run opened no pull request")
	}
	if res.Sandbox != "" {
		t.Errorf("Result.Sandbox = %q, want empty (native path)", res.Sandbox)
	}
	if !log.has("sandbox: off (Docker not available") {
		t.Errorf("missing the Docker-not-available audit line")
	}
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
}

// TestSolveDryRunNeverRunsSandbox: a dry run applies the patch
// natively and must never touch Docker, whatever is installed.
func TestSolveDryRunNeverRunsSandbox(t *testing.T) {
	gitAvailable(t)
	stubLogPath := installDockerStub(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:     ai,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image: "golang:1.22", DryRun: true,
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR != nil {
		t.Error("dry run opened a pull request")
	}
	if !log.has("sandbox: off (dry-run)") {
		t.Errorf("missing the dry-run audit line")
	}
	if stubLog := readStubLog(t, stubLogPath); strings.Contains(stubLog, "docker run") {
		t.Errorf("dry run invoked the sandbox:\n%s", stubLog)
	}
}
