package githubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	repo := `{"full_name": "owner/repo", "private": false, "html_url": "https://github.com/owner/repo"}`
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
