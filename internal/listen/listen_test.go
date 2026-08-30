package listen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pefman/Shipyard/internal/githubclient"
	"github.com/pefman/Shipyard/internal/guardrails"
	"github.com/pefman/Shipyard/internal/piagent"
	"github.com/pefman/Shipyard/internal/solve"
)

// Unit tests for the polling/loop logic of listen mode. The GitHub API
// is an in-process HTTP mock; git and the agent are faked, so these
// tests cover the loop itself (filters, skipping, state, shutdown)
// without a git binary and without an agent binary. The full flow with
// real git and the stub agent is covered in listen_e2e_test.go.

type testIssue struct {
	number int
	title  string
	labels []string
	isPR   bool
}

var testIssues = []testIssue{
	{number: 11, title: "bug one", labels: []string{"bug"}},
	{number: 12, title: "no labels"},
	{number: 13, title: "feature", labels: []string{"feature"}},
	{number: 77, title: "a PR entry", isPR: true},
}

type fakeGitHub struct {
	srv        *httptest.Server
	mu         sync.Mutex
	issues     []testIssue
	missing    map[int]bool // issue numbers whose detail fetch 404s
	prsByHead  map[string][]githubclient.PR
	createdPRs []githubclient.PRRequest
}

func newFakeGitHub(t *testing.T, issues []testIssue) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{
		issues:    issues,
		missing:   map[int]bool{},
		prsByHead: map[string][]githubclient.PR{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/towner/trepo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"full_name":"towner/trepo","private":false,"clone_url":"unused","default_branch":"main","html_url":"https://github.com/towner/trepo"}`))
	})
	mux.HandleFunc("GET /repos/towner/trepo/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("issue list state = %q, want open", r.URL.Query().Get("state"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`)) // no more pages
			return
		}
		var entries []string
		for _, is := range f.issues {
			s := fmt.Sprintf(`{"number":%d,"title":%s,"body":"body of %d","state":"open","html_url":"https://github.com/towner/trepo/issues/%d","labels":[`,
				is.number, jsonString(is.title), is.number, is.number)
			names := make([]string, 0, len(is.labels))
			for _, l := range is.labels {
				names = append(names, `{"name":"`+l+`"}`)
			}
			s += strings.Join(names, ",") + "]"
			if is.isPR {
				s += `,"pull_request":{"url":"x"}`
			}
			s += "}"
			entries = append(entries, s)
		}
		_, _ = w.Write([]byte("[" + strings.Join(entries, ",") + "]"))
	})
	mux.HandleFunc("GET /repos/towner/trepo/issues/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		if f.missing[n] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		for _, is := range f.issues {
			if is.number == n {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fmt.Sprintf(
					`{"number":%d,"title":%s,"body":"body of %d","state":"open","html_url":"https://github.com/towner/trepo/issues/%d"}`,
					n, jsonString(is.title), n, n)))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("GET /repos/towner/trepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		prs := f.prsByHead[r.URL.Query().Get("head")]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if len(prs) == 0 {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var out []string
		for _, pr := range prs {
			out = append(out, fmt.Sprintf(`{"number":%d,"title":%q,"state":%q,"html_url":%q}`, pr.Number, pr.Title, pr.State, pr.HTMLURL))
		}
		_, _ = w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	})
	mux.HandleFunc("POST /repos/towner/trepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req githubclient.PRRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.createdPRs = append(f.createdPRs, req)
		w.Header().Set("Content-Type", "application/json")
		n := len(f.createdPRs)
		_, _ = fmt.Fprintf(w, `{"number":%d,"title":%q,"state":"open","html_url":"https://github.com/towner/trepo/pull/%d"}`, n, req.Title, n)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// fakeGit is a solve.GitRunner that pretends every git command
// succeeds: no files are read or written, so tests exercise only the
// listen loop. The cached diff reports the change the faked agent
// "made", so the flow finds something to commit.
type fakeGit struct{}

func (fakeGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	if args[0] == "ls-files" {
		return "hello.py\nREADME.md", nil
	}
	if args[0] == "diff" {
		return "diff --git a/hello.py b/hello.py\n--- a/hello.py\n+++ b/hello.py\n@@ -1 +1 @@\n-old\n+new\n", nil
	}
	return "", nil
}

// fakeAgent is a piagent.Runner that always succeeds without touching
// anything: the listen unit tests fake the agent exactly like they fake
// git; the loop logic is the unit under test.
type fakeAgent struct{}

func (fakeAgent) RunAgent(ctx context.Context, spec piagent.RunSpec) (*piagent.RunOutcome, error) {
	// Respect cancellation the way a real agent run does: the agent
	// step is where a shutdown mid-solve lands.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &piagent.RunOutcome{
		Container: spec.BaseImage != "",
		Image:     spec.BaseImage,
		Turns:     1,
		Summary:   "fake agent: fixed it",
	}, nil
}

