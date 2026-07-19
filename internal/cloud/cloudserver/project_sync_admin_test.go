package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// ─── Fake projectSyncControlAdminStore ────────────────────────────────────────
//
// fakeProjectSyncAdminStore embeds fakeTeamsStore (which already embeds the OBL-13
// admin-dashboard fake, which embeds fakeMembershipStore — the ChunkStore +
// membershipStore + MutationStore fake). It adds SetProjectSyncEnabled /
// GetProjectSyncControl (OBL-04) writing into the SAME syncEnabledMap that
// fakeMembershipStore.IsProjectSyncEnabled already reads, so a pause issued
// through the new admin HTTP route is visible to the EXISTING mutation-push
// enforcement gate on the identical store instance — a true wiring test, not a
// re-test of enforcement (already covered by mutations_test.go /
// cloudserver_test.go). It also records InsertAuditEntry calls for assertions.
type fakeProjectSyncAdminStore struct {
	*fakeTeamsStore
	pausedReason map[string]string
	updatedBy    map[string]string
	auditCalls   []cloudstore.AuditEntry
}

func newFakeProjectSyncAdminStore() *fakeProjectSyncAdminStore {
	return &fakeProjectSyncAdminStore{
		fakeTeamsStore: newFakeTeamsStore(),
		pausedReason:   map[string]string{},
		updatedBy:      map[string]string{},
	}
}

func (s *fakeProjectSyncAdminStore) SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error {
	if s.syncEnabledMap == nil {
		s.syncEnabledMap = map[string]bool{}
	}
	s.syncEnabledMap[project] = enabled
	s.updatedBy[project] = updatedBy
	if enabled {
		delete(s.pausedReason, project)
	} else {
		s.pausedReason[project] = reason
	}
	return nil
}

func (s *fakeProjectSyncAdminStore) GetProjectSyncControl(project string) (*cloudstore.ProjectSyncControl, error) {
	enabled, ok := s.syncEnabledMap[project]
	if !ok {
		return nil, nil // absent row: default enabled, per do_not_touch semantics
	}
	ctrl := &cloudstore.ProjectSyncControl{Project: project, SyncEnabled: enabled}
	if r, ok := s.pausedReason[project]; ok && r != "" {
		rr := r
		ctrl.PausedReason = &rr
	}
	if u, ok := s.updatedBy[project]; ok && u != "" {
		uu := u
		ctrl.UpdatedBy = &uu
	}
	return ctrl, nil
}

// ListProjectSyncControlsMap is the fake's batched equivalent of calling
// GetProjectSyncControl per project: it builds the SAME map shape from the
// SAME syncEnabledMap/pausedReason/updatedBy state, in one pass.
func (s *fakeProjectSyncAdminStore) ListProjectSyncControlsMap(context.Context) (map[string]cloudstore.ProjectSyncControl, error) {
	out := make(map[string]cloudstore.ProjectSyncControl, len(s.syncEnabledMap))
	for project, enabled := range s.syncEnabledMap {
		ctrl := cloudstore.ProjectSyncControl{Project: project, SyncEnabled: enabled}
		if r, ok := s.pausedReason[project]; ok && r != "" {
			rr := r
			ctrl.PausedReason = &rr
		}
		if u, ok := s.updatedBy[project]; ok && u != "" {
			uu := u
			ctrl.UpdatedBy = &uu
		}
		out[project] = ctrl
	}
	return out, nil
}

func (s *fakeProjectSyncAdminStore) InsertAuditEntry(_ context.Context, entry cloudstore.AuditEntry) error {
	s.auditCalls = append(s.auditCalls, entry)
	return nil
}

