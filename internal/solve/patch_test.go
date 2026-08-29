package solve

import (
	"errors"
	"strings"
	"testing"
)

const (
	cannedPatch = "diff --git a/hello.py b/hello.py\n" +
		"index 1234567..89abcde 100644\n" +
		"--- a/hello.py\n" +
		"+++ b/hello.py\n" +
		"@@ -1,2 +1,4 @@\n" +
		" def greet(name):\n" +
		"+    if not name:\n" +
		"+        return \"Hello, stranger\"\n" +
		"     return \"Hello, \" + name\n"
	cannedResponse = "I fixed the greeting helper: it now returns a fallback\n" +
		"greeting for empty input instead of panicking.\n\n" +
		"```diff\n" + cannedPatch + "```\n\n" +
		"You can extend this to other helpers later.\n"
)

func TestExtractPatchFenced(t *testing.T) {
	patch, explanation, err := ExtractPatch(cannedResponse)
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	for _, want := range []string{"diff --git a/hello.py b/hello.py", `+    if not name:`, `     return "Hello, " + name`} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch missing %q:\n%s", want, patch)
		}
	}
	if !strings.Contains(patch, "\n") || strings.HasSuffix(patch, "\n\n") {
		t.Errorf("patch should end with a single newline:\n%q", patch)
	}
	for _, want := range []string{"fallback", "greeting helper"} {
		if !strings.Contains(explanation, want) {
			t.Errorf("explanation missing %q:\n%s", want, explanation)
		}
	}
	if strings.Contains(explanation, "diff --git") {
		t.Errorf("explanation should not contain patch lines:\n%s", explanation)
	}
}

func TestExtractPatchUnfenced(t *testing.T) {
	resp := "Here is the fix:\n\ndiff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"
	patch, _, err := ExtractPatch(resp)
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	if !strings.HasPrefix(patch, "diff --git a/x.go b/x.go") {
		t.Errorf("patch = %q", patch)
	}
}

func TestExtractPatchUnfencedTrailingProse(t *testing.T) {
	// Models often put the explanation after the diff; on the unfenced
	// path that prose must not be handed to git apply.
	resp := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n\nThat was the change: the old line was wrong.\nYou can ship this now.\n"
	patch, explanation, err := ExtractPatch(resp)
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	if strings.Contains(patch, "That was the change") {
		t.Errorf("trailing prose leaked into the patch:\n%s", patch)
	}
	if !strings.Contains(patch, "+new\n") || strings.HasSuffix(patch, "+new\n\n") {
		t.Errorf("patch should end at the last hunk line:\n%q", patch)
	}
	if !strings.Contains(explanation, "That was the change") {
		t.Errorf("trailing prose should end up in the explanation:\n%s", explanation)
	}
}

func TestExtractPatchBareHunks(t *testing.T) {
	resp := "Fix below:\n```git\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-a\n+b\n```\n"
	patch, _, err := ExtractPatch(resp)
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	if !strings.Contains(patch, "--- a/f.txt") {
		t.Errorf("patch = %q", patch)
	}
}

func TestExtractPatchSkipsNonDiffBlocks(t *testing.T) {
	resp := "Thoughts:\n```python\nprint('not a patch')\n```\n\nThe actual patch:\n```diff\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-a\n+b\n```\n"
	patch, _, err := ExtractPatch(resp)
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	if strings.Contains(patch, "print(") {
		t.Errorf("picked the wrong block:\n%s", patch)
	}
	if !strings.Contains(patch, "+++ b/f.txt") {
		t.Errorf("patch = %q", patch)
	}
}

func TestExtractPatchNoUsableChanges(t *testing.T) {
	for _, resp := range []string{
		"I could not find any changes to make.",
		"Some prose with a markdown rule:\n\n---\n\nmore prose", // horizontal rule is not a diff
		"Here is code, not a patch:\n```go\nfunc main() {}\n```\n",
	} {
		if _, _, err := ExtractPatch(resp); !errors.Is(err, ErrNoUsableChanges) {
			t.Errorf("ExtractPatch(%q): want ErrNoUsableChanges, got %v", resp, err)
		}
	}
}

func TestCloneURLWithToken(t *testing.T) {
	tests := []struct {
		name, url, token, want string
	}{
		{"https gets token", "https://github.com/o/r.git", "tok", "https://x-access-token:tok@github.com/o/r.git"},
		{"empty token passthrough", "https://github.com/o/r.git", "", "https://github.com/o/r.git"},
		{"ssh passthrough", "git@github.com:o/r.git", "tok", "git@github.com:o/r.git"},
		{"file passthrough", "/srv/repos/o/r.git", "tok", "/srv/repos/o/r.git"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CloneURLWithToken(tc.url, tc.token); got != tc.want {
				t.Errorf("CloneURLWithToken = %q, want %q", got, tc.want)
			}
		})
	}
}
