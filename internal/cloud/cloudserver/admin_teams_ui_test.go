package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Velion-SpA/omnia/internal/cloud/auth"
	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
)

// htmlPageRequest is a browser-style navigation: it carries Accept: text/html so
// the content-negotiating admin routes serve the HTML page rather than JSON.
func htmlPageRequest(method, url string, cookie *http.Cookie) *http.Request {
	req := cookieRequest(method, url, cookie, "")
	req.Header.Set("Accept", "text/html")
	return req
}

func profileIDByName(t *testing.T, store *fakeTeamsStore, name string) string {
	t.Helper()
	profiles, _ := store.ListProfiles(context.Background())
	for _, p := range profiles {
		if strings.EqualFold(p.Name, name) {
			return p.ID
		}
	}
	t.Fatalf("profile %q not seeded", name)
	return ""
}

// TestAdminProfilesPageRendersAndGated verifies the Profiles page renders the
// seeded presets with R/I/U/D controls for the operator, is 403 for a non-operator,
// and that the SAME URL still serves JSON to a non-browser (content negotiation).
func TestAdminProfilesPageRendersAndGated(t *testing.T) {
	srv, _, authSvc := newTeamsAdminTestServer(t)

	// Operator, browser navigation → HTML page.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/profiles", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/profiles (html): expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"PROFILES", "Moderator", "NEW PROFILE", `data-perm-bit="1"`, `data-perms-into="perms"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Profiles page missing %q, body=%q", want, body)
		}
	}

	// Non-operator → 403 even with a browser Accept.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/profiles", accountCookie(t, authSvc, "9", "eve")))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-operator GET /admin/profiles: expected 403, got %d", rec.Code)
	}

	// Same URL, JSON caller (no Accept) → JSON array, not HTML. This proves the
	// OBL-14 endpoint is preserved under content negotiation.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cookieRequest(http.MethodGet, "/admin/profiles", operatorCookie(t, authSvc), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/profiles (json): expected 200, got %d", rec.Code)
	}
	var list []profileJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("expected JSON from non-browser GET /admin/profiles, got %q (err %v)", rec.Body.String(), err)
	}
	if len(list) == 0 {
		t.Fatalf("expected seeded profiles in JSON list")
	}
}

// TestAdminTeamsPageAndDetail verifies the Teams list groups by kind and the detail
// page renders the searchable add-project selector, the members section and the
// per-member profile controls.
func TestAdminTeamsPageAndDetail(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	team, _ := store.CreateTeam(context.Background(), "Migración", "work")
	_ = store.AddTeamProject(context.Background(), team.ID, "workly")
	_ = store.AddTeamMember(context.Background(), team.ID, "1", profileIDByName(t, store, "Moderator"))

	// Teams list.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/teams", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/teams (html): expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Work teams", "Personal teams", "Migración", "NEW TEAM", "/admin/teams/" + team.ID} {
		if !strings.Contains(body, want) {
			t.Fatalf("Teams page missing %q, body=%q", want, body)
		}
	}

	// Team detail.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/teams/"+team.ID, operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/teams/%s (html): expected 200, got %d", team.ID, rec.Code)
	}
	body = rec.Body.String()
	for _, want := range []string{
		"PROJECTS", "MEMBERS", "workly", "Add project", "Add member",
		"data-proj-select", // searchable selector present
		"alice",            // member username resolved
		"Moderator",        // profile option
		"/admin/teams/" + team.ID + "/projects/{project}", // add-project URL template
		"/admin/teams/" + team.ID + "/members/{account_id}",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Team detail page missing %q, body=%q", want, body)
		}
	}
}

// TestAdminProjectsPageSplit verifies the Projects page splits known projects into
// Personal / Work / Unclassified with a reclassify control per row.
func TestAdminProjectsPageSplit(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	ctx := context.Background()
	_ = store.UpsertProjectMeta(ctx, "homelab", "personal", "Home Lab")
	_ = store.UpsertProjectMeta(ctx, "workly", "work", "")
	store.grant("1", "unclass", 1, "member") // classified nowhere → Unclassified

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/projects", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/projects (html): expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Personal", "Work", "Unclassified", "homelab", "workly", "unclass", "/admin/projects/homelab/meta"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Projects page missing %q, body=%q", want, body)
		}
	}
}