// withAgentConfig returns o with the dummy agent config the flow's
// validation requires; the faked agent never connects to it.
func withAgentConfig(o Options) Options {
	if o.AgentConfig.Endpoint == "" {
		o.AgentConfig = piagent.Config{Provider: "custom", Endpoint: "http://127.0.0.1:1/v1", Model: "mock-model"}
	}
	return o
}

// cancelOnFirstGit is a fake git runner that cancels its context on the
// first git call it sees, simulating SIGINT arriving in the middle of a
// solve.
type cancelOnFirstGit struct {
	cancel context.CancelFunc
}

func (c *cancelOnFirstGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if args[0] == "ls-files" {
		return "hello.py\nREADME.md", nil
	}
	return "", nil
}

func newTestDeps(t *testing.T, gh *fakeGitHub, git solve.GitRunner) Deps {
	t.Helper()
	return Deps{
		GitHub: githubclient.NewClient(gh.srv.URL, "gh-token"),
		Agent:  fakeAgent{},
		Git:    git,
		// Force the native agent path: these tests must not depend
		// on a Docker daemon being present or not.
		DockerOK: func(context.Context) bool { return false },
		Log:      func(format string, args ...any) {}, // keep test output quiet
	}
}

// TestRunOnceSolvesNewIssues is the acceptance test for the loop: one
// pass over a repo's open issues runs the solving flow on each of them
// (pull requests are not issues and must be ignored) and records them
// in the state file.
func TestRunOnceSolvesNewIssues(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: stateFile,
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Seen != 3 || out.New != 3 || out.Skipped != 0 || out.Failed != 0 {
		t.Errorf("outcome = %+v, want Seen=3 New=3 Skipped=0 Failed=0", out)
	}
	gh.mu.Lock()
	if len(gh.createdPRs) != 3 {
		t.Errorf("created %d pull requests, want 3 (one per issue)", len(gh.createdPRs))
	}
	gh.mu.Unlock()

	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, n := range []int{11, 12, 13} {
		if _, ok := st.IsProcessed(n); !ok {
			t.Errorf("state does not record issue %d", n)
		}
	}
	if _, ok := st.IsProcessed(77); ok {
		t.Errorf("state records pull request 77 as an issue")
	}
}

// TestRunOnceSkipsProcessed: a second pass (or a restart, since the
// state persists) must not re-solve issues that were already handled.
func TestRunOnceSkipsProcessed(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := newTestDeps(t, gh, fakeGit{})

	if _, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile}); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Fresh deps (a restart) with the same state file: everything is
	// already processed, so no new pull request.
	d2 := newTestDeps(t, gh, fakeGit{})
	out, err := d2.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if out.Seen != 3 || out.New != 0 || out.Skipped != 3 {
		t.Errorf("outcome = %+v, want Seen=3 New=0 Skipped=3", out)
	}
	gh.mu.Lock()
	if len(gh.createdPRs) != 3 { // still only the first pass's three
		t.Errorf("created %d pull requests across both passes, want 3", len(gh.createdPRs))
	}
	gh.mu.Unlock()
}

// TestRunOnceLabelFilter: with a label filter, only issues carrying one
// of the labels are seen at all.
func TestRunOnceLabelFilter(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: stateFile, Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Seen != 1 || out.New != 1 {
		t.Errorf("outcome = %+v, want Seen=1 New=1 (only the issue with label bug)", out)
	}
	gh.mu.Lock()
	if len(gh.createdPRs) != 1 {
		t.Errorf("created %d pull requests, want 1", len(gh.createdPRs))
	}
	gh.mu.Unlock()
}

