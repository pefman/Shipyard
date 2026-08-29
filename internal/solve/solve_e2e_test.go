package solve

// End-to-end tests for the solving flow: real git (clone, apply, commit,
// push) against a local bare "remote", with the GitHub API and the AI
// endpoint served by in-process mocks. They exercise the same code the
// CLI runs, minus the real GitHub/AI hosts.

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

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/githubclient"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; skipping end-to-end test")
	}
}

const (
	seedHello = "def greet(name):\n    return \"Hello, \" + name\n"
	seedPatch = "diff --git a/hello.py b/hello.py\n" +
		"index 1234567..89abcde 100644\n" +
		"--- a/hello.py\n" +
		"+++ b/hello.py\n" +
		"@@ -1,2 +1,4 @@\n" +
		" def greet(name):\n" +
		"+    if not name:\n" +
		"+        return \"Hello, stranger\"\n" +
		"     return \"Hello, \" + name\n"
	seedResponse = "greet() crashes on empty input; it now returns a fallback greeting.\n" +
		"```diff\n" + seedPatch + "```\n"
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

func newFakeAI(t *testing.T, response string) *aiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	var lastPrompt string
	var aiCalled bool
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) > 0 {
			lastPrompt = req.Messages[0].Content
			aiCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, jsonString(response))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := aiclient.NewClient(srv.URL+"/v1", "ai-key")
	t.Cleanup(func() {
		if !aiCalled {
			return // the flow failed before reaching the AI endpoint
		}
		for _, want := range []string{"Issue #9: greet() crashes on empty input", "bug", "greet('') should return", "unified diff"} {
			if !strings.Contains(lastPrompt, want) {
				t.Errorf("prompt sent to AI missing %q:\n%s", want, lastPrompt)
			}
		}
	})
	return c
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

func newDeps(gh *fakeGitHub, ai *aiclient.Client, t *testing.T) Deps {
	return Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:     ai,
		Log:    func(format string, args ...any) {}, // keep test output quiet
	}
}

// TestSolveE2E is the acceptance test for the core loop: one issue in
// one repo produces one reviewable pull request.
func TestSolveE2E(t *testing.T) {
	gitAvailable(t)
	bare, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, bare)
	ai := newFakeAI(t, seedResponse)
	d := newDeps(gh, ai, t)

	res, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, // build on the seeded checkout
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

	// The branch landed on the remote.
	refs := run(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9")
	if !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
	// ... with the fixed code.
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
	// The workdir holds the applied fix.
	data, err := os.ReadFile(filepath.Join(workdir, "hello.py"))
	must(t, err)
	if !strings.Contains(string(data), "Hello, stranger") {
		t.Errorf("workdir file not fixed:\n%s", data)
	}
}

// TestSolveE2EClone covers the flow when no local checkout is given:
// Shipyard must clone, solve, and deliver.
func TestSolveE2EClone(t *testing.T) {
	gitAvailable(t)
	bare, _ := newFakeRemote(t)
	gh := newFakeGitHub(t, bare) // clone_url points at the bare repo
	ai := newFakeAI(t, seedResponse)
	d := newDeps(gh, ai, t)
	d.TempDir = t.TempDir()

	res, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
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

func TestSolveE2ENoUsableChanges(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	ai := newFakeAI(t, "I could not find a change that fixes this issue.")
	d := newDeps(gh, ai, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir})
	if !errors.Is(err, ErrNoUsableChanges) {
		t.Fatalf("want ErrNoUsableChanges, got %v", err)
	}
}

func TestSolveE2EPatchDoesNotApply(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	badPatch := "diff --git a/no_such_file.py b/no_such_file.py\n--- a/no_such_file.py\n+++ b/no_such_file.py\n@@ -1 +1 @@\n-a\n+b\n"
	ai := newFakeAI(t, "Fix:\n```diff\n"+badPatch+"```\n")
	gh := newFakeGitHub(t, "")
	d := newDeps(gh, ai, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir})
	if err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("want 'does not apply' error, got %v", err)
	}
	if !strings.Contains(err.Error(), ".patch") {
		t.Errorf("error should point at the saved patch file: %v", err)
	}
}

func TestSolveE2EMissingPRPermissions(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	gh.prStatus = http.StatusForbidden
	gh.prBody = `{"message": "Resource not accessible by integration"}`
	ai := newFakeAI(t, seedResponse)
	d := newDeps(gh, ai, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir})
	if err == nil || !strings.Contains(err.Error(), "opening pull request") {
		t.Fatalf("want PR-opening error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing the permissions") {
		t.Errorf("error should hint at token permissions: %v", err)
	}
}

func TestSolveDirtyWorkdirRejected(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	must(t, os.WriteFile(filepath.Join(workdir, "dirty.txt"), []byte("x"), 0o644))
	gh := newFakeGitHub(t, "")
	ai := newFakeAI(t, seedResponse)
	d := newDeps(gh, ai, t)

	_, err := Solve(context.Background(), d, Options{Owner: "towner", Repo: "trepo", IssueNumber: 9, Workdir: workdir})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("want uncommitted-changes error, got %v", err)
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
