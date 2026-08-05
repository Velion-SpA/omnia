package store

import "testing"

// Callers always pass the NORMALIZED (lowercased) project name — every cloud
// path routes through NormalizeProject. The export filter compared it with
// `WHERE project = ?`, an exact case-sensitive match, so a project whose rows
// were stored with uppercase letters matched nothing and exported NOTHING while
// the CLI reported "all memories already exported".
//
// Measured on a real store: 18 projects and ~366 memories never reached the
// cloud that way, the largest being 239 observations stored as
// "GestionServiciosTerreno". This is silent non-replication reported as success,
// which is worse than a loud failure.
func TestExportProjectIsCaseInsensitive(t *testing.T) {
	s := newTestStore(t)

	// Writes normalize the project today, so mixed-case rows can only be LEGACY —
	// written before that normalization existed. Insert them directly to
	// reproduce that state, which is exactly what a real store still holds.
	const stored = "GestionServiciosTerreno"
	if err := s.CreateSession("sess-mixed", "placeholder", "/tmp/mixed"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-mixed",
		Type:      "decision",
		Title:     "memory in a mixed-case project",
		Content:   "must be exportable under the normalized name",
		Project:   "placeholder",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET project = ? WHERE id = 'sess-mixed'`, stored); err != nil {
		t.Fatalf("backdate session project: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = ? WHERE session_id = 'sess-mixed'`, stored); err != nil {
		t.Fatalf("backdate observation project: %v", err)
	}

	// The normalized name is what every cloud caller actually passes.
	normalized, _ := NormalizeProject(stored)
	data, err := s.ExportProject(normalized)
	if err != nil {
		t.Fatalf("export %q: %v", normalized, err)
	}
	if len(data.Observations) == 0 {
		t.Fatalf("exporting %q found none of the rows stored as %q — they would never reach the cloud", normalized, stored)
	}
	if len(data.Sessions) == 0 {
		t.Fatalf("exporting %q found no sessions for rows stored as %q", normalized, stored)
	}

	// The original casing must keep working too — nothing regresses for callers
	// that pass the stored name verbatim.
	verbatim, err := s.ExportProject(stored)
	if err != nil {
		t.Fatalf("export %q: %v", stored, err)
	}
	if len(verbatim.Observations) != len(data.Observations) {
		t.Errorf("normalized and verbatim lookups must agree: %d vs %d",
			len(data.Observations), len(verbatim.Observations))
	}
}

// Case-duplicate projects collapse into one export. The cloud normalizes project
// names anyway — memberships are keyed on the lowercased form — so both variants
// belong to the same cloud project, and leaving one behind would silently drop
// memories.
func TestExportProjectMergesCaseVariants(t *testing.T) {
	s := newTestStore(t)

	for _, c := range []struct{ sess, title string }{
		{"sess-lower", "lowercase memory"},
		{"sess-upper", "uppercase memory"},
	} {
		if err := s.CreateSession(c.sess, "habitia", "/tmp/"+c.sess); err != nil {
			t.Fatalf("create %s: %v", c.sess, err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: c.sess, Type: "decision", Title: c.title,
			Content: "both variants belong to one cloud project",
			Project: "habitia", Scope: "project",
		}); err != nil {
			t.Fatalf("add %q: %v", c.title, err)
		}
	}
	// Backdate one pair to the legacy uppercase form.
	if _, err := s.db.Exec(`UPDATE sessions SET project = 'Habitia' WHERE id = 'sess-upper'`); err != nil {
		t.Fatalf("backdate session: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = 'Habitia' WHERE session_id = 'sess-upper'`); err != nil {
		t.Fatalf("backdate observation: %v", err)
	}

	data, err := s.ExportProject("habitia")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(data.Observations) != 2 {
		t.Fatalf("expected both case variants in one export, got %d: %+v", len(data.Observations), data.Observations)
	}
}
