// account.go implements the small account commands: `shipyard whoami` and
// `shipyard logout`, which work on the same stored credentials as
// `shipyard login` (internal/auth).

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/pefman/Shipyard/internal/auth"
	"github.com/pefman/Shipyard/internal/config"
)

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// runWhoami implements `shipyard whoami`: it reports the GitHub identity
// the current configuration resolves to, using the same token precedence
// as every other command: --github-token flag, SHIPYARD_GITHUB_TOKEN,
// then the credentials stored by `shipyard login`.
func runWhoami(args []string) error {
	return whoami(args, os.Stdout)
}

func whoami(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	tokenFlag := fs.String("github-token", "", "GitHub token (same precedence as on the other commands)")
	apiBase := fs.String("github-api", "", "GitHub API root (env SHIPYARD_GITHUB_API; default https://api.github.com)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	apiRoot := *apiBase

	token := firstNonEmpty(*tokenFlag, os.Getenv(config.EnvGitHubToken))
	stored := loadStoredCredentials()
	switch {
	case token != "":
		// Flag or env token carries no stored identity: ask GitHub.
		login, err := verifyToken(apiRoot, token)
		if err != nil {
			return fmt.Errorf("whoami: %w", err)
		}
		fmt.Fprintf(out, "@%s\n", login)
	case stored != nil:
		login := stored.Username
		if login == "" {
			// Malformed credentials without a username: verify instead.
			var err error
			if login, err = verifyToken(apiRoot, stored.AccessToken); err != nil {
				return fmt.Errorf("whoami: %w", err)
			}
		}
		fmt.Fprintf(out, "@%s\n", login)
		// Only the refresh-token expiry is stored by `shipyard login`;
		// show it when that metadata exists.
		if !stored.RefreshTokenExpiresAt.IsZero() {
			fmt.Fprintf(out, "refresh token expires %s\n", stored.RefreshTokenExpiresAt.UTC().Format(time.RFC3339))
		}
	default:
		return fmt.Errorf("not logged in: run 'shipyard login', or set --github-token / %s", config.EnvGitHubToken)
	}
	return nil
}

// loadStoredCredentials returns the credentials stored by `shipyard login`,
// or nil when none are stored (or none are readable).
func loadStoredCredentials() *auth.Credentials {
	path, err := auth.CredentialsPath()
	if err != nil {
		return nil
	}
	creds, err := auth.LoadCredentials(path)
	if err != nil {
		return nil
	}
	if creds.AccessToken == "" {
		return nil
	}
	return creds
}

// verifyToken asks GET /user which account token belongs to. apiBase is
// overridden in this order: explicit value, SHIPYARD_GITHUB_API, the
// default public API.
func verifyToken(apiBase, token string) (string, error) {
	if apiBase == "" {
		apiBase = firstNonEmpty(os.Getenv(config.EnvGitHubAPIRoot), auth.DefaultAPIBase)
	}
	return auth.VerifyUser(context.Background(), http.DefaultClient, apiBase, token)
}

// runLogout implements `shipyard logout`: it removes the stored
// credentials file and reports where it went.
func runLogout() error {
	return logout(os.Stdout)
}

func logout(out io.Writer) error {
	path, err := auth.CredentialsPath()
	if err != nil {
		return fmt.Errorf("logout: cannot determine credentials location (set XDG_CONFIG_HOME): %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("logout: not logged in (no stored credentials at %s)", path)
		}
		return fmt.Errorf("logout: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("logout: removing %s: %w", path, err)
	}
	fmt.Fprintf(out, "logged out (removed %s)\n", path)
	return nil
}
