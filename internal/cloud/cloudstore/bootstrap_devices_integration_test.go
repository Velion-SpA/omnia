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

// newBDTestStore spins a CloudStore against an isolated throwaway Postgres schema.
func newBDTestStore(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}
	schema := fmt.Sprintf("cloudstore_bd_%d", time.Now().UnixNano())
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

// TestHasAnyUserAndCreateUserConflict covers the OBL-02 store contract: HasAnyUser
// flips once an account exists, and a duplicate-username CreateUser fails cleanly
// with ErrUserExists WITHOUT mutating the existing row's email.
func TestHasAnyUserAndCreateUserConflict(t *testing.T) {
	cs := newBDTestStore(t)
	ctx := context.Background()

	has, err := cs.HasAnyUser(ctx)
	if err != nil {
		t.Fatalf("HasAnyUser: %v", err)
	}
	if has {
		t.Fatal("fresh store must report no users")
	}

	if _, err := cs.CreateUser("root", "root@first.example", "hash1"); err != nil {
		t.Fatalf("create first user: %v", err)
	}

	has, err = cs.HasAnyUser(ctx)
	if err != nil {
		t.Fatalf("HasAnyUser after create: %v", err)
	}
	if !has {
		t.Fatal("HasAnyUser must be true after an account exists")
	}

	// Duplicate username must fail with ErrUserExists and NOT overwrite the email.
	_, err = cs.CreateUser("root", "attacker@evil.example", "hash2")
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists on duplicate username, got %v", err)
	}
	got, err := cs.GetUserByUsername("root")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got == nil || got.Email != "root@first.example" {
		t.Fatalf("existing row must be unchanged; got %+v", got)
	}
}

// TestTouchDeviceAdvancesLastSeen covers OBL-08: TouchDevice stamps last_seen_at
// and the value is surfaced through the device read paths.
func TestTouchDeviceAdvancesLastSeen(t *testing.T) {
	cs := newBDTestStore(t)
	ctx := context.Background()

	user, err := cs.CreateUser("alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	dev, err := cs.GetOrCreateDevice(user.ID, "notebook-a")
	if err != nil {
		t.Fatalf("GetOrCreateDevice: %v", err)
	}
	if dev.LastSeenAt != nil {
		t.Fatalf("new device must have nil last_seen_at, got %v", dev.LastSeenAt)
	}

	if err := cs.TouchDevice(ctx, dev.ID); err != nil {
		t.Fatalf("TouchDevice: %v", err)
	}

	got, err := cs.GetDevice(dev.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got == nil || got.LastSeenAt == nil {
		t.Fatalf("expected last_seen_at set after TouchDevice, got %+v", got)
	}

	list, err := cs.ListDevicesForAccount(user.ID)
	if err != nil {
		t.Fatalf("ListDevicesForAccount: %v", err)
	}
	if len(list) != 1 || list[0].LastSeenAt == nil {
		t.Fatalf("expected listed device with last_seen_at, got %+v", list)
	}
}
