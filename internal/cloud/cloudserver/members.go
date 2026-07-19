package cloudserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
	"github.com/velion/omnia/internal/store"
)

// membershipManager is the subset of cloudstore.CloudStore needed to claim a
// project and manage its members. It is detected via type assertion on s.store
// (the same capability-detection pattern used for MutationStore, AccountService,
// and InsertAuditEntry) so the core ChunkStore interface is NOT extended.
type membershipManager interface {
	ListProjectMembers(project string) ([]cloudstore.Membership, error)
	GrantMembership(accountID, project string, perms int, role string) error
	RevokeMembership(accountID, project string) error
	GetMembership(accountID, project string) (*cloudstore.Membership, error)
	// ClaimProjectOwnership atomically grants (accountID, perms, role) as the
	// sole owner of project IFF project has no existing members yet — see
	// cloudstore.ClaimProjectOwnership for the full race-closing contract.
	ClaimProjectOwnership(ctx context.Context, accountID, project string, perms int, role string) (bool, error)
}

// Compile-time assertion: *cloudstore.CloudStore must satisfy membershipManager.
var _ membershipManager = (*cloudstore.CloudStore)(nil)

// memberView is the JSON shape returned for a project member.
type memberView struct {
	AccountID string `json:"account_id"`
	Perms     int    `json:"perms"`
	Role      string `json:"role"`
}

func toMemberView(m cloudstore.Membership) memberView {
	return memberView{AccountID: m.AccountID, Perms: m.Perms, Role: m.Role}
}

// memberManager returns the store typed as a membershipManager, or (nil,false)
// when the injected store does not support member management.
func (s *CloudServer) memberManager() (membershipManager, bool) {
	mm, ok := s.store.(membershipManager)
	return mm, ok
}

// claimOrphanProject makes the first account to touch a brand-new project its
// owner. If an account is present in the request context AND the project has
// no members yet, the account is atomically granted PermAll + RoleOwner.
//
// It is called from the chunk-push and mutation-push handlers BEFORE the
// per-operation authorization check, so the first push to a fresh project
// succeeds (the pusher becomes owner and therefore has PermInsert).
//
// The claim itself is atomic and serialized (cloudstore.ClaimProjectOwnership
// wraps the check-then-act in one transaction behind a per-project
// pg_advisory_xact_lock), so at most ONE account can ever win the race for a
// given fresh project name — even when several different accounts push it
// concurrently for the first time. The loser is left with NO membership at
// all (not a reduced one), so its own subsequent push is correctly denied by
// the per-operation authorization check that runs right after this returns.
//
// This closes a CRITICAL cross-tenant race: before this fix,
// ListProjectMembers-then-GrantMembership was check-then-act with no lock
// between the read and the write, and cloud_memberships' unique key is
// (account_id, project) — a COMPOSITE key — so nothing stopped two DIFFERENT
// accounts from each becoming full owner of the SAME project when they raced
// its first push. The claim contract stays best-effort from this caller's
// point of view: it returns nil on every non-error path regardless of
// whether THIS account won or lost the race, and lets the downstream
// per-operation authz decide (a loser simply has no perms => denied).
//
// KNOWN GAP (Phase 5): project names are assumed globally unique, and the
// FIRST account to ever push a given project name claims it — permanently
// and exclusively, now that the claim itself is race-free. Cross-account
// same-name NAMESPACING (letting two unrelated tenants each have their own
// independent "marketing" project instead of racing for one shared
// namespace) remains a deferred Phase-5 product decision — this fix closes
// the concurrent-race security hole, it does not change who "should" own a
// given name in a multi-tenant namespace model.
func (s *CloudServer) claimOrphanProject(r *http.Request, project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil
	}
	claims, ok := auth.AccountFromContext(r.Context())
	if !ok || claims == nil {
		// Legacy shared token (no account) never claims ownership.
		return nil
	}
	mm, ok := s.memberManager()
	if !ok {
		// Store does not support membership management — nothing to claim.
		return nil
	}
	_, err := mm.ClaimProjectOwnership(r.Context(), claims.AccountID, project, int(auth.PermAll), auth.RoleOwner)
	return err
}

// ─── Member-management endpoints ──────────────────────────────────────────────

// callerRole resolves the role the authenticated account holds on the project.
// Returns ("", nil) when the caller has no membership. Management endpoints
// require an account; callers with a nil membership are not authorized to
// manage.
func (s *CloudServer) callerRole(mm membershipManager, accountID, project string) (string, error) {
	m, err := mm.GetMembership(accountID, project)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", nil
	}
	return m.Role, nil
}

// requireMemberManagement is the common gate for all management endpoints. It
// resolves the project, ensures member management is supported, and requires an
// authenticated account (claims != nil). The legacy shared token has no
// per-project role, so it is rejected with 403. Returns the manager, the
// caller's claims, and the normalized project on success; otherwise it has
// already written the HTTP error and returns ok == false.
func (s *CloudServer) requireMemberManagement(w http.ResponseWriter, r *http.Request) (membershipManager, *auth.AccountClaims, string, bool) {
	project := strings.TrimSpace(r.PathValue("project"))
	if project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return nil, nil, "", false
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return nil, nil, "", false
	}
	mm, ok := s.memberManager()
	if !ok {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "member management unavailable"})
		return nil, nil, "", false
	}
	claims, _ := auth.AccountFromContext(r.Context())
	if claims == nil {
		// Legacy shared token has no per-project role — management is account-only.
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "account authentication required for member management"})
		return nil, nil, "", false
	}
	return mm, claims, project, true
}

