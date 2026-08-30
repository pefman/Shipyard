package listen

// End-to-end tests for listen mode: real git (clone, apply, commit,
// push) against a local bare "remote", with the GitHub API and the AI
// endpoint served by in-process mocks. They exercise the same code the
// CLI runs, minus the real GitHub/AI hosts.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/aiclient"
	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/solve"
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

type e2eFakeGitHub struct {
	srv      *httptest.Server
	cloneURL string
	issues   []testIssue
	existing []githubclientPR
}

type githubclientPR struct {
	issue   int // the issue this PR fixes (the fix branch is shipyard/issue-<issue>)
	number  int
	title   string
	state   string
	htmlURL string
}

func newE2EGitHub(t *testing.T, cloneURL string, issues []testIssue, existing []githubclientPR) *e2eFakeGitHub {
	t.Helper()
	f := &e2eFakeGitHub{cloneURL: cloneURL, issues: issues, existing: existing}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/towner/trepo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cloneURLJSON, _ := json.Marshal(cloneURL)
		fmt.Fprintf(w, `{"full_name":"towner/trepo","private":false,"clone_url":%s,"default_branch":"main","html_url":"https://github.com/towner/trepo"}`, cloneURLJSON)
	})
	mux.HandleFunc("GET /repos/towner/trepo/issues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		entries := make([]string, 0, len(f.issues))
		for _, is := range f.issues {
			entries = append(entries, fmt.Sprintf(
				`{"number":%d,"title":%s,"body":"greet('') should return a fallback greeting","state":"open","html_url":"https://github.com/towner/trepo/issues/%d","labels":[]}`,
				is.number, jsonString(is.title), is.number))
		}
		_, _ = w.Write([]byte("[" + strings.Join(entries, ",") + "]"))
	})
	mux.HandleFunc("GET /repos/towner/trepo/issues/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		for _, is := range f.issues {
			if is.number == n {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fmt.Sprintf(
					`{"number":%d,"title":%s,"body":"greet('') should return a fallback greeting","state":"open","html_url":"https://github.com/towner/trepo/issues/%d"}`,
					n, jsonString(is.title), n)))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("GET /repos/towner/trepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		head := r.URL.Query().Get("head")
		if head == "" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var out []string
		for _, pr := range f.existing {
			if head == fmt.Sprintf("towner:shipyard/issue-%d", pr.issue) {
				out = append(out, fmt.Sprintf(`{"number":%d,"title":%q,"state":%q,"html_url":%q}`, pr.number, pr.title, pr.state, pr.htmlURL))
			}
		}
		_, _ = w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	})
	mux.HandleFunc("POST /repos/towner/trepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":42,"title":"t","state":"open","html_url":"https://github.com/towner/trepo/pull/42"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newE2EAIClient(t *testing.T) *aiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(seedResponse)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, body)
	}))
	t.Cleanup(srv.Close)
	return aiclient.NewClient(srv.URL+"/v1", "ai-key", "mock-model")
}

// newE2ERemote creates a bare repo plus a working seed checkout pushed
// to it: the local stand-in for a GitHub repository.
func newE2ERemote(t *testing.T) (bare, workdir string) {
	t.Helper()
	dir := t.TempDir()
	bare = filepath.Join(dir, "origin.git")
	workdir = filepath.Join(dir, "seed")
	runE2EGit(t, "", "init", "--bare", "-b", "main", bare)
	mustE2E(t, os.MkdirAll(workdir, 0o755))
	mustE2E(t, os.WriteFile(filepath.Join(workdir, "hello.py"), []byte(seedHello), 0o644))
	mustE2E(t, os.WriteFile(filepath.Join(workdir, "README.md"), []byte("# test repo\n"), 0o644))
	runE2EGit(t, workdir, "init", "-b", "main")
	runE2EGit(t, workdir, "add", "-A")
	runE2EGit(t, workdir, "-c", "user.name=seed", "-c", "user.email=seed@test", "commit", "--quiet", "-m", "seed")
	runE2EGit(t, workdir, "remote", "add", "origin", bare)
	runE2EGit(t, workdir, "push", "--quiet", "-u", "origin", "main")
	return bare, workdir
}

