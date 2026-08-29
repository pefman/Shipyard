package solve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ErrNoUsableChanges is returned when the AI response does not contain a
// unified diff that could be applied.
var ErrNoUsableChanges = errors.New("the AI returned no usable changes: its response contains no unified diff")

// fencedBlockRe matches ```-fenced code blocks, capturing the content
// (the opening line, including an optional language tag, is not part of
// the capture).
var fencedBlockRe = regexp.MustCompile("(?m)(?s)^```[^\n]*\n(.*?)^```[ \t]*$")

// ExtractPatch pulls the unified diff out of the AI response and returns
// it together with the prose explanation (the response minus the patch).
//
// It looks, in order, for:
//  1. a fenced code block whose content contains "diff --git" lines,
//  2. another fenced block that at least looks like a diff
//     ("--- " / "+++ " line pairs),
//  3. an unfenced region starting at the first "diff --git" line.
func ExtractPatch(response string) (patch, explanation string, err error) {
	blocks := fencedBlockRe.FindAllStringSubmatch(response, -1)

	for _, b := range blocks {
		if strings.Contains(b[1], "diff --git ") {
			patch = cleanPatch(b[1])
			return patch, explain(response, b[1]), nil
		}
	}
	for _, b := range blocks {
		if looksLikeDiff(b[1]) {
			patch = cleanPatch(b[1])
			return patch, explain(response, b[1]), nil
		}
	}
	if i := rawDiffStart(response); i >= 0 {
		patch = cleanPatch(response[i:])
		return patch, explain(response, patch), nil
	}
	return "", "", fmt.Errorf("%w (save the raw response, check the endpoint/model configuration, or re-run)", ErrNoUsableChanges)
}

// looksLikeDiff reports whether s has the shape of a unified diff even
// without "diff --git" headers (models sometimes emit bare --- / +++
// hunks).
func looksLikeDiff(s string) bool {
	hasMinus, hasPlus := false, false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "--- ") {
			hasMinus = true
		}
		if strings.HasPrefix(line, "+++ ") {
			hasPlus = true
		}
	}
	return hasMinus && hasPlus
}

// rawDiffStart returns the byte offset of the first line starting with
// "diff --git " or "--- " (unfenced responses), or -1.
func rawDiffStart(s string) int {
	lines := strings.Split(s, "\n")
	off := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

// cleanPatch trims trailing blank lines and stray fence markers so the
// result is a clean file for git apply.
func cleanPatch(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && (strings.TrimSpace(lines[len(lines)-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```")) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// explain returns the response with the patch region removed: the prose
// the model wrote alongside the diff.
func explain(response, patch string) string {
	rest := response
	if i := strings.Index(rest, patch); i >= 0 {
		rest = rest[:i] + rest[i+len(patch):]
	}
	// Drop fence markers left behind.
	var b strings.Builder
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

// ApplyPatch applies patch to the git working tree in dir. On failure the
// error carries git's output and patchPath, so the user can inspect what
// the AI produced.
func ApplyPatch(ctx context.Context, git GitRunner, dir, patch, patchPath string) error {
	f, err := os.CreateTemp("", "shipyard-patch-*.patch")
	if err != nil {
		return fmt.Errorf("writing patch to a temp file: %w", err)
	}
	patchFile := f.Name()
	defer os.Remove(patchFile)
	if _, err := f.WriteString(patch); err != nil {
		f.Close()
		return fmt.Errorf("writing patch to a temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing patch to a temp file: %w", err)
	}

	if _, err := git.Run(ctx, dir, "apply", "--whitespace=nowarn", patchFile); err != nil {
		return fmt.Errorf("the generated patch does not apply to %s: %v (full patch saved to %s, AI response kept next to it — fix the AI output or retry)", dir, err, patchPath)
	}
	return nil
}

// CloneURLWithToken embeds the GitHub token in an https clone URL so the
// git CLI can authenticate. Other URL schemes (ssh, file://) are passed
// through unchanged; git's own credentials handling applies there.
func CloneURLWithToken(cloneURL, token string) string {
	if token == "" {
		return cloneURL
	}
	if strings.HasPrefix(cloneURL, "https://") {
		return "https://x-access-token:" + token + "@" + strings.TrimPrefix(cloneURL, "https://")
	}
	return cloneURL
}
