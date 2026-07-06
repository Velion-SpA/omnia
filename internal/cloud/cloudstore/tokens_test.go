package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/cloud"
)

// newTokenTestStore spins up a CloudStore against an isolated, throwaway Postgres
// schema. It skips when CLOUDSTORE_TEST_DSN is unset (no live Postgres).
func newTokenTestStore(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}
	schema := fmt.Sprintf("cloudstore_tokens_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		adminDB.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		adminDB.Close()
	})

	testDSN := dsn + "?search_path=" + schema
	if strings.Contains(dsn, "?") {
		testDSN = dsn + "&search_path=" + schema
	}
	cs, err := New(cloud.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("New (migrate): %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestManagedTokenLifecycleIntegration(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	mt, err := cs.IssueManagedToken(ctx, user.ID, "hash-abc", "laptop", AuditEntry{Contributor: "operator"})
	if err != nil {
		t.Fatalf("issue managed token: %v", err)
	}
	if mt.UserID != user.ID || mt.ID == "" {
		t.Fatalf("unexpected token: %+v", mt)
	}

	// Resolve returns the joined validation view.
	res, err := cs.ResolveManagedToken(ctx, "hash-abc")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res == nil || res.UserID != user.ID || res.Username != "alice" || res.Revoked || res.UserDisabled {
		t.Fatalf("unexpected resolution: %+v", res)
	}

	// Unknown hash resolves to nil.
	if got, err := cs.ResolveManagedToken(ctx, "does-not-exist"); err != nil || got != nil {
		t.Fatalf("unknown hash must resolve nil, got %+v err=%v", got, err)
	}

	// Issuance wrote an atomic audit row.
	rows, total, err := cs.ListAuditEntriesPaginated(ctx, AuditFilter{Outcome: AuditOutcomeTokenIssued}, 10, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if total < 1 || len(rows) < 1 || rows[0].Action != AuditActionTokenIssue {
		t.Fatalf("expected an issued-token audit row, got total=%d rows=%+v", total, rows)
	}

	// Revocation flips the runtime signal.
	if err := cs.RevokeManagedToken(ctx, mt.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	res, err = cs.ResolveManagedToken(ctx, "hash-abc")
	if err != nil || res == nil || !res.Revoked {
		t.Fatalf("expected revoked resolution, got %+v err=%v", res, err)
	}

	// Disabling the user flips the runtime signal (independently of revocation).
	if err := cs.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	res, err = cs.ResolveManagedToken(ctx, "hash-abc")
	if err != nil || res == nil || !res.UserDisabled {
		t.Fatalf("expected disabled-user resolution, got %+v err=%v", res, err)
	}

	// Re-enabling clears it.
	if err := cs.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatalf("enable user: %v", err)
	}
	res, _ = cs.ResolveManagedToken(ctx, "hash-abc")
	if res == nil || res.UserDisabled {
		t.Fatalf("expected user re-enabled, got %+v", res)
	}
}

func TestIssueManagedTokenRejectsDisabledUserAndUnknownUser(t *testing.T) {
	cs := newTokenTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("bob", "bob@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := cs.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := cs.IssueManagedToken(ctx, user.ID, "hash-1", "", AuditEntry{}); !errors.Is(err, ErrManagedTokenUserDisabled) {
		t.Fatalf("disabled user must not receive a token, got %v", err)
	}

	if _, err := cs.IssueManagedToken(ctx, "999999", "hash-2", "", AuditEntry{}); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("unknown user must fail with ErrManagedTokenUserNotFound, got %v", err)
	}
	if err := cs.SetUserDisabled(ctx, "999999", true); !errors.Is(err, ErrManagedTokenUserNotFound) {
		t.Fatalf("disabling unknown user must fail with ErrManagedTokenUserNotFound, got %v", err)
	}
}
