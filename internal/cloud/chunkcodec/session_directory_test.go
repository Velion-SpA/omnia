package chunkcodec

import (
	"encoding/json"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// A session created by saving a memory by hand has no working directory — there
// was no repo involved. That is its correct state, not missing data: on a real
// store 49 of 2224 sessions are like this, all `manual-save-*`.
//
// The canonicalizer required `directory` on every session upsert, so one such
// session aborted the entire chunk and blocked the project:
//
//	status 400: invalid push payload: sessions[0].directory is required
//
// Same shape as the unjudged relation in #249: a legitimate state the validator
// refused. The identity field (id) stays required; directory does not.
func TestCanonicalizeAcceptsSessionWithoutDirectory(t *testing.T) {
	const project = "proj-manual"
	payload, err := json.Marshal(map[string]any{
		"id":         "manual-save-proj",
		"project":    project,
		"started_at": "2026-08-04 10:00:00",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	chunk := map[string]any{
		"sessions": []any{}, "observations": []any{}, "prompts": []any{},
		"mutations": []any{map[string]any{
			"entity": store.SyncEntitySession, "entity_key": "manual-save-proj",
			"op": store.SyncOpUpsert, "payload": string(payload),
			"project": project, "occurred_at": "2026-08-04 10:00:00",
		}},
	}
	raw, _ := json.Marshal(chunk)

	if _, err := CanonicalizeForProject(raw, project); err != nil {
		t.Fatalf("a manual-save session has no directory and must still sync: %v", err)
	}
}

// The identity field is a different matter: without an id the upsert targets
// nothing, so it stays required.
func TestCanonicalizeStillRejectsSessionWithoutID(t *testing.T) {
	const project = "proj-manual"
	payload, _ := json.Marshal(map[string]any{"project": project, "directory": "/tmp/x"})
	chunk := map[string]any{
		"sessions": []any{}, "observations": []any{}, "prompts": []any{},
		"mutations": []any{map[string]any{
			"entity": store.SyncEntitySession, "entity_key": "", "op": store.SyncOpUpsert,
			"payload": string(payload), "project": project, "occurred_at": "2026-08-04 10:00:00",
		}},
	}
	raw, _ := json.Marshal(chunk)

	if _, err := CanonicalizeForProject(raw, project); err == nil {
		t.Fatal("a session payload without id must still be rejected")
	}
}