// TestAdminAccessPageTeamDerived verifies the Access page shows the per-project
// OVERRIDES ABOVE the read-only "from teams" section, marks a project that is
// overridden, and surfaces the contributing team + profile.
func TestAdminAccessPageTeamDerived(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	ctx := context.Background()
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	team, _ := store.CreateTeam(ctx, "Migración", "work")
	_ = store.AddTeamProject(ctx, team.ID, "workly")
	_ = store.AddTeamProject(ctx, team.ID, "trackly")
	_ = store.AddTeamMember(ctx, team.ID, "1", profileIDByName(t, store, "Moderator"))
	_ = store.UpsertProjectMeta(ctx, "workly", "work", "")
	_ = store.UpsertProjectMeta(ctx, "trackly", "work", "")
	// An override on workly (read-only) must win and be flagged in the team view.
	store.grant("1", "workly", int(cloudauth.PermRead), "member")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, "/admin/access?user=1", operatorCookie(t, authSvc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator GET /admin/access (html): expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"PER-PROJECT OVERRIDES", "FROM TEAMS", "Migración", "Moderator", "trackly", "overridden above"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Access page missing %q, body=%q", want, body)
		}
	}
	// Overrides must sit ABOVE the team-derived section.
	if strings.Index(body, "PER-PROJECT OVERRIDES") >= strings.Index(body, "FROM TEAMS") {
		t.Fatalf("overrides must render above the from-teams section")
	}
}

// TestAdminAddTeamMemberViaUIGrantsProfile is the UI end-to-end for task 8: adding a
// member the way admin.js does (JSON {profile_id} to the member endpoint) attaches
// the profile, and the team detail then reports the profile's perms for that member.
func TestAdminAddTeamMemberViaUIGrantsProfile(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	ctx := context.Background()
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	team, _ := store.CreateTeam(ctx, "Migración", "work")
	_ = store.AddTeamProject(ctx, team.ID, "workly")
	moderator := profileIDByName(t, store, "Moderator")

	// admin.js posts a JSON body with HX-Request set; expect an HX-Refresh reload.
	rec := httptest.NewRecorder()
	req := cookieRequest(http.MethodPut, "/admin/teams/"+team.ID+"/members/1", operatorCookie(t, authSvc), `{"profile_id":"`+moderator+`"}`)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add member via UI: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh on htmx member add, got %q", rec.Header().Get("HX-Refresh"))
	}

	// The team detail JSON must now show alice with the Moderator perms (15).
	detRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(detRec, cookieRequest(http.MethodGet, "/admin/teams/"+team.ID, operatorCookie(t, authSvc), ""))
	var detail teamDetailJSON
	if err := json.Unmarshal(detRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode team detail: %v body=%q", err, detRec.Body.String())
	}
	if len(detail.Members) != 1 || detail.Members[0].AccountID != "1" || detail.Members[0].Perms != 15 {
		t.Fatalf("expected alice with Moderator perms(15), got %+v", detail.Members)
	}
}

// TestAdminTeamPagesForbiddenForNonOperator is the acceptance guard: none of the new
// Admin pages render for a non-operator account (403, no nav).
func TestAdminTeamPagesForbiddenForNonOperator(t *testing.T) {
	srv, store, authSvc := newTeamsAdminTestServer(t)
	store.users = []cloudstore.AdminUser{{ID: "1", Username: "alice"}}
	team, _ := store.CreateTeam(context.Background(), "T", "work")

	for _, path := range []string{"/admin/teams", "/admin/teams/" + team.ID, "/admin/profiles", "/admin/projects"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, htmlPageRequest(http.MethodGet, path, accountCookie(t, authSvc, "1", "alice")))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-operator GET %s: expected 403, got %d", path, rec.Code)
		}
	}
}
