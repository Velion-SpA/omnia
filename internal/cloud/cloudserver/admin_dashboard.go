package cloudserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/dashboard"
	"github.com/velion/omnia/internal/ui"
)

// Operator-only dashboard Admin section (OBL-13). These handlers render the Admin
// Users and Access pages and back their HTMX mutations. Everything here is gated
// on the operator session via requireOperator — the UI is never trusted. The
// pages reuse the shared internal/ui design system (the same shell + CSS the rest
// of the dashboard renders), and the section is cloud-only: the routes register
// only when the store supports operator administration (adminDashboardStore),
// which the local dashboard's data source never does.

// adminDashboardStore is the operator-administration slice of the store. It is
// detected via type assertion on s.store (the same capability-detection pattern as
// membershipManager / managedTokenAdminStore), so the core ChunkStore interface is
// NOT extended. *cloudstore.CloudStore satisfies it.
type adminDashboardStore interface {
	ListUsers(ctx context.Context) ([]cloudstore.AdminUser, error)
	ListMembershipsForUser(ctx context.Context, accountID string) ([]cloudstore.Membership, error)
	UpsertMembership(ctx context.Context, accountID, project string, perms int, role string) error
	DeleteMembership(ctx context.Context, accountID, project string) error
	ListManagedTokensForUser(ctx context.Context, userID string) ([]cloudstore.ManagedTokenView, error)
}

// Compile-time assertion: the concrete store must satisfy the admin seam.
var _ adminDashboardStore = (*cloudstore.CloudStore)(nil)

func (s *CloudServer) adminStore() (adminDashboardStore, bool) {
	as, ok := s.store.(adminDashboardStore)
	return as, ok
}

// requireOperator authorizes an operator-only request. It accepts EITHER the
// operator dashboard SESSION cookie (the UI path — dashboardSessionClaims resolves
// operator==true only for the admin credential, per OBL-03) OR the admin Bearer
// credential (the CLI/API path — requireAdminBearer). An authenticated but
// non-operator account session is rejected with 403 (never sees the Admin nav and
// every /admin/* call it makes is forbidden). On failure it has written the HTTP
// error and returns false.
func (s *CloudServer) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	claims, operator := s.dashboardSessionClaims(r)
	if operator {
		return true
	}
	if claims != nil {
		// A valid account session, but not the operator.
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "operator credential required"})
		return false
	}
	// No dashboard session cookie — fall back to the admin Bearer credential so
	// existing CLI/API callers (OBL-01) keep working with the same status codes.
	return s.requireAdminBearer(w, r)
}

// adminLayoutProps builds the shared shell props for an Admin page: the standard
// dashboard nav plus the operator-only Admin entry, the operator identity and a
// logout action.
func (s *CloudServer) adminLayoutProps(title, active string) ui.LayoutProps {
	nav := append(dashboard.BaseNavItems(), dashboard.AdminNavItem())
	return ui.LayoutProps{
		Title:      title,
		BrandTitle: "Omnia",
		BrandSub:   "Unified Knowledge",
		BrandHref:  "/",
		Nav:        nav,
		Active:     active,
		StatusText: "Online",
		User:       "operator",
		LogoutURL:  dashboardLogoutPath,
		AssetBase:  "/static",
	}
}

// ─── view models ─────────────────────────────────────────────────────────────

type adminTokenRow struct {
	ID       string
	Label    string
	Created  string
	LastUsed string
	Revoked  bool
}

type adminUserRow struct {
	ID           string
	Username     string
	Email        string
	Disabled     bool
	Created      string
	TokenCount   int
	LastTokenUse string
	Tokens       []adminTokenRow
}

type adminUsersView struct {
	Props ui.LayoutProps
	Users []adminUserRow
}

type adminUserOption struct {
	ID       string
	Username string
	Selected bool
}