func newProjectSyncAdminTestServer(t *testing.T) (*CloudServer, *fakeProjectSyncAdminStore, *cloudauth.Service) {
	t.Helper()
	store := newFakeProjectSyncAdminStore()
	authSvc, err := cloudauth.NewService(&cloudstore.CloudStore{}, strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	authSvc.SetBearerToken("sync-token")
	authSvc.SetDashboardSessionTokens([]string{"admin-token"})
	srv := New(store, authSvc, 0, WithDashboardAdminToken("admin-token"))
	return srv, store, authSvc
}

// ─── Route + gating tests ─────────────────────────────────────────────────────

// TestAdminPauseResumeRoutesOperatorOnly verifies POST /admin/projects/{p}/pause
// and /resume are gated exactly like the rest of the Admin section: an operator
// (dashboard cookie) succeeds, a non-operator account is 403'd, and the store is
// left untouched by the rejected attempt (non-admin cannot pause).
func TestAdminPauseResumeRoutesOperatorOnly(t *testing.T) {
	srv, store, authSvc := newProjectSyncAdminTestServer(t)

	// Non-operator account → 403, store untouched.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-x/pause", accountCookie(t, authSvc, "9", "eve"), `{"paused_reason":"abuse"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator pause: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, err := store.IsProjectSyncEnabled(context.Background(), "proj-x"); err != nil || !enabled {
		t.Fatalf("non-operator pause must not change sync state: enabled=%v err=%v", enabled, err)
	}
	if len(store.auditCalls) != 0 {
		t.Fatalf("non-operator pause must not audit, got %d calls", len(store.auditCalls))
	}

	// Operator → 200, project now paused.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-x/pause", operatorCookie(t, authSvc), `{"paused_reason":"runaway agent"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator pause: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, err := store.IsProjectSyncEnabled(context.Background(), "proj-x"); err != nil || enabled {
		t.Fatalf("operator pause must disable sync: enabled=%v err=%v", enabled, err)
	}

	// Operator resume → 200, project enabled again.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-x/resume", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator resume: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, err := store.IsProjectSyncEnabled(context.Background(), "proj-x"); err != nil || !enabled {
		t.Fatalf("operator resume must re-enable sync: enabled=%v err=%v", enabled, err)
	}

	// Non-operator resume → 403 too.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-x/resume", accountCookie(t, authSvc, "9", "eve"), ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator resume: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminPauseResumeRoutesViaAdminBearer verifies the CLI path: the admin
// credential presented as a raw Bearer token (no dashboard session cookie at
// all) authorizes pause/resume via the requireAdminBearer fallback inside
// requireOperator, exactly as `omnia cloud project pause|resume` uses it.
func TestAdminPauseResumeRoutesViaAdminBearer(t *testing.T) {
	srv, store, _ := newProjectSyncAdminTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/projects/proj-cli/pause", strings.NewReader(`{"paused_reason":"cli pause"}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin-bearer pause: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, _ := store.IsProjectSyncEnabled(context.Background(), "proj-cli"); enabled {
		t.Fatal("admin-bearer pause must disable sync")
	}

	// Wrong bearer (the plain sync token) must NOT authorize an operator action.
	req = httptest.NewRequest(http.MethodPost, "/admin/projects/proj-cli/resume", nil)
	req.Header.Set("Authorization", "Bearer sync-token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sync-token resume: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, _ := store.IsProjectSyncEnabled(context.Background(), "proj-cli"); enabled {
		t.Fatal("rejected resume attempt must not change sync state")
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/projects/proj-cli/resume", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin-bearer resume: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if enabled, _ := store.IsProjectSyncEnabled(context.Background(), "proj-cli"); !enabled {
		t.Fatal("admin-bearer resume must re-enable sync")
	}
}

// TestAdminPauseResumeAuditsOperatorAction verifies pause/resume each emit a
// distinct audit entry with the actor, project, reason and Action/Outcome
// constants — separate from the REQ-404/405 audit trail already emitted when a
// push is REJECTED for a paused project (mutations_test.go /
// cloudserver_test.go own that path).
func TestAdminPauseResumeAuditsOperatorAction(t *testing.T) {
	srv, store, authSvc := newProjectSyncAdminTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-y/pause", operatorCookie(t, authSvc), `{"paused_reason":"abuse"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("pause: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/proj-y/resume", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	if len(store.auditCalls) != 2 {
		t.Fatalf("expected 2 audit calls (pause + resume), got %d: %+v", len(store.auditCalls), store.auditCalls)
	}
	pause := store.auditCalls[0]
	if pause.Action != cloudstore.AuditActionProjectPause || pause.Outcome != cloudstore.AuditOutcomeProjectPaused {
		t.Fatalf("pause audit: unexpected action/outcome %+v", pause)
	}
	if pause.Project != "proj-y" || pause.Contributor == "" {
		t.Fatalf("pause audit: expected project=proj-y and a non-empty actor, got %+v", pause)
	}
	if pause.Metadata == nil || pause.Metadata["reason"] != "abuse" {
		t.Fatalf("pause audit: expected reason metadata, got %+v", pause.Metadata)
	}

	resume := store.auditCalls[1]
	if resume.Action != cloudstore.AuditActionProjectResume || resume.Outcome != cloudstore.AuditOutcomeProjectResumed {
		t.Fatalf("resume audit: unexpected action/outcome %+v", resume)
	}
	if resume.Project != "proj-y" {
		t.Fatalf("resume audit: expected project=proj-y, got %+v", resume)
	}
}

