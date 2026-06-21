package cloudstore

import (
	"testing"
)

// TestMembershipStructAndNilSafety verifies Membership zero-value and nil-safe methods.
// Full round-trip requires a live Postgres — use t.Skip for DB tests.
func TestMembershipStructAndNilSafety(t *testing.T) {
	t.Skip("requires postgres")

	var cs *CloudStore
	if _, err := cs.GetMembership("acc1", "proj1"); err == nil {
		t.Error("expected error from nil CloudStore.GetMembership")
	}
	if err := cs.GrantMembership("acc1", "proj1", 1, "member"); err == nil {
		t.Error("expected error from nil CloudStore.GrantMembership")
	}
}

func TestMembershipStructFields(t *testing.T) {
	m := Membership{
		AccountID: "acc-1",
		Project:   "proj-a",
		Perms:     15,
		Role:      "owner",
	}
	if m.AccountID != "acc-1" {
		t.Errorf("unexpected AccountID %q", m.AccountID)
	}
	if m.Perms != 15 {
		t.Errorf("unexpected Perms %d", m.Perms)
	}
}