type adminMembershipRow struct {
	Project   string
	Read      bool
	Insert    bool
	Update    bool
	Delete    bool
	Role      string
	Summary   string
	DeleteURL string // pre-encoded DELETE /admin/memberships/{account_id}/{project}
}

type adminAccessView struct {
	Props        ui.LayoutProps
	Users        []adminUserOption
	SelectedID   string
	SelectedName string
	Memberships  []adminMembershipRow
	Roles        []string
}

type adminTokenIssuedView struct {
	Username string
	Raw      string
	TokenID  string
}

// adminRoleOptions is the role dropdown, highest privilege first. The operator is
// god-mode and may assign any role directly (the per-project anti-escalation rules
// apply to owners/admins, not the server operator).
func adminRoleOptions() []string {
	return []string{auth.RoleOwner, auth.RoleAdmin, auth.RoleModerator, auth.RoleMember}
}

// ─── page handlers ───────────────────────────────────────────────────────────

// handleAdminUsersPage renders GET /admin — the operator Users page.
func (s *CloudServer) handleAdminUsersPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	users, err := as.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not list users", http.StatusInternalServerError)
		return
	}
	rows := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		tokens, terr := as.ListManagedTokensForUser(r.Context(), u.ID)
		if terr != nil {
			tokens = nil
		}
		rows = append(rows, toAdminUserRow(u, tokens))
	}
	view := adminUsersView{Props: s.adminLayoutProps("Admin · Users", "admin"), Users: rows}
	if err := adminUsersPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminAccessPage renders GET /admin/access — the operator Access page for a
// selected user's memberships.
func (s *CloudServer) handleAdminAccessPage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	users, err := as.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "could not list users", http.StatusInternalServerError)
		return
	}
	selected := strings.TrimSpace(r.URL.Query().Get("user"))
	if selected == "" && len(users) > 0 {
		selected = users[0].ID
	}

	opts := make([]adminUserOption, 0, len(users))
	selectedName := ""
	for _, u := range users {
		isSel := u.ID == selected
		if isSel {
			selectedName = u.Username
		}
		opts = append(opts, adminUserOption{ID: u.ID, Username: u.Username, Selected: isSel})
	}

	var memRows []adminMembershipRow
	if selected != "" {
		mems, merr := as.ListMembershipsForUser(r.Context(), selected)
		if merr != nil {
			http.Error(w, "could not list memberships", http.StatusInternalServerError)
			return
		}
		memRows = make([]adminMembershipRow, 0, len(mems))
		for _, m := range mems {
			row := toAdminMembershipRow(m)
			row.DeleteURL = adminMembershipDeletePath(selected, m.Project)
			memRows = append(memRows, row)
		}
	}

	view := adminAccessView{
		Props:        s.adminLayoutProps("Admin · Access", "admin"),
		Users:        opts,
		SelectedID:   selected,
		SelectedName: selectedName,
		Memberships:  memRows,
		Roles:        adminRoleOptions(),
	}
	if err := adminAccessPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// ─── JSON list endpoints (task 1) ────────────────────────────────────────────

type adminUserJSON struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	Email        string  `json:"email"`
	CreatedAt    string  `json:"created_at"`
	DisabledAt   *string `json:"disabled_at,omitempty"`
	TokenCount   int     `json:"token_count"`
	LastTokenUse *string `json:"last_token_use,omitempty"`
}

// handleAdminListUsers handles GET /admin/users — operator-only JSON user list.
func (s *CloudServer) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	users, err := as.ListUsers(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list users"})
		return
	}
	out := make([]adminUserJSON, 0, len(users))
	for _, u := range users {
		out = append(out, adminUserJSON{
			ID:           u.ID,
			Username:     u.Username,
			Email:        u.Email,
			CreatedAt:    u.CreatedAt.UTC().Format(time.RFC3339),
			DisabledAt:   rfc3339Ptr(u.DisabledAt),
			TokenCount:   u.TokenCount,
			LastTokenUse: rfc3339Ptr(u.LastTokenUse),
		})
	}
	jsonResponse(w, http.StatusOK, out)
}