// TestAdminPauseThenPushRejectedResumeThenAccepted is the full acceptance-criteria
// round trip: an operator pauses a project via the new admin route, a subsequent
// mutation push is rejected by the ALREADY-WIRED enforcement (409 sync-paused),
// resume flips it back, and the push then succeeds. Proves the control side
// (this obligation) actually drives the enforcement side (already shipped).
func TestAdminPauseThenPushRejectedResumeThenAccepted(t *testing.T) {
	srv, store, authSvc := newProjectSyncAdminTestServer(t)
	const project = "proj-e2e"

	// Seed an account with PermInsert on the project so the mutation push itself
	// is authorized — pause/resume is the only variable under test here.
	store.grant("42", project, int(cloudauth.PermInsert), cloudauth.RoleMember)
	acctToken, err := authSvc.MintAccountToken("42", "bob")
	if err != nil {
		t.Fatalf("mint account token: %v", err)
	}

	push := func() int {
		t.Helper()
		body := marshalPushRequest(t, makeMutationEntries(1, project))
		req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
		req.Header.Set("Authorization", "Bearer "+acctToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// Before any pause: push succeeds.
	if code := push(); code != http.StatusOK {
		t.Fatalf("push before pause: expected 200, got %d", code)
	}

	// Operator pauses the project via the new admin route.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/"+project+"/pause", operatorCookie(t, authSvc), `{"paused_reason":"incident"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("pause: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Push is now rejected by the EXISTING enforcement (409 sync-paused).
	body := marshalPushRequest(t, makeMutationEntries(1, project))
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer "+acctToken)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("push while paused: expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync-paused") {
		t.Fatalf("push while paused: expected sync-paused in body, got %q", rec.Body.String())
	}

	// Operator resumes the project.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodPost, "/admin/projects/"+project+"/resume", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Push succeeds again.
	if code := push(); code != http.StatusOK {
		t.Fatalf("push after resume: expected 200, got %d", code)
	}
}

// ─── Admin Projects page surfacing (task 3) ───────────────────────────────────

// TestAdminProjectsPageShowsPauseState verifies the Projects admin page renders
// the OBL-04 control state per project: "sync enabled" + a Pause control for an
// untouched project, and "paused" + its reason + a Resume control once paused.
func TestAdminProjectsPageShowsPauseState(t *testing.T) {
	srv, store, authSvc := newProjectSyncAdminTestServer(t)
	// KnownProjects needs at least a membership/team/meta row to surface the project.
	store.grant("1", "proj-z", int(cloudauth.PermRead), cloudauth.RoleMember)

	// Before any pause: enabled badge + a Pause control targeting the right URL.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/projects: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Command Center v2, Slice 4b: the enabled/paused badge text moved from
	// "sync enabled"/"paused" to a compact "Sync"/"⏸ Paused" card footer badge,
	// and the pause form moved from an inline row into the "•••" kebab menu
	// (still present in the HTML, just behind a hidden .admin-menu wrapper).
	for _, want := range []string{"proj-z", ">Sync<", "/admin/projects/proj-z/pause", "paused_reason"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Projects page (pre-pause) missing %q, body=%q", want, body)
		}
	}
	if strings.Contains(body, "/admin/projects/proj-z/resume") {
		t.Fatalf("Projects page (pre-pause) must not show a Resume control yet, body=%q", body)
	}

	// Pause it, then re-render.
	if err := store.SetProjectSyncEnabled("proj-z", false, "operator", "spam"); err != nil {
		t.Fatalf("seed pause: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/projects (paused): expected 200, got %d", rec.Code)
	}
	// i18n Slice 3: Spanish default ("Paused" → "Pausado").
	body = rec.Body.String()
	for _, want := range []string{"proj-z", "Pausado", "spam", "/admin/projects/proj-z/resume"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Projects page (paused) missing %q, body=%q", want, body)
		}
	}
}
