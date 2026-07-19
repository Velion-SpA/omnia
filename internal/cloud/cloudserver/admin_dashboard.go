package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/velion/omnia/internal/ui/i18n"
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
	// OBL-16: account-level admin flag administration.
	IsUserAdmin(ctx context.Context, accountID string) (bool, error)
	SetUserAdmin(ctx context.Context, accountID string, admin bool) error
	CountAdmins(ctx context.Context) (int, error)
	// Security fix (Slice 1 review): DemoteUserAdminGuarded is the atomic,
	// race-safe demote entry point (see cloudstore.lockAdminIDsForUpdate).
	// Unlike SetUserAdmin (still used directly for promote, which never
	// needs guarding), this folds the last-admin check and the write into
	// one transaction.
	DemoteUserAdminGuarded(ctx context.Context, accountID string) error
	// Command Center v2, Slice 1: operator-facing user CRUD (create, edit,
	// password reset, hard delete). Named with an Admin prefix on the store
	// side (cloudstore.CloudStore) so they never collide with the plain
	// CreateUser used by the self-service Signup path.
	AdminCreateUser(ctx context.Context, username, email, passwordHash string) (string, error)
	AdminUpdateUser(ctx context.Context, accountID, username, email string) error
	AdminSetUserPassword(ctx context.Context, accountID, passwordHash string) error
	AdminHardDeleteUser(ctx context.Context, accountID string) error
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
//
// i18n Slice 3: takes ctx (every caller already has r.Context() on hand) so
// it can run the SAME dashboard.TranslateShellProps pass the shared
// dashboard's own layoutPropsForContext applies — translating the Nav item
// labels (nav.<id> catalog keys), BrandSub and StatusText — which this
// function never went through before (Admin pages build LayoutProps
// directly, not via layoutPropsForContext). The "operator" identity itself
// is translated separately since it isn't part of TranslateShellProps' scope
// (it's Admin-specific hardcoded identity, not the account-session username
// layoutPropsForContext handles).
func (s *CloudServer) adminLayoutProps(ctx context.Context, title, active string) ui.LayoutProps {
	nav := append(dashboard.BaseNavItems(), dashboard.AdminNavItem())
	props := ui.LayoutProps{
		Title:      title,
		BrandTitle: "Omnia",
		BrandSub:   "Unified Knowledge",
		BrandHref:  "/",
		Nav:        nav,
		Active:     active,
		StatusText: "Online",
		User:       ui.T(ctx, "admin.operatorLabel"),
		LogoutURL:  dashboardLogoutPath,
		AssetBase:  "/static",
	}
	dashboard.TranslateShellProps(ctx, &props)
	return props
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
	IsAdmin      bool
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
	Email    string
	Selected bool
}

// adminAccessView backs the Command Center v2, Slice 3 unified Access page:
// ONE merged row per project (see mergeUnifiedAccessRows in
// admin_access_unified.go) instead of the old two disconnected sections
// (a per-project override list + a read-only "from teams" list below it).
type adminAccessView struct {
	Props         ui.LayoutProps
	Users         []adminUserOption
	SelectedID    string
	SelectedName  string
	SelectedEmail string
	Rows          []unifiedAccessRow
	Roles         []string
}

// adminTeamPermRow is one project's team-derived effective perms for the selected
// account: the union across every contributing team, plus which teams grant it and
// whether a per-project override (shown above) supersedes it.
type adminTeamPermRow struct {
	Project    string
	Kind       string
	Summary    string
	Perms      int
	Read       bool
	Insert     bool
	Update     bool
	Delete     bool
	Overridden bool
	Sources    []adminTeamPermSource
}

