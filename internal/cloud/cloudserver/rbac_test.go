package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/store"
)

// ─── Fakes for RBAC tests ────────────────────────────────────────────────────

// fakeMembershipStore is an in-memory membership store for RBAC tests.
type fakeMembershipStore struct {
	fakeStore
	memberships    map[string]*cloudstore.Membership // key: accountID+"/"+project
	syncEnabledMap map[string]bool
	mutations      []MutationEntry
	devices        map[string]*cloudstore.Device // key: device ID
}

func newFakeMembershipStore() *fakeMembershipStore {
	return &fakeMembershipStore{
		fakeStore:      fakeStore{chunks: make(map[string][]byte)},
		memberships:    make(map[string]*cloudstore.Membership),
		syncEnabledMap: make(map[string]bool),
	}
}

func membershipKey(accountID, project string) string {
	return accountID + "/" + project
}

func (s *fakeMembershipStore) grant(accountID, project string, perms int, role string) {
	s.memberships[membershipKey(accountID, project)] = &cloudstore.Membership{
		AccountID: accountID,
		Project:   project,
		Perms:     perms,
		Role:      role,
	}
}

func (s *fakeMembershipStore) GetMembership(accountID, project string) (*cloudstore.Membership, error) {
	m, ok := s.memberships[membershipKey(accountID, project)]
	if !ok {
		return nil, nil
	}
	return m, nil
}

