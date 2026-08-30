package solve

// End-to-end tests for the solving flow: real git (clone, agent run,
// commit, push) against a local bare "remote", with the GitHub API
// served by an in-process mock and the built-in pi agent stubbed on
// PATH (internal/testpi). They exercise the same code the CLI runs,
// minus the real GitHub host and the real agent.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/testpi"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; skipping end-to-end test")
	}
}

const (
	seedHello = "def greet(name):\n    return \"Hello, \" + name\n"

	seedHelloFixed = `def greet(name):
    if not name:
        return "Hello, stranger"
    return "Hello, " + name
`
)

type fakeGitHub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	pulls []githubclient.PRRequest
	// prStatus/prBody control the POST /pulls response.
	prStatus int
	prBody   string
	cloneURL string
}

func newFakeGitHub(t *testing.T, cloneURL string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{prStatus: http.StatusCreated, cloneURL: cloneURL}
	f.prBody = `{"number": 42, "title": "t", "state": "open", "html_url": "https://github.com/towner/trepo/pull/42"}`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/towner/trepo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"full_name":"towner/trepo","private":false,"clone_url":%s,"default_branch":"main","html_url":"https://github.com/towner/trepo"}`, jsonString(cloneURL))
	})
	mux.HandleFunc("GET /repos/towner/trepo/issues/9", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":9,"title":"greet() crashes on empty input","body":"greet('') should return a fallback greeting","state":"open","html_url":"https://github.com/towner/trepo/issues/9","labels":[{"name":"bug"}]}`))
	})
	mux.HandleFunc("POST /repos/towner/trepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		// Decode from the raw wire format (not the Go struct): the
		// previous version mirrored the struct's field names and masked
		// a missing-json-tags bug.
		var raw map[string]string
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Errorf("pull request payload is not a JSON object: %v", err)
		}
		for _, k := range []string{"title", "head", "base", "body"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("pull request payload missing lowercase key %q: %s", k, body)
			}
		}
		var pr githubclient.PRRequest
		_ = json.Unmarshal(body, &pr)
		f.pulls = append(f.pulls, pr)
		w.WriteHeader(f.prStatus)
		_, _ = w.Write([]byte(f.prBody))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// installAgent installs the stub pi binary on PATH. The stub acts on
// the task file shipyard wrote into the checkout: it "fixes" the repo
// when the task carries the seed issue, and makes no changes
// otherwise (PI_STUB_MODE=noop). The test asserts afterwards that the
// task file really carried the issue.
func installAgent(t *testing.T, mode, fixFile, fixContent string) {
	t.Helper()
	testpi.Install(t)
	if mode != "" {
		t.Setenv("PI_STUB_MODE", mode)
	}
	if fixFile != "" {
		t.Setenv("PI_STUB_FIX_FILE", fixFile)
	}
	if fixContent != "" {
		t.Setenv("PI_STUB_FIX_CONTENT", fixContent)
	}
}

// assertTaskWritten checks that the agent run wrote the task file with
// the issue in it (the stub agent worked from that file).
func assertTaskWritten(t *testing.T, workdir string, wants ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workdir, piagent.DirName, piagent.TaskFile))
	if err != nil {
		t.Fatalf("the agent's task file was not written: %v", err)
	}
	for _, want := range wants {
		if !strings.Contains(string(data), want) {
			t.Errorf("task file missing %q:\n%s", want, data)
		}
	}
}

// newFakeRemote creates a bare repo plus a working seed checkout pushed
// to it: the local stand-in for a GitHub repository.
func newFakeRemote(t *testing.T) (bare, workdir string) {
	t.Helper()
	dir := t.TempDir()
	bare = filepath.Join(dir, "origin.git")
	workdir = filepath.Join(dir, "seed")
	run(t, "", "init", "--bare", "-b", "main", bare)
	must(t, os.MkdirAll(workdir, 0o755))
	must(t, os.WriteFile(filepath.Join(workdir, "hello.py"), []byte(seedHello), 0o644))
	must(t, os.WriteFile(filepath.Join(workdir, "README.md"), []byte("# test repo\n"), 0o644))
	run(t, workdir, "init", "-b", "main")
	run(t, workdir, "add", "-A")
	run(t, workdir, "-c", "user.name=seed", "-c", "user.email=seed@test", "commit", "--quiet", "-m", "seed")
	run(t, workdir, "remote", "add", "origin", bare)
	run(t, workdir, "push", "--quiet", "-u", "origin", "main")
	return bare, workdir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, errb.String())
	}
	return out.String()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func agentConfig() piagent.Config {
	return piagent.Config{
		Provider: "custom",
		Endpoint: "http://127.0.0.1:8765/v1",
		Model:    "mock-model",
	}
}

