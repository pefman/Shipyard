package config

import (
	"os"
	"strings"
	"testing"
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

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		raw     Raw
		env     map[string]string
		want    Config
		wantErr string // substring; "" means no error
	}{
		{
			name: "nothing anywhere",
			wantErr: "missing required configuration: " +
				"--github-token or SHIPYARD_GITHUB_TOKEN, " +
				"--ai-endpoint or SHIPYARD_AI_ENDPOINT, " +
				"--ai-key or SHIPYARD_AI_KEY",
		},
		{
			name: "only env",
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "https://ai.example/v1", EnvAIKey: "env-key"},
			want: Config{GitHubToken: "env-token", AIEndpoint: "https://ai.example/v1", AIKey: "env-key", GitHubAPIRoot: "https://api.github.com"},
		},
		{
			name: "flags win over env",
			raw:  Raw{GitHubToken: "flag-token", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "https://ai.example/v1", EnvAIKey: "env-key"},
			want: Config{GitHubToken: "flag-token", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key", GitHubAPIRoot: "https://api.github.com"},
		},
		{
			name: "per-field precedence",
			raw:  Raw{AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token", EnvAIEndpoint: "https://ai.example/v1"},
			want: Config{GitHubToken: "env-token", AIEndpoint: "https://ai.example/v1", AIKey: "flag-key", GitHubAPIRoot: "https://api.github.com"},
		},
		{
			name: "partially missing lists exactly what is missing",
			raw:  Raw{GitHubToken: "flag-token"},
			wantErr: "missing required configuration: " +
				"--ai-endpoint or SHIPYARD_AI_ENDPOINT, " +
				"--ai-key or SHIPYARD_AI_KEY",
		},
		{
			name: "empty flag falls back to env",
			raw:  Raw{GitHubToken: "", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key"},
			env:  map[string]string{EnvGitHubToken: "env-token"},
			want: Config{GitHubToken: "env-token", AIEndpoint: "https://flag.example/v1", AIKey: "flag-key", GitHubAPIRoot: "https://api.github.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