func (s *fakeMembershipStore) ListMembershipsForAccount(accountID string) ([]cloudstore.Membership, error) {
	var out []cloudstore.Membership
	for _, m := range s.memberships {
		if m.AccountID == accountID {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (s *fakeMembershipStore) IsProjectSyncEnabled(project string) (bool, error) {
	if enabled, ok := s.syncEnabledMap[project]; ok {
		return enabled, nil
	}
	return true, nil
}

func (s *fakeMembershipStore) InsertMutationBatch(_ context.Context, batch []MutationEntry) ([]int64, error) {
	seqs := make([]int64, len(batch))
	for i := range batch {
		seq := int64(len(s.mutations) + i + 1)
		seqs[i] = seq
		s.mutations = append(s.mutations, batch[i])
	}
	return seqs, nil
}

func (s *fakeMembershipStore) ListMutationsSince(_ context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]StoredMutation, bool, int64, error) {
	allowed := make(map[string]struct{})
	for _, p := range allowedProjects {
		allowed[p] = struct{}{}
	}
	useFilter := allowedProjects != nil
	var all []StoredMutation
	for i, m := range s.mutations {
		seq := int64(i + 1)
		if seq <= sinceSeq {
			continue
		}
		if useFilter {
			if _, ok := allowed[m.Project]; !ok {
				continue
			}
		}
		all = append(all, StoredMutation{
			Seq:       seq,
			Project:   m.Project,
			Entity:    m.Entity,
			EntityKey: m.EntityKey,
			Op:        m.Op,
			Payload:   m.Payload,
		})
		if len(all) >= limit {
			break
		}
	}
	hasMore := false
	latestSeq := int64(0)
	if len(all) > 0 {
		latestSeq = all[len(all)-1].Seq
	}
	return all, hasMore, latestSeq, nil
}

// ─── Device support on the RBAC fake store ───────────────────────────────────
// These complete deviceScopeStore + deviceManager + deviceRegistrar so the fake
// can back device-scope enforcement and the device-management endpoints.

func (s *fakeMembershipStore) addDevice(d *cloudstore.Device) {
	if s.devices == nil {
		s.devices = make(map[string]*cloudstore.Device)
	}
	s.devices[d.ID] = d
}

func (s *fakeMembershipStore) GetDevice(id string) (*cloudstore.Device, error) {
	if d, ok := s.devices[id]; ok {
		return d, nil
	}
	return nil, nil
}

func (s *fakeMembershipStore) ListDevicesForAccount(accountID string) ([]cloudstore.Device, error) {
	var out []cloudstore.Device
	for _, d := range s.devices {
		if d.AccountID == accountID {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (s *fakeMembershipStore) SetDeviceScope(id string, projects []string) error {
	if d, ok := s.devices[id]; ok {
		d.ScopeProjects = projects
		return nil
	}
	return fmt.Errorf("device not found: %s", id)
}

func (s *fakeMembershipStore) DeleteDevice(id string) error {
	delete(s.devices, id)
	return nil
}

func (s *fakeMembershipStore) GetOrCreateDevice(accountID, name string) (*cloudstore.Device, error) {
	for _, d := range s.devices {
		if d.AccountID == accountID && d.Name == name {
			return d, nil
		}
	}
	id := fmt.Sprintf("dev-%d", len(s.devices)+1)
	d := &cloudstore.Device{ID: id, AccountID: accountID, Name: name, ScopeProjects: []string{}}
	if s.devices == nil {
		s.devices = make(map[string]*cloudstore.Device)
	}
	s.devices[id] = d
	return d, nil
}

// fakeRBACAuth is an Authenticator that validates account tokens and
// returns AccountClaims for known accounts.
type fakeRBACAuth struct {
	accounts map[string]*cloudauth.AccountClaims // token → claims; "" token = legacy
	legacy   string                              // the shared bearer token value
	err      error
}

func (a *fakeRBACAuth) Authorize(r *http.Request) error {
	_, err := a.AuthorizeAccount(r)
	return err
}

func (a *fakeRBACAuth) AuthorizeAccount(r *http.Request) (*cloudauth.AccountClaims, error) {
	if a.err != nil {
		return nil, a.err
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(header)
	if len(parts) != 2 {
		return nil, cloudauth.ErrInvalidAccountToken
	}
	token := parts[1]
	if a.legacy != "" && token == a.legacy {
		return nil, nil // legacy shared token — authorized, no claims
	}
	if claims, ok := a.accounts[token]; ok {
		return claims, nil
	}
	return nil, cloudauth.ErrInvalidAccountToken
}

func (a *fakeRBACAuth) AuthorizeProject(project string) error {
	return nil // legacy path allows all for tests
}

func (a *fakeRBACAuth) EnrolledProjects() []string {
	return nil // legacy = no filter
}

// ─── Helper to build an RBAC-aware test server ───────────────────────────────

func newRBACTestServer(ms *fakeMembershipStore, authSvc *fakeRBACAuth) *CloudServer {
	return New(ms, authSvc, 0)
}

func makeAccountRequest(method, url, token string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestRBACReadOnlyMembership verifies that an account with only PermRead can pull
// but not push.
func TestRBACReadOnlyMembership(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermRead), "viewer")
	srv := newRBACTestServer(ms, authSvc)

	// Pull manifest — should be allowed (PermRead)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for read-only pull, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Push chunk — should be denied (PermInsert not granted)
	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, _ := coerceChunkProject(payload, "proj-a")
	chunkID := chunkIDFromPayload(normalized)
	pushBody, _ := json.Marshal(map[string]any{
		"chunk_id":   chunkID,
		"project":    "proj-a",
		"created_by": "alice",
		"data":       json.RawMessage(payload),
	})
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodPost, "/sync/push", "token-alice", pushBody))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for read-only push, got %d body=%q", rec2.Code, rec2.Body.String())
	}
}

// TestRBACDeleteDeniedWithoutPermDelete verifies that PermWrite is not enough for delete mutations.
func TestRBACDeleteDeniedWithoutPermDelete(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-bob": {AccountID: "bob", Username: "bob"},
		},
	}
	// Grant PermWrite = PermInsert | PermUpdate but NOT PermDelete
	ms.grant("bob", "proj-b", int(cloudauth.PermWrite), "writer")
	srv := newRBACTestServer(ms, authSvc)

	// Push a delete mutation — should be denied
	deletePayload, _ := json.Marshal(map[string]any{
		"entries": []map[string]any{
			{
				"project":    "proj-b",
				"entity":     store.SyncEntityObservation,
				"entity_key": "obs-1",
				"op":         store.SyncOpDelete,
				"payload":    json.RawMessage(`{"sync_id":"obs-1","deleted":true}`),
			},
		},
		"created_by": "bob",
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/sync/mutations/push", "token-bob", deletePayload))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for delete without PermDelete, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAccountIsolation verifies that account A cannot access account B's project.
// This is the cross-tenant isolation test.
func TestAccountIsolation(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
			"token-bob":   {AccountID: "bob", Username: "bob"},
		},
	}
	// Alice has access to proj-alice, Bob has access to proj-bob
	ms.grant("alice", "proj-alice", int(cloudauth.PermAll), "owner")
	ms.grant("bob", "proj-bob", int(cloudauth.PermAll), "owner")

	// Seed proj-bob with some data so there's something to leak
	ms.mutations = append(ms.mutations, MutationEntry{
		Project:   "proj-bob",
		Entity:    store.SyncEntitySession,
		EntityKey: "s-bob",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"id":"s-bob"}`),
	})

	srv := newRBACTestServer(ms, authSvc)

	// Alice tries to pull from proj-bob — must be 403
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-bob", "token-alice", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("TestAccountIsolation: alice should NOT access proj-bob manifest, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Alice tries to pull chunk from proj-bob — must be 403
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodGet, "/sync/pull/chunk-xyz?project=proj-bob", "token-alice", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("TestAccountIsolation: alice should NOT access proj-bob chunk, got %d body=%q", rec2.Code, rec2.Body.String())
	}

	// Alice tries mutation pull — should return nothing from proj-bob
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, makeAccountRequest(http.MethodGet, "/sync/mutations/pull?since_seq=0", "token-alice", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("TestAccountIsolation: expected 200 from mutation pull, got %d", rec3.Code)
	}
	var pullResp struct {
		Mutations []StoredMutation `json:"mutations"`
	}
	if err := json.NewDecoder(rec3.Body).Decode(&pullResp); err != nil {
		t.Fatalf("decode mutation pull response: %v", err)
	}
	for _, m := range pullResp.Mutations {
		if m.Project == "proj-bob" {
			t.Fatalf("TestAccountIsolation: alice received mutation from proj-bob: %+v", m)
		}
	}

	// Alice can still access her own project
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-alice", "token-alice", nil))
	if rec4.Code != http.StatusOK {
		t.Fatalf("TestAccountIsolation: alice should access proj-alice, got %d body=%q", rec4.Code, rec4.Body.String())
	}
}

// TestLegacySharedTokenStillWorks verifies backward compatibility.
func TestLegacySharedTokenStillWorks(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		legacy:   "shared-secret",
		accounts: map[string]*cloudauth.AccountClaims{},
	}
	srv := newRBACTestServer(ms, authSvc)

	// Legacy token can pull (no per-project enforcement in this path when allowAll)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "shared-secret", nil))
	// The legacy path calls AuthorizeProject which in fakeRBACAuth allows all
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for legacy token pull, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestDenyAllProjects verifies that the fail-closed fallback always returns an
// error, regardless of the supplied project name.
func TestDenyAllProjects(t *testing.T) {
	d := denyAllProjects{}
	err := d.AuthorizeProject("any-project")
	if err == nil {
		t.Fatal("expected error from denyAllProjects, got nil")
	}
}

// TestWithAuthStashesAccountInContext verifies that withAuth puts AccountClaims
// in ctx by wrapping a probe handler that reads the account back out of the
// request context. This asserts the stashing directly rather than inferring it.
func TestWithAuthStashesAccountInContext(t *testing.T) {
	ms := newFakeMembershipStore()
	wantClaims := &cloudauth.AccountClaims{AccountID: "acc-probe", Username: "probe"}
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"probe-token": wantClaims,
		},
	}
	srv := newRBACTestServer(ms, authSvc)

	var (
		probeFound  bool
		probeClaims *cloudauth.AccountClaims
	)
	probe := srv.withAuth(func(_ http.ResponseWriter, r *http.Request) {
		probeClaims, probeFound = cloudauth.AccountFromContext(r.Context())
	})

	rec := httptest.NewRecorder()
	probe(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-probe", "probe-token", nil))

	if !probeFound {
		t.Fatalf("expected account claims to be stashed in context, found none")
	}
	if probeClaims == nil || probeClaims.AccountID != "acc-probe" {
		t.Fatalf("expected stashed account acc-probe, got %+v", probeClaims)
	}

	// Legacy shared token must NOT stash any account into context.
	legacyAuth := &fakeRBACAuth{legacy: "shared-secret", accounts: map[string]*cloudauth.AccountClaims{}}
	legacySrv := newRBACTestServer(ms, legacyAuth)
	var legacyFound bool
	legacyProbe := legacySrv.withAuth(func(_ http.ResponseWriter, r *http.Request) {
		_, legacyFound = cloudauth.AccountFromContext(r.Context())
	})
	rec2 := httptest.NewRecorder()
	legacyProbe(rec2, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-probe", "shared-secret", nil))
	if legacyFound {
		t.Fatalf("legacy shared token must not stash an account into context")
	}
}

// ─── Phase 4: device-scope tests ─────────────────────────────────────────────

// TestDeviceScopeAdditiveRestriction: device with scope ["proj-a"] → proj-a OK,
// proj-b DENIED EVEN THOUGH the account has full membership on proj-b.
// This is THE key test proving the gate is AND-only.
func TestDeviceScopeAdditiveRestriction(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice", DeviceID: "dev-1"},
		},
	}
	// Alice has full access to BOTH projects via membership.
	ms.grant("alice", "proj-a", int(cloudauth.PermAll), "owner")
	ms.grant("alice", "proj-b", int(cloudauth.PermAll), "owner")
	// Device dev-1 is scoped to proj-a only.
	ms.addDevice(&cloudstore.Device{ID: "dev-1", AccountID: "alice", Name: "laptop", ScopeProjects: []string{"proj-a"}})

	srv := newRBACTestServer(ms, authSvc)

	// proj-a: membership OK + device scope OK → 200
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("device scope allows proj-a: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// proj-b: membership OK but device scope BLOCKS → 403
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-b", "token-alice", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("device scope blocks proj-b even with membership: expected 403, got %d body=%q", rec2.Code, rec2.Body.String())
	}
}

// TestDeviceEmptyScopeNoRestriction: device with empty scope_projects
// imposes NO additional restriction.
func TestDeviceEmptyScopeNoRestriction(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice", DeviceID: "dev-2"},
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermAll), "owner")
	// Device dev-2 has EMPTY scope → no restriction.
	ms.addDevice(&cloudstore.Device{ID: "dev-2", AccountID: "alice", Name: "phone", ScopeProjects: []string{}})

	srv := newRBACTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty device scope should not restrict: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestDeviceScopeCannotGrantBeyondMembership: device scope ["proj-x"] but no
// membership on proj-x → must still be denied (403). Scope never grants access.
func TestDeviceScopeCannotGrantBeyondMembership(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice", DeviceID: "dev-3"},
		},
	}
	// Alice has NO membership on proj-x.
	ms.addDevice(&cloudstore.Device{ID: "dev-3", AccountID: "alice", Name: "tablet", ScopeProjects: []string{"proj-x"}})

	srv := newRBACTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-x", "token-alice", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("device scope must not grant access without membership: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestDeviceNoDeviceID: a token without DeviceID (plain account token) imposes
// no device scope restriction (behaves exactly as today).
func TestDeviceNoDeviceID(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"}, // no DeviceID
		},
	}
	ms.grant("alice", "proj-a", int(cloudauth.PermAll), "owner")
	srv := newRBACTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodGet, "/sync/pull?project=proj-a", "token-alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no device id should not restrict: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestDeviceOwnershipEndpoints: account A cannot set scope on account B's device.
// Uses newMemberTestServer so s.account != nil and the /devices routes register,
// exercising the ownership check in the handler (404, existence hidden).
func TestDeviceOwnershipEndpoints(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
			"token-bob":   {AccountID: "bob", Username: "bob"},
		},
	}
	// Bob's device
	ms.addDevice(&cloudstore.Device{ID: "dev-bob", AccountID: "bob", Name: "bobs-laptop", ScopeProjects: []string{}})

	srv := newMemberTestServer(ms, authSvc)

	// Alice tries to set scope on Bob's device → 404 (ownership hidden)
	body, _ := json.Marshal(map[string]any{"projects": []string{"proj-x"}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodPost, "/devices/dev-bob/scope", "token-alice", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("alice setting scope on bob's device: expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Alice tries to delete Bob's device → 404
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, makeAccountRequest(http.MethodDelete, "/devices/dev-bob", "token-alice", nil))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("alice deleting bob's device: expected 404, got %d body=%q", rec2.Code, rec2.Body.String())
	}
}
