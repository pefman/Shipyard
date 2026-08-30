package solve

// Verbose-log tests: with Options.Verbose the flow's log carries the
// full AI conversation — the prompt sent, the response, the
// thinking/reasoning block, and the call's diagnostics (HTTP status,
// latency, finish_reason) — and credentials embedded in URLs stay
// redacted. Without it the log is exactly what it is today.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pefman/Shipyard/internal/aiclient"
)

// newFakeAIDetailed serves one canned completion: the given content,
// finish_reason, and reasoning_content (each optional).
func newFakeAIDetailed(t *testing.T, content, finishReason, reasoning string) *aiclient.Client {
	t.Helper()
	body := func() []byte {
		msg := map[string]any{"role": "assistant", "content": content}
		if reasoning != "" {
			msg["reasoning_content"] = reasoning
		}
		choice := map[string]any{"message": msg}
		if finishReason != "" {
			choice["finish_reason"] = finishReason
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{choice}})
		return b
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return aiclient.NewClient(srv.URL+"/v1", "ai-key", "mock-model")
}

// capturingDeps returns a Deps whose log goes to buf.
func capturingDeps(gh *fakeGitHub, ai *aiclient.Client, t *testing.T, buf *bytes.Buffer) Deps {
	t.Helper()
	d := newDeps(gh, ai, t)
	d.Log = func(format string, args ...any) { fmt.Fprintf(buf, format+"\n", args...) }
	return d
}

// TestSolveVerboseLogsFullConversation: verbose on, the log shows
// everything sent and everything received, per AI call.
func TestSolveVerboseLogsFullConversation(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	response := "Guarded the empty input in greet().\n```diff\n" + seedPatch + "```"
	ai := newFakeAIDetailed(t, response, "stop", "I should check the empty-name case first")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, Verbose: true,
	}); err != nil {
		t.Fatalf("Solve: %v", err)
	}

	logged := buf.String()
	for _, want := range []string{
		"AI request: model mock-model; prompt", // request diagnostics
		"AI request (prompt sent to the endpoint):",
		"Issue #9: greet() crashes on empty input", // the actual prompt content
		"AI response: HTTP 200",                    // response diagnostics
		"finish_reason: stop",
		"AI response (content):",
		"Guarded the empty input in greet().", // the actual response content
		"AI thinking (reasoning_content",      // the thinking block, labelled
		"I should check the empty-name case first",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("verbose log missing %q:\n%s", want, logged)
		}
	}
}

// TestSolveVerboseSurfacesLengthFinish: a length finish_reason (the
// model stopped at its token limit — the classic weak/local-model
// failure) must be announced explicitly in the log, even though the run
// itself fails (no usable diff).
func TestSolveVerboseSurfacesLengthFinish(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	ai := newFakeAIDetailed(t, "I would like to explain the fix in detail, but I ran out of room…", "length", "")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, Verbose: true,
	}); err == nil {
		t.Fatal("expected the run to fail (no usable changes), got nil")
	}

	logged := buf.String()
	if !strings.Contains(logged, "finish_reason: length") {
		t.Errorf("finish_reason must be surfaced in the log:\n%s", logged)
	}
	if !strings.Contains(logged, "response truncated by token limit") {
		t.Errorf("a length finish must be announced explicitly:\n%s", logged)
	}
}

// TestSolveVerboseRedactsCredentials: the existing secret redaction
// applies to verbose output — a credential embedded in a URL (in the
// response or the prompt) reaches the log only redacted.
func TestSolveVerboseRedactsCredentials(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "https://x-access-token:supersecret@remote.invalid/towner/trepo.git")
	ai := newFakeAIDetailed(t,
		"Applied the fix.\n```diff\n"+seedPatch+"```\n(reproduced from https://user:leakedpass@github.example/mirror.git)",
		"stop", "")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, Verbose: true,
	}); err != nil {
		t.Fatalf("Solve: %v", err)
	}

	logged := buf.String()
	for _, secret := range []string{"supersecret", "leakedpass"} {
		if strings.Contains(logged, secret) {
			t.Errorf("credential %q leaked into the verbose log:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, "https://***@github.example/mirror.git") {
		t.Errorf("expected the redacted URL in the verbose response log:\n%s", logged)
	}
}

// TestSolveVerboseLogsDiagnosticsOnFailedCall: a failed AI call (HTTP 500
// from the endpoint) must still land its diagnostics in the verbose log —
// failed local-model calls are the whole point of the feature — even
// though Solve itself returns an error.
func TestSolveVerboseLogsDiagnosticsOnFailedCall(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "server boom"}`))
	}))
	t.Cleanup(srv.Close)
	ai := aiclient.NewClient(srv.URL+"/v1", "ai-key", "mock-model")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, Verbose: true,
	}); err == nil {
		t.Fatal("expected Solve to fail when the AI endpoint returns 500")
	}

	logged := buf.String()
	if !strings.Contains(logged, "AI response: HTTP 500") {
		t.Errorf("the failed call's diagnostics must be in the verbose log:\n%s", logged)
	}
	if strings.Contains(logged, "HTTP 0 ") {
		t.Errorf("no 'HTTP 0' line expected when the endpoint answered:\n%s", logged)
	}
}

// TestSolveVerboseAnnouncesTransportFailure: no response at all (the
// endpoint is unreachable) must not print "HTTP 0 in 0s".
func TestSolveVerboseAnnouncesTransportFailure(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	// A server that closes immediately after starting: the client
	// gets a transport error, no status line.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := closed.URL
	closed.Close()
	ai := aiclient.NewClient(url+"/v1", "ai-key", "mock-model")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, Verbose: true,
	}); err == nil {
		t.Fatal("expected Solve to fail when the AI endpoint is unreachable")
	}

	logged := buf.String()
	if strings.Contains(logged, "HTTP 0 ") {
		t.Errorf("a transport failure must not read 'HTTP 0 in 0s':\n%s", logged)
	}
	if !strings.Contains(logged, "no HTTP response received") {
		t.Errorf("the transport failure must be announced:\n%s", logged)
	}
}

// TestSolveNotVerboseIsUnchanged: off by default — no conversation in
// the log, and the log lines are the same ones as before verbose
// existed.
func TestSolveNotVerboseIsUnchanged(t *testing.T) {
	gitAvailable(t)
	_, workdir := newFakeRemote(t)
	gh := newFakeGitHub(t, "")
	ai := newFakeAIDetailed(t, "Fixed.\n```diff\n"+seedPatch+"```", "stop", "deliberations")

	var buf bytes.Buffer
	d := capturingDeps(gh, ai, t, &buf)
	if _, err := Solve(context.Background(), d, Options{
		Owner: "towner", Repo: "trepo", IssueNumber: 9,
		Workdir: workdir, // Verbose intentionally unset (off)
	}); err != nil {
		t.Fatalf("Solve: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "calling AI endpoint ...") {
		t.Errorf("the pre-existing call line must remain:\n%s", logged)
	}
	for _, absent := range []string{"AI request", "AI response (content)", "AI thinking", "finish_reason", "deliberations"} {
		if strings.Contains(logged, absent) {
			t.Errorf("verbose content %q must not appear when verbose is off:\n%s", absent, logged)
		}
	}
}