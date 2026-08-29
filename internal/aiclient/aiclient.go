// Package aiclient sends prompts to a configurable AI endpoint and returns
// the response text. The protocol is an OpenAI-compatible
// /chat/completions API; the endpoint base URL and API key are both
// configurable so any compatible server can be used.
package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	// BaseURL is the endpoint base (e.g. https://api.openai.com/v1).
	BaseURL string
	// APIKey is sent as a Bearer credential.
	APIKey string
	// Model is the model name passed to the endpoint.
	Model string
	// HTTPClient is the underlying client; nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// NewClient returns a Client for the given endpoint base URL and API key.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      "gpt-4o-mini",
		HTTPClient: http.DefaultClient,
	}
}

// Complete sends the prompt to the endpoint and returns the response text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model":    c.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}

	u := c.BaseURL
	if !hasTrailingSlash(u) {
		u += "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai: %s: %s: %s", "/chat/completions", resp.Status, string(body))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("ai: decoding response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("ai: response contains no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func hasTrailingSlash(u string) bool {
	return len(u) > 0 && u[len(u)-1] == '/'
}
