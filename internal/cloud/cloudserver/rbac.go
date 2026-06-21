package cloudserver

import (
	"fmt"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// AccountProjectAuthorizer is an optional interface detected via type assertion.
// When the injected store satisfies it, per-account RBAC is active.
type AccountProjectAuthorizer interface {
	AuthorizeAccountProject(claims *auth.AccountClaims, project string, required auth.Permission) error
}

// rbacAuthorizer implements AccountProjectAuthorizer using membership records.
// It lives in cloudserver (not auth) to avoid import cycles, since cloudserver
// already imports both auth and cloudstore.
type rbacAuthorizer struct {
	authSvc projectLegacyAuthorizer
	store   membershipStore
}

// projectLegacyAuthorizer is the subset of auth.Service used for legacy fallback.
type projectLegacyAuthorizer interface {
	AuthorizeProject(project string) error
}

// membershipStore is the subset of cloudstore.CloudStore needed by rbacAuthorizer.
type membershipStore interface {
	GetMembership(accountID, project string) (*cloudstore.Membership, error)
	ListMembershipsForAccount(accountID string) ([]cloudstore.Membership, error)
}

// AuthorizeAccountProject checks RBAC membership. When claims is nil (legacy
// shared token), it falls back to the legacy allowlist check.
func (r *rbacAuthorizer) AuthorizeAccountProject(claims *auth.AccountClaims, project string, required auth.Permission) error {
	if claims == nil {
		return r.authSvc.AuthorizeProject(project)
	}
	m, err := r.store.GetMembership(claims.AccountID, project)
	if err != nil {
		return err
	}
	if m == nil || !auth.Permission(m.Perms).Has(required) {
		return auth.ErrPermissionDenied
	}
	return nil
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
