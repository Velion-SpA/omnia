package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// stubLoginServer spins an httptest.Server that handles /auth/login and
// /auth/signup with configurable behaviour.
type stubLoginServer struct {
	// login settings
	loginToken      string
	loginStatusCode int // default 200

	// signup settings
	signupID         int
	signupUsername   string
	signupEmail      string
	signupStatusCode int // default 201

	// request capture
	loginBody  []byte
	signupBody []byte
}

func (s *stubLoginServer) newServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		s.loginBody = append([]byte{}, buf[:n]...)

		code := s.loginStatusCode
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": s.loginToken})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
		}
	})

	mux.HandleFunc("/auth/signup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		s.signupBody = append([]byte{}, buf[:n]...)

		code := s.signupStatusCode
		if code == 0 {
			code = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		switch code {
		case http.StatusCreated:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       s.signupID,
				"username": s.signupUsername,
				"email":    s.signupEmail,
			})
		case http.StatusConflict:
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "username or email already taken"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestCloudLoginWritesTokenToCloudJSON verifies that a successful login
// writes the returned token into cloud.json, preserving ServerURL.
func TestCloudLoginWritesTokenToCloudJSON(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "account-token-abc123",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	// Pre-populate cloud.json with an existing ServerURL so we can check it is preserved.
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://original.example.test",
	}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--server", srv.URL,
		"--username", "alice",
		"--password", "s3cr3t",
	)

	stdout, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("cmdCloudLogin panicked with exitCode; stdout=%q", stdout)
	}

	// Token must be written.
	cc, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc == nil {
		t.Fatal("cloud.json is nil after login")
	}
	if cc.Token != stub.loginToken {
		t.Errorf("expected token %q, got %q", stub.loginToken, cc.Token)
	}
	// ServerURL must be preserved.
	if cc.ServerURL != "https://original.example.test" {
		t.Errorf("ServerURL was overwritten: got %q", cc.ServerURL)
	}
	// Output message.
	if !strings.Contains(stdout, "alice") {
		t.Errorf("expected username in output, got: %q", stdout)
	}
}

// TestCloudLoginInvalidCredentialsExitsNonZero verifies that a 401 from the
// server causes cmdCloudLogin to exit with a non-zero status.
func TestCloudLoginInvalidCredentialsExitsNonZero(t *testing.T) {
	stub := &stubLoginServer{
		loginStatusCode: http.StatusUnauthorized,
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--server", srv.URL,
		"--username", "alice",
		"--password", "wrong",
	)

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})

	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic on 401, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit code on invalid credentials")
	}
	if !strings.Contains(stderr, "invalid credentials") {
		t.Errorf("expected 'invalid credentials' in stderr, got: %q", stderr)
	}
}

// TestCloudLoginPreservesServerURLWhenNoneInFile verifies that when cloud.json
// has no pre-existing ServerURL, login still succeeds and writes only the token.
func TestCloudLoginPreservesServerURLWhenNoneInFile(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "fresh-token-xyz",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	// No pre-existing cloud.json — use --server flag.
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--server", srv.URL,
		"--username", "bob",
		"--password", "pw",
	)

	_, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("unexpected exit on valid login")
	}

	cc, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("loadCloudConfig: %v", err)
	}
	if cc.Token != stub.loginToken {
		t.Errorf("expected token %q, got %q", stub.loginToken, cc.Token)
	}
}

// TestCloudSignupPostsCorrectBody verifies that signup sends the expected
// JSON body and prints the created account on success.
func TestCloudSignupPostsCorrectBody(t *testing.T) {
	stub := &stubLoginServer{
		signupID:       42,
		signupUsername: "carol",
		signupEmail:    "carol@example.test",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "signup",
		"--server", srv.URL,
		"--username", "carol",
		"--email", "carol@example.test",
		"--password", "hunter2",
	)

	stdout, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudSignup(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}

	// Verify body sent to server.
	var sent map[string]string
	if err := json.Unmarshal(stub.signupBody, &sent); err != nil {
		t.Fatalf("parse sent body: %v; raw=%q", err, stub.signupBody)
	}
	if sent["username"] != "carol" {
		t.Errorf("username: want %q got %q", "carol", sent["username"])
	}
	if sent["email"] != "carol@example.test" {
		t.Errorf("email: want %q got %q", "carol@example.test", sent["email"])
	}
	if sent["password"] != "hunter2" {
		t.Errorf("password: want %q got %q", "hunter2", sent["password"])
	}

	// Verify output.
	if !strings.Contains(stdout, "carol") {
		t.Errorf("expected username in output, got: %q", stdout)
	}
}