// adminTeamPermSource names a team (and the member's profile) that contributes to a
// project's team-derived perms.
type adminTeamPermSource struct {
	Team    string
	Profile string
	Summary string
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
	view := adminUsersView{Props: s.adminLayoutProps(r.Context(), "Admin · Users", "admin"), Users: rows}
	if err := adminUsersPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminAccessPage renders GET /admin/access — the operator's unified
// Access page for a selected account (Command Center v2, Slice 3): one merged
// row per project instead of the old override list + read-only team section.
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
	selectedName, selectedEmail := "", ""
	for _, u := range users {
		isSel := u.ID == selected
		if isSel {
			selectedName = u.Username
			selectedEmail = u.Email
		}
		opts = append(opts, adminUserOption{ID: u.ID, Username: u.Username, Email: u.Email, Selected: isSel})
	}

	var rows []unifiedAccessRow
	if selected != "" {
		rows, err = s.buildUnifiedAccessRows(r.Context(), as, selected)
		if err != nil {
			http.Error(w, "could not compute access", http.StatusInternalServerError)
			return
		}
	}

	view := adminAccessView{
		Props:         s.adminLayoutProps(r.Context(), "Admin · Access", "admin"),
		Users:         opts,
		SelectedID:    selected,
		SelectedName:  selectedName,
		SelectedEmail: selectedEmail,
		Rows:          rows,
		Roles:         adminRoleOptions(),
	}

	if err := adminAccessPage(view).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// ─── unified Access row fragments (Command Center v2, Slice 3) ──────────────
//
// Pure view wiring, no store mutation — the same convention as the Slice 2
// hard-delete confirm/cancel round trip (handleAdminUserDeleteConfirm/
// -Cancel): every fragment recomputes buildUnifiedAccessRows (the SAME
// view-model the full page renders) so a row's edit / revoke-confirm state
// can never drift from what GET /admin/access shows. The actual mutations
// they trigger are the PRE-EXISTING, operator-gated PUT/DELETE
// /admin/memberships routes — nothing new is added to the write path.

// loadAccessRow resolves the (account_id, project) path pair, requires the
// operator, and returns the merged row for that pair. It writes the HTTP
// error itself and returns ok=false on any failure (missing store support,
// missing path params, or an unknown project for that account).
func (s *CloudServer) loadAccessRow(w http.ResponseWriter, r *http.Request) (unifiedAccessRow, bool) {
	if !s.requireOperator(w, r) {
		return unifiedAccessRow{}, false
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return unifiedAccessRow{}, false
	}
	accountID := strings.TrimSpace(r.PathValue("account_id"))
	project := strings.TrimSpace(r.PathValue("project"))
	if accountID == "" || project == "" {
		http.Error(w, "account_id and project are required", http.StatusBadRequest)
		return unifiedAccessRow{}, false
	}
	rows, err := s.buildUnifiedAccessRows(r.Context(), as, accountID)
	if err != nil {
		http.Error(w, "could not compute access", http.StatusInternalServerError)
		return unifiedAccessRow{}, false
	}
	for _, row := range rows {
		if row.Project == project {
			return row, true
		}
	}
	http.Error(w, "project not found", http.StatusNotFound)
	return unifiedAccessRow{}, false
}

// handleAdminAccessRowView handles GET /admin/access/rows/{account_id}/{project}
// — the default (non-editing) state of one row, also the Cancel target for
// both the edit form and the revoke-confirm dialog below.
func (s *CloudServer) handleAdminAccessRowView(w http.ResponseWriter, r *http.Request) {
	row, ok := s.loadAccessRow(w, r)
	if !ok {
		return
	}
	if err := adminAccessRowView(row).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminAccessRowEdit handles GET .../{project}/edit — swaps the row
// into an inline form (role + R/W/U/D checkboxes) that PUTs to the existing
// /admin/memberships route. Serves all three triggers ("Edit" on an override,
// "Override…" on a team row, "Grant…" on a None row) since they differ only
// in the form's pre-filled defaults, computed from the SAME row.
func (s *CloudServer) handleAdminAccessRowEdit(w http.ResponseWriter, r *http.Request) {
	row, ok := s.loadAccessRow(w, r)
	if !ok {
		return
	}
	if err := adminAccessRowEdit(adminRoleOptions(), row).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminAccessRowRevokeConfirm handles GET .../{project}/revoke — swaps
// the row into the reused ui.ConfirmDialog (Slice 0), wired to the existing
// DELETE /admin/memberships/{account_id}/{project} route.
func (s *CloudServer) handleAdminAccessRowRevokeConfirm(w http.ResponseWriter, r *http.Request) {
	row, ok := s.loadAccessRow(w, r)
	if !ok {
		return
	}
	if err := adminAccessRowRevokeConfirm(row).Render(r.Context(), w); err != nil {
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

// ─── account-level admin promote/demote (OBL-16) ─────────────────────────────

// requireAdminStepUp is the checkpoint for the DEFERRED step-up admin auth
// (OBL-16). The user deferred ("luego") the design where an account admin's
// MUTATIONS additionally require a separate operator token on top of
// username+password. Until that lands this is a no-op that always authorizes.
//
// It is deliberately wired HERE — at the operator MUTATION path, after
// requireOperator — so that landing step-up later is a single-function change with
// no call-site churn: fill in this body (parse a step-up token / recent-auth
// header, verify it, write 401/403 and return false on failure) and every admin
// mutation that already calls it is covered. It is intentionally NOT on the read
// path, so browsing the Admin section never demands a step-up.
func (s *CloudServer) requireAdminStepUp(_ http.ResponseWriter, _ *http.Request) bool {
	// DEFERRED (OBL-16): step-up token verification goes here. No-op for now.
	return true
}

// handleAdminPromoteUser handles POST /admin/users/{id}/promote — grants is_admin
// so the account is treated as operator on its next dashboard request.
func (s *CloudServer) handleAdminPromoteUser(w http.ResponseWriter, r *http.Request) {
	s.setUserAdmin(w, r, true)
}

// handleAdminDemoteUser handles POST /admin/users/{id}/demote — revokes is_admin,
// refusing to demote the LAST remaining admin so the account-level Admin section
// can never be locked out.
func (s *CloudServer) handleAdminDemoteUser(w http.ResponseWriter, r *http.Request) {
	s.setUserAdmin(w, r, false)
}

func (s *CloudServer) setUserAdmin(w http.ResponseWriter, r *http.Request, admin bool) {
	if !s.requireOperator(w, r) {
		return
	}
	// DEFERRED step-up checkpoint (OBL-16): a no-op seam today, the single place
	// where "admin also needs a token" enforcement will hook in later.
	if !s.requireAdminStepUp(w, r) {
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
	if !isNumericID(userID) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "user id must be numeric"})
		return
	}
	// Last-admin guard: demoting the only remaining admin would lock every account
	// admin out of the Admin section, so refuse it. The OMNIA_CLOUD_ADMIN recovery
	// token still grants operator regardless — this only protects the account model
	// from becoming unreachable through the UI.
	//
	// SECURITY FIX (Slice 1 review): the guard used to be a separate
	// IsUserAdmin + CountAdmins read followed by a SEPARATE SetUserAdmin
	// write — a classic check-then-act TOCTOU where two concurrent demotes
	// of DIFFERENT admins could each observe count > 1 before either write
	// committed, leaving zero admins. DemoteUserAdminGuarded folds the check
	// and the write into ONE transaction with row-level locking
	// (lockAdminIDsForUpdate), making it race-safe. Promote never needs
	// guarding (granting admin can't reduce the admin count), so it still
	// calls the plain, unconditional SetUserAdmin.
	var err error
	if admin {
		err = as.SetUserAdmin(r.Context(), userID, true)
	} else {
		err = as.DemoteUserAdminGuarded(r.Context(), userID)
	}
	if err != nil {
		switch {
		case errors.Is(err, cloudstore.ErrLastAdmin):
			jsonResponse(w, http.StatusConflict, map[string]string{"error": "cannot demote the last remaining admin"})
		case errors.Is(err, cloudstore.ErrManagedTokenUserNotFound):
			jsonResponse(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		default:
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not update admin status"})
		}
		return
	}
	status := "demoted"
	action := cloudstore.AuditActionAdminDemote
	outcome := cloudstore.AuditOutcomeAdminDemoted
	if admin {
		status = "promoted"
		action = cloudstore.AuditActionAdminPromote
		outcome = cloudstore.AuditOutcomeAdminPromoted
	}
	// OBL-05: best-effort audit of the operator promote/demote action.
	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: s.operatorActor(r),
		Project:     cloudstore.AuditProjectSentinel,
		Action:      action,
		Outcome:     outcome,
		Metadata:    map[string]any{"user_id": userID},
	})
	s.writeOperatorMutationResult(w, r, http.StatusOK, map[string]string{"status": status, "user_id": userID})
}

// ─── hard-delete confirm/cancel fragments (Command Center v2, Slice 2) ──────
//
// These two handlers are pure view wiring: no store call, no business logic.
// The username round-trips through the query string set by the trigger
// button itself (adminUserDeleteConfirmPath/adminUserDeleteCancelPath), so
// rendering the swap-in-place confirm step never needs a fresh ListUsers
// lookup. They mirror internal/dashboard/detail.templ's
// delete-confirm/delete-cancel round trip, just scoped to the Users row menu.

// handleAdminUserDeleteConfirm handles GET /admin/users/{id}/delete-confirm —
// swaps the row's "Delete…" trigger for the reused ui.ConfirmDialog.
//
// SECURITY FIX (Slice 2 review): this used to trust a `?username=` query
// value verbatim, which any client could spoof independently of `id` — the
// delete POST itself is safely bound to `id` alone, but the CONFIRM PROMPT
// text could show an attacker-chosen name that didn't match the account
// actually being deleted. The username is now always looked up from the
// store by `id` and the query string is ignored entirely.
func (s *CloudServer) handleAdminUserDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || !isNumericID(id) {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	username, ok := s.lookupAdminUsername(r.Context(), as, id)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	u := adminUserRow{ID: id, Username: username}
	if err := adminUserDeleteConfirmFragment(u).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handleAdminUserDeleteCancel handles GET /admin/users/{id}/delete-cancel —
// restores the original "Delete…" trigger, undoing the confirm step above.
// Same store lookup, same reason (see handleAdminUserDeleteConfirm).
func (s *CloudServer) handleAdminUserDeleteCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	as, ok := s.adminStore()
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "admin unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || !isNumericID(id) {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	username, ok := s.lookupAdminUsername(r.Context(), as, id)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	u := adminUserRow{ID: id, Username: username}
	if err := adminUserDeleteTrigger(u).Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// lookupAdminUsername resolves the authoritative username for id straight
// from the store (ListUsers — there is no single-record lookup on
// adminDashboardStore), so the delete-confirm/cancel fragments can never
// display a caller-supplied name that doesn't match the account being
// deleted.
func (s *CloudServer) lookupAdminUsername(ctx context.Context, as adminDashboardStore, id string) (string, bool) {
	users, err := as.ListUsers(ctx)
	if err != nil {
		return "", false
	}
	for _, u := range users {
		if u.ID == id {
			return u.Username, true
		}
	}
	return "", false
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
		IsAdmin:      u.IsAdmin,
		Disabled:     u.Disabled(),
		Created:      formatAdminTime(&u.CreatedAt),
		TokenCount:   u.TokenCount,
		LastTokenUse: formatAdminTime(u.LastTokenUse),
		Tokens:       rows,
	}
}

// permSummary renders a human-readable label for a perms bitfield.
//
// i18n Slice 3: takes lang so its ONE actual render call site
// (handleAdminTeamDetailPage's memberRows loop, admin_teams_dashboard.go —
// rendered as m.Summary in adminTeamMemberRowView) can localize it. Callers
// resolve lang from r.Context() (a Go handler, not a templ file, so ctx
// isn't implicit there).
func permSummary(lang i18n.Lang, perms int) string {
	p := auth.Permission(perms)
	if perms == 0 {
		return i18n.T(lang, "admin.perm.noAccess")
	}
	if p.Has(auth.PermAll) {
		return i18n.T(lang, "admin.perm.full")
	}
	readWord := i18n.T(lang, "admin.perm.read")
	parts := make([]string, 0, 4)
	if p.Has(auth.PermRead) {
		parts = append(parts, readWord)
	}
	if p.Has(auth.PermInsert) {
		parts = append(parts, i18n.T(lang, "admin.perm.write"))
	}
	if p.Has(auth.PermUpdate) {
		parts = append(parts, i18n.T(lang, "admin.perm.update"))
	}
	if p.Has(auth.PermDelete) {
		parts = append(parts, i18n.T(lang, "admin.perm.delete"))
	}
	if len(parts) == 1 && parts[0] == readWord {
		return i18n.T(lang, "admin.perm.readOnly")
	}
	return strings.Join(parts, "+")
}

// adminMembershipDeletePath builds the DELETE URL for a membership, path-escaping
// both segments so project names containing spaces or dots route correctly.
func adminMembershipDeletePath(accountID, project string) string {
	return "/admin/memberships/" + url.PathEscape(accountID) + "/" + url.PathEscape(project)
}

// adminUserCountLabel renders the Users toolbar's "N accounts · M admins"
// summary (Command Center v2, Slice 2). i18n Slice 3: takes lang — called
// from admin_ui.templ (adminUsersPage), so ctx is implicit there.
func adminUserCountLabel(lang i18n.Lang, users []adminUserRow) string {
	admins := 0
	for _, u := range users {
		if u.IsAdmin {
			admins++
		}
	}
	accountsWord := i18n.T(lang, "admin.users.countAccountPlural")
	if len(users) == 1 {
		accountsWord = i18n.T(lang, "admin.users.countAccountSingular")
	}
	adminsWord := i18n.T(lang, "admin.users.countAdminPlural")
	if admins == 1 {
		adminsWord = i18n.T(lang, "admin.users.countAdminSingular")
	}
	return fmt.Sprintf("%d %s · %d %s", len(users), accountsWord, admins, adminsWord)
}

// adminUserDeleteConfirmPath / adminUserDeleteCancelPath build the GET urls for
// the inline hard-delete confirm swap (Slice 2). id is the only input — the
// displayed username is looked up server-side from the store
// (lookupAdminUsername), never trusted from the client, so no username needs
// to round-trip through the URL at all.
func adminUserDeleteConfirmPath(id string) string {
	return "/admin/users/" + id + "/delete-confirm"
}

func adminUserDeleteCancelPath(id string) string {
	return "/admin/users/" + id + "/delete-cancel"
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
// NOT translated (i18n Slice 3): "token"/"tokens" is the same word in both
// supported languages (a standard tech loanword in professional Spanish), so
// there is no copy difference to resolve through the catalog here.
func tokenCountLabel(n int) string {
	if n == 1 {
		return "1 token"
	}
	return fmt.Sprintf("%d tokens", n)
}

// tokenLabel renders a token's label, defaulting to a placeholder for
// unlabeled tokens. i18n Slice 3: takes lang — called from admin_ui.templ
// (adminTokenList), so ctx is implicit there.
func tokenLabel(lang i18n.Lang, label string) string {
	if strings.TrimSpace(label) == "" {
		return i18n.T(lang, "admin.users.noLabel")
	}
	return label
}
