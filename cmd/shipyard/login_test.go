package main

import "testing"

func TestResolveClientIDPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flagValue string
		env       string
		want      string
	}{
		{
			name:      "flag beats env",
			flagValue: "cid-flag",
			env:       "cid-env",
			want:      "cid-flag",
		},
		{
			name:      "flag alone",
			flagValue: "cid-flag",
			env:       "",
			want:      "cid-flag",
		},
		{
			name:      "env beats built-in default",
			flagValue: "",
			env:       "cid-env",
			want:      "cid-env",
		},
		{
			name:      "no flag, no env: built-in default",
			flagValue: "",
			env:       "",
			want:      defaultGitHubClientID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SHIPYARD_GITHUB_CLIENT_ID", tt.env)
			if got := resolveClientID(tt.flagValue); got != tt.want {
				t.Fatalf("resolveClientID(%q) = %q, want %q", tt.flagValue, got, tt.want)
			}
		})
	}
}

// Pins the pre-registered OAuth App client ID that ships as the built-in
// default; a silent change here would break zero-config login for every
// user until someone re-registered the app.
func TestDefaultClientIDIsOwnerRegisteredApp(t *testing.T) {
	if defaultGitHubClientID != "Iv23lipRhtA8srclwbp3" {
		t.Fatalf("built-in default client ID = %q, want %q", defaultGitHubClientID, "Iv23lipRhtA8srclwbp3")
	}
}
