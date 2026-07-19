package cloudserver

import (
	"context"
	"fmt"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// AccountProjectAuthorizer is an optional interface detected via type assertion.
// When the injected store satisfies it, per-account RBAC is active.
//
// H1: AuthorizeAccountProject takes the request ctx so the per-request RBAC DB
// call (EffectivePerms + the C1 disabled check) is cancellable instead of running
// on context.Background() — a stuck query is aborted on client disconnect rather
// than pinning a pooled connection.
type AccountProjectAuthorizer interface {
	AuthorizeAccountProject(ctx context.Context, claims *auth.AccountClaims, project string, required auth.Permission) error
}

// rbacAuthorizer implements AccountProjectAuthorizer using the layered
// effective-perms model (OBL-14). It lives in cloudserver (not auth) to avoid
// import cycles, since cloudserver already imports both auth and cloudstore.
type rbacAuthorizer struct {
	authSvc  projectLegacyAuthorizer
	store    membershipStore
	resolver effectivePermsResolver // OBL-14 layered resolver; nil ⇒ membership-only
	disabled accountDisabledChecker // C1 disabled-account gate; nil ⇒ not enforced
}

// accountDisabledChecker is the C1 seam: a single PK-indexed lookup that reports
// whether an account is deactivated. *cloudstore.CloudStore satisfies it; when the
// store does not, the disabled gate is simply not applied (older stores that predate
// the disabled_at column) and deny-by-default via perms still holds.
type accountDisabledChecker interface {
	IsAccountDisabled(ctx context.Context, accountID string) (bool, error)
}

// Compile-time assertion: the real store must satisfy the C1 disabled seam so the
// sync data-plane gate is always active in production.
var _ accountDisabledChecker = (*cloudstore.CloudStore)(nil)

// projectLegacyAuthorizer is the subset of auth.Service used for legacy fallback.
type projectLegacyAuthorizer interface {
	AuthorizeProject(project string) error
}

// membershipStore is the subset of cloudstore.CloudStore needed by rbacAuthorizer.
type membershipStore interface {
	GetMembership(accountID, project string) (*cloudstore.Membership, error)
	ListMembershipsForAccount(accountID string) ([]cloudstore.Membership, error)
}

// effectivePermsResolver is the OBL-14 layered resolver: a per-project override
// (cloud_memberships) takes precedence over the bit_or union of the account's
// team-profile perms, else 0 (deny). *cloudstore.CloudStore satisfies it; a store
// that does not is transparently handled with the membership-only fallback below,
// preserving deny-by-default.
type effectivePermsResolver interface {
	EffectivePerms(ctx context.Context, accountID, project string) (int, error)
}

// AuthorizeAccountProject checks RBAC using the layered effective-perms resolver.
// When claims is nil (legacy shared token), it falls back to the legacy allowlist
// check. Deny-by-default is preserved: 0 effective perms fails Has(required). The
// OBL-08 device-scope AND-gate is applied separately by authorizeProjectOp and is
// untouched here.
func (r *rbacAuthorizer) AuthorizeAccountProject(ctx context.Context, claims *auth.AccountClaims, project string, required auth.Permission) error {
	if claims == nil {
		return r.authSvc.AuthorizeProject(project)
	}
	// C1: a disabled account resolves to deny everywhere, mirroring the managed-token
	// UserDisabled runtime rejection. Checked BEFORE perms (a single PK-indexed
	// lookup) so a disabled account that still holds memberships/teams granting perms
	// gets 0 access on the sync data plane. An error here fails closed (denied).
	if r.disabled != nil {
		isDisabled, err := r.disabled.IsAccountDisabled(ctx, claims.AccountID)
		if err != nil {
			return err
		}
		if isDisabled {
			return auth.ErrPermissionDenied
		}
	}
	perms, err := r.effectivePerms(ctx, claims.AccountID, project)
	if err != nil {
		return err
	}
	if !auth.Permission(perms).Has(required) {
		return auth.ErrPermissionDenied
	}
	return nil
}

// effectivePerms resolves the account's effective perms on the project. It prefers
// the OBL-14 layered resolver (override > team-profile union > 0); when the store
// does not provide one it falls back to the flat per-project membership perms so
// deny-by-default still holds. H1: the request ctx is threaded to the resolver's DB
// call instead of context.Background().
func (r *rbacAuthorizer) effectivePerms(ctx context.Context, accountID, project string) (int, error) {
	if r.resolver != nil {
		return r.resolver.EffectivePerms(ctx, accountID, project)
	}
	m, err := r.store.GetMembership(accountID, project)
	if err != nil {
		return 0, err
	}
	if m == nil {
		return 0, nil
	}
	return m.Perms, nil
}

// enrolledProjectsFromMemberships returns the projects an account has PermRead on.
func enrolledProjectsFromMemberships(memberships []cloudstore.Membership) []string {
	out := make([]string, 0, len(memberships))
	for _, m := range memberships {
		if auth.Permission(m.Perms).Has(auth.PermRead) {
			out = append(out, m.Project)
		}
	}
	return out
}

// denyAllProjects is a fail-closed fallback used when no ProjectAuthorizer
// is configured. Every project authorization attempt is rejected.
type denyAllProjects struct{}

func (denyAllProjects) AuthorizeProject(string) error {
	return fmt.Errorf("no project authorization configured")
}