// TestCloudSignupConflictExitsNonZero verifies that a 409 from the server
// causes cmdCloudSignup to exit non-zero with a clear message.
func TestCloudSignupConflictExitsNonZero(t *testing.T) {
	stub := &stubLoginServer{
		signupStatusCode: http.StatusConflict,
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "signup",
		"--server", srv.URL,
		"--username", "alice",
		"--email", "alice@example.test",
		"--password", "pw",
	)

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudSignup(cfg)
	})

	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic on 409, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit on conflict")
	}
	if !strings.Contains(stderr, "already taken") {
		t.Errorf("expected 'already taken' in stderr, got: %q", stderr)
	}
}

// TestCloudLoginUsesServerURLFromCloudJSON verifies that when --server is
// omitted, the server URL is read from cloud.json.
func TestCloudLoginUsesServerURLFromCloudJSON(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "token-from-file-server",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: srv.URL,
	}); err != nil {
		t.Fatalf("saveCloudConfig: %v", err)
	}

	stubExitWithPanic(t)
	// No --server flag.
	withArgs(t, "engram", "cloud", "login",
		"--username", "dave",
		"--password", "pw",
	)

	_, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("unexpected exit — server URL from cloud.json should have worked")
	}

	cc, _ := loadCloudConfig(cfg)
	if cc == nil || cc.Token != stub.loginToken {
		t.Errorf("expected token %q, got %v", stub.loginToken, cc)
	}
}

// TestCloudLoginNoServerConfiguredExitsNonZero verifies the error when neither
// --server nor cloud.json ServerURL is present.
func TestCloudLoginNoServerConfiguredExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--username", "eve",
		"--password", "pw",
	)

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})

	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit when no server configured")
	}
	if !strings.Contains(stderr, "no cloud server configured") {
		t.Errorf("expected 'no cloud server configured' in stderr, got: %q", stderr)
	}
}

// TestCloudLoginPasswordPromptFallback verifies that when --password is
// omitted, the readPasswordFn hook is called and its result is used.
func TestCloudLoginPasswordPromptFallback(t *testing.T) {
	stub := &stubLoginServer{
		loginToken: "prompted-token",
	}
	srv := stub.newServer(t)

	cfg := testConfig(t)

	// Override the password-read hook so no terminal interaction is needed.
	old := readPasswordFn
	readPasswordFn = func(prompt string) (string, error) {
		return "prompted-password", nil
	}
	t.Cleanup(func() { readPasswordFn = old })

	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--server", srv.URL,
		"--username", "frank",
		// No --password flag.
	)

	_, _, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("unexpected exit when password is prompted")
	}

	// Check the body sent to the server contains the prompted password.
	var sent map[string]string
	if err := json.Unmarshal(stub.loginBody, &sent); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if sent["password"] != "prompted-password" {
		t.Errorf("expected prompted password in body, got: %q", sent["password"])
	}

	// And the token was persisted.
	cc, _ := loadCloudConfig(cfg)
	if cc == nil || cc.Token != stub.loginToken {
		t.Errorf("expected token %q, got %v", stub.loginToken, cc)
	}
}

// TestCloudLoginMissingUsernameExitsNonZero checks that omitting --username
// causes a non-zero exit.
func TestCloudLoginMissingUsernameExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "login",
		"--server", "http://localhost:9999",
		"--password", "pw",
	)

	_, stderr, recovered := captureOutputAndRecover(t, func() {
		cmdCloudLogin(cfg)
	})

	code, ok := recovered.(exitCode)
	if !ok {
		t.Fatalf("expected exitCode panic, got: %v", recovered)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit on missing --username")
	}
	if !strings.Contains(stderr, "--username") {
		t.Errorf("expected mention of --username in stderr, got: %q", stderr)
	}
}

// Ensure the os import is used (suppress "imported and not used" if tests change).
var _ = os.Stderr
