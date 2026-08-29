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
	EnvAIProvider    = "SHIPYARD_AI_PROVIDER"
	EnvAIModel       = "SHIPYARD_AI_MODEL"
	EnvOpenAIKey     = "SHIPYARD_OPENAI_KEY"
	EnvXAIKey        = "SHIPYARD_XAI_KEY"
	EnvGitHubAPIRoot = "SHIPYARD_GITHUB_API"
)

// AI provider presets. The openai/xai presets pin the provider's standard
// API base URL and current default model (verified against the providers'
// docs); custom means "any OpenAI-compatible endpoint" and requires an
// explicit endpoint but no key.
var providers = map[string]struct {
	baseURL      string
	defaultModel string
	keyRequired  bool
}{
	"openai": {"https://api.openai.com/v1", "gpt-5.6-sol", true},
	"xai":    {"https://api.x.ai/v1", "grok-4.6", true},
	"custom": {"", "", false},
}

// Config is the resolved configuration for a Shipyard run.
type Config struct {
	// GitHubToken authenticates calls to the GitHub API.
	GitHubToken string
	// AIProvider is the resolved provider name: openai, xai, or custom.
	AIProvider string
	// AIEndpoint is the base URL of the AI endpoint (e.g. an OpenAI
	// compatible /v1/chat/completions server).
	AIEndpoint string
	// AIKey is the API key sent to the AI endpoint; empty for keyless
	// custom endpoints.
	AIKey string
	// AIModel is the model name sent to the endpoint.
	AIModel string
	// GitHubAPIRoot overrides the GitHub API base URL (mainly for tests).
	GitHubAPIRoot string
}

// Raw carries the values supplied via flags; empty fields are not set.
type Raw struct {
	GitHubToken string
	Provider    string
	AIEndpoint  string
	AIKey       string
	AIModel     string
}

// Load resolves cfg by layering flag values over environment variables.
// It returns an error if a required value is still missing after merging.
func Load(raw Raw) (*Config, error) {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(raw.Provider, os.Getenv(EnvAIProvider), "custom")))
	preset, ok := providers[provider]
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q: expected openai, xai, or custom", provider)
	}

	endpoint := firstNonEmpty(raw.AIEndpoint, os.Getenv(EnvAIEndpoint), preset.baseURL)
	key := firstNonEmpty(raw.AIKey, os.Getenv(providerKeyEnv(provider)), os.Getenv(EnvAIKey))
	model := firstNonEmpty(raw.AIModel, os.Getenv(EnvAIModel), preset.defaultModel)

	cfg := &Config{
		GitHubToken:   firstNonEmpty(raw.GitHubToken, os.Getenv(EnvGitHubToken)),
		AIProvider:    provider,
		AIEndpoint:    endpoint,
		AIKey:         key,
		AIModel:       model,
		GitHubAPIRoot: firstNonEmpty(os.Getenv(EnvGitHubAPIRoot), "https://api.github.com"),
	}

	var missing []string
	if cfg.GitHubToken == "" {
		missing = append(missing, "--github-token or "+EnvGitHubToken)
	}
	if cfg.AIEndpoint == "" {
		missing = append(missing, "--ai-endpoint or "+EnvAIEndpoint)
	}
	if cfg.AIKey == "" && preset.keyRequired {
		missing = append(missing, "an AI key for "+provider+": --ai-key, "+providerKeyEnv(provider)+", or "+EnvAIKey)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// providerKeyEnv returns the provider-specific key variable; custom
// endpoints have none (they fall back to the generic SHIPYARD_AI_KEY).
func providerKeyEnv(provider string) string {
	switch provider {
	case "openai":
		return EnvOpenAIKey
	case "xai":
		return EnvXAIKey
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
