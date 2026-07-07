package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// TestDeleteDeviceEmitsAudit verifies OBL-05: an account deleting one of its
// own devices emits a device_revoke audit row (best-effort, account-scoped
// via cloudstore.AuditProjectSentinel).
func TestDeleteDeviceEmitsAudit(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.addDevice(&cloudstore.Device{ID: "dev-1", AccountID: "alice", Name: "laptop", ScopeProjects: []string{}})
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodDelete, "/devices/dev-1", "token-alice", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditEntries) != 1 {
		t.Fatalf("expected 1 audit entry after device delete, got %d: %+v", len(ms.auditEntries), ms.auditEntries)
	}
	entry := ms.auditEntries[0]
	if entry.Action != cloudstore.AuditActionDeviceRevoke || entry.Outcome != cloudstore.AuditOutcomeDeviceRevoked {
		t.Fatalf("unexpected action/outcome: %+v", entry)
	}
	if entry.Contributor != "alice" || entry.Project != cloudstore.AuditProjectSentinel {
		t.Fatalf("expected contributor=alice project=%q, got %+v", cloudstore.AuditProjectSentinel, entry)
	}
	if entry.Metadata == nil || entry.Metadata["device_id"] != "dev-1" || entry.Metadata["device_name"] != "laptop" {
		t.Fatalf("expected device_id/device_name in metadata, got %+v", entry.Metadata)
	}
}

// TestDeleteOtherAccountDeviceDoesNotAudit verifies the ownership check still
// wins: a rejected delete attempt on someone else's device (404) must not
// emit an audit row.
func TestDeleteOtherAccountDeviceDoesNotAudit(t *testing.T) {
	ms := newFakeMembershipStore()
	authSvc := &fakeRBACAuth{
		accounts: map[string]*cloudauth.AccountClaims{
			"token-alice": {AccountID: "alice", Username: "alice"},
		},
	}
	ms.addDevice(&cloudstore.Device{ID: "dev-bob", AccountID: "bob", Name: "bobs-laptop", ScopeProjects: []string{}})
	srv := newMemberTestServer(ms, authSvc)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, makeAccountRequest(http.MethodDelete, "/devices/dev-bob", "token-alice", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(ms.auditEntries) != 0 {
		t.Fatalf("expected 0 audit entries on rejected delete, got %d: %+v", len(ms.auditEntries), ms.auditEntries)
	}
}