func runE2EGit(t *testing.T, dir string, args ...string) string {
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

func mustE2E(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestListenE2E is the acceptance test for listen mode against real
// git: one poll pass over a repo with an open issue clones the repo,
// solves the issue, pushes the fix branch, and opens a pull request;
// the state file records it.
func TestListenE2E(t *testing.T) {
	gitAvailable(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	d := Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:     ai,
		Git:    solve.ExecGit,
		// Native fix-step path: the end-to-end tests must not depend
		// on a Docker daemon (the sandbox wiring is covered by
		// solve_sandbox_e2e_test.go with a stub docker binary).
		DockerOK: func(context.Context) bool { return false },
		Log:      func(format string, args ...any) {},
	}

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: stateFile,
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Seen != 1 || out.New != 1 {
		t.Fatalf("outcome = %+v, want Seen=1 New=1", out)
	}

	// The fix landed on the "remote" ...
	if refs := runE2EGit(t, "", "ls-remote", "--heads", bare, "shipyard/issue-9"); !strings.Contains(refs, "refs/heads/shipyard/issue-9") {
		t.Errorf("branch not pushed to remote: %q", refs)
	}
	// ... with the fixed code.
	if show := runE2EGit(t, bare, "show", "shipyard/issue-9:hello.py"); !strings.Contains(show, "Hello, stranger") {
		t.Errorf("remote branch does not contain the fix:\n%s", show)
	}
	// ... and the state file recorded the issue with the PR URL.
	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if url, ok := st.IsProcessed(9); !ok || !strings.HasSuffix(url, "/pull/42") {
		t.Errorf("state for issue 9 = %q (processed=%v), want the PR URL", url, ok)
	}
}

// TestListenE2ESecondPassSkips: with the state file in place, a second
// pass does not touch the already-solved issue again (no re-solve, no
// duplicate branch).
func TestListenE2ESecondPassSkips(t *testing.T) {
	gitAvailable(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare, []testIssue{{number: 9, title: "greet() crashes on empty input"}}, nil)
	ai := newE2EAIClient(t)
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	d := Deps{
		GitHub:   githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:       ai,
		Git:      solve.ExecGit,
		DockerOK: func(context.Context) bool { return false },
		Log:      func(format string, args ...any) {},
	}

	if _, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	out, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if out.Seen != 1 || out.New != 0 || out.Skipped != 1 {
		t.Errorf("second pass = %+v, want Seen=1 New=0 Skipped=1", out)
	}
}

// TestListenE2ERecoveryFromLostState: even when the state file is lost
// and the fix branch already exists on the remote, the listener must
// notice the existing pull request (via the GitHub API) and skip the
// issue instead of failing on the branch collision.
func TestListenE2ERecoveryFromLostState(t *testing.T) {
	gitAvailable(t)
	bare, _ := newE2ERemote(t)
	gh := newE2EGitHub(t, bare,
		[]testIssue{{number: 9, title: "greet() crashes on empty input"}},
		[]githubclientPR{{issue: 9, number: 42, title: "Fix #9", state: "open", htmlURL: "https://github.com/towner/trepo/pull/42"}})
	ai := newE2EAIClient(t)
	d := Deps{
		GitHub:   githubclient.NewClient(gh.srv.URL, "gh-token"),
		AI:       ai,
		Git:      solve.ExecGit,
		DockerOK: func(context.Context) bool { return false },
		Log:      func(format string, args ...any) {},
	}

	// Fresh state file (lost state): the existing-PR check must seed
	// it and skip the solve.
	stateFile := filepath.Join(t.TempDir(), "state.json")
	out, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Seen != 1 || out.New != 0 || out.Skipped != 1 {
		t.Errorf("outcome = %+v, want Seen=1 New=0 Skipped=1 (existing PR)", out)
	}
	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := st.IsProcessed(9); !ok {
		t.Error("the skipped issue should be re-recorded in the state file")
	}
}
