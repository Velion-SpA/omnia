package auth

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
)

// fakeUserStore is an in-memory userStore so account unit tests never require a
// live Postgres. It is injected into Service.accountStore (white-box test).
type fakeUserStore struct {
	users     []*cloudstore.User
	createErr error
}

func (f *fakeUserStore) GetUserByUsername(username string) (*cloudstore.User, error) {
	username = strings.TrimSpace(username)
	for _, u := range f.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) GetUserByEmail(email string) (*cloudstore.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) CreateUser(username, email, passwordHash string) (*cloudstore.User, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	u := &cloudstore.User{
		ID:           fmt.Sprintf("user-%d", len(f.users)+1),
		Username:     strings.TrimSpace(username),
		Email:        strings.TrimSpace(email),
		PasswordHash: passwordHash,
	}
	f.users = append(f.users, u)
	return u, nil
}

// TestSignupMapsUserExistsToAccountExists verifies that a late unique-violation
// surfaced by the store as cloudstore.ErrUserExists (the race path, since the
// ON CONFLICT upsert was removed) becomes a clean ErrAccountExists (OBL-02).
func TestSignupMapsUserExistsToAccountExists(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{createErr: cloudstore.ErrUserExists}
	if _, err := svc.Signup("neo", "neo@example.com", "supersecret"); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("expected ErrAccountExists, got %v", err)
	}
}

func newAccountService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestAccountTokenMintParseRoundTrip(t *testing.T) {
	svc := newAccountService(t)
	issuedAt := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return issuedAt }

	token, err := svc.MintAccountToken("acct-123", "alice")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	if !strings.Contains(token, ".") {
		t.Fatalf("expected payload.signature token, got %q", token)
	}

	claims, err := svc.ParseAccountToken(token)
	if err != nil {
		t.Fatalf("parse account token: %v", err)
	}
	if claims.AccountID != "acct-123" {
		t.Fatalf("account id = %q, want acct-123", claims.AccountID)
	}
	if claims.Username != "alice" {
		t.Fatalf("username = %q, want alice", claims.Username)
	}
	if claims.Typ != accountTokenType {
		t.Fatalf("typ = %q, want %q", claims.Typ, accountTokenType)
	}
	if claims.Exp != issuedAt.Add(accountTokenTTL).Unix() {
		t.Fatalf("exp = %d, want %d", claims.Exp, issuedAt.Add(accountTokenTTL).Unix())
	}
}

func TestParseAccountTokenRejectsTamperAndExpiry(t *testing.T) {
	svc := newAccountService(t)
	issuedAt := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return issuedAt }

	token, err := svc.MintAccountToken("acct-123", "alice")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}

	if _, err := svc.ParseAccountToken(token + "tampered"); !errors.Is(err, ErrInvalidAccountToken) {
		t.Fatalf("expected tampered signature to fail with ErrInvalidAccountToken, got %v", err)
	}

	// Tamper the payload part while keeping a "." separator.
	parts := strings.SplitN(token, ".", 2)
	if _, err := svc.ParseAccountToken(parts[0] + "AAAA" + "." + parts[1]); !errors.Is(err, ErrInvalidAccountToken) {
		t.Fatalf("expected tampered payload to fail with ErrInvalidAccountToken, got %v", err)
	}

	svc.now = func() time.Time { return issuedAt.Add(accountTokenTTL + time.Hour) }
	if _, err := svc.ParseAccountToken(token); !errors.Is(err, ErrInvalidAccountToken) {
		t.Fatalf("expected expired token to fail with ErrInvalidAccountToken, got %v", err)
	}
}

func TestAccountTokenIsNotDashboardToken(t *testing.T) {
	svc := newAccountService(t)
	svc.SetBearerToken("secret-token")

	dashboard, err := svc.MintDashboardSession("secret-token")
	if err != nil {
		t.Fatalf("mint dashboard session: %v", err)
	}
	if _, err := svc.ParseAccountToken(dashboard); !errors.Is(err, ErrInvalidAccountToken) {
		t.Fatalf("dashboard token must not parse as account token, got %v", err)
	}

	account, err := svc.MintAccountToken("acct-1", "bob")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	if _, err := svc.ParseDashboardSession(account); !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Fatalf("account token must not parse as dashboard token, got %v", err)
	}
}

