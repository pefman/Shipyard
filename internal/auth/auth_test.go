package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockServer is an in-test GitHub: it answers the device-code, token
// poll, and /user endpoints from scripted responses.
type mockServer struct {
	mu          sync.Mutex
	deviceCode  DeviceCode
	deviceCalls int
	poll        []pollScript
	pollIdx     int
	user        []userScript
	userIdx     int
	deviceCID   string
	pollCID     string
	userAuth    string
}

type pollScript struct {
	code  string // empty means "success: return token"
	token Token
}

type userScript struct {
	status int // 0 means 200
	login  string
}

func (m *mockServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			m.mu.Lock()
			m.deviceCalls++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.deviceCID = body["client_id"]
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(m.deviceCode)
		case "/login/oauth/access_token":
			m.mu.Lock()
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.pollCID = body["client_id"]
			if m.pollIdx >= len(m.poll) {
				m.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"error":"test: poll script exhausted"}`)
				return
			}
			next := m.poll[m.pollIdx]
			m.pollIdx++
			m.mu.Unlock()
			if next.code == "" {
				_ = json.NewEncoder(w).Encode(next.token)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"code": next.code})
		case "/user":
			m.mu.Lock()
			m.userAuth = r.Header.Get("Authorization")
			var next userScript
			if m.userIdx < len(m.user) {
				next = m.user[m.userIdx]
				m.userIdx++
			}
			m.mu.Unlock()
			if next.status != 0 {
				w.WriteHeader(next.status)
				fmt.Fprint(w, `{"message":"denied"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"login": next.login})
		default:
			http.NotFound(w, r)
		}
	}
}

func (m *mockServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockServer) pollCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pollIdx
}

func (m *mockServer) deviceFlow(t *testing.T) {
	t.Helper()
	m.deviceCode = DeviceCode{
		DeviceCode:      "dev-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://github.com/login/device",
		Expiration:      900,
		Interval:        5,
	}
	m.poll = []pollScript{{token: Token{AccessToken: "new-tok", TokenType: "bearer", Scope: "read:user"}}}
}

func fastSleep(sleeps *[]time.Duration) func(time.Duration) {
	return func(d time.Duration) { *sleeps = append(*sleeps, d) }
}

func TestRequestDeviceCode(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	srv := m.start(t)

	code, err := RequestDeviceCode(context.Background(), srv.Client(), srv.URL+"/login", "cid-test")
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if code.UserCode != "ABCD-1234" || code.VerificationURI != "https://github.com/login/device" {
		t.Fatalf("unexpected device code: %+v", code)
	}
	if m.deviceCID != "cid-test" {
		t.Errorf("device code request sent client_id %q, want %q", m.deviceCID, "cid-test")
	}
	if m.deviceCalls != 1 {
		t.Errorf("device code endpoint called %d times", m.deviceCalls)
	}
}

func TestPollPendingSlowDownSuccess(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.poll = []pollScript{
		{code: PollPending},
		{code: PollSlowDown},
		{code: PollPending},
		{token: Token{AccessToken: "tok-1"}},
	}
	srv := m.start(t)

	var sleeps []time.Duration
	token, err := PollForToken(context.Background(), srv.Client(), srv.URL+"/login", "cid-test",
		&DeviceCode{DeviceCode: "dev-code", Interval: 5, Expiration: 900}, fastSleep(&sleeps))
	if err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if token.AccessToken != "tok-1" {
		t.Fatalf("token = %+v", token)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("slept %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v", i, sleeps[i], want[i])
		}
	}
	if m.pollCalls() != 4 {
		t.Errorf("polled %d times, want 4", m.pollCalls())
	}
	if m.pollCID != "cid-test" {
		t.Errorf("poll request sent client_id %q, want %q", m.pollCID, "cid-test")
	}
}

func TestPollAccessDenied(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.poll = []pollScript{{code: PollPending}, {code: PollDenied}}
	srv := m.start(t)

	_, err := PollForToken(context.Background(), srv.Client(), srv.URL+"/login", "cid-test",
		&DeviceCode{Interval: 5, Expiration: 900}, fastSleep(&nilSleeps))
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("err = %v, want ErrAccessDenied", err)
	}
}

func TestPollExpiredToken(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.poll = []pollScript{{code: PollPending}, {code: PollExpired}}
	srv := m.start(t)

	_, err := PollForToken(context.Background(), srv.Client(), srv.URL+"/login", "cid-test",
		&DeviceCode{Interval: 5, Expiration: 900}, fastSleep(&nilSleeps))
	if !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("err = %v, want ErrExpiredToken", err)
	}
}

func TestPollDeadlineExpiry(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	srv := m.start(t)

	_, err := PollForToken(context.Background(), srv.Client(), srv.URL+"/login", "cid-test",
		&DeviceCode{Interval: 5, Expiration: 0}, fastSleep(&nilSleeps))
	if !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("err = %v, want ErrExpiredToken", err)
	}
	if m.pollCalls() != 0 {
		t.Errorf("polled %d times after deadline, want 0", m.pollCalls())
	}
}

