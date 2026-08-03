package store

import (
	"strings"
	"testing"
)

// A relation that nobody has judged yet has no marking actor — that is its
// correct state, not corruption. But the cloud chunk canonicalizer requires
// marked_by_actor on every relation upsert, so exporting one aborted the whole
// chunk:
//
//	canonicalize cloud chunk: mutations[5]: relation payload marked_by_actor is
//	required for upsert
//
// On the reporting install exactly 2 of 343 exportable relations were in this
// state, and those 2 blocked ~1900 memories from reaching the cloud.
//
// These rows are created by the system's own conflict detection, so the
// documented system provenance (actor "engram", kind "system" — the same pair
// MarkAnchorStale and JudgeBySemantic use) is the accurate value to stamp,
// applied only when the field is genuinely absent.
func TestExportRelationMutationsStampsSystemProvenanceWhenUnjudged(t *testing.T) {
	s := newTestStore(t)
	const project = "proj-rel"

	if err := s.CreateSession("sess-rel", project, "/tmp/proj-rel"); err != nil {
		t.Fatalf("session: %v", err)
	}
	srcID, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-rel", Type: "decision", Title: "source",
		Content: "source memory", Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("add source: %v", err)
	}
	tgtID, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-rel", Type: "decision", Title: "target",
		Content: "target memory", Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}
	src, err := s.GetObservation(srcID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	tgt, err := s.GetObservation(tgtID)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}

	// An unjudged relation: no actor, no kind — exactly what conflict detection
	// leaves behind until someone rules on it.
	if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES ('rel-unjudged', ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))`,
		src.SyncID, tgt.SyncID,
	); err != nil {
		t.Fatalf("insert unjudged relation: %v", err)
	}

	muts, err := s.ExportRelationMutations(project)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	var found bool
	for _, m := range muts {
		if m.EntityKey != "rel-unjudged" {
			continue
		}
		found = true
		if strings.Contains(m.Payload, `"marked_by_actor":null`) || !strings.Contains(m.Payload, `"marked_by_actor"`) {
			t.Fatalf("an unjudged relation must export with system provenance, got %s", m.Payload)
		}
		if !strings.Contains(m.Payload, `"marked_by_actor":"engram"`) {
			t.Errorf("expected the documented system actor, got %s", m.Payload)
		}
		if !strings.Contains(m.Payload, `"marked_by_kind":"system"`) {
			t.Errorf("expected the documented system kind, got %s", m.Payload)
		}
	}
	if !found {
		t.Fatal("the unjudged relation was not exported at all")
	}
}

// The stamp must never overwrite a real human or model judgment.
func TestExportRelationMutationsPreservesExistingProvenance(t *testing.T) {
	s := newTestStore(t)
	const project = "proj-rel-keep"

	if err := s.CreateSession("sess-keep", project, "/tmp/proj-rel-keep"); err != nil {
		t.Fatalf("session: %v", err)
	}
	srcID, _ := s.AddObservation(AddObservationParams{
		SessionID: "sess-keep", Type: "decision", Title: "s", Content: "s", Project: project, Scope: "project",
	})
	tgtID, _ := s.AddObservation(AddObservationParams{
		SessionID: "sess-keep", Type: "decision", Title: "t", Content: "t", Project: project, Scope: "project",
	})
	src, _ := s.GetObservation(srcID)
	tgt, _ := s.GetObservation(tgtID)

	if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, marked_by_actor, marked_by_kind, created_at, updated_at)
		VALUES ('rel-judged', ?, ?, 'supersedes', 'confirmed', 'benja', 'human', datetime('now'), datetime('now'))`,
		src.SyncID, tgt.SyncID,
	); err != nil {
		t.Fatalf("insert judged relation: %v", err)
	}

	muts, err := s.ExportRelationMutations(project)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, m := range muts {
		if m.EntityKey != "rel-judged" {
			continue
		}
		if !strings.Contains(m.Payload, `"marked_by_actor":"benja"`) || !strings.Contains(m.Payload, `"marked_by_kind":"human"`) {
			t.Fatalf("a real judgment must survive export untouched, got %s", m.Payload)
		}
		return
	}
	t.Fatal("the judged relation was not exported")
}