// TestRunOnceLabelFilterCaseInsensitive: the loop's label filter uses
// the shared guardrails matching, so a case-different entry behaves
// like on solve: --labels BUG matches an issue labelled "bug".
func TestRunOnceLabelFilterCaseInsensitive(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		Labels: []string{"BUG"},
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Seen != 1 || out.New != 1 {
		t.Errorf("outcome = %+v, want Seen=1 New=1 (bug matched case-insensitively)", out)
	}
}

// TestRunOnceSeedsStateFromExistingPR: a lost state file must not make
// the listener re-solve an issue that already has a pull request on
// its fix branch.
func TestRunOnceSeedsStateFromExistingPR(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	gh.prsByHead["towner:shipyard/issue-11"] = []githubclient.PR{
		{Number: 50, Title: "Fix #11", State: "open", HTMLURL: "https://github.com/towner/trepo/pull/50"},
	}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.Skipped != 1 || out.New != 2 {
		t.Errorf("outcome = %+v, want Skipped=1 (issue 11 has a PR) New=2", out)
	}
	gh.mu.Lock()
	if len(gh.createdPRs) != 2 {
		t.Errorf("created %d pull requests, want 2 (issue 11 is already handled)", len(gh.createdPRs))
	}
	gh.mu.Unlock()

	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if url, ok := st.IsProcessed(11); !ok || url != "https://github.com/towner/trepo/pull/50" {
		t.Errorf("state for issue 11 = %q (processed=%v), want the existing PR URL", url, ok)
	}
}

// TestRunOnceFailureIsLoggedAndRetried: an issue the solving flow
// rejects does not abort the pass and is not marked processed, so a
// later pass retries it.
func TestRunOnceFailureIsLoggedAndRetried(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	gh.missing[12] = true // fetching issue 12's details fails
	stateFile := filepath.Join(t.TempDir(), "state.json")
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: stateFile})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.New != 3 || out.Failed != 1 {
		t.Errorf("outcome = %+v, want New=3 Failed=1 (issue 12)", out)
	}
	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := st.IsProcessed(12); ok {
		t.Errorf("failed issue 12 must not be marked processed")
	}
	for _, n := range []int{11, 13} {
		if _, ok := st.IsProcessed(n); !ok {
			t.Errorf("issue %d should be recorded despite issue 12 failing", n)
		}
	}
}

// TestRunOnceStopsOnCancellation: when the context is canceled in the
// middle of a pass, the pass stops (the in-flight issue is aborted,
// counted neither as solved nor as failed) and no further issue is
// picked up.
func TestRunOnceStopsOnCancellation(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	ctx, cancel := context.WithCancel(context.Background())
	d := newTestDeps(t, gh, &cancelOnFirstGit{cancel: cancel})

	out, err := d.RunOnce(ctx, Options{Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.New != 1 || out.Failed != 0 {
		t.Errorf("outcome = %+v, want the first issue aborted mid-solve and the rest untouched (New=1, Failed=0)", out)
	}
	gh.mu.Lock()
	if len(gh.createdPRs) != 0 {
		t.Errorf("%d pull request(s) created after shutdown, want 0", len(gh.createdPRs))
	}
	gh.mu.Unlock()
}

// TestRunStopsOnContextCancel: Run loops until the context is canceled
// and then returns cleanly (the graceful-shutdown acceptance
// criterion).
func TestRunStopsOnContextCancel(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	dir := t.TempDir()
	d := newTestDeps(t, gh, fakeGit{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, Options{
			Owner: "towner", Repo: "trepo",
			StateFile: filepath.Join(dir, "state.json"),
			Interval:  10 * time.Millisecond,
			Unguarded: true, // guardrails are not under test here
		})
	}()

	// Let the first pass run and at least one tick elapse.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after the context was canceled")
	}
	gh.mu.Lock()
	if len(gh.createdPRs) == 0 {
		t.Error("no issue was solved before shutdown")
	}
	gh.mu.Unlock()
}

// TestRunOnceCorruptStateFileFails: a corrupt state file is reported,
// not swallowed — the operator has to fix or delete it.
func TestRunOnceCorruptStateFileFails(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RunOnce(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: path}); err == nil {
		t.Fatal("RunOnce: expected an error for a corrupt state file")
	}
}

