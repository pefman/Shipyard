package listen

// Sandbox end-to-end tests for the solving flow: real git, a stub
// docker binary (the "container" runs its script in the mounted
// directory on the host) and the stub pi agent on PATH. They cover
// the container path the live run takes: the built-in agent runs in
// a wrapper image over the language image, and its changes plus the
// artifact-free verification steps go through the container before the
// commit, push, and pull request proceed natively.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/solve"
	"github.com/pefman/Shipyard/internal/testpi"
)

// installDockerStub puts the stub docker binary on PATH (next to the
// stub pi) and returns the stub's log file path.
func installDockerStub(t *testing.T) string {
	t.Helper()
	return testpi.InstallDocker(t)
}

func readStubLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// newSolveLog collects the solve run's log lines for assertions (the
// container path logs from several pumps concurrently, so access is
// locked).
type newSolveLogType struct {
	mu    sync.Mutex
	lines []string
}

func newSolveLog() *newSolveLogType { return &newSolveLogType{} }

func (l *newSolveLogType) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *newSolveLogType) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func (l *newSolveLogType) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// TestSolveLiveGoRepoRunsAgentInSandbox is the acceptance test for the
// container path: a live solve on a Go repo auto-detects the golang
// image, builds the built-in wrapper on top of it, runs the agent (the
// stub) inside, and re-verifies the final tree with the artifact-free
// steps: the pushed branch contains exactly the seeded files plus the
// fix, and no compiled binary. (The image is left empty on purpose so
// the run also exercises auto-detection: go.mod → golang, source
// auto.)
func TestSolveLiveGoRepoRunsAgentInSandbox(t *testing.T) {
	gitAvailable(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed; the verification steps would run on the host through the stub")
	}
	testpi.Install(t)
	stubLogPath := installDockerStub(t)
	t.Setenv("PI_STUB_FIX_FILE", "main.go")
	t.Setenv("PI_STUB_FIX_CONTENT", goSeedMainFixed)
	bare := newGoE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "bump the greeting"}}, nil)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  piagent.DefaultRunner,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		AgentConfig: agentConfigForTests(),
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR == nil {
		t.Fatal("live run opened no pull request")
	}
	if res.Sandbox != piagent.WrapperImageName(sandboxGolangImage) {
		t.Errorf("Result.Sandbox = %q, want the built-in wrapper %q", res.Sandbox, piagent.WrapperImageName(sandboxGolangImage))
	}
	if !log.has("sandbox: golang:1.22 (source: auto)") {
		t.Errorf("missing the auto-detected sandbox audit line (go.mod should pick the golang image)")
	}
	if !log.has("agent: running the built-in pi agent in") {
		t.Errorf("missing the in-sandbox agent line:\n%s", log)
	}
	// The pushed branch must be exactly the seeded files plus the
	// fix — no compiled binary left behind by the agent or the
	// verification steps.
	files := runE2EGit(t, bare, "ls-tree", "-r", "--name-only", "shipyard/issue-9")
	if files != "go.mod\nmain.go\n" {
		t.Errorf("remote branch file set = %q, want exactly go.mod and main.go (no build artifacts)", files)
	}
	if show := runE2EGit(t, bare, "show", "shipyard/issue-9:main.go"); !strings.Contains(show, `shipyard v1`) {
		t.Errorf("remote branch does not contain the fix:\n%s", show)
	}
	// ... and the container really ran (info, run, rm).
	if stubLog := readStubLog(t, stubLogPath); !strings.Contains(stubLog, "docker run") {
		t.Errorf("the agent run never went through the container:\n%s", stubLog)
	}
}

// sandboxGolangImage pins the image the tests assert on.
const sandboxGolangImage = "golang:1.22"

func agentConfigForTests() piagent.Config {
	return piagent.Config{
		Provider: "custom",
		Endpoint: "http://127.0.0.1:8765/v1",
		Model:    "mock-model",
	}
}

// goSeedMain is a single `package main` at the module root — the repo
// shape where `go build ./...` writes a binary into the root.
const goSeedMain = "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"shipyard v0\")\n}\n"

const goSeedMainFixed = "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"shipyard v1\")\n}\n"

