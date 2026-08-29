// Package auth implements `shipyard login`: the GitHub OAuth device flow
// and secure local credential storage.
//
// The client_id of the owner's OAuth App is never baked into the binary;
// it is supplied via --github-client-id or SHIPYARD_GITHUB_CLIENT_ID.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultLoginBase is the GitHub device-flow base URL (distinct from the
// api.github.com API root used for token verification).
const DefaultLoginBase = "https://github.com/login"

// DefaultAPIBase is the GitHub REST API root.
const DefaultAPIBase = "https://api.github.com"

const (
	deviceCodePath  = "/device/code"
	accessTokenPath = "/oauth/access_token"
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	credentialsFile = "credentials.json"
	credDirPerm     = 0o700
	credFilePerm    = 0o600
)

// Poll responses from POST /login/oauth/access_token.
const (
	PollPending  = "authorization_pending"
	PollSlowDown = "slow_down"
	PollDenied   = "access_denied"
	PollExpired  = "expired_token"
)

// DeviceCode is the response of POST /login/device/code. The JSON keys
// are the wire names GitHub actually sends (asserted by the wire-
// contract test in auth_test.go): GitHub sends expires_in, not
// expiration.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete is the URI with the user code pre-filled.
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	// ExpiresIn is the device code lifetime in seconds.
	ExpiresIn int `json:"expires_in"`
	Interval  int `json:"interval"`
}

// Token is the success response of POST /login/oauth/access_token.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
}