// TestRunRefusesUnguarded: with neither a repo nor a label allowlist
// set, a live run that has not been acknowledged with
// --i-know-this-is-unguarded is refused before it does any work.
func TestRunRefusesUnguarded(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	err := d.Run(context.Background(), Options{Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json")})
	if !errors.Is(err, guardrails.ErrUnguarded) {
		t.Fatalf("Run = %v, want the unguarded refusal", err)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 0 {
		t.Errorf("%d pull request(s) created by a refused run, want 0", n)
	}
	gh.mu.Unlock()
}

// TestRunDryRunUnguardedProceeds: a dry run commits nothing and opens
// no pull requests, so it starts with no allowlist and no
// acknowledgment — this is the safe default for a fresh installation.
func TestRunDryRunUnguardedProceeds(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, Options{
			Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
			Interval: 10 * time.Millisecond,
			DryRun:   true,
		})
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after the context was canceled")
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 0 {
		t.Errorf("%d pull request(s) created by a dry run, want 0", n)
	}
	gh.mu.Unlock()
}

// lineLog collects the run's log lines in order.
type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineLog) printf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *lineLog) first() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return l.lines[0]
}

// TestRunLiveModeAuditLineFirst: in live mode the very first log line
// is the guardrails audit line (mode, allowlists, pull-request budget),
// so a long-running container's first line is an audit line.
func TestRunLiveModeAuditLineFirst(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	log := &lineLog{}
	d := newTestDeps(t, gh, fakeGit{})
	d.Log = log.printf

	err := d.Run(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		Labels: []string{"bug"},
		MaxPRs: 1, // the run stops on its own once the budget is spent
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	first := log.first()
	if !strings.HasPrefix(first, "live mode: guardrails: labels: bug; max-prs: 1") {
		t.Errorf("first log line = %q, want the live-mode guardrails audit line", first)
	}
}

// TestRunDryRunModeAuditLineFirst: a dry run announces its mode and the
// (inert) guardrails as its first log line, and makes explicit how to
// go live.
func TestRunDryRunModeAuditLineFirst(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	log := &lineLog{}
	d := newTestDeps(t, gh, fakeGit{})
	d.Log = log.printf

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, Options{
			Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
			Interval: 10 * time.Millisecond,
			DryRun:   true,
		})
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after the context was canceled")
	}
	first := log.first()
	if !strings.HasPrefix(first, "dry-run mode:") {
		t.Errorf("first log line = %q, want the dry-run mode audit line", first)
	}
	if !strings.Contains(first, "SHIPYARD_MODE=live") {
		t.Errorf("dry-run audit line should tell the operator how to go live: %q", first)
	}
}

