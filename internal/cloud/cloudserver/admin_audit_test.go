package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// TestAdminAuditPageOperatorOnly verifies GET /admin/audit is gated exactly
// like the rest of the Admin section: the operator sees 200, a non-operator
// account is forbidden.
func TestAdminAuditPageOperatorOnly(t *testing.T) {
	srv, _, authSvc := newAdminDashboardTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/audit", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/audit: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "REGISTRO DE AUDITORÍA") {
		t.Fatalf("expected Audit page body (Spanish default), got %q", rec.Body.String())
	}

	forbidden := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forbidden, cookieRequest(http.MethodGet, "/admin/audit", accountCookie(t, authSvc, "9", "eve"), ""))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("non-operator GET /admin/audit: expected 403, got %d", forbidden.Code)
	}
}

// TestAdminAuditPageListsEmittedEntries verifies the Audit page surfaces rows
// produced by real actions (here: a promote/demote round trip) — the read side
// of the OBL-05 expanded audit trail.
func TestAdminAuditPageListsEmittedEntries(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true

	srv.Handler().ServeHTTP(httptest.NewRecorder(), cookieRequest(http.MethodPost, "/admin/users/5/promote", operatorCookie(t, authSvc), ""))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/audit", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{cloudstore.AuditActionAdminPromote, cloudstore.AuditOutcomeAdminPromoted, "operator"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Audit page missing %q, body=%q", want, body)
		}
	}
}

// TestAdminAuditPageFiltersByOutcome verifies the ?outcome= query filter is
// applied: seeding two distinct outcomes and filtering to one must exclude
// the other from the rendered page.
func TestAdminAuditPageFiltersByOutcome(t *testing.T) {
	srv, store, authSvc := newAdminDashboardTestServer(t)
	store.admins["1"] = true

	srv.Handler().ServeHTTP(httptest.NewRecorder(), cookieRequest(http.MethodPost, "/admin/users/5/promote", operatorCookie(t, authSvc), ""))
	srv.Handler().ServeHTTP(httptest.NewRecorder(), cookieRequest(http.MethodPost, "/admin/users/5/demote", operatorCookie(t, authSvc), ""))

	rec := httptest.NewRecorder()
	url := "/admin/audit?outcome=" + cloudstore.AuditOutcomeAdminPromoted
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, url, operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, cloudstore.AuditActionAdminPromote) {
		t.Fatalf("filtered Audit page missing promote row, body=%q", body)
	}
	if strings.Contains(body, cloudstore.AuditActionAdminDemote) {
		t.Fatalf("filtered Audit page must exclude demote row, body=%q", body)
	}
}