type adminMembershipJSON struct {
	Project string `json:"project"`
	Perms   int    `json:"perms"`
	Role    string `json:"role"`
}

// handleAdminListUserMemberships handles GET /admin/users/{id}/memberships.
func (s *CloudServer) handleAdminListUserMemberships(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	mems, err := as.ListMembershipsForUser(r.Context(), userID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list memberships"})
		return
	}
	out := make([]adminMembershipJSON, 0, len(mems))
	for _, m := range mems {
		out = append(out, adminMembershipJSON{Project: m.Project, Perms: m.Perms, Role: m.Role})
	}
	jsonResponse(w, http.StatusOK, out)
}

// ─── mutation endpoints (task 1 + 5) ─────────────────────────────────────────

// handleAdminUpsertMembership handles PUT /admin/memberships. It grants or updates
// a membership for ANY project as the operator (no per-project ownership needed),
// mirroring the psql seed upsert. Accepts a JSON body {account_id, project, perms,
// role} (CLI/API) OR an HTMX form (Read/Insert/Update/Delete checkboxes + role).
func (s *CloudServer) handleAdminUpsertMembership(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	accountID, project, perms, role, err := s.parseMembershipInput(w, r)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if accountID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "account_id is required"})
		return
	}
	if project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}
	if role == "" {
		role = auth.RoleMember
	}
	if err := as.UpsertMembership(r.Context(), accountID, project, perms, role); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not save membership"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, adminMembershipJSON{Project: project, Perms: perms, Role: role})
}

// handleAdminDeleteMembership handles DELETE /admin/memberships/{account_id}/{project}.
func (s *CloudServer) handleAdminDeleteMembership(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	accountID := strings.TrimSpace(r.PathValue("account_id"))
	project := strings.TrimSpace(r.PathValue("project"))
	if accountID == "" || project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "account_id and project are required"})
		return
	}
	if err := as.DeleteMembership(r.Context(), accountID, project); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke membership"})
		return
	}
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": "revoked", "account_id": accountID, "project": project})
}

// ─── request parsing + response helpers ──────────────────────────────────────

// parseMembershipInput reads a membership mutation from either a JSON body or an
// HTMX form. Perms are masked to the defined bit range so a caller can never grant
// phantom permissions. account_id/project/role are trimmed.
func (s *CloudServer) parseMembershipInput(w http.ResponseWriter, r *http.Request) (accountID, project string, perms int, role string, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if isFormContentType(r) {
		if perr := r.ParseForm(); perr != nil {
			return "", "", 0, "", perr
		}
		accountID = strings.TrimSpace(r.PostFormValue("account_id"))
		project = strings.TrimSpace(r.PostFormValue("project"))
		role = strings.TrimSpace(r.PostFormValue("role"))
		if v := strings.TrimSpace(r.PostFormValue("perms")); v != "" {
			perms, _ = strconv.Atoi(v)
		} else {
			perms = permsFromForm(r)
		}
	} else {
		var body struct {
			AccountID string `json:"account_id"`
			Project   string `json:"project"`
			Perms     int    `json:"perms"`
			Role      string `json:"role"`
		}
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			return "", "", 0, "", derr
		}
		accountID = strings.TrimSpace(body.AccountID)
		project = strings.TrimSpace(body.Project)
		perms = body.Perms
		role = strings.TrimSpace(body.Role)
	}
	perms &= int(auth.PermAll)
	return accountID, project, perms, role, nil
}

// permsFromForm composes the perms bitfield from the Read/Insert/Update/Delete
// checkbox fields sent by the Access page.
func permsFromForm(r *http.Request) int {
	perms := 0
	if formFlag(r, "read") {
		perms |= int(auth.PermRead)
	}
	if formFlag(r, "insert") {
		perms |= int(auth.PermInsert)
	}
	if formFlag(r, "update") {
		perms |= int(auth.PermUpdate)
	}
	if formFlag(r, "delete") {
		perms |= int(auth.PermDelete)
	}
	return perms
}

