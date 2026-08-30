package solve

import (
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/githubclient"
)

func TestBuildPrompt(t *testing.T) {
	repo := &githubclient.Repo{FullName: "owner/repo"}
	issue := &githubclient.Issue{
		Number: 7,
		Title:  "Broken login",
		Body:   "Login fails on submit.",
		Labels: []string{"bug", "auth"},
	}
	p := BuildPrompt(repo, issue, "main", "src/login.go\nsrc/main.go", map[string]string{
		"src/login.go": "func Login() {}",
	}, "")

	for _, want := range []string{
		"owner/repo",
		"main",
		"Issue #7: Broken login",
		"bug, auth",
		"Login fails on submit.",
		"src/login.go",
		"func Login() {}",
		"unified diff",
		"diff",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestBuildPromptEmptyOptionalParts(t *testing.T) {
	repo := &githubclient.Repo{FullName: "owner/repo"}
	issue := &githubclient.Issue{Number: 1, Title: "t"}
	p := BuildPrompt(repo, issue, "", "", nil, "")
	if strings.Contains(p, "Labels:") || strings.Contains(p, "Issue body:") || strings.Contains(p, "Base branch:") {
		t.Errorf("empty optional parts should be omitted:\n%s", p)
	}
	if strings.Contains(p, "disposable container") {
		t.Errorf("no sandbox line without a sandbox image:\n%s", p)
	}
}

// TestBuildPromptSandboxLine: a live run tells the AI which image its
// fix will be built and tested in, so generated code targets that
// toolchain; without one the line must not appear.
func TestBuildPromptSandboxLine(t *testing.T) {
	repo := &githubclient.Repo{FullName: "owner/repo"}
	issue := &githubclient.Issue{Number: 1, Title: "t"}
	p := BuildPrompt(repo, issue, "main", "", nil, "golang:1.22")
	if !strings.Contains(p, "disposable container running the golang:1.22 image") {
		t.Errorf("prompt missing the sandbox environment line:\n%s", p)
	}
}

func TestCapLines(t *testing.T) {
	lines := ""
	for i := 0; i < 600; i++ {
		lines += "f" + string(rune('a'+i%26)) + "\n"
	}
	got := capLines(lines, 500)
	if n := len(strings.Split(got, "\n")); n != 501 { // 500 + "more files"
		t.Errorf("capLines returned %d lines, want 501", n)
	}
	if !strings.Contains(got, "100 more files") {
		t.Errorf("missing more-files note:\n%q", got)
	}
	if got := capLines("a\nb", 500); got != "a\nb" {
		t.Errorf("short input mangled: %q", got)
	}
}
