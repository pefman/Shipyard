// Package githubclient is a minimal GitHub REST API client: issue details
// and repository info, enough for the solving flow.
package githubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client talks to the GitHub REST API.
type Client struct {
	// BaseURL is the API root, e.g. https://api.github.com.
	BaseURL string
	// Token is the GitHub token sent as a Bearer credential.
	Token string
	// HTTPClient is the underlying client; nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Issue is the subset of a GitHub issue the solving flow needs.
type Issue struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Labels  []string `json:"-"`
	State   string   `json:"state"`
	HTMLURL string   `json:"html_url"`
}

// Repo is the subset of a GitHub repository the solving flow needs.
type Repo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

// PRRequest is a pull request creation request.
type PRRequest struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// PR is the subset of a GitHub pull request the solving flow needs.
type PR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// NewClient returns a Client for the given API base URL and token.
func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTPClient: http.DefaultClient}
}

// GetIssue fetches the details of issue number in owner/repo.
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Number  int `json:"number"`
		Title   string
		Body    string
		State   string
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("github: decoding %s: %w", path, err)
	}
	issue := &Issue{
		Number:  raw.Number,
		Title:   raw.Title,
		Body:    raw.Body,
		State:   raw.State,
		HTMLURL: raw.HTMLURL,
	}
	for _, l := range raw.Labels {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue, nil
}

// GetRepo fetches the info for owner/repo.
func (c *Client) GetRepo(ctx context.Context, owner, repo string) (*Repo, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info Repo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("github: decoding %s: %w", path, err)
	}
	return &info, nil
}

// CreatePR opens a pull request in owner/repo from head to base.
func (c *Client) CreatePR(ctx context.Context, owner, repo string, req PRRequest) (*PR, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr PR
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("github: decoding %s: %w", path, err)
	}
	return &pr, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	// On error the body is drained and closed here; on success the caller
	// owns the response and must close resp.Body.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		hint := ""
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			hint = ": check that the GitHub token is valid and not expired"
		case http.StatusForbidden:
			hint = ": this token is missing the permissions GitHub needs here (classic: repo scope / fine-grained: contents + pull-requests read-write)"
		}
		return nil, fmt.Errorf("github: %s %s: %s: %s%s", method, path, resp.Status, string(body), hint)
	}
	return resp, nil
}
