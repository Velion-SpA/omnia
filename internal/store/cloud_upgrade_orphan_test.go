package store

import (
	"strings"
	"testing"
)

// A pending upsert whose target observation no longer exists locally is
// unsatisfiable by definition: there is nothing to replicate and no payload to
// repair from. Today it is classed manual-action-required, which gates cloud
// sync for the ENTIRE project on a row no human can act on — the CLI points at
// `cloud upgrade repair --apply`, and that command declines to touch it.
//
// On the reporting install a single such orphan (obs-e6f0056a884b4f48, seq
// 10274) blocked roughly 1900 memories from ever reaching the cloud.
//
// Dropping it is deterministic and lossless: the local store is the source of
// truth, and it holds no such observation.
func TestRepairAcksUpsertMutationWhoseObservationIsGone(t *testing.T) {
	s := newTestStore(t)
	const project = "proj-orphan"

	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.CreateSession("sess-orphan", project, "/tmp/proj-orphan"); err != nil {
		t.Fatalf("session: %v", err)
	}

	// A legacy upsert for an observation that is not in the store, with a
	// payload missing the required content field — the exact shape observed.
	const orphanKey = "obs-does-not-exist"
	if _, err := s.db.Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, occurred_at)
		VALUES (?, 'observation', ?, 'upsert', ?, 'local', ?, datetime('now'))`,
		DefaultSyncTargetKey, orphanKey,
		`{"sync_id":"`+orphanKey+`","session_id":"sess-orphan","type":"decision","title":"orphan","scope":"project"}`,
		project,
	); err != nil {
		t.Fatalf("insert orphan mutation: %v", err)
	}

	// Before: the project is blocked and repair cannot act.
	report, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected the orphan to be reported")
	}
	if !report.Findings[0].Repairable {
		t.Fatalf("an upsert for a nonexistent observation must be repairable, got reason_code=%q message=%q",
			report.Findings[0].ReasonCode, report.Findings[0].Message)
	}
	if !strings.Contains(strings.ToLower(report.Findings[0].Message), "no longer exists") {
		t.Errorf("the message must say why it is safe to drop, got %q", report.Findings[0].Message)
	}

	// Applying the repair must clear it, unblocking the project.
	if err := s.applyCloudUpgradeLegacyMutationRepairs(project); err != nil {
		t.Fatalf("repair: %v", err)
	}
	after, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		t.Fatalf("diagnose after repair: %v", err)
	}
	if len(after.Findings) != 0 {
		t.Fatalf("repair must clear the orphan, still reported: %+v", after.Findings)
	}
}

// The safety boundary. An upsert whose observation DOES exist is a different
// problem: dropping it would lose a real pending replication. That case is
// already handled by back-filling the payload from the live row, and this test
// locks that in so the orphan rule can never widen into it.
func TestRepairBackfillsIncompletePayloadWhenObservationExists(t *testing.T) {
	s := newTestStore(t)
	const project = "proj-present"

	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := s.CreateSession("sess-present", project, "/tmp/proj-present"); err != nil {
		t.Fatalf("session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-present",
		Type:      "decision",
		Title:     "real memory",
		Content:   "this one genuinely exists",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}

	if _, err := s.db.Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, occurred_at)
		VALUES (?, 'observation', ?, 'upsert', ?, 'local', ?, datetime('now'))`,
		DefaultSyncTargetKey, obs.SyncID,
		`{"sync_id":"`+obs.SyncID+`","session_id":"sess-present","type":"decision","title":"real memory","scope":"project"}`,
		project,
	); err != nil {
		t.Fatalf("insert incomplete mutation: %v", err)
	}

	report, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected a finding for the incomplete payload")
	}
	if report.Findings[0].ReasonCode != UpgradeReasonRepairableLegacyMutationPayload {
		t.Fatalf("an incomplete payload for a LIVE observation must be repaired from the live row, got %q",
			report.Findings[0].ReasonCode)
	}

	// And the repair must preserve the real content rather than drop the row.
	if err := s.applyCloudUpgradeLegacyMutationRepairs(project); err != nil {
		t.Fatalf("repair: %v", err)
	}
	var payload string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity_key = ? AND acked_at IS NULL`, obs.SyncID,
	).Scan(&payload); err != nil {
		t.Fatalf("the live observation's mutation must survive the repair: %v", err)
	}
	if !strings.Contains(payload, "this one genuinely exists") {
		t.Errorf("repair must back-fill the real content, got %q", payload)
	}
}
