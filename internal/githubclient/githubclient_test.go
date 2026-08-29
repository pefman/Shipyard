package githubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		var req struct {
			Title, Head, Base, Body string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding pull request payload: %v", err)
		}
		gotTitle, gotHead, gotBase, gotBody = req.Title, req.Head, req.Base, req.Body
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
