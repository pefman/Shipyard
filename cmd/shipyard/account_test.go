package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pefman/Shipyard/internal/auth"
	"github.com/pefman/Shipyard/internal/config"
)

// userServer is a stub GET /user that answers with the given login.
func userServer(t *testing.T, login string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"login":%q}`, login)
	}))
}

// isolateConfigHome points the stored-credentials layer at an empty temp
// dir so a login on the test machine can never leak into the tests.
func isolateConfigHome(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return xdg
}

// storeCredentials writes a login-shaped credentials file under xdg.
func storeCredentials(t *testing.T, xdg string, c *auth.Credentials) {
	t.Helper()
	if err := auth.SaveCredentials(filepath.Join(xdg, "shipyard"), c); err != nil {
		t.Fatalf("saving credentials: %v", err)
	}
}

func TestRunWhoami(t *testing.T) {
	tests := []struct {
		name string
		// args; the placeholder {{srv}} is replaced with the stub /user
		// URL (only present when srvLogin is set).
		args     []string
		envGH    string // SHIPYARD_GITHUB_TOKEN value; "" means unset
		stored   *auth.Credentials
		srvLogin string // login the stub /user answers with; "" = no stub
		wantOut  string
		wantErr  string // substring; "" means no error
	}{
		{
			name:     "flag token verified via /user",
			args:     []string{"--github-token", "flag-tok", "--github-api", "{{srv}}"},
			srvLogin: "flaguser",
			wantOut:  "@flaguser\n",
		},
		{
			name:     "env token verified via /user",
			args:     []string{"--github-api", "{{srv}}"},
			envGH:    "env-tok",
			srvLogin: "envuser",
			wantOut:  "@envuser\n",
		},
		{
			name:    "nothing anywhere: clear not-logged-in error",
			wantErr: "not logged in",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xdg := isolateConfigHome(t)
			if tc.stored != nil {
				storeCredentials(t, xdg, tc.stored)
			}
			if tc.envGH != "" {
				t.Setenv(config.EnvGitHubToken, tc.envGH)
			} else {
				t.Setenv(config.EnvGitHubToken, "")
			}

			var srv *httptest.Server
			if tc.srvLogin != "" {
				srv = userServer(t, tc.srvLogin)
				defer srv.Close()
			}
			args := make([]string, len(tc.args))
			for i, a := range tc.args {
				args[i] = strings.ReplaceAll(a, "{{srv}}", srv.URL)
			}

			var out bytes.Buffer
			err := whoami(args, &out)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("whoami: expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("whoami error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("whoami: %v", err)
			}
			if out.String() != tc.wantOut {
				t.Errorf("whoami output = %q, want %q", out.String(), tc.wantOut)
			}
		})
	}

	// Stored-login cases: the stored username is shown as-is, so no /user
	// stub is involved.
	t.Run("stored credentials: stored username, no network", func(t *testing.T) {
		xdg := isolateConfigHome(t)
		t.Setenv(config.EnvGitHubToken, "")
		storeCredentials(t, xdg, &auth.Credentials{AccessToken: "stored-tok", Username: "octocat"})
		var out bytes.Buffer
		if err := whoami([]string{}, &out); err != nil {
			t.Fatalf("whoami: %v", err)
		}
		if want := "@octocat\n"; out.String() != want {
			t.Errorf("whoami output = %q, want %q", out.String(), want)
		}
	})
	t.Run("stored credentials: expiry shown when metadata exists", func(t *testing.T) {
		xdg := isolateConfigHome(t)
		t.Setenv(config.EnvGitHubToken, "")
		exp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
		storeCredentials(t, xdg, &auth.Credentials{AccessToken: "stored-tok", Username: "octocat", RefreshTokenExpiresAt: exp})
		var out bytes.Buffer
		if err := whoami([]string{}, &out); err != nil {
			t.Fatalf("whoami: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, "@octocat") {
			t.Errorf("whoami output = %q, want it to contain @octocat", got)
		}
		if want := "refresh token expires 2027-01-02T03:04:05Z"; !strings.Contains(got, want) {
			t.Errorf("whoami output = %q, want it to contain %q", got, want)
		}
	})
}

func TestRunLogout(t *testing.T) {
	t.Run("removes the stored credentials file", func(t *testing.T) {
		xdg := isolateConfigHome(t)
		storeCredentials(t, xdg, &auth.Credentials{AccessToken: "stored-tok", Username: "octo"})
		var out bytes.Buffer
		if err := logout(&out); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if !strings.Contains(out.String(), "logged out") {
			t.Errorf("logout output = %q, want it to contain %q", out.String(), "logged out")
		}
		path, err := auth.CredentialsPath()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := auth.LoadCredentials(path); err == nil {
			t.Error("credentials still loadable after logout")
		}
	})
	t.Run("clear error when nothing is stored", func(t *testing.T) {
		isolateConfigHome(t)
		var out bytes.Buffer
		err := logout(&out)
		if err == nil {
			t.Fatal("logout: expected error when no credentials are stored")
		}
		if !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("logout error = %q, want it to mention that nothing is logged in", err)
		}
	})
}
