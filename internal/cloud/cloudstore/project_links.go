package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Command Center v2, Slice 5a: cloud sub-project linking. The model is
// ASSOCIATE, never MERGE — a linked child stays a real, independent project
// with its own chunks; SetProjectParent only records a parent relationship on
// cloud_project_meta.parent_project (see the migration in cloudstore.go).
// Nothing here moves or deletes chunks, and ClearProjectParent fully restores
// the prior (unlinked) state.
//
// Validation mirrors internal/dashboard/groups.go's 2-level model: no
// self-reference, no parent-of-a-parent, no child-that-is-itself-a-parent.
// The PRIMARY KEY on cloud_project_meta.project means a project can never
// have more than one parent — SetProjectParent on an already-linked child
// simply overwrites the prior parent.

// ErrProjectLinkSelf is returned when a project would be linked to itself.
var ErrProjectLinkSelf = errors.New("cloudstore: a project cannot be its own parent")

// ErrProjectLinkParentIsChild is returned when the requested parent is
// itself already linked under another parent — linking through it would
// create a 3-level chain, which this model does not support.
var ErrProjectLinkParentIsChild = errors.New("cloudstore: parent project is itself a sub-project of another project")

// ErrProjectLinkChildIsParent is returned when the requested child already
// has its own linked sub-projects — it cannot also become a sub-project,
// which would create a 3-level chain from the other direction.
var ErrProjectLinkChildIsParent = errors.New("cloudstore: project already has sub-projects linked to it and cannot become a sub-project itself")

// projectLinkLockKey is a fixed, transaction-scoped advisory-lock key that
// serializes ALL project-parent mutations (SetProjectParent / ClearProjectParent)
// against each other. The read-validate-write sequence in SetProjectParent is a
// classic check-then-act: two concurrent links (e.g. A->B and B->C, or the
// mutual A->B and B->A) could each pass their 2-level checks at read time and
// then both commit, producing a forbidden 3-level chain or a cycle. Unlike
// admin_lastguard.go — whose guarded rows always exist, so it can use
// SELECT ... FOR UPDATE — the child/parent cloud_project_meta rows may not exist
// yet (they are upserted), so row locks would lock nothing. A single advisory
// lock on this key makes every Set/Clear run serially, so each one observes the
// fully committed state of the previous. The value is arbitrary but must stay
// stable across the codebase.
const projectLinkLockKey int64 = 0x0C1D5A11 // mnemonic: clouD Slice-5A Link

// rowQuerier is the read subset shared by *sql.DB and *sql.Tx, so the
// hierarchy-validation helpers below can run against either the pool (never, in
// practice, now) or an open transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SetProjectParent links child under parent. If child has no
// cloud_project_meta row yet, one is upserted (kind defaults to "work",
// matching UpsertProjectMeta's own default) — linking never requires the
// operator to classify a project first. Enforces the strict 2-level
// hierarchy described above; on rejection it returns one of the sentinel
// errors above rather than corrupting the hierarchy or panicking.
func (cs *CloudStore) SetProjectParent(ctx context.Context, child, parent string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	child = strings.TrimSpace(child)
	parent = strings.TrimSpace(parent)
	if child == "" || parent == "" {
		return fmt.Errorf("cloudstore: child and parent are required")
	}
	if child == parent {
		return ErrProjectLinkSelf
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin set project parent tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Serialize link mutations before validating: see projectLinkLockKey.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, projectLinkLockKey); err != nil {
		return fmt.Errorf("cloudstore: lock project links: %w", err)
	}

	parentOfParent, err := projectParentOf(ctx, tx, parent)
	if err != nil {
		return err
	}
	if parentOfParent != "" {
		return ErrProjectLinkParentIsChild
	}

	hasChildren, err := projectHasLinkedChildren(ctx, tx, child)
	if err != nil {
		return err
	}
	if hasChildren {
		return ErrProjectLinkChildIsParent
	}

	const q = `
		INSERT INTO cloud_project_meta (project, kind, parent_project)
		VALUES ($1, 'work', $2)
		ON CONFLICT (project) DO UPDATE SET parent_project = EXCLUDED.parent_project`
	if _, err := tx.ExecContext(ctx, q, child, parent); err != nil {
		return fmt.Errorf("cloudstore: set project parent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit set project parent: %w", err)
	}
	tx = nil
	return nil
}

// ClearProjectParent unlinks child from its parent, if any. Idempotent: a
// project with no meta row, or a meta row with no parent set, is left
// untouched and no error is returned — "already unlinked" is success, not a
// failure condition.
func (cs *CloudStore) ClearProjectParent(ctx context.Context, child string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	child = strings.TrimSpace(child)
	if child == "" {
		return fmt.Errorf("cloudstore: child is required")
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin clear project parent tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Take the same lock as SetProjectParent so a concurrent Set can never read
	// a stale "child has children" state while this Clear removes a link.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, projectLinkLockKey); err != nil {
		return fmt.Errorf("cloudstore: lock project links: %w", err)
	}

	const q = `UPDATE cloud_project_meta SET parent_project = NULL WHERE project = $1`
	if _, err := tx.ExecContext(ctx, q, child); err != nil {
		return fmt.Errorf("cloudstore: clear project parent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit clear project parent: %w", err)
	}
	tx = nil
	return nil
}

// ListProjectParents returns every linked project as child -> parent. Only
// rows with a non-NULL parent_project are included.
func (cs *CloudStore) ListProjectParents(ctx context.Context) (map[string]string, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT project, parent_project FROM cloud_project_meta WHERE parent_project IS NOT NULL`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list project parents: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project parent: %w", err)
		}
		out[child] = parent
	}
	return out, rows.Err()
}

// projectParentOf returns project's own parent ("" when unlinked or when no
// cloud_project_meta row exists yet — an unclassified project is never a
// child). It runs against the provided querier (a *sql.Tx during a guarded
// mutation) so its read participates in the caller's serialized transaction.
func projectParentOf(ctx context.Context, q rowQuerier, project string) (string, error) {
	const sqlText = `SELECT COALESCE(parent_project, '') FROM cloud_project_meta WHERE project = $1`
	var parent string
	err := q.QueryRowContext(ctx, sqlText, project).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cloudstore: get project parent: %w", err)
	}
	return parent, nil
}

// projectHasLinkedChildren reports whether any project is currently linked
// under the given project as its parent. Runs against the provided querier so
// the check shares the caller's transaction (see projectParentOf).
func projectHasLinkedChildren(ctx context.Context, q rowQuerier, project string) (bool, error) {
	const sqlText = `SELECT EXISTS(SELECT 1 FROM cloud_project_meta WHERE parent_project = $1)`
	var exists bool
	if err := q.QueryRowContext(ctx, sqlText, project).Scan(&exists); err != nil {
		return false, fmt.Errorf("cloudstore: check project children: %w", err)
	}
	return exists, nil
}