// newGoE2ERemote is newE2ERemote's twin seeding a Go module instead of
// the Python hello.py repo.
func newGoE2ERemote(t *testing.T) (bare string) {
	t.Helper()
	dir := t.TempDir()
	bare = filepath.Join(dir, "origin.git")
	workdir := filepath.Join(dir, "seed")
	runE2EGit(t, "", "init", "--bare", "-b", "main", bare)
	mustE2E(t, os.MkdirAll(workdir, 0o755))
	mustE2E(t, os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module m\n\ngo 1.22\n"), 0o644))
	mustE2E(t, os.WriteFile(filepath.Join(workdir, "main.go"), []byte(goSeedMain), 0o644))
	runE2EGit(t, workdir, "init", "-b", "main")
	runE2EGit(t, workdir, "add", "-A")
	runE2EGit(t, workdir, "-c", "user.name=seed", "-c", "user.email=seed@test", "commit", "--quiet", "-m", "seed")
	runE2EGit(t, workdir, "remote", "add", "origin", bare)
	runE2EGit(t, workdir, "push", "--quiet", "-u", "origin", "main")
	return bare
}

// TestSolveLiveAgentInSandbox is the wiring acceptance test: a live
// solve with an explicit image runs the built-in agent in the wrapper
// container over it; the agent's changes land through the container,
// the commit/push/PR then proceed natively.
func TestSolveLiveAgentInSandbox(t *testing.T) {
	gitAvailable(t)
	testpi.Install(t)
	stubLogPath := installDockerStub(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  piagent.DefaultRunner,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image:       "ubuntu:24.04", // explicit → source: flag; no verify steps
		AgentConfig: agentConfigForTests(),
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR == nil {
		t.Fatal("live run opened no pull request")
	}
	if res.Sandbox != piagent.WrapperImageName("ubuntu:24.04") {
		t.Errorf("Result.Sandbox = %q, want the built-in wrapper %q", res.Sandbox, piagent.WrapperImageName("ubuntu:24.04"))
	}
	if !log.has("sandbox: ubuntu:24.04 (source: flag)") {
		t.Errorf("missing the sandbox audit line")
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
// fails in the sandbox (here: `go build` of a tree the agent broke)
// must stop the run before commit, push, and PR.
func TestSolveLiveSandboxVerificationFailure(t *testing.T) {
	gitAvailable(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed; the verification step would run on the host through the stub")
	}
	testpi.Install(t)
	installDockerStub(t)
	t.Setenv("PI_STUB_FIX_FILE", "main.go")
	t.Setenv("PI_STUB_FIX_CONTENT", "package main\n// the agent broke the build\n")
	bare := newGoE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "bump the greeting"}}, nil)
	log := newSolveLog()

	_, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  piagent.DefaultRunner,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image:       sandboxGolangImage, // its `go build` step fails on the agent's tree
		AgentConfig: agentConfigForTests(),
	})
	if err == nil {
		t.Fatal("Solve succeeded although a verification step failed")
	}
	if !strings.Contains(err.Error(), "verify step") {
		t.Errorf("error = %v, want a failed verification step", err)
	}
	if !log.has("sandbox: golang:1.22 (source: flag)") {
		t.Errorf("missing the sandbox audit line")
	}
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); strings.TrimSpace(refs) != "" {
		t.Errorf("branch pushed although the verification step failed: %q", refs)
	}
}

// TestSolveLiveNoDockerFallsBackNative: a live run without Docker must
// run the agent natively on the host and say so in the audit line.
func TestSolveLiveNoDockerFallsBackNative(t *testing.T) {
	gitAvailable(t)
	testpi.Install(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub:   githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:    piagent.DefaultRunner,
		Git:      solve.ExecGit,
		DockerOK: func(context.Context) bool { return false },
		Log:      log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Image:       sandboxGolangImage, // irrelevant: no sandbox without Docker
		AgentConfig: agentConfigForTests(),
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
	if !log.has("natively on the host") {
		t.Errorf("missing the native agent line:\n%s", log)
	}
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
}

// TestSolveDryRunInSandbox: a dry run with Docker available runs the
// agent in the sandbox (the agent executes code, so the container is
// the safe choice even for dry runs) and still commits, pushes, and
// opens nothing.
func TestSolveDryRunInSandbox(t *testing.T) {
	gitAvailable(t)
	testpi.Install(t)
	stubLogPath := installDockerStub(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	log := newSolveLog()

	res, err := solve.Solve(context.Background(), solve.Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  piagent.DefaultRunner,
		Git:    solve.ExecGit,
		Log:    log.logf,
	}, solve.Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		DryRun:      true,
		AgentConfig: agentConfigForTests(),
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR != nil {
		t.Error("dry run opened a pull request")
	}
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); strings.TrimSpace(refs) != "" {
		t.Errorf("dry run pushed a branch: %q", refs)
	}
	if !log.has("dry run") {
		t.Errorf("missing the dry-run notice:\n%s", log)
	}
	// The agent ran in the stubbed container ...
	if stubLog := readStubLog(t, stubLogPath); !strings.Contains(stubLog, "docker run") {
		t.Errorf("dry run did not use the sandbox (Docker was available):\n%s", stubLog)
	}
}
