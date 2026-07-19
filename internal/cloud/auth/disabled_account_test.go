package auth

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// seededUser builds a *cloudstore.User with a real bcrypt hash of password and an
// optional disabled marker, so the disabled-enforcement tests exercise the same
// bcrypt-then-disabled ordering the production flows use.
func seededUser(t *testing.T, id, username, password string, disabled bool) *cloudstore.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &cloudstore.User{ID: id, Username: username, Email: username + "@example.com", PasswordHash: string(hash)}
	if disabled {
		u.DisabledAt = sql.NullTime{Valid: true, Time: time.Now()}
	}
	return u
}

// TestLoginRejectsDisabledUser proves Login rejects a disabled account with the
// new ErrAccountDisabled sentinel AFTER a successful bcrypt compare, while an
// active account with the same password logs in unchanged (C1 point 2).
func TestLoginRejectsDisabledUser(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{users: []*cloudstore.User{
		seededUser(t, "user-1", "alice", "supersecret", true),
		seededUser(t, "user-2", "bob", "supersecret", false),
	}}

	// Disabled account with the CORRECT password → ErrAccountDisabled.
	if _, _, err := svc.Login("alice", "supersecret"); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled login error = %v, want ErrAccountDisabled", err)
	}

	// Disabled account with the WRONG password → still ErrInvalidCredentials (the
	// bcrypt failure fires first; never leak that the account is disabled).
	if _, _, err := svc.Login("alice", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled+wrong-password error = %v, want ErrInvalidCredentials", err)
	}

	// Active account still logs in and mints a valid token (unchanged behavior).
	token, user, err := svc.Login("bob", "supersecret")
	if err != nil {
		t.Fatalf("active login: %v", err)
	}
	if _, err := svc.ParseAccountToken(token); err != nil {
		t.Fatalf("active login token must parse: %v", err)
	}
	if user == nil || user.ID != "user-2" {
		t.Fatalf("unexpected active user: %+v", user)
	}
}

// TestLoginForDeviceRejectsDisabledUser proves the device-bound login path also
// rejects a disabled account after bcrypt succeeds.
func TestLoginForDeviceRejectsDisabledUser(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{users: []*cloudstore.User{
		seededUser(t, "user-1", "alice", "supersecret", true),
		seededUser(t, "user-2", "bob", "supersecret", false),
	}}
	svc.deviceStore = newFakeDeviceStore()

	if _, _, err := svc.LoginForDevice("alice", "supersecret", "laptop"); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled device login error = %v, want ErrAccountDisabled", err)
	}

	// Active account still logs in via device path.
	if _, _, err := svc.LoginForDevice("bob", "supersecret", "laptop"); err != nil {
		t.Fatalf("active device login: %v", err)
	}
}

// TestRefreshRejectsDisabledUser proves Refresh will NOT mint a fresh token for an
// account that has been disabled since its current token was issued (C1 point 2).
func TestRefreshRejectsDisabledUser(t *testing.T) {
	svc := newAccountService(t)
	store := &fakeUserStore{users: []*cloudstore.User{
		seededUser(t, "user-1", "alice", "supersecret", false),
	}}
	svc.accountStore = store

	// Alice logs in while active → valid token.
	token, _, err := svc.Login("alice", "supersecret")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Refresh while still active → new token minted (unchanged behavior).
	if _, err := svc.Refresh(token); err != nil {
		t.Fatalf("active refresh must succeed: %v", err)
	}

	// Operator disables alice; her old token signature is still valid, but Refresh
	// must now reject rather than hand out a fresh 24h token.
	store.users[0].DisabledAt = sql.NullTime{Valid: true, Time: time.Now()}
	if _, err := svc.Refresh(token); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled refresh error = %v, want ErrAccountDisabled", err)
	}
}