// newDeps builds the solve dependencies for the end-to-end tests: the
// stub agent as engine and the native run path (no Docker), so the
// tests never need a Docker daemon or a live model.
func newDeps(gh *fakeGitHub, t *testing.T) Deps {
	return Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  piagent.DefaultRunner,
		// Native agent path: the stub pi binary on PATH does the work.
		DockerOK: func(context.Context) bool { return false },
		Log:      func(format string, args ...any) {}, // keep test output quiet
	}
}

// TestSolveE2E is the acceptance test for the core loop: one issue in
// one repo produces one reviewable pull request, with the changes made
// by the agent on the checkout.
func TestSolveE2E(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	bare, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, bare)
	d := newDeps(gh, t)

	res, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir:     workdir, // build on the seeded checkout
		AgentConfig: agentConfig(),
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.Branch != "shipyard/issue-9" || res.Base != "main" {
		t.Errorf("branch/base = %s/%s, want shipyard/issue-9/main", res.Branch, res.Base)
	}
	if res.PR == nil || res.PR.Number != 42 {
		t.Fatalf("expected an opened PR, got %+v", res.PR)
	}
	assertTaskWritten(t, workdir,
		"issue #9", "greet() crashes on empty input", "bug",
		"greet('') should return a fallback greeting", "Do not commit",
	)

	// The branch landed on the remote.
	refs := run(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9")
	if !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
	// ... with the agent's fix.
	show := run(t, bare, "show", "shipyard/issue-9:hello.py")
	if !strings.Contains(show, "Hello, stranger") {
		t.Errorf("remote branch does not contain the fix:\n%s", show)
	}
	// The PR request linked the source issue and used the right branches.
	gh.mu.Lock()
	if len(gh.pulls) != 1 {
		t.Fatalf("expected exactly 1 PR creation, got %d", len(gh.pulls))
	}
	pr := gh.pulls[0]
	gh.mu.Unlock()
	if pr.Head != "shipyard/issue-9" || pr.Base != "main" {
		t.Errorf("PR head/base = %s/%s", pr.Head, pr.Base)
	}
	if !strings.Contains(pr.Body, "towner/trepo/issues/9") && !strings.Contains(pr.Body, "issues/9") {
		t.Errorf("PR body should link the source issue: %q", pr.Body)
	}
	if !strings.Contains(pr.Title, "#9") || !strings.Contains(pr.Title, "greet()") {
		t.Errorf("PR title = %q", pr.Title)
	}
	if !strings.Contains(pr.Body, "built-in pi agent") {
		t.Errorf("PR body should credit the built-in agent: %q", pr.Body)
	}
	// The workdir holds the agent's fix, and its agent config directory
	// did not leak into the branch.
	files := run(t, bare, "ls-tree", "-r", "--name-only", "shipyard/issue-9")
	if strings.Contains(files, piagent.DirName) {
		t.Errorf("the agent's config directory leaked into the branch:\n%s", files)
	}
	data, err := os.ReadFile(filepath.Join(workdir, "hello.py"))
	must(t, err)
	if !strings.Contains(string(data), "Hello, stranger") {
		t.Errorf("workdir file not fixed:\n%s", data)
	}
}

// TestSolveE2EClone covers the flow when no local checkout is given:
// Shipyard must clone, run the agent, and deliver.
func TestSolveE2EClone(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	bare, _ := newFakeRemote(t)
	gh := newFakeGitHub(t, bare) // clone_url points at the bare repo
	d := newDeps(gh, t)
	d.TempDir = t.TempDir()

	res, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		AgentConfig: agentConfig(),
	})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR == nil {
		t.Fatal("expected an opened PR")
	}
	if refs := run(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed: %q", refs)
	}
}