// TestRunUnguardedWithFlagProceeds: the same configuration with the
// explicit acknowledgment passes the guard and runs.
func TestRunUnguardedWithFlagProceeds(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	// MaxPRs 1 so the run stops on its own once it has proved it ran.
	err := d.Run(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		Unguarded: true,
		MaxPRs:    1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 1 {
		t.Errorf("%d pull request(s) created, want 1", n)
	}
	gh.mu.Unlock()
}

// TestRunExplicitAllStartsAndAudits: --all ("no allowlist on this
// axis, on purpose") lets an unguarded live run start without the
// sentence-flag, and the first log line is the audit line that tells
// a long-running container's operator the run is deliberately
// unguarded.
func TestRunExplicitAllStartsAndAudits(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	lg := &lineLog{}
	d := newTestDeps(t, gh, fakeGit{})
	d.Log = lg.printf
	// MaxPRs 1 so the run stops on its own once it has proved it ran.
	err := d.Run(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		All:    true,
		MaxPRs: 1,
	})
	if err != nil {
		t.Fatalf("Run with --all: %v, want the run to start", err)
	}
	first := lg.first()
	if !strings.HasPrefix(first, "live mode: guardrails: NONE (explicit --all); max-prs: 1") {
		t.Errorf("first log line = %q, want the explicit --all audit line", first)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 1 {
		t.Errorf("%d pull request(s) created, want 1", n)
	}
	gh.mu.Unlock()
}

// TestRunExplicitAllConflictsWithAllowlist: --all is "no allowlist on
// this axis, on purpose", so combining it with a set allowlist is a
// configuration error — the same treatment as --live + --dry-run —
// and the run starts nothing.
func TestRunExplicitAllConflictsWithAllowlist(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	for name, o := range map[string]Options{
		"--all + labels": {
			Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
			All: true, Labels: []string{"bug"},
		},
		"--all + repos": {
			Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
			All: true, Repos: []string{"towner/trepo"},
		},
	} {
		err := d.Run(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Errorf("Run (%s) = %v, want the --all/allowlist conflict", name, err)
		}
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 0 {
		t.Errorf("%d pull request(s) created by refused runs, want 0", n)
	}
	gh.mu.Unlock()
}

// TestRunRefusesRepoNotInAllowlist: a listener pointed at a repo that
// is not on the repo allowlist starts nothing.
func TestRunRefusesRepoNotInAllowlist(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	err := d.Run(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		Repos: []string{"other/repo"},
	})
	if err == nil || !strings.Contains(err.Error(), "not in the repo allowlist") {
		t.Fatalf("Run = %v, want a repo-allowlist refusal", err)
	}
}

// TestRunStopsAtPRCap: once the run's pull-request budget is spent,
// the listener stops the run — a clean exit, not an error — with one
// pull request opened and the rest of the issues left for a later run.
func TestRunStopsAtPRCap(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	err := d.Run(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		MaxPRs:    1,
		Unguarded: true,
	})
	if err != nil {
		t.Fatalf("Run = %v, want a clean exit at the pull-request cap", err)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 1 {
		t.Errorf("%d pull request(s) opened, want exactly 1 (the cap)", n)
	}
	gh.mu.Unlock()
}

// TestRunOncePRCap: a RunOnce pass handed a cap stops mid-pass once
// the budget is spent; the issues it skipped are not marked processed.
func TestRunOncePRCap(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})
	stateFile := filepath.Join(t.TempDir(), "state.json")

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: stateFile,
		PRCap: guardrails.NewPRCap(1),
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !out.CapReached || out.PRsOpened != 1 || out.New != 1 {
		t.Errorf("outcome = %+v, want CapReached with 1 PR after 1 issue", out)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 1 {
		t.Errorf("%d pull request(s) opened, want 1", n)
	}
	gh.mu.Unlock()

	st, err := LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := st.IsProcessed(13); ok {
		t.Error("an issue skipped at the cap must not be marked processed")
	}
}

// TestRunOncePRCapZero: a run whose budget is zero solves nothing and
// opens nothing, and reports the cap reached.
func TestRunOncePRCapZero(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		PRCap: guardrails.NewPRCap(0),
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !out.CapReached || out.New != 0 || out.PRsOpened != 0 {
		t.Errorf("outcome = %+v, want CapReached with nothing solved", out)
	}
	gh.mu.Lock()
	if n := len(gh.createdPRs); n != 0 {
		t.Errorf("%d pull request(s) opened under a zero cap, want 0", n)
	}
	gh.mu.Unlock()
}

// TestRunOnceDryRunIgnoresPRCap: dry runs open no pull requests, so
// the cap never trips and every issue is processed.
func TestRunOnceDryRunIgnoresPRCap(t *testing.T) {
	gh := newFakeGitHub(t, testIssues)
	d := newTestDeps(t, gh, fakeGit{})

	out, err := d.RunOnce(context.Background(), Options{
		Owner: "towner", Repo: "trepo", StateFile: filepath.Join(t.TempDir(), "state.json"),
		DryRun: true,
		PRCap:  guardrails.NewPRCap(1),
	})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out.New != 3 || out.PRsOpened != 0 || out.CapReached {
		t.Errorf("outcome = %+v, want all 3 dry-run-solved with no cap", out)
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