func formFlag(r *http.Request, key string) bool {
	v := strings.TrimSpace(r.PostFormValue(key))
	return v != "" && v != "0" && v != "false" && v != "off"
}

// isFormContentType reports whether the request body is an HTML form (the HTMX UI
// path). Everything else — including a JSON body with no explicit Content-Type, as
// the CLI/API sends — is parsed as JSON.
func isFormContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.Contains(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "multipart/form-data")
}

// writeOperatorMutationResult renders the result of an operator mutation. For an
// HTMX request it triggers a client-side refresh (so the page re-renders with the
// authoritative server state); for an API request it returns JSON.
func (s *CloudServer) writeOperatorMutationResult(w http.ResponseWriter, r *http.Request, status int, payload any) {
	if isHTMXRequest(r) {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	jsonResponse(w, status, payload)
}

// ─── view mapping helpers ────────────────────────────────────────────────────

func toAdminUserRow(u cloudstore.AdminUser, tokens []cloudstore.ManagedTokenView) adminUserRow {
	rows := make([]adminTokenRow, 0, len(tokens))
	for _, t := range tokens {
		rows = append(rows, adminTokenRow{
			ID:       t.ID,
			Label:    t.Label,
			Created:  formatAdminTime(&t.CreatedAt),
			LastUsed: formatAdminTime(t.LastUsedAt),
			Revoked:  t.Revoked(),
		})
	}
	return adminUserRow{
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		Disabled:     u.Disabled(),
		Created:      formatAdminTime(&u.CreatedAt),
		TokenCount:   u.TokenCount,
		LastTokenUse: formatAdminTime(u.LastTokenUse),
		Tokens:       rows,
	}
}

func toAdminMembershipRow(m cloudstore.Membership) adminMembershipRow {
	p := auth.Permission(m.Perms)
	return adminMembershipRow{
		Project: m.Project,
		Read:    p.Has(auth.PermRead),
		Insert:  p.Has(auth.PermInsert),
		Update:  p.Has(auth.PermUpdate),
		Delete:  p.Has(auth.PermDelete),
		Role:    m.Role,
		Summary: permSummary(m.Perms),
	}
}

// permSummary renders a human-readable label for a perms bitfield.
func permSummary(perms int) string {
	p := auth.Permission(perms)
	if perms == 0 {
		return "no access"
	}
	if p.Has(auth.PermAll) {
		return "full (read+write+update+delete)"
	}
	parts := make([]string, 0, 4)
	if p.Has(auth.PermRead) {
		parts = append(parts, "read")
	}
	if p.Has(auth.PermInsert) {
		parts = append(parts, "write")
	}
	if p.Has(auth.PermUpdate) {
		parts = append(parts, "update")
	}
	if p.Has(auth.PermDelete) {
		parts = append(parts, "delete")
	}
	if len(parts) == 1 && parts[0] == "read" {
		return "read-only"
	}
	return strings.Join(parts, "+")
}

// adminMembershipDeletePath builds the DELETE URL for a membership, path-escaping
// both segments so project names containing spaces or dots route correctly.
func adminMembershipDeletePath(accountID, project string) string {
	return "/admin/memberships/" + url.PathEscape(accountID) + "/" + url.PathEscape(project)
}

func formatAdminTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// tokenCountLabel renders the token-count summary shown in the Users table.
func tokenCountLabel(n int) string {
	if n == 1 {
		return "1 token"
	}
	return fmt.Sprintf("%d tokens", n)
}

// tokenLabel renders a token's label, defaulting to a placeholder for unlabeled
// tokens.
func tokenLabel(label string) string {
	if strings.TrimSpace(label) == "" {
		return "(no label)"
	}
	return label
}
