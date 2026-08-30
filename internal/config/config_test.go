package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pefman/Shipyard/internal/auth"
)

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"no values", nil, ""},
		{"all empty", []string{"", ""}, ""},
		{"first wins", []string{"flag", "env"}, "flag"},
		{"skips empty", []string{"", "env"}, "env"},
		{"last one", []string{"", "", "c"}, "c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.values...); got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

// setEnv sets every variable to its value; empty values are unset so the
// test can express "not provided" without leaking stale env state.
func setEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for k, v := range values {
		if v == "" {
			os.Unsetenv(k)
		} else {
			t.Setenv(k, v)
		}
	}
}

// writeStoredCredentials stores a `shipyard login`-shaped credentials file
// under xdg/shipyard/credentials.json so the stored-token fallback layer
// can be exercised deterministically.
func writeStoredCredentials(t *testing.T, xdg, token string) {
	t.Helper()
	creds := &auth.Credentials{AccessToken: token, Username: "octocat", UpdatedAt: time.Now()}
	if err := auth.SaveCredentials(filepath.Join(xdg, "shipyard"), creds); err != nil {
		t.Fatalf("saving credentials: %v", err)
	}
}

func TestLoad(t *testing.T) {
	const ghAPI = "https://api.github.com"
	tests := []struct {
		name        string
		raw         Raw
		env         map[string]string
		storedToken string // token stored via `shipyard login`, when not ""
		want        Config
		wantErr     string // substring; "" means no error
	}{
		{
			// Custom is the default provider: the key is optional, so a
			// keyless local endpoint is valid.
			name: "nothing anywhere (default custom)",
			wantErr: "missing required configuration: " +
				"--github-token, SHIPYARD_GITHUB_TOKEN, or shipyard login, " +
				"--ai-endpoint or SHIPYARD_AI_ENDPOINT",
		},
		{
			name:        "stored login is the last fallback",
			raw:         Raw{AIEndpoint: "http://localhost:8080"},
			storedToken: "stored-token",
			want:        Config{GitHubToken: "stored-token", AIProvider: "custom", AIEndpoint: "http://localhost:8080", GitHubAPIRoot: ghAPI},
		},
		{
			name:        "env beats stored login",
			raw:         Raw{AIEndpoint: "http://localhost:8080"},
			env:         map[string]string{EnvGitHubToken: "env-token"},
			storedToken: "stored-token",
			want:        Config{GitHubToken: "env-token", AIProvider: "custom", AIEndpoint: "http://localhost:8080", GitHubAPIRoot: ghAPI},
		},
		{
			name:        "flag beats env beats stored login",
			raw:         Raw{GitHubToken: "flag-token", AIEndpoint: "http://localhost:8080"},
			env:         map[string]string{EnvGitHubToken: "env-token"},
			storedToken: "stored-token",
			want:        Config{GitHubToken: "flag-token", AIProvider: "custom", AIEndpoint: "http://localhost:8080", GitHubAPIRoot: ghAPI},
		},
		{
			name: "custom: endpoint and key from env",
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "http://localhost:8080", EnvAIKey: "env-key"},
			want: Config{GitHubToken: "env-token", AIProvider: "custom", AIEndpoint: "http://localhost:8080", AIKey: "env-key", GitHubAPIRoot: ghAPI},
		},
		{
			name: "custom: key optional",
			raw:  Raw{GitHubToken: "flag-token", AIEndpoint: "http://localhost:8080"},
			want: Config{GitHubToken: "flag-token", AIProvider: "custom", AIEndpoint: "http://localhost:8080", GitHubAPIRoot: ghAPI},
		},
		{
			name: "flags win over env",
			raw:  Raw{GitHubToken: "flag-token", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "https://ai.example/v1", EnvAIKey: "env-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "custom", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key", GitHubAPIRoot: ghAPI},
		},
		{
			name: "per-field precedence",
			raw:  Raw{AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "https://ai.example/v1"},
			want: Config{GitHubToken: "env-token", AIProvider: "custom", AIEndpoint: "https://ai.example/v1", AIKey: "flag-key", GitHubAPIRoot: ghAPI},
		},
		{
			name: "partially missing lists exactly what is missing",
			raw:  Raw{GitHubToken: "flag-token"},
			wantErr: "missing required configuration: " +
				"--ai-endpoint or SHIPYARD_AI_ENDPOINT",
		},
		{
			name: "empty flag falls back to env",
			raw:  Raw{GitHubToken: "", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token"},
			want: Config{GitHubToken: "env-token", AIProvider: "custom", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key", GitHubAPIRoot: ghAPI},
		},
		{
			name: "openai preset: base URL and model from preset, key via provider env",
			raw:  Raw{GitHubToken: "flag-token", Provider: "openai"},
			env:  map[string]string{EnvOpenAIKey: "openai-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://api.openai.com/v1",
				AIKey: "openai-key", AIModel: "gpt-5.6-sol", GitHubAPIRoot: ghAPI},
		},
		{
			name:    "openai without a key errors",
			raw:     Raw{GitHubToken: "flag-token", Provider: "openai"},
			wantErr: "an AI key for openai: --ai-key, SHIPYARD_OPENAI_KEY, or SHIPYARD_AI_KEY",
		},
		{
			name: "openai accepts the generic key",
			raw:  Raw{GitHubToken: "flag-token", Provider: "openai"},
			env:  map[string]string{EnvAIKey: "generic-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://api.openai.com/v1",
				AIKey: "generic-key", AIModel: "gpt-5.6-sol", GitHubAPIRoot: ghAPI},
		},
		{
			name: "provider-specific key wins over generic",
			raw:  Raw{GitHubToken: "flag-token", Provider: "openai"},
			env:  map[string]string{EnvOpenAIKey: "openai-key", EnvAIKey: "generic-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://api.openai.com/v1",
				AIKey: "openai-key", AIModel: "gpt-5.6-sol", GitHubAPIRoot: ghAPI},
		},
		{
			name: "xai preset: base URL, model, key via provider env",
			raw:  Raw{GitHubToken: "flag-token", Provider: "xai"},
			env:  map[string]string{EnvXAIKey: "xai-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "xai", AIEndpoint: "https://api.x.ai/v1",
				AIKey: "xai-key", AIModel: "grok-4.6", GitHubAPIRoot: ghAPI},
		},
		{
			name:    "xai without a key errors",
			raw:     Raw{GitHubToken: "flag-token", Provider: "xai"},
			wantErr: "an AI key for xai: --ai-key, SHIPYARD_XAI_KEY, or SHIPYARD_AI_KEY",
		},
		{
			name: "provider flag wins over provider env",
			raw:  Raw{GitHubToken: "flag-token", Provider: "xai", AIKey: "flag-key"},
			env:  map[string]string{EnvAIProvider: "openai"},
			want: Config{GitHubToken: "flag-token", AIProvider: "xai", AIEndpoint: "https://api.x.ai/v1",
				AIKey: "flag-key", AIModel: "grok-4.6", GitHubAPIRoot: ghAPI},
		},
		{
			name: "provider is case-insensitive",
			raw:  Raw{GitHubToken: "flag-token", Provider: "OpenAI", AIKey: "flag-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://api.openai.com/v1",
				AIKey: "flag-key", AIModel: "gpt-5.6-sol", GitHubAPIRoot: ghAPI},
		},
		{
			name:    "unknown provider errors",
			raw:     Raw{Provider: "anthropic"},
			wantErr: "unknown AI provider \"anthropic\": expected openai, xai, or custom",
		},
		{
			name: "explicit endpoint overrides the preset base URL",
			raw:  Raw{GitHubToken: "flag-token", Provider: "openai", AIEndpoint: "https://proxy.example/v1", AIKey: "flag-key"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://proxy.example/v1",
				AIKey: "flag-key", AIModel: "gpt-5.6-sol", GitHubAPIRoot: ghAPI},
		},
		{
			name: "model flag wins over the preset default",
			raw:  Raw{GitHubToken: "flag-token", Provider: "xai", AIKey: "flag-key", AIModel: "grok-4.5"},
			want: Config{GitHubToken: "flag-token", AIProvider: "xai", AIEndpoint: "https://api.x.ai/v1",
				AIKey: "flag-key", AIModel: "grok-4.5", GitHubAPIRoot: ghAPI},
		},
		{
			name: "model env beats the preset default",
			raw:  Raw{GitHubToken: "flag-token", Provider: "openai", AIKey: "flag-key"},
			env:  map[string]string{EnvAIModel: "gpt-5.6-luna"},
			want: Config{GitHubToken: "flag-token", AIProvider: "openai", AIEndpoint: "https://api.openai.com/v1",
				AIKey: "flag-key", AIModel: "gpt-5.6-luna", GitHubAPIRoot: ghAPI},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Isolate the stored-credentials layer: XDG_CONFIG_HOME points
			// at an empty temp dir so a login on the test machine can never
			// leak into these cases.
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			if tc.storedToken != "" {
				writeStoredCredentials(t, xdg, tc.storedToken)
			}
			setEnv(t, tc.env)
			got, err := Load(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Load: expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Load error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if *got != tc.want {
				t.Errorf("Load = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestLoadGitHubAPIRoot(t *testing.T) {
	// SHIPYARD_GITHUB_API is env-only (no flag) and overrides the default.
	t.Setenv(EnvGitHubAPIRoot, "https://ghe.example/api/v3")
	cfg, err := Load(Raw{GitHubToken: "t", AIEndpoint: "e", AIKey: "k"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "https://ghe.example/api/v3"; cfg.GitHubAPIRoot != want {
		t.Errorf("GitHubAPIRoot = %q, want %q", cfg.GitHubAPIRoot, want)
	}
}
