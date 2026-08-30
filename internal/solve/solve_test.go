package solve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactCredentials(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			"clone https://x-access-token:supersecret@github.com/o/r.git",
			"clone https://***@github.com/o/r.git",
		},
		{
			"fatal: unable to access 'https://user:hunter2@ghe.example/x.git': The requested URL returned error",
			"fatal: unable to access 'https://***@ghe.example/x.git': The requested URL returned error",
		},
		{"plain https://github.com/o/r.git is fine", "plain https://github.com/o/r.git is fine"},
		{"no urls here", "no urls here"},
	}
	for _, tc := range tests {
		if got := RedactCredentials(tc.in); got != tc.want {
			t.Errorf("RedactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExecGitRunRedactsFailureOutput guards the blocker class: a failed
// command (here a clone with an in-URL credential) must not echo the
// token through the error it returns.
func TestExecGitRunRedactsFailureOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()

	_, err := ExecGit.Run(ctx, dir, "clone", "--quiet",
		"https://x-access-token:secretpass123@github.invalid/nope/repo.git",
		filepath.Join(dir, "clone"))
	if err == nil {
		t.Fatal("expected a clone failure, got nil")
	}
	if strings.Contains(err.Error(), "secretpass123") {
		t.Errorf("credentials leaked into the error: %v", err)
	}
}