func TestVerifyUser(t *testing.T) {
	m := &mockServer{}
	m.user = []userScript{{login: "pefman"}}
	srv := m.start(t)

	login, err := VerifyUser(context.Background(), srv.Client(), srv.URL, "tok-1")
	if err != nil {
		t.Fatalf("VerifyUser: %v", err)
	}
	if login != "pefman" {
		t.Fatalf("login = %q", login)
	}
	if got := m.userAuth; got != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok-1")
	}

	// 401 means the stored token is unusable.
	m = &mockServer{}
	m.user = []userScript{{status: http.StatusUnauthorized}}
	srv = m.start(t)
	if _, err := VerifyUser(context.Background(), srv.Client(), srv.URL, "stale"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestRunExistingValidTokenShortCircuits(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.user = []userScript{{login: "pefman"}}
	srv := m.start(t)

	dir := t.TempDir()
	creds := &Credentials{AccessToken: "stored-tok", TokenType: "bearer", Username: "pefman", UpdatedAt: time.Now()}
	if err := SaveCredentials(dir, creds); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	res, err := Run(context.Background(), Deps{
		ClientID:  "cid-test",
		LoginBase: srv.URL + "/login",
		APIBase:   srv.URL,
		ConfigDir: dir,
		Sleep:     fastSleep(&nilSleeps),
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.AlreadyLoggedIn {
		t.Error("AlreadyLoggedIn = false, want true")
	}
	if res.Username != "pefman" {
		t.Errorf("Username = %q", res.Username)
	}
	if m.deviceCalls != 0 {
		t.Errorf("device code requested %d times despite valid stored token", m.deviceCalls)
	}
	if !strings.Contains(out.String(), "already logged in as @pefman") {
		t.Errorf("out = %q, want 'already logged in as @pefman'", out.String())
	}
}

func TestRunInvalidStoredTokenRedoesFlow(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.user = []userScript{
		{status: http.StatusUnauthorized}, // stored token rejected
		{login: "pefman"},                 // new token accepted
	}
	srv := m.start(t)

	dir := t.TempDir()
	if err := SaveCredentials(dir, &Credentials{AccessToken: "stale-tok", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	res, err := Run(context.Background(), Deps{
		ClientID:  "cid-test",
		LoginBase: srv.URL + "/login",
		APIBase:   srv.URL,
		ConfigDir: dir,
		Sleep:     fastSleep(&nilSleeps),
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AlreadyLoggedIn {
		t.Error("AlreadyLoggedIn = true despite invalid stored token")
	}
	if res.Username != "pefman" {
		t.Errorf("Username = %q", res.Username)
	}
	// The device-flow prompt must be printed clearly.
	for _, want := range []string{"https://github.com/login/device", "ABCD-1234", "Logged in as @pefman"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("out = %q, want it to contain %q", out.String(), want)
		}
	}
	// The new token must be stored — and only the new one.
	saved, err := LoadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if saved.AccessToken != "new-tok" {
		t.Errorf("stored access token = %q, want new-tok", saved.AccessToken)
	}
	if saved.RefreshToken != "" {
		t.Errorf("stored refresh token = %q, want empty", saved.RefreshToken)
	}
}

func TestRunForceRerunsDeviceFlow(t *testing.T) {
	m := &mockServer{}
	m.deviceFlow(t)
	m.user = []userScript{{login: "pefman"}, {login: "pefman"}}
	srv := m.start(t)

	dir := t.TempDir()
	if err := SaveCredentials(dir, &Credentials{AccessToken: "stored-tok", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	res, err := Run(context.Background(), Deps{
		ClientID:  "cid-test",
		Force:     true,
		LoginBase: srv.URL + "/login",
		APIBase:   srv.URL,
		ConfigDir: dir,
		Sleep:     fastSleep(&nilSleeps),
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AlreadyLoggedIn {
		t.Error("AlreadyLoggedIn = true with --force")
	}
	if m.deviceCalls != 1 {
		t.Errorf("device code requested %d times with --force, want 1", m.deviceCalls)
	}
	saved, err := LoadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "new-tok" {
		t.Errorf("stored access token = %q, want new-tok", saved.AccessToken)
	}
}

func TestRunNoClientIDError(t *testing.T) {
	_, err := Run(context.Background(), Deps{Sleep: fastSleep(&nilSleeps)})
	if err == nil || !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("err = %v, want client ID requirement", err)
	}
}

func TestSaveAndLoadCredentials(t *testing.T) {
	dir := t.TempDir()
	creds := &Credentials{
		AccessToken:  "tok",
		TokenType:    "bearer",
		Scope:        "read:user",
		RefreshToken: "refresh",
		Username:     "pefman",
		UpdatedAt:    time.Date(2026, 8, 29, 20, 32, 7, 0, time.UTC),
	}
	if err := SaveCredentials(dir, creds); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", mode)
	}
	dfi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := dfi.Mode().Perm(); mode != 0o700 {
		t.Errorf("credentials dir mode = %o, want 700", mode)
	}

	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != creds.AccessToken || got.RefreshToken != creds.RefreshToken ||
		got.Username != creds.Username || !got.UpdatedAt.Equal(creds.UpdatedAt) {
		t.Errorf("round trip mismatch: %+v", got)
	}

	// The raw file must never contain the token in a form logs would
	// pick up as plain text — it is JSON, and nothing else is written.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"access_token": "tok"`) {
		t.Errorf("credentials JSON = %s", data)
	}
}

func TestSaveCredentialsAtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := SaveCredentials(dir, &Credentials{AccessToken: "one", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(dir, &Credentials{AccessToken: "two", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCredentials(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "two" {
		t.Errorf("access token = %q, want two", got.AccessToken)
	}
	// No temp files may linger in the credentials directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Errorf("dir entries = %v, want only credentials.json", entries)
	}
}

func TestCredentialsPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "shipyard", "credentials.json")
	if path != want {
		t.Errorf("CredentialsPath() = %s, want %s", path, want)
	}
}

// nilSleeps is a package-level discard sink for tests that don't care
// about the sleep schedule.
var nilSleeps []time.Duration
