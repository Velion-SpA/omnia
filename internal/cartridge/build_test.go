package cartridge_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/velion/omnia/internal/cartridge"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func newCartridgeTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("cartridge-session", "cartridge-project", "/repo"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s
}

// TestBuildProducesVersionedArtifactTaggedWithCommit is the RED test for
// task 11.1: building at commit `abc123` must produce an in-memory
// Cartridge (and, once saved, an on-disk JSON artifact) tagged with commit
// `abc123` and the current schema version, containing top_memories,
// anchors, and trusted_procedures (REQ-451/456).
func TestBuildProducesVersionedArtifactTaggedWithCommit(t *testing.T) {
	s := newCartridgeTestStore(t)

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "cartridge-session", Type: "decision", Title: "Use SQLite",
		Content: "We chose SQLite for local-first storage.", Project: "cartridge-project", Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if _, err := s.UpsertAnchor(store.UpsertAnchorParams{
		ObsSyncID: obs.SyncID, RepoRoot: "/repo", FilePath: "store.go", Symbol: "New",
		LineStart: 10, LineEnd: 20, ContentHash: "hash1",
	}); err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if _, err := s.UpsertProcedure(store.Procedure{
		Project: "cartridge-project", Polarity: store.ProcedurePolarityPlaybook,
		Trigger: "nil pointer in handler", Steps: []store.ProcedureStep{{Order: 1, Template: "add guard"}},
		PostconditionKind: store.PostconditionTestsPass, State: store.ProcedureStateTrusted,
		SourceObsSyncIDs: []string{obs.SyncID},
	}); err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: "cartridge-project", RepoRoot: "/repo", HeadSHA: "abc123",
		TopN: 10, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.SchemaVersion != cartridge.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", c.SchemaVersion, cartridge.SchemaVersion)
	}
	if c.RepoRoot != "/repo" || c.HeadSHA != "abc123" {
		t.Errorf("RepoRoot/HeadSHA = %q/%q, want /repo/abc123", c.RepoRoot, c.HeadSHA)
	}
	if c.Project != "cartridge-project" {
		t.Errorf("Project = %q, want %q", c.Project, "cartridge-project")
	}
	if len(c.TopMemories) != 1 || c.TopMemories[0].SyncID != obs.SyncID {
		t.Fatalf("TopMemories = %+v, want exactly the seeded observation", c.TopMemories)
	}
	if len(c.Anchors) != 1 || c.Anchors[0].Memory.SyncID != obs.SyncID {
		t.Fatalf("Anchors = %+v, want exactly the seeded anchor edge", c.Anchors)
	}
	if len(c.TrustedProcedures) != 1 || c.TrustedProcedures[0].Trigger != "nil pointer in handler" {
		t.Fatalf("TrustedProcedures = %+v, want exactly the seeded trusted procedure", c.TrustedProcedures)
	}
	if c.BuiltAt == "" {
		t.Error("BuiltAt is empty, want a timestamp")
	}

	dir := t.TempDir()
	path, err := cartridge.Save(dir, c, config.EncryptionConfig{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("Save wrote to %q, want directory %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved cartridge: %v", err)
	}
	var roundTrip cartridge.Cartridge
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal saved cartridge: %v", err)
	}
	if roundTrip.HeadSHA != "abc123" || roundTrip.SchemaVersion != cartridge.SchemaVersion {
		t.Fatalf("round-tripped cartridge = %+v, want HeadSHA=abc123 SchemaVersion=%d", roundTrip, cartridge.SchemaVersion)
	}
}

// TestBuildOnlyTrustedProceduresAreIncluded confirms candidate/retired
// procedures never leak into the cartridge (mirrors the enforcement gate's
// own "trusted only" feed convention).
func TestBuildOnlyTrustedProceduresAreIncluded(t *testing.T) {
	s := newCartridgeTestStore(t)
	if _, err := s.UpsertProcedure(store.Procedure{
		Project: "cartridge-project", Polarity: store.ProcedurePolarityPlaybook,
		Trigger: "candidate trigger", Steps: []store.ProcedureStep{{Order: 1, Template: "step"}},
		PostconditionKind: store.PostconditionTestsPass, State: store.ProcedureStateCandidate,
	}); err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}

	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: "cartridge-project", RepoRoot: "/repo", HeadSHA: "abc123", TopN: 10,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(c.TrustedProcedures) != 0 {
		t.Fatalf("TrustedProcedures = %+v, want none (candidate state must not gate)", c.TrustedProcedures)
	}
}

// TestBuildTopMemoriesRespectsTopN confirms the top-N truncation contract.
func TestBuildTopMemoriesRespectsTopN(t *testing.T) {
	s := newCartridgeTestStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "cartridge-session", Type: "decision", Title: fmt.Sprintf("Decision %d", i),
			Content: fmt.Sprintf("distinct content body number %d", i), Project: "cartridge-project", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}
	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: "cartridge-project", RepoRoot: "/repo", HeadSHA: "abc123", TopN: 3,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(c.TopMemories) != 3 {
		t.Fatalf("TopMemories len = %d, want 3", len(c.TopMemories))
	}
}