// Credentials is what is stored on disk (0600), with expiry metadata.
type Credentials struct {
	AccessToken           string    `json:"access_token"`
	TokenType             string    `json:"token_type,omitempty"`
	Scope                 string    `json:"scope,omitempty"`
	RefreshToken          string    `json:"refresh_token,omitempty"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at,omitempty"`
	Username              string    `json:"username,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// PollError is a terminal device-flow poll outcome.
type PollError struct {
	Code string
}

func (e *PollError) Error() string {
	switch e.Code {
	case PollDenied:
		return "github: device login was denied by the user"
	case PollExpired:
		return "github: device code expired; run shipyard login again"
	default:
		return "github: device login failed: " + e.Code
	}
}

// ErrAccessDenied: the user rejected authorization at the verification URI.
var ErrAccessDenied = &PollError{Code: PollDenied}

// ErrExpiredToken: the device code expired before authorization.
var ErrExpiredToken = &PollError{Code: PollExpired}

// IsInvalidToken reports whether err means a stored token is no longer
// accepted by the GitHub API.
func IsInvalidToken(err error) bool {
	return errors.Is(err, ErrTokenInvalid)
}

// ErrTokenInvalid means /user rejected the stored token (401/403).
var ErrTokenInvalid = errors.New("github: stored token is invalid or expired")

// Deps carries everything Run needs; the zero value plus ClientID works
// against the real GitHub endpoints with time.Sleep as the poll sleeper.
type Deps struct {
	// ClientID of the owner's GitHub OAuth App. Required.
	ClientID string
	// HTTPClient; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// LoginBase overrides the device-flow base URL (tests).
	LoginBase string
	// APIBase overrides the GitHub API root used for /user (tests).
	APIBase string
	// ConfigDir is the directory for credentials.json; empty uses
	// os.UserConfigDir()/shipyard (which honors $XDG_CONFIG_HOME).
	ConfigDir string
	// Force re-does the device flow even when a valid stored token exists.
	Force bool
	// Sleep is the poll delay; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Out receives the user-facing prompt (verification URI, user code)
	// and the "Logged in as" line; nil uses os.Stdout.
	Out io.Writer
}

// Result is the outcome of a login run.
type Result struct {
	// AlreadyLoggedIn is true when a stored token was verified and the
	// device flow was skipped.
	AlreadyLoggedIn bool
	// Username is the GitHub account the session belongs to.
	Username string
	// Path of the credentials file the session is stored at.
	CredentialsPath string
}

func (d *Deps) httpClient() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return http.DefaultClient
}

func (d *Deps) sleeper() func(time.Duration) {
	if d.Sleep != nil {
		return d.Sleep
	}
	return time.Sleep
}

func (d *Deps) out() io.Writer {
	if d.Out != nil {
		return d.Out
	}
	return os.Stdout
}

func (d *Deps) loginBase() string {
	if d.LoginBase != "" {
		return d.LoginBase
	}
	return DefaultLoginBase
}

func (d *Deps) apiBase() string {
	if d.APIBase != "" {
		return d.APIBase
	}
	return DefaultAPIBase
}

// Run performs `shipyard login`: if a valid stored token exists (and
// Force is false) it verifies that token via GET /user and reports it;
// otherwise it drives the device flow end to end and stores the new
// credentials with 0600 permissions.
func Run(ctx context.Context, d Deps) (*Result, error) {
	if d.ClientID == "" {
		return nil, fmt.Errorf("auth: client ID is required (--github-client-id or SHIPYARD_GITHUB_CLIENT_ID)")
	}
	configDir, err := d.configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(configDir, credentialsFile)

	// A valid stored token short-circuits the whole flow.
	if !d.Force {
		creds, err := LoadCredentials(path)
		switch {
		case err == nil && creds.AccessToken != "":
			login, verr := VerifyUser(ctx, d.httpClient(), d.apiBase(), creds.AccessToken)
			if verr != nil {
				if !errors.Is(verr, ErrTokenInvalid) {
					// A non-auth failure (network blip, API 5xx) must not
					// silently re-run the device flow.
					return nil, fmt.Errorf("auth: verifying stored token: %w", verr)
				}
				// Invalid token: fall through and redo the device flow.
			} else {
				fmt.Fprintf(d.out(), "already logged in as @%s (stored token valid; --force to log in again)\n", login)
				return &Result{AlreadyLoggedIn: true, Username: login, CredentialsPath: path}, nil
			}
		case err != nil && !errors.Is(err, os.ErrNotExist):
			// A file that exists but can't be read or parsed (corrupt or
			// undecodable JSON) is neither "valid" nor "rejected" — make
			// the fallback to the device flow visible instead of silent.
			fmt.Fprintf(d.out(), "warning: stored credentials could not be read (%v); starting the device flow\n", err)
		}
	}

	code, err := RequestDeviceCode(ctx, d.httpClient(), d.loginBase(), d.ClientID)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(d.out(), "1. Open this URL in your browser:\n   %s\n", code.VerificationURI)
	fmt.Fprintf(d.out(), "2. Enter this code: %s\n", code.UserCode)
	fmt.Fprintln(d.out(), "Waiting for you to authorize...")

	token, err := PollForToken(ctx, d.httpClient(), d.loginBase(), d.ClientID, code, d.sleeper())
	if err != nil {
		return nil, err
	}

	login, err := VerifyUser(ctx, d.httpClient(), d.apiBase(), token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("auth: verifying new token: %w", err)
	}

	creds := &Credentials{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		Scope:        token.Scope,
		RefreshToken: token.RefreshToken,
		Username:     login,
		UpdatedAt:    time.Now().UTC(),
	}
	// GitHub reports no refresh-token expiry in the device flow, so
	// RefreshTokenExpiresAt is deliberately left unset (zero value, and
	// therefore omitted from the stored JSON) rather than guessed.
	if err := SaveCredentials(configDir, creds); err != nil {
		return nil, err
	}
	fmt.Fprintf(d.out(), "Logged in as @%s\n", login)
	return &Result{Username: login, CredentialsPath: path}, nil
}

func (d *Deps) configDir() (string, error) {
	if d.ConfigDir != "" {
		return d.ConfigDir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("auth: cannot determine user config dir (set XDG_CONFIG_HOME): %w", err)
	}
	return filepath.Join(base, "shipyard"), nil
}

// RequestDeviceCode starts the device flow by POSTing to
// /login/device/code.
func RequestDeviceCode(ctx context.Context, hc *http.Client, base, clientID string) (*DeviceCode, error) {
	resp, err := postJSON(ctx, hc, base+deviceCodePath, map[string]string{"client_id": clientID})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github: requesting device code: %s: %s", resp.Status, string(body))
	}
	var code DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&code); err != nil {
		return nil, fmt.Errorf("github: decoding device code response: %w", err)
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		return nil, fmt.Errorf("github: device code response is missing device_code/user_code")
	}
	return &code, nil
}

// PollForToken polls /login/oauth/access_token until the user authorizes
// (success), denies, the code expires, or the deadline from
// dc.ExpiresIn is reached. Pending-style responses (authorization_pending,
// whatever field they arrive in) are never terminal. sleep is called
// between polls — its duration grows by 5s on every slow_down, per the
// OAuth device flow spec.
func PollForToken(ctx context.Context, hc *http.Client, base, clientID string, dc *DeviceCode, sleep func(time.Duration)) (*Token, error) {
	interval := dc.Interval
	if interval <= 0 {
		interval = 5
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		if time.Now().After(deadline) {
			return nil, ErrExpiredToken
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := postJSON(ctx, hc, base+accessTokenPath, map[string]string{
			"client_id":   clientID,
			"device_code": dc.DeviceCode,
			"grant_type":  deviceGrantType,
		})
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var out struct {
			// GitHub's poll outcomes arrive in the OAuth-spec "error"
			// field (verified against live responses) — the docs also
			// mention "code" — so both are accepted below.
			Code string `json:"code"`
			Err  string `json:"error"`
			Token
		}
		if err := json.Unmarshal(body, &out); err != nil {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("github: polling for token: %s: %s", resp.Status, string(body))
			}
			return nil, fmt.Errorf("github: decoding token response: %w", err)
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github: polling for token: %s: %s", resp.Status, string(body))
		}

		// Live GitHub responses carry the poll outcome in the
		// "error" field; the docs also show "code". Accept both —
		// pending-style outcomes are NOT terminal and must keep
		// polling, whatever field they arrive in.
		status := out.Code
		if status == "" {
			status = out.Err
		}
		switch status {
		case "":
			if out.Token.AccessToken != "" {
				return &out.Token, nil
			}
			return nil, fmt.Errorf("github: token response has no access_token: %s", string(body))
		case PollPending:
			// Keep polling at the current interval.
		case PollSlowDown:
			// Not an error: slow the polling down by 5s and continue.
			interval += 5
		case PollDenied:
			return nil, ErrAccessDenied
		case PollExpired:
			return nil, ErrExpiredToken
		default:
			return nil, &PollError{Code: status}
		}

		sleep(time.Duration(interval) * time.Second)
	}
}

// VerifyUser checks a token against GET /user and returns the account
// login. ErrTokenInvalid is returned for 401/403; other non-2xx
// responses are plain errors.
func VerifyUser(ctx context.Context, hc *http.Client, apiBase, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", ErrTokenInvalid
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("github: GET /user: %s: %s", resp.Status, string(body))
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("github: decoding /user: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("github: /user response has no login")
	}
	return user.Login, nil
}

// CredentialsPath returns the default path of the credentials file
// (honoring $XDG_CONFIG_HOME via os.UserConfigDir).
func CredentialsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "shipyard", credentialsFile), nil
}

// LoadCredentials reads the credentials file. It returns an error
// wrapping os.ErrNotExist when no file exists.
func LoadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("auth: decoding %s: %w", path, err)
	}
	return &c, nil
}

// SaveCredentials writes the credentials as JSON into dir, creating it
// if needed, with 0600 file permissions. The write is atomic (temp
// file + rename) so a crash can never leave a truncated file that
// silently "logs in" with a broken token.
func SaveCredentials(dir string, c *Credentials) error {
	if err := os.MkdirAll(dir, credDirPerm); err != nil {
		return fmt.Errorf("auth: creating %s: %w", dir, err)
	}
	// MkdirAll leaves a pre-existing directory's mode untouched; force
	// 0700 so old world-readable dirs can't keep the token readable.
	if err := os.Chmod(dir, credDirPerm); err != nil {
		return fmt.Errorf("auth: tightening perms on %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("auth: creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: writing %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(credFilePerm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	path := filepath.Join(dir, credentialsFile)
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("auth: installing %s: %w", path, err)
	}
	// Belt and braces: if a pre-existing world-readable file lingered
	// (rename replaces the inode, but ensure the final state is 0600).
	return os.Chmod(path, credFilePerm)
}

func postJSON(ctx context.Context, hc *http.Client, url string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
