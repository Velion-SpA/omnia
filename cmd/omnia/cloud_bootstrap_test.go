package main

import (
	"context"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/cloud"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── Fakes for bootstrap-admin ───────────────────────────────────────────────

type fakeBootstrapStore struct {
	hasUser        bool
	err            error
	adminSetID     string
	adminSetValue  bool
	adminSetCalled bool
	adminErr       error
}

func (f *fakeBootstrapStore) HasAnyUser(context.Context) (bool, error) {
	return f.hasUser, f.err
}

func (f *fakeBootstrapStore) SetUserAdmin(_ context.Context, accountID string, admin bool) error {
	f.adminSetCalled = true
	f.adminSetID = accountID
	f.adminSetValue = admin
	return f.adminErr
}

type fakeBootstrapSignup struct {
	user      *cloudstore.User
	err       error
	gotUser   string
	gotEmail  string
	gotPasswd string
}

func (f *fakeBootstrapSignup) Signup(username, email, password string) (*cloudstore.User, error) {
	f.gotUser, f.gotEmail, f.gotPasswd = username, email, password
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

type fakeBootstrapIssuer struct {
	raw      string
	id       string
	err      error
	called   bool
	gotLabel string
}

func (f *fakeBootstrapIssuer) IssueManagedToken(_ context.Context, _, label string) (string, string, error) {
	f.called = true
	f.gotLabel = label
	if f.err != nil {
		return "", "", f.err
	}
	return f.raw, f.id, nil
}

// injectBootstrapDeps overrides newBootstrapDeps for the duration of a test.
func injectBootstrapDeps(t *testing.T, cs bootstrapAdminStore, su bootstrapAdminSignup, ti bootstrapTokenIssuer) {
	t.Helper()
	old := newBootstrapDeps
	newBootstrapDeps = func(cloud.Config, bool) (bootstrapAdminStore, bootstrapAdminSignup, bootstrapTokenIssuer, func() error, error) {
		return cs, su, ti, func() error { return nil }, nil
	}
	t.Cleanup(func() { newBootstrapDeps = old })
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestBootstrapAdminCreatesFirstAdmin(t *testing.T) {
	cfg := testConfig(t)
	su := &fakeBootstrapSignup{user: &cloudstore.User{ID: "1", Username: "root"}}
	cs := &fakeBootstrapStore{hasUser: false}
	injectBootstrapDeps(t, cs, su, &fakeBootstrapIssuer{})

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "bootstrap-admin", "--username", "root", "--password", "supersecret")

	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudBootstrapAdmin(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if su.gotUser != "root" || su.gotPasswd != "supersecret" {
		t.Fatalf("signup got unexpected args: user=%q pass=%q", su.gotUser, su.gotPasswd)
	}
	// OBL-16: the first admin must be stamped with the account-level admin flag.
	if !cs.adminSetCalled || cs.adminSetID != "1" || !cs.adminSetValue {
		t.Fatalf("expected SetUserAdmin(id=1, true), got called=%v id=%q value=%v", cs.adminSetCalled, cs.adminSetID, cs.adminSetValue)
	}
	if !strings.Contains(stdout, "Created first admin account") || !strings.Contains(stdout, "username=root") {
		t.Fatalf("unexpected output: %q", stdout)
	}
}

func TestBootstrapAdminRefusesWhenAccountExists(t *testing.T) {
	cfg := testConfig(t)
	su := &fakeBootstrapSignup{user: &cloudstore.User{ID: "1", Username: "root"}}
	injectBootstrapDeps(t, &fakeBootstrapStore{hasUser: true}, su, &fakeBootstrapIssuer{})

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "bootstrap-admin", "--username", "root", "--password", "supersecret")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudBootstrapAdmin(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if su.gotUser != "" {
		t.Fatal("signup must not be called when an account already exists")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("expected 'already exists' in stderr, got: %q", stderr)
	}
}

func TestBootstrapAdminIssuesTokenWithFlagAndEnv(t *testing.T) {
	t.Setenv("OMNIA_CLOUD_MANAGED_TOKENS", "1")
	t.Setenv("OMNIA_CLOUD_TOKEN_PEPPER", "a-strong-non-default-pepper-value")

	cfg := testConfig(t)
	su := &fakeBootstrapSignup{user: &cloudstore.User{ID: "7", Username: "root"}}
	issuer := &fakeBootstrapIssuer{raw: "omct_rawtoken123", id: "42"}
	injectBootstrapDeps(t, &fakeBootstrapStore{hasUser: false}, su, issuer)

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "bootstrap-admin", "--username", "root", "--password", "supersecret", "--issue-token")

	stdout, _, recovered := captureOutputAndRecover(t, func() { cmdCloudBootstrapAdmin(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("unexpected exit; stdout=%q", stdout)
	}
	if !issuer.called {
		t.Fatal("expected the managed-token issuer to be called")
	}
	if !strings.Contains(stdout, "omct_rawtoken123") || !strings.Contains(stdout, "token id: 42") {
		t.Fatalf("expected raw token + id in output, got: %q", stdout)
	}
}

func TestBootstrapAdminIssueTokenRequiresManagedTokensEnabled(t *testing.T) {
	// Neither OMNIA_CLOUD_MANAGED_TOKENS nor a non-default pepper set.
	t.Setenv("OMNIA_CLOUD_MANAGED_TOKENS", "")
	t.Setenv("OMNIA_CLOUD_TOKEN_PEPPER", "")

	cfg := testConfig(t)
	issuer := &fakeBootstrapIssuer{}
	injectBootstrapDeps(t, &fakeBootstrapStore{hasUser: false}, &fakeBootstrapSignup{user: &cloudstore.User{ID: "1", Username: "root"}}, issuer)

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "bootstrap-admin", "--username", "root", "--password", "supersecret", "--issue-token")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudBootstrapAdmin(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if issuer.called {
		t.Fatal("issuer must not be called when managed tokens are disabled")
	}
	if !strings.Contains(stderr, "OMNIA_CLOUD_MANAGED_TOKENS") {
		t.Fatalf("expected managed-tokens hint in stderr, got: %q", stderr)
	}
}

func TestBootstrapAdminMissingUsernameExitsNonZero(t *testing.T) {
	cfg := testConfig(t)
	injectBootstrapDeps(t, &fakeBootstrapStore{}, &fakeBootstrapSignup{}, &fakeBootstrapIssuer{})

	stubExitWithPanic(t)
	withArgs(t, "omnia", "cloud", "bootstrap-admin", "--password", "supersecret")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloudBootstrapAdmin(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || code == 0 {
		t.Fatalf("expected non-zero exit, got recovered=%v", recovered)
	}
	if !strings.Contains(stderr, "--username") {
		t.Fatalf("expected '--username' in stderr, got: %q", stderr)
	}
}