// TestBuildContentShapeAssertion is the REFACTOR task (11.11): confirms no
// weight-level/KV-cache artifact is ever included in the cartridge payload
// (REQ-455) by asserting the JSON payload's top-level keys are exactly the
// documented allowlist.
func TestBuildContentShapeAssertion(t *testing.T) {
	s := newCartridgeTestStore(t)
	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: "cartridge-project", RepoRoot: "/repo", HeadSHA: "abc123", TopN: 10,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"schema_version": true, "repo_root": true, "project": true, "head_sha": true, "built_at": true,
		"top_memories": true, "anchors": true, "trusted_procedures": true, "ranker_model_version": true,
	}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("unexpected cartridge field %q — no model weights/KV-cache artifact may ever be included (REQ-455)", key)
		}
	}
}

// TestBuildNeverWritesSyncMutations is part of the REFACTOR task (11.11):
// confirms the cartridge artifact is never referenced by cloud sync
// (REQ-454) — Build/Save never touch the store's sync_mutations pipeline.
func TestBuildNeverWritesSyncMutations(t *testing.T) {
	s := newCartridgeTestStore(t)
	before, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations (before): %v", err)
	}
	c, err := cartridge.Build(cartridge.BuildParams{
		Store: s, Project: "cartridge-project", RepoRoot: "/repo", HeadSHA: "abc123", TopN: 10,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := cartridge.Save(t.TempDir(), c, config.EncryptionConfig{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("pending sync mutations changed from %d to %d — cartridge build/save must never touch cloud sync", len(before), len(after))
	}
}

// TestBuildRequiresStoreAndHeadSHA confirms the input-validation contract.
func TestBuildRequiresStoreAndHeadSHA(t *testing.T) {
	if _, err := cartridge.Build(cartridge.BuildParams{}); err == nil {
		t.Fatal("expected error for missing Store")
	}
	s := newCartridgeTestStore(t)
	if _, err := cartridge.Build(cartridge.BuildParams{Store: s}); err == nil {
		t.Fatal("expected error for missing HeadSHA")
	}
}

// TestSaveKeysCartridgeByProjectAvoidingCrossProjectCollision is the RED
// test for the review's collision finding: two different projects sharing
// one repo+commit (a supported, tested scenario — see
// internal/project/detect_test.go's monorepo-subproject tests) MUST produce
// two distinct on-disk cartridge files, never overwrite each other.
// Previously the filename was keyed only by RepoID+HeadSHA, so the second
// Save silently clobbered the first project's file.
func TestSaveKeysCartridgeByProjectAvoidingCrossProjectCollision(t *testing.T) {
	dir := t.TempDir()
	built := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cA := &cartridge.Cartridge{SchemaVersion: cartridge.SchemaVersion, RepoRoot: "/repo", Project: "project-a", HeadSHA: "abc123", BuiltAt: built}
	cB := &cartridge.Cartridge{SchemaVersion: cartridge.SchemaVersion, RepoRoot: "/repo", Project: "project-b", HeadSHA: "abc123", BuiltAt: built}

	pathA, err := cartridge.Save(dir, cA, config.EncryptionConfig{})
	if err != nil {
		t.Fatalf("Save (project-a): %v", err)
	}
	pathB, err := cartridge.Save(dir, cB, config.EncryptionConfig{})
	if err != nil {
		t.Fatalf("Save (project-b): %v", err)
	}
	if pathA == pathB {
		t.Fatalf("project-a and project-b both saved to %q at the same repo+commit — expected distinct files", pathA)
	}

	dataA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read project-a cartridge: %v", err)
	}
	var roundTripA cartridge.Cartridge
	if err := json.Unmarshal(dataA, &roundTripA); err != nil {
		t.Fatalf("unmarshal project-a cartridge: %v", err)
	}
	if roundTripA.Project != "project-a" {
		t.Fatalf("project-a's own file was clobbered: got Project = %q, want %q", roundTripA.Project, "project-a")
	}
}

// TestSaveRefusesPlaintextWhenEncryptionEnabled is the RED test for the
// review's at-rest-encryption-bypass finding: Save must fail closed (return
// an error, write nothing) when the operator has EncryptionConfig.Enabled
// on, since cartridge export has no encrypted-output path yet.
func TestSaveRefusesPlaintextWhenEncryptionEnabled(t *testing.T) {
	dir := t.TempDir()
	c := &cartridge.Cartridge{
		SchemaVersion: cartridge.SchemaVersion, RepoRoot: "/repo", Project: "cartridge-project",
		HeadSHA: "abc123", BuiltAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	if _, err := cartridge.Save(dir, c, config.EncryptionConfig{Enabled: true}); err == nil {
		t.Fatal("expected Save to refuse writing a plaintext cartridge when at-rest encryption is enabled")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no cartridge file written when encryption is enabled and unsupported, found %d entries", len(entries))
	}
}
