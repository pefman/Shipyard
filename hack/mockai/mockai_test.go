package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerStreamsResponse: the mock speaks the SSE shape pi's
// openai-completions client consumes: content deltas that reassemble
// into the canned response, a tool-call finish, and the [DONE] marker.
func TestHandlerStreamsResponse(t *testing.T) {
	const canned = "greet() fixed\nit now returns a fallback"
	srv := httptest.NewServer(newHandler(canned))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body, _ := io.ReadAll(resp.Body)

	// Decode the SSE payloads properly and reassemble the deltas.
	var (
		reassembled string
		stopped     bool
		done        bool
	)
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		var payload struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("payload is not JSON: %v: %s", err, data)
		}
		for _, c := range payload.Choices {
			reassembled += c.Delta.Content
			if c.FinishReason != nil && *c.FinishReason == "stop" {
				stopped = true
			}
		}
	}
	if reassembled != canned {
		t.Errorf("reassembled stream = %q, want the canned response", reassembled)
	}
	if !stopped {
		t.Error("the stream has no stop finish event")
	}
	if !done {
		t.Error("the stream has no [DONE] marker")
	}
}

// TestHandlerHealthCheck: GET / answers, so operators can tell the
// mock is up.
func TestHandlerHealthCheck(t *testing.T) {
	srv := httptest.NewServer(newHandler("x"))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
