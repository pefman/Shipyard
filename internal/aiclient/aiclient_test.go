package aiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func startAIServer(t *testing.T, respStatus int, respBody []byte) (*Client, *httptest.Server) {
	t.Helper()
	var gotAuth, gotModel, gotPrompt string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding request payload: %v", err)
		}
		gotModel = payload.Model
		if len(payload.Messages) == 1 {
			gotPrompt = payload.Messages[0].Content
		}
		w.WriteHeader(respStatus)
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/v1", "ai-key", "test-model")
	t.Cleanup(func() {
		if gotAuth != "" && gotAuth != "Bearer ai-key" {
			t.Errorf("expected Bearer ai-key auth header, got %q", gotAuth)
		}
		if gotPrompt == "" {
			t.Error("AI endpoint received an empty prompt")
		}
		if gotModel == "" {
			t.Error("AI endpoint received an empty model field")
		}
	})
	return c, srv
}

func TestComplete(t *testing.T) {
	body := []byte(`{"choices": [{"message": {"role": "assistant", "content": "Fix the bug."}}]}`)
	c, _ := startAIServer(t, http.StatusOK, body)

	out, err := c.Complete(context.Background(), "Solve issue 7: broken login")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != "Fix the bug." {
		t.Errorf("Complete = %q, want %q", out.Content, "Fix the bug.")
	}
	if out.FinishReason != "" {
		t.Errorf("FinishReason = %q, want empty (endpoint reported none)", out.FinishReason)
	}
}

// TestCompleteDiagnostics covers the fields the verbose log surfaces:
// HTTP status, latency, finish_reason, and the reasoning/thinking block
// when the endpoint returns one (ninfer, DeepSeek-style responses).
func TestCompleteDiagnostics(t *testing.T) {
	body := []byte(`{"choices": [{"finish_reason": "length", "message": {"role": "assistant", "content": "partial answer", "reasoning_content": "let me think through this"}}]}`)
	c, _ := startAIServer(t, http.StatusOK, body)

	out, err := c.Complete(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != "partial answer" {
		t.Errorf("Content = %q, want %q", out.Content, "partial answer")
	}
	if out.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want %q", out.FinishReason, "length")
	}
	if out.Reasoning != "let me think through this" {
		t.Errorf("Reasoning = %q, want the reasoning_content block", out.Reasoning)
	}
	if out.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", out.Model)
	}
	if out.Prompt != "hello" {
		t.Errorf("Prompt = %q, want the exact prompt sent", out.Prompt)
	}
	if out.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want %d", out.HTTPStatus, http.StatusOK)
	}
	if out.Latency < 0 {
		t.Errorf("Latency = %s, want a non-negative duration", out.Latency)
	}
}

// TestVerboseRequestLines: the verbose request log carries the model,
// the prompt size, and the full prompt.
func TestVerboseRequestLines(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/v1", "", "test-model")
	lines := c.VerboseRequestLines("line one\nline two")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}
	want := "AI request: model test-model; prompt 17 bytes, 2 lines"
	if lines[0] != want {
		t.Errorf("diagnostic line = %q, want %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], "AI request (prompt") {
		t.Errorf("label line = %q, want the prompt label", lines[1])
	}
	if lines[2] != "line one\nline two" {
		t.Errorf("prompt line = %q, want the full prompt", lines[2])
	}
}

// TestVerboseRequestLinesAnnouncesTruncation: an extremely long prompt
// is rendered as size plus first/last lines — the omission is stated
// explicitly, never silent.
func TestVerboseRequestLinesAnnouncesTruncation(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/v1", "", "test-model")
	var b strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&b, "filler line %04d with some padding so it weighs in\n", i)
		if i == 3000 {
			b.WriteString("MIDDLE-MARKER-LINE\n")
		}
	}
	tail := "final line of the prompt\n"
	prompt := b.String() + tail
	if len(prompt) <= verboseFullLimit {
		t.Fatalf("test prompt is %d bytes, must exceed the %d-byte full limit", len(prompt), verboseFullLimit)
	}

	lines := c.VerboseRequestLines(prompt)
	rendered := strings.Join(lines, "\n")
	if strings.Contains(rendered, "MIDDLE-MARKER-LINE") {
		t.Error("oversized prompt must not be dumped in full into the log")
	}
	if !strings.Contains(rendered, "filler line 0000 with some padding so it weighs in") {
		t.Errorf("first lines must be shown:\n%s", rendered[:min(len(rendered), 200)])
	}
	if !strings.Contains(rendered, "final line of the prompt") {
		t.Error("last lines must be shown")
	}
	if !strings.Contains(rendered, "omitted") {
		t.Error("the middle omission must be announced explicitly")
	}
}

// TestVerboseCompletionLines: the response side carries the diagnostics
// (status, latency, finish_reason), the full content, and the thinking
// block when present; a non-length finish makes no truncation claim.
func TestVerboseCompletionLines(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/v1", "", "test-model")
	comp := Completion{
		Model: "test-model", Content: "the response body",
		Reasoning: "internal deliberations", HTTPStatus: 200,
		Latency: 1234 * time.Millisecond, FinishReason: "stop",
	}
	joined := strings.Join(c.VerboseCompletionLines(comp), "\n")
	for _, want := range []string{"HTTP 200", "1.234s", "finish_reason: stop", "the response body", "AI thinking", "internal deliberations"} {
		if !strings.Contains(joined, want) {
			t.Errorf("verbose response log missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "truncated by token limit") {
		t.Errorf("a stop finish must not claim truncation:\n%s", joined)
	}
}

// TestVerboseCompletionLinesLengthFinish: a length finish_reason must be
// surfaced explicitly — the most common local-model failure mode.
func TestVerboseCompletionLinesLengthFinish(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/v1", "", "test-model")
	comp := Completion{Content: "as much as fit", HTTPStatus: 200, Latency: time.Second, FinishReason: "length"}
	joined := strings.Join(c.VerboseCompletionLines(comp), "\n")
	if !strings.Contains(joined, "finish_reason: length") {
		t.Errorf("finish_reason must be surfaced:\n%s", joined)
	}
	if !strings.Contains(joined, "response truncated by token limit") {
		t.Errorf("a length finish must be announced explicitly:\n%s", joined)
	}
}

func TestCompleteTrailingSlashBaseURL(t *testing.T) {
	body := []byte(`{"choices": [{"message": {"content": "ok"}}]}`)
	c, _ := startAIServer(t, http.StatusOK, body)
	c.BaseURL += "/"

	out, err := c.Complete(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != "ok" {
		t.Errorf("Complete = %q, want %q", out.Content, "ok")
	}
}

func TestCompleteEndpointError(t *testing.T) {
	c, _ := startAIServer(t, http.StatusUnauthorized, []byte(`{"error": "bad key"}`))

	_, err := c.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("Complete: expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected status in error, got %v", err)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	c, _ := startAIServer(t, http.StatusOK, []byte(`{"choices": []}`))

	if _, err := c.Complete(context.Background(), "hello"); err == nil {
		t.Fatal("Complete: expected error for empty choices, got nil")
	}
}