// handleListMembers handles GET /projects/{project}/members.
// The caller must have a membership on the project (any perms). Returns the
// member list as [{account_id, perms, role}]. 403 when the caller is not a
// member.
func (s *CloudServer) handleListMembers(w http.ResponseWriter, r *http.Request) {
	mm, claims, project, ok := s.requireMemberManagement(w, r)
	if !ok {
		return
	}
	// Any membership on the project authorizes a read of the member list.
	caller, err := mm.GetMembership(claims.AccountID, project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve caller membership"})
		return
	}
	if caller == nil {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "not a member of this project"})
		return
	}
	members, err := mm.ListProjectMembers(project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list members"})
		return
	}
	out := make([]memberView, 0, len(members))
	for _, m := range members {
		out = append(out, toMemberView(m))
	}
	jsonResponse(w, http.StatusOK, out)
}

type addMemberRequest struct {
	AccountID string `json:"account_id"`
	Perms     int    `json:"perms"`
	Role      string `json:"role"`
}

// handleAddMember handles POST /projects/{project}/members.
// The caller's role must satisfy canManageMembers AND canActorAssign(callerRole,
// role). If the target already exists, the caller must also be allowed to modify
// the target's CURRENT role (so an admin cannot overwrite another admin/owner).
// 404 when the project is orphan (no members). 403 on any rule violation.
func (s *CloudServer) handleAddMember(w http.ResponseWriter, r *http.Request) {
	mm, claims, project, ok := s.requireMemberManagement(w, r)
	if !ok {
		return
	}
	var req addMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.AccountID = strings.TrimSpace(req.AccountID)
	req.Role = strings.TrimSpace(req.Role)
	if req.AccountID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "account_id is required"})
		return
	}
	if req.Role == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "role is required"})
		return
	}

	// The project must already be claimed (have at least one member). A truly
	// orphan project is claimed by pushing, not by adding members to it.
	members, err := mm.ListProjectMembers(project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list members"})
		return
	}
	if len(members) == 0 {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	callerRole, err := s.callerRole(mm, claims.AccountID, project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve caller role"})
		return
	}
	// Coarse gate: caller must be allowed to manage members at all.
	if !auth.CanManageMembers(callerRole) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "insufficient role to manage members"})
		return
	}
	// Anti-escalation: caller may not assign a role >= their own (owner→admin excepted).
	if !auth.CanActorAssign(callerRole, req.Role) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "cannot assign this role"})
		return
	}
	// If the target already exists, the caller must be allowed to modify the
	// target's CURRENT role. This blocks e.g. an admin overwriting another admin
	// or the owner via the add endpoint.
	if existing := findMember(members, req.AccountID); existing != nil {
		if !auth.CanActorModifyTarget(callerRole, existing.Role) {
			jsonResponse(w, http.StatusForbidden, map[string]string{"error": "cannot modify this member"})
			return
		}
	}

	// Mask out any bits that exceed the defined permission set so callers cannot
	// grant phantom permissions by sending an out-of-range value.
	perms := req.Perms & int(auth.PermAll)

	status := http.StatusCreated
	if findMember(members, req.AccountID) != nil {
		status = http.StatusOK
	}
	if err := mm.GrantMembership(req.AccountID, project, perms, req.Role); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not grant membership"})
		return
	}
	// OBL-05: best-effort audit of the grant. Never blocks the response.
	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: claims.AccountID,
		Project:     project,
		Action:      cloudstore.AuditActionMembershipGrant,
		Outcome:     cloudstore.AuditOutcomeMembershipGranted,
		Metadata:    map[string]any{"target_account_id": req.AccountID, "role": req.Role, "perms": perms},
	})
	jsonResponse(w, status, memberView{AccountID: req.AccountID, Perms: perms, Role: req.Role})
}

// handleRemoveMember handles DELETE /projects/{project}/members/{account_id}.
// The caller must satisfy canManageMembers AND canActorModifyTarget(callerRole,
// targetRole). An owner can never be removed via this endpoint. 204 on success.
func (s *CloudServer) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	mm, claims, project, ok := s.requireMemberManagement(w, r)
	if !ok {
		return
	}
	targetID := strings.TrimSpace(r.PathValue("account_id"))
	if targetID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "account_id is required"})
		return
	}

	callerRole, err := s.callerRole(mm, claims.AccountID, project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve caller role"})
		return
	}
	if !auth.CanManageMembers(callerRole) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "insufficient role to manage members"})
		return
	}

	target, err := mm.GetMembership(targetID, project)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not resolve target membership"})
		return
	}
	if target == nil {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "member not found"})
		return
	}
	// Anti-escalation + owner protection: caller may only remove a target whose
	// role is strictly below the caller's, and never an owner.
	if !auth.CanActorModifyTarget(callerRole, target.Role) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "cannot remove this member"})
		return
	}
	if err := mm.RevokeMembership(targetID, project); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke membership"})
		return
	}
	// OBL-05: best-effort audit of the revoke. Never blocks the response.
	s.emitAudit(r, cloudstore.AuditEntry{
		Contributor: claims.AccountID,
		Project:     project,
		Action:      cloudstore.AuditActionMembershipRevoke,
		Outcome:     cloudstore.AuditOutcomeMembershipRevoked,
		Metadata:    map[string]any{"target_account_id": targetID},
	})
	w.WriteHeader(http.StatusNoContent)
}

func findMember(members []cloudstore.Membership, accountID string) *cloudstore.Membership {
	for i := range members {
		if members[i].AccountID == accountID {
			return &members[i]
		}
	}
	return nil
}
