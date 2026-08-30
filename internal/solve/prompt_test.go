package solve

import (
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/githubclient"
)

func TestBuildTask(t *testing.T) {
	repo := &githubclient.Repo{FullName: "owner/repo"}
	issue := &githubclient.Issue{
		Number: 7,
		Title:  "Broken login",
		Body:   "Login fails on submit.",
		Labels: []string{"bug", "auth"},
	}
	p := BuildTask(repo, issue, "main", "shipyard/issue-7", "a disposable container running the golang:1.22 image")
	for _, want := range []string{
		"owner/repo",
		"shipyard/issue-7",
		"issue #7",
		"Broken login",
		"bug, auth",
		"Login fails on submit.",
		"Do not commit",
		"working tree",
		"go build ./...",
		"a disposable container running the golang:1.22 image",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("task missing %q:\n%s", want, p)
		}
	}
}

func TestBuildTaskEmptyOptionalParts(t *testing.T) {
	repo := &githubclient.Repo{FullName: "owner/repo"}
	issue := &githubclient.Issue{Number: 1, Title: "t"}
	p := BuildTask(repo, issue, "main", "b", "the host machine it runs on")
	if strings.Contains(p, "Labels:") || strings.Contains(p, "Issue body:") {
		t.Errorf("empty optional parts should be omitted:\n%s", p)
	}
	if !strings.Contains(p, "the host machine it runs on") {
		t.Errorf("environment line missing:\n%s", p)
	}
}
