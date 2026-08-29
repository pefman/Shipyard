package aiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if out != "Fix the bug." {
		t.Errorf("Complete = %q, want %q", out, "Fix the bug.")
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
	if out != "ok" {
		t.Errorf("Complete = %q, want %q", out, "ok")
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
