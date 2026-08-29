// Package config holds Shipyard's runtime configuration and the logic to
// populate it from flags and environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables understood by Shipyard. Flags take precedence over
// environment values; environment variables over defaults.
const (
	EnvGitHubToken   = "SHIPYARD_GITHUB_TOKEN"
	EnvAIEndpoint    = "SHIPYARD_AI_ENDPOINT"
	EnvAIKey         = "SHIPYARD_AI_KEY"
	EnvGitHubAPIRoot = "SHIPYARD_GITHUB_API"
)

// Config is the resolved configuration for a Shipyard run.
type Config struct {
	// GitHubToken authenticates calls to the GitHub API.
	GitHubToken string
	// AIEndpoint is the base URL of the AI endpoint (e.g. an OpenAI
	// compatible /v1/chat/completions server).
	AIEndpoint string
	// AIKey is the API key sent to the AI endpoint.
	AIKey string
	// GitHubAPIRoot overrides the GitHub API base URL (mainly for tests).
	GitHubAPIRoot string
}

// Raw carries the values supplied via flags; empty fields are not set.
type Raw struct {
	GitHubToken string
	AIEndpoint  string
	AIKey       string
}

// Load resolves cfg by layering flag values over environment variables.
// It returns an error if a required value is still missing after merging.
func Load(raw Raw) (*Config, error) {
	cfg := &Config{
		GitHubToken:   firstNonEmpty(raw.GitHubToken, os.Getenv(EnvGitHubToken)),
		AIEndpoint:    firstNonEmpty(raw.AIEndpoint, os.Getenv(EnvAIEndpoint)),
		AIKey:         firstNonEmpty(raw.AIKey, os.Getenv(EnvAIKey)),
		GitHubAPIRoot: firstNonEmpty(os.Getenv(EnvGitHubAPIRoot), "https://api.github.com"),
	}

	var missing []string
	if cfg.GitHubToken == "" {
		missing = append(missing, "--github-token or "+EnvGitHubToken)
	}
	if cfg.AIEndpoint == "" {
		missing = append(missing, "--ai-endpoint or "+EnvAIEndpoint)
	}
	if cfg.AIKey == "" {
		missing = append(missing, "--ai-key or "+EnvAIKey)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