func TestSignupCreatesBcryptUser(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{}

	user, err := svc.Signup("alice", "alice@example.com", "supersecret")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if user.Username != "alice" || user.Email != "alice@example.com" {
		t.Fatalf("unexpected user: %+v", user)
	}
	if user.PasswordHash == "supersecret" || user.PasswordHash == "" {
		t.Fatalf("password must be hashed, got %q", user.PasswordHash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("supersecret")); err != nil {
		t.Fatalf("bcrypt hash does not verify: %v", err)
	}
}

func TestSignupValidationAndDuplicates(t *testing.T) {
	tests := []struct {
		name     string
		seed     []*cloudstore.User
		username string
		email    string
		password string
		wantErr  error
	}{
		{name: "empty username", username: "", email: "a@e.com", password: "supersecret", wantErr: ErrUsernameRequired},
		{name: "short password", username: "alice", email: "a@e.com", password: "short", wantErr: ErrPasswordTooShort},
		{
			name:     "duplicate username",
			seed:     []*cloudstore.User{{ID: "user-1", Username: "alice", Email: "old@e.com"}},
			username: "alice", email: "new@e.com", password: "supersecret", wantErr: ErrAccountExists,
		},
		{
			name:     "duplicate email",
			seed:     []*cloudstore.User{{ID: "user-1", Username: "bob", Email: "taken@e.com"}},
			username: "alice", email: "taken@e.com", password: "supersecret", wantErr: ErrAccountExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAccountService(t)
			svc.accountStore = &fakeUserStore{users: tt.seed}
			_, err := svc.Signup(tt.username, tt.email, tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Signup() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoginFlow(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{}
	if _, err := svc.Signup("alice", "alice@example.com", "supersecret"); err != nil {
		t.Fatalf("signup: %v", err)
	}

	token, user, err := svc.Login("alice", "supersecret")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := svc.ParseAccountToken(token)
	if err != nil {
		t.Fatalf("login token must be a valid account token: %v", err)
	}
	if claims.AccountID != user.ID {
		t.Fatalf("token account id = %q, want %q", claims.AccountID, user.ID)
	}

	if _, _, err := svc.Login("alice", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login("ghost", "supersecret"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthorizeAcceptsAccountTokenAndLegacyBearer(t *testing.T) {
	svc := newAccountService(t)
	svc.SetBearerToken("legacy-token")

	legacy := httptest.NewRequest("GET", "/sync/pull", nil)
	legacy.Header.Set("Authorization", "Bearer legacy-token")
	if err := svc.Authorize(legacy); err != nil {
		t.Fatalf("legacy token must be accepted, got %v", err)
	}
	if claims, err := svc.AuthorizeAccount(legacy); claims != nil || err != nil {
		t.Fatalf("legacy token must yield no account claims, got %+v err=%v", claims, err)
	}

	token, err := svc.MintAccountToken("acct-9", "carol")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}
	accountReq := httptest.NewRequest("GET", "/sync/pull", nil)
	accountReq.Header.Set("Authorization", "Bearer "+token)
	if err := svc.Authorize(accountReq); err != nil {
		t.Fatalf("account token must be accepted, got %v", err)
	}
	claims, err := svc.AuthorizeAccount(accountReq)
	if err != nil {
		t.Fatalf("AuthorizeAccount account token: %v", err)
	}
	if claims == nil || claims.AccountID != "acct-9" {
		t.Fatalf("expected account claims for acct-9, got %+v", claims)
	}

	bad := httptest.NewRequest("GET", "/sync/pull", nil)
	bad.Header.Set("Authorization", "Bearer not-a-real-token")
	if err := svc.Authorize(bad); err == nil {
		t.Fatal("invalid token must be rejected")
	}
}

// fakeDeviceStore is an in-memory deviceRegistrar for account unit tests.
type fakeDeviceStore struct {
	devices map[string]*cloudstore.Device
	nextID  int
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{devices: make(map[string]*cloudstore.Device)}
}

func (f *fakeDeviceStore) GetOrCreateDevice(accountID, name string) (*cloudstore.Device, error) {
	key := accountID + "/" + name
	if existing, ok := f.devices[key]; ok {
		return existing, nil
	}
	f.nextID++
	d := &cloudstore.Device{
		ID:            fmt.Sprintf("dev-%d", f.nextID),
		AccountID:     accountID,
		Name:          name,
		ScopeProjects: []string{},
	}
	f.devices[key] = d
	return d, nil
}

func TestMintAccountTokenForDeviceRoundTrip(t *testing.T) {
	svc := newAccountService(t)
	token, err := svc.MintAccountTokenForDevice("acct-123", "alice", "dev-abc")
	if err != nil {
		t.Fatalf("mint device token: %v", err)
	}
	claims, err := svc.ParseAccountToken(token)
	if err != nil {
		t.Fatalf("parse device token: %v", err)
	}
	if claims.DeviceID != "dev-abc" {
		t.Fatalf("device id = %q, want dev-abc", claims.DeviceID)
	}
}

func TestMintAccountTokenBackwardCompat(t *testing.T) {
	svc := newAccountService(t)
	token, err := svc.MintAccountToken("acct-123", "alice")
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	claims, err := svc.ParseAccountToken(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.DeviceID != "" {
		t.Fatalf("device id = %q, want empty", claims.DeviceID)
	}
}

func TestLoginForDeviceBindsDeviceID(t *testing.T) {
	svc := newAccountService(t)
	svc.accountStore = &fakeUserStore{}
	svc.deviceStore = newFakeDeviceStore()
	user, err := svc.Signup("alice", "alice@example.com", "supersecret")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	token, _, err := svc.LoginForDevice("alice", "supersecret", "laptop")
	if err != nil {
		t.Fatalf("login for device: %v", err)
	}
	claims, err := svc.ParseAccountToken(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.DeviceID == "" {
		t.Fatal("expected device id in claims, got empty")
	}
	dev, err := svc.deviceStore.GetOrCreateDevice(user.ID, "laptop")
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if claims.DeviceID != dev.ID {
		t.Fatalf("token device id = %q, want %q", claims.DeviceID, dev.ID)
	}
}
