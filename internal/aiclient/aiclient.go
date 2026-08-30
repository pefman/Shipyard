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
	"strings"
	"time"
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

// NewClient returns a Client for the given endpoint base URL, API key, and
// model name. An empty API key sends no Authorization header (keyless
// custom endpoints); an empty model is sent as-is.
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		HTTPClient: http.DefaultClient,
	}
}

// Completion is everything Shipyard learned from one AI call: the
// response text plus the call's diagnostics (model, prompt, HTTP status,
// latency, finish reason, and the reasoning/thinking block when the
// endpoint returns one) that a verbose log can surface.
type Completion struct {
	// Model is the model name sent to the endpoint.
	Model string
	// Prompt is the exact prompt that was sent.
	Prompt string
	// Content is the response text (message.content).
	Content string
	// Reasoning is the endpoint's reasoning_content / thinking field,
	// when it returns one (ninfer, DeepSeek-style models).
	Reasoning string
	// FinishReason is the response's finish_reason (e.g. "stop",
	// "length"). "length" means the response was cut off at the
	// endpoint's token limit; empty means the endpoint reported none.
	FinishReason string
	// HTTPStatus is the endpoint's HTTP status code.
	HTTPStatus int
	// Latency is the round-trip time of the call.
	Latency time.Duration
}

// Complete sends the prompt to the endpoint and returns the completion:
// the response text plus the call's diagnostics.
func (c *Client) Complete(ctx context.Context, prompt string) (Completion, error) {
	start := time.Now()
	comp := Completion{Model: c.Model, Prompt: prompt}
	payload, err := json.Marshal(map[string]any{
		"model":    c.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return comp, err
	}

	u := c.BaseURL
	if !hasTrailingSlash(u) {
		u += "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"chat/completions", bytes.NewReader(payload))
	if err != nil {
		return comp, err
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
		return comp, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	comp.Latency = time.Since(start)
	comp.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return comp, fmt.Errorf("ai: %s: %s: %s", "/chat/completions", resp.Status, string(body))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return comp, fmt.Errorf("ai: decoding response: %w", err)
	}
	if len(out.Choices) == 0 {
		return comp, fmt.Errorf("ai: response contains no choices")
	}
	comp.Content = out.Choices[0].Message.Content
	comp.FinishReason = out.Choices[0].FinishReason
	comp.Reasoning = out.Choices[0].Message.ReasoningContent
	if comp.Reasoning == "" {
		comp.Reasoning = out.Choices[0].Message.Reasoning
	}
	return comp, nil
}

func hasTrailingSlash(u string) bool {
	return len(u) > 0 && u[len(u)-1] == '/'
}

const (
	// verboseFullLimit is the size up to which the verbose log shows a
	// prompt or response in full. Beyond it, the content is rendered as
	// a size annotation plus its first and last lines, so a multi-hundred-
	// kilobyte prompt does not swamp the log.
	verboseFullLimit = 256 << 10

	// verboseEdgeLines is how many lines each end of an oversized
	// prompt/response keeps in the verbose log.
	verboseEdgeLines = 40
)

// Verbatim renders text for the verbose log: in full at or below
// verboseFullLimit, otherwise as a size annotation plus the first and
// last lines. When the content is cut, the cut is announced — it is
// never silent.
func Verbatim(s string) string {
	if len(s) <= verboseFullLimit {
		return s
	}
	all := strings.Split(s, "\n")
	edge := verboseEdgeLines
	if edge*2 >= len(all) {
		edge = len(all) / 3
	}
	head := strings.Join(all[:edge], "\n")
	tail := strings.Join(all[len(all)-edge:], "\n")
	return fmt.Sprintf(
		"[%d bytes, %d lines; the middle is omitted from this log line — the omission is announced, not silent]\n\n[first %d lines]\n%s\n\n[... %d middle lines omitted ...]\n\n[last %d lines]\n%s",
		len(s), len(all), edge, head, len(all)-2*edge, edge, tail,
	)
}

// VerboseRequestLines renders the request side of one AI call as log
// lines: the model and the prompt size, then the full prompt (or its
// size plus first and last lines when extremely long).
func (c *Client) VerboseRequestLines(prompt string) []string {
	return []string{
		fmt.Sprintf("AI request: model %s; prompt %d bytes, %d lines", c.Model, len(prompt), strings.Count(prompt, "\n")+1),
		"AI request (prompt sent to the endpoint):",
		Verbatim(prompt),
	}
}

// VerboseCompletionLines renders the response side of one AI call as
// log lines: the diagnostics (HTTP status, latency, finish_reason — a
// "length" finish is announced as a response truncated by the token
// limit, the most common local-model failure mode), the full response
// content, and the thinking/reasoning block when the endpoint returns
// one.
func (c *Client) VerboseCompletionLines(comp Completion) []string {
	finish := comp.FinishReason
	if finish == "" {
		finish = "(not reported by the endpoint)"
	}
	diag := fmt.Sprintf("AI response: HTTP %d in %s; finish_reason: %s", comp.HTTPStatus, comp.Latency.Round(time.Millisecond), finish)
	if comp.FinishReason == "length" {
		diag += " — response truncated by token limit: the model stopped at its maximum output length, so an unfinished answer (e.g. prose instead of a diff, or a diff cut mid-block) is to be expected here"
	}
	lines := []string{diag, "AI response (content):", Verbatim(comp.Content)}
	if comp.Reasoning != "" {
		lines = append(lines, "AI thinking (reasoning_content, as reported by the endpoint):", Verbatim(comp.Reasoning))
	}
	return lines
}