func TestSolveBranchCollision(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	bare, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, bare)
	d := newDeps(gh, t)
	opts := Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig()}

	// First run delivers the branch; the second run with the same name
	// must fail with a clear error instead of a confusing push rejection.
	if _, err := Solve(context.Background(), d, opts); err != nil {
		t.Fatalf("first Solve: %v", err)
	}
	_, err := Solve(context.Background(), d, opts)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Solve: want already-exists error, got %v", err)
	}
}

// TestSolveE2ENoUsableChanges: an agent run that leaves the repository
// unchanged must fail with ErrNoUsableChanges (nothing to review).
func TestSolveE2ENoUsableChanges(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "noop", "", "")
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	d := newDeps(gh, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig()})
	if !errors.Is(err, ErrNoUsableChanges) {
		t.Fatalf("want ErrNoUsableChanges, got %v", err)
	}
}

// TestSolveE2EAgentFails: a failing agent run (non-zero exit) must fail
// the solve before any commit, push, or PR.
func TestSolveE2EAgentFails(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fail", "", "")
	bare, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	d := newDeps(gh, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig()})
	if err == nil || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("want the agent failure, got %v", err)
	}
	if refs := run(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); strings.TrimSpace(refs) != "" {
		t.Errorf("branch pushed although the agent failed: %q", refs)
	}
}

// TestSolveE2ETurnBudget: an agent that keeps going past the turn
// budget is stopped, and the run fails without commit, push, or PR.
func TestSolveE2ETurnBudget(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "noop", "", "")
	t.Setenv("PI_STUB_TURNS", "5")
	t.Setenv("PI_STUB_SLEEP", "0.2")
	bare, _ := newFakeRemote(t)
	gh := newFakeGitHub(t, bare)
	d := newDeps(gh, t)
	d.TempDir = t.TempDir()

	_, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		AgentConfig:   agentConfig(),
		AgentMaxTurns: 2, AgentTimeout: 30 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "turn budget") {
		t.Fatalf("want the turn budget error, got %v", err)
	}
}

func TestSolveE2EMissingPRPermissions(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	gh.prStatus = http.StatusForbidden
	gh.prBody = `{"message": "Resource not accessible by integration"}`
	d := newDeps(gh, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig()})
	if err == nil || !strings.Contains(err.Error(), "opening pull request") {
		t.Fatalf("want PR-opening error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing the permissions") {
		t.Errorf("error should hint at token permissions: %v", err)
	}
}

func TestSolveDirtyWorkdirRejected(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	_, workdir := newFakeRemote(t)
	must(t, os.WriteFile(filepath.Join(workdir, "dirty.txt"), []byte("x"), 0o644))
	gh := newFakeGitHub(t, "")
	d := newDeps(gh, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig()})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("want uncommitted-changes error, got %v", err)
	}
}

// TestSolveDryRunLeavesWorkdir: a dry run runs the agent but commits,
// pushes, and opens nothing; the workdir keeps the agent's changes.
func TestSolveDryRunLeavesWorkdir(t *testing.T) {
	gitAvailable(t)
	installAgent(t, "fix", "hello.py", seedHelloFixed)
	bare, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, bare)
	d := newDeps(gh, t)

	res, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir, AgentConfig: agentConfig(), DryRun: true})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if res.PR != nil {
		t.Error("dry run opened a pull request")
	}
	if refs := run(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); strings.TrimSpace(refs) != "" {
		t.Errorf("dry run pushed a branch: %q", refs)
	}
	data, err := os.ReadFile(filepath.Join(workdir, "hello.py"))
	must(t, err)
	if !strings.Contains(string(data), "Hello, stranger") {
		t.Errorf("dry run workdir does not hold the agent's changes:\n%s", data)
	}
}

// jsonString is a small local JSON-string encoder for test fixtures.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
