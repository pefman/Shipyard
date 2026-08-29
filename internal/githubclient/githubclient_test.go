package githubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func startGitHubServer(t *testing.T, issue json.RawMessage, repo json.RawMessage) *Client {
	t.Helper()
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/7", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(issue)
	})
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("missing GitHub Accept header, got %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(repo)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if gotAuth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token auth header, got %q", gotAuth)
		}
	})
	return NewClient(srv.URL, "test-token")
}

func TestGetIssue(t *testing.T) {
	payload := `{
		"number": 7,
		"title": "Broken login",
		"body": "Login fails on submit.",
		"state": "open",
		"html_url": "https://github.com/owner/repo/issues/7",
		"labels": [{"name": "bug"}, {"name": "auth"}]
	}`
	repo := `{"full_name": "owner/repo", "private": false, "html_url": "https://github.com/owner/repo", "clone_url": "https://github.com/owner/repo.git", "default_branch": "main"}`
	c := startGitHubServer(t, []byte(payload), []byte(repo))

	issue, err := c.GetIssue(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Number != 7 {
		t.Errorf("Number = %d, want 7", issue.Number)
	}
	if issue.Title != "Broken login" {
		t.Errorf("Title = %q, want %q", issue.Title, "Broken login")
	}
	if issue.Body != "Login fails on submit." {
		t.Errorf("Body = %q", issue.Body)
	}
	want := []string{"bug", "auth"}
	if len(issue.Labels) != len(want) {
		t.Fatalf("Labels = %v, want %v", issue.Labels, want)
	}
	for i := range want {
		if issue.Labels[i] != want[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, issue.Labels[i], want[i])
		}
	}
}

func TestGetRepo(t *testing.T) {
	c := startGitHubServer(t, nil, []byte(`{"full_name": "owner/repo", "private": true, "html_url": "https://github.com/owner/repo"}`))

	repo, err := c.GetRepo(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if repo.FullName != "owner/repo" || !repo.Private {
		t.Errorf("Repo = %+v", repo)
	}
}

func TestListIssues(t *testing.T) {
	var (
		gotState   string
		gotPerPage int
		gotPages   []string // the "page=..." query values, in order
		mu         sync.Mutex
	)
	issue := func(n int, labels ...string) string {
		ls := []string{}
		for _, l := range labels {
			ls = append(ls, `{"name":"`+l+`"}`)
		}
		return fmt.Sprintf(`{"number":%d,"title":"issue %d","state":"open","html_url":"https://github.com/owner/repo/issues/%d","labels":[%s]}`, n, n, n, strings.Join(ls, ","))
	}
	prEntry := `{"number":200,"title":"a PR that is not an issue","state":"open","html_url":"https://github.com/owner/repo/pull/200","pull_request":{"url":"x"}}`
	// Page 1: 99 plain issues + 1 pull-request entry = a full page, so
	// the client must fetch page 2.
	var page1 []string
	for n := 1; n <= 99; n++ {
		page1 = append(page1, issue(n))
	}
	page1 = append(page1, prEntry)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		mu.Lock()
		gotState = r.URL.Query().Get("state")
		gotPerPage, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
		page := r.URL.Query().Get("page")
		gotPages = append(gotPages, page)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte("[" + strings.Join(page1, ",") + "]"))
		default:
			_, _ = w.Write([]byte("[" + issue(100, "bug") + "]"))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-token")

	issues, err := c.ListIssues(context.Background(), "owner", "repo", "open")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	mu.Lock()
	state, perPage, pages := gotState, gotPerPage, gotPages
	mu.Unlock()
	if state != "open" || perPage != 100 {
		t.Errorf("query = state %q per_page %d, want open/100", state, perPage)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Errorf("pages fetched = %v, want [1 2] (pagination until a short page)", pages)
	}
	if len(issues) != 100 { // 99 from page 1 + 1 from page 2, PR excluded
		t.Fatalf("ListIssues returned %d issues, want 100 (pull requests excluded)", len(issues))
	}
	if issues[0].Number != 1 || len(issues[0].Labels) != 0 {
		t.Errorf("first issue = %+v", issues[0])
	}
	if issues[99].Number != 100 || issues[99].Labels[0] != "bug" {
		t.Errorf("last issue = %+v, want #100 with label bug", issues[99])
	}
	for _, is := range issues {
		if is.Number == 200 {
			t.Errorf("pull request 200 leaked into the issue list")
		}
	}
}

func TestListPRsHeadFilter(t *testing.T) {
	var gotHead string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		gotHead = r.URL.Query().Get("head")
		if r.URL.Query().Get("state") != "all" {
			t.Errorf("state = %q, want all (closed PRs must count as processed)", r.URL.Query().Get("state"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":50,"title":"t","state":"open","html_url":"https://github.com/owner/repo/pull/50"}]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-token")

	prs, err := c.ListPRs(context.Background(), "owner", "repo", "owner:shipyard/issue-7")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 50 {
		t.Errorf("PRs = %+v", prs)
	}
	if gotHead != "owner:shipyard/issue-7" {
		t.Errorf("server saw head=%q, want owner:shipyard/issue-7", gotHead)
	}
}

func TestListPRsNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-token")

	prs, err := c.ListPRs(context.Background(), "owner", "repo", "")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("PRs = %+v, want none", prs)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	if _, err := c.GetIssue(context.Background(), "owner", "repo", 404); err == nil {
		t.Fatal("GetIssue: expected error for 404, got nil")
	}
}

func TestCreatePR(t *testing.T) {
	var gotAuth, gotHead, gotBase, gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/owner/repo/pulls" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var raw map[string]string
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decoding pull request payload: %v", err)
		}
		// Assert on the raw lowercase wire keys so the test catches
		// missing json tags on PRRequest.
		gotTitle, gotHead, gotBase, gotBody = raw["title"], raw["head"], raw["base"], raw["body"]
		if raw["head"] == "" || raw["base"] == "" {
			t.Errorf("pull request payload missing head/base: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number": 42, "title": "Fix #7: Broken login", "state": "open", "html_url": "https://github.com/owner/repo/pull/42"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if gotAuth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", gotAuth)
		}
	})
	c := NewClient(srv.URL, "test-token")

	pr, err := c.CreatePR(context.Background(), "owner", "repo", PRRequest{
		Title: "Fix #7: Broken login", Head: "shipyard/issue-7", Base: "main",
		Body: "Solves [Broken login](https://github.com/owner/repo/issues/7) (#7). Fix #7.",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 42 || !strings.HasSuffix(pr.HTMLURL, "/pull/42") {
		t.Errorf("PR = %+v", pr)
	}
	if gotTitle != "Fix #7: Broken login" || gotHead != "shipyard/issue-7" || gotBase != "main" {
		t.Errorf("server saw title=%q head=%q base=%q", gotTitle, gotHead, gotBase)
	}
	if !strings.Contains(gotBody, "issues/7") {
		t.Errorf("PR body should link the source issue, got %q", gotBody)
	}
}

func TestCreatePRMissingPermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "Resource not accessible by integration"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	_, err := c.CreatePR(context.Background(), "owner", "repo", PRRequest{Title: "x", Head: "h", Base: "b"})
	if err == nil {
		t.Fatal("CreatePR: expected error for 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing the permissions") {
		t.Errorf("expected permission hint in error, got %v", err)
	}
}

func TestCreatePRBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "Bad credentials"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	_, err := c.CreatePR(context.Background(), "owner", "repo", PRRequest{Title: "x", Head: "h", Base: "b"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "token is valid and not expired") {
		t.Errorf("expected 401 with token hint, got %v", err)
	}
}
