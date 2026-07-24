package main

// import_claude_memory_test.go — RED→GREEN CLI tests for `omnia import
// claude-memory <dir>` (omnia-0.3.1-write-hygiene PR10, design D10, spec
// claude-memory-import). Mirrors dedupe_test.go/review_due_test.go's
// withArgs/captureOutput/testConfig style. Reuses write_hygiene_wiring_test.go's
// writeHygieneEnabledFixture/withWriteHygieneAppConfig (same package) to
// deterministically drive the write-gate rather than depending on cwd
// config.yaml lookup.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func writeClaudeMemoryFile(t *testing.T, dir, name, frontmatterBody, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\n" + frontmatterBody + "\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// ─── 10.1 RED cases ─────────────────────────────────────────────────────────

func TestCmdImportClaudeMemory_OnlyIndexFile_ZeroObservationsNoError(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "MEMORY.md", "", "# Memory Index\n")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importonlyindex")
	stdout, stderr := captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Imported from "+dir) {
		t.Fatalf("expected import summary, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	stats, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalObservations != 0 {
		t.Fatalf("expected zero observations for a MEMORY.md-only dir, got %d", stats.TotalObservations)
	}
}

func TestCmdImportClaudeMemory_SkipsIndexImportsRest(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "MEMORY.md", "", "# Memory Index\n- [a](a.md)\n- [b](b.md)")
	writeClaudeMemoryFile(t, dir, "a.md", "name: mem-a\ndescription: First memory", "Body A content for import.")
	writeClaudeMemoryFile(t, dir, "b.md", "name: mem-b\ndescription: Second memory", "Body B content for import.")
	writeClaudeMemoryFile(t, dir, "notes.txt", "name: ignored", "not markdown, must be skipped")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importskipindex")
	stdout, stderr := captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.Contains(stdout, "MEMORY.md") {
		t.Fatalf("MEMORY.md must never be listed as a considered file, got: %s", stdout)
	}
	if strings.Contains(stdout, "notes.txt") {
		t.Fatalf("non-markdown file must never be listed as a considered file, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("importskipindex", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected exactly 2 imported observations (MEMORY.md and notes.txt skipped), got %d", len(observations))
	}
}

func TestCmdImportClaudeMemory_ProvenanceTagSet(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: provenance-check\ndescription: Provenance check memory", "Some content.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importprovenance")
	_, stderr := captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("importprovenance", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected exactly 1 imported observation, got %d", len(observations))
	}
	obs := observations[0]
	if obs.Source == nil || *obs.Source != "claude-memory" {
		t.Fatalf("expected source=%q provenance tag, got %v", "claude-memory", obs.Source)
	}
	if obs.TopicKey == nil || *obs.TopicKey != "claude-memory/provenance-check" {
		t.Fatalf("expected topic_key=%q, got %v", "claude-memory/provenance-check", obs.TopicKey)
	}
}

func TestCmdImportClaudeMemory_IdempotentReimportUnchangedDir_ZeroNewObservations(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: idempotent-check\ndescription: Idempotent check memory", "Unchanged content across re-imports.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importidempotent")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	statsAfterFirst, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	s.Close()
	if statsAfterFirst.TotalObservations != 1 {
		t.Fatalf("expected 1 observation after first import, got %d", statsAfterFirst.TotalObservations)
	}

	// Re-run over the SAME unchanged directory.
	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importidempotent")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s2.Close()
	statsAfterSecond, err := s2.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if statsAfterSecond.TotalObservations != 1 {
		t.Fatalf("expected re-import over an unchanged dir to create ZERO new observations (still 1 total), got %d", statsAfterSecond.TotalObservations)
	}

	observations, err := s2.AllObservations("importidempotent", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected 1 row after two imports, got %d", len(observations))
	}
	if observations[0].RevisionCount < 1 {
		t.Fatalf("expected the re-import to bump revision_count via topic_key upsert, got %d", observations[0].RevisionCount)
	}
}

func TestCmdImportClaudeMemory_ReimportAfterEdit_AutoUpdateNotDuplicate(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: edited-check\ndescription: Edited check memory", "Original content before edit.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importedited")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })

	// Edit the file's body, then re-import.
	writeClaudeMemoryFile(t, dir, "a.md", "name: edited-check\ndescription: Edited check memory", "Materially different content after the edit.")
	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importedited")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("importedited", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected an edited-file re-import to AUTO-UPDATE (still 1 row), not duplicate, got %d rows", len(observations))
	}
	if observations[0].Content != "Materially different content after the edit." {
		t.Fatalf("expected the row's content to reflect the edited body, got %q", observations[0].Content)
	}
}

func TestCmdImportClaudeMemory_KillSwitchOff_DuplicatesPossible(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneKillSwitchFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: kill-switch-check\ndescription: Kill switch check memory", "Same content every time.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importkillswitch")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importkillswitch")
	captureOutput(t, func() { cmdImportClaudeMemory(cfg) })

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("importkillswitch", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	// NOTE: topic_key upsert (design D3 step 1, unconditional/explicit-intent)
	// runs regardless of WriteHygieneEnabled — only steps 3-6 (the
	// Jaccard-based NOOP/AUTO-UPDATE/RELATE ladder) are gated by the
	// kill-switch. Since claude-memory import ALWAYS sets topic_key, the
	// kill-switch does not reopen duplication for THIS particular caller —
	// this test pins that observed behavior explicitly (documented, not a
	// silent assumption) rather than asserting the more general "kill-switch
	// off restores v0.3 duplicate semantics" claim, which the topic_key path
	// was never conditioned on in the first place (see design D2/D3: topic_key
	// upsert is step 1, "existing, UNCHANGED", independent of the gate).
	if len(observations) != 1 {
		t.Fatalf("expected topic_key upsert (unconditional) to still collapse to 1 row even with the write-gate off, got %d", len(observations))
	}
}

// ─── Malformed frontmatter: skip, never abort the batch ────────────────────

func TestCmdImportClaudeMemory_MalformedFrontmatterSkipsNeverAbortsBatch(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	// No opening "---" at all.
	if err := os.WriteFile(filepath.Join(dir, "no-frontmatter.md"), []byte("Just a plain markdown file, no frontmatter.\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Opening "---" but never closed.
	if err := os.WriteFile(filepath.Join(dir, "unclosed.md"), []byte("---\nname: unclosed\nBody without a closing delimiter.\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Invalid YAML inside the frontmatter block.
	writeClaudeMemoryFile(t, dir, "bad-yaml.md", "name: [unterminated", "body")
	// Missing the required name field.
	writeClaudeMemoryFile(t, dir, "no-name.md", "description: has no name field", "body")
	// A genuinely valid file that MUST still import despite the 4 malformed siblings.
	writeClaudeMemoryFile(t, dir, "valid.md", "name: valid-entry\ndescription: A valid entry", "Valid body content.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importmalformed")
	stdout, _ := captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	if !strings.Contains(stdout, "valid.md") {
		t.Fatalf("expected the valid file to still be reported, got: %s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("importmalformed", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected exactly 1 observation (only valid.md), got %d — malformed files must be skipped, never abort the batch", len(observations))
	}
}

// ─── --dry-run: no store mutations at all ──────────────────────────────────

func TestCmdImportClaudeMemory_DryRunPerformsNoStoreMutations(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: dry-run-check\ndescription: Dry run check memory", "Body content.")
	writeClaudeMemoryFile(t, dir, "MEMORY.md", "", "# index")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importdryrun", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdImportClaudeMemory(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "dry-run") && !strings.Contains(stdout, "Dry-run") {
		t.Fatalf("expected dry-run to be explicitly labeled in the report, got: %s", stdout)
	}

	// The data dir must not even contain a database file — dry-run never
	// touches storage at all (storeNew/CreateSession/SaveObservation are all
	// skipped in this mode).
	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil {
		t.Fatalf("ReadDir data dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected --dry-run to leave the data dir untouched (no db file created), found: %v", entries)
	}
}

// ─── Type mapping table (design D10 / tasks 10.2: reference→discovery, note→discovery, default→manual) ───

func TestMapClaudeMemoryType(t *testing.T) {
	tests := []struct {
		name     string
		metaType string
		want     string
	}{
		{"reference maps to discovery", "reference", "discovery"},
		{"note maps to discovery", "note", "discovery"},
		{"unknown type maps to manual default", "gotcha", "manual"},
		{"empty type maps to manual default", "", "manual"},
		{"case-insensitive reference", "Reference", "discovery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapClaudeMemoryType(tt.metaType); got != tt.want {
				t.Fatalf("mapClaudeMemoryType(%q) = %q, want %q", tt.metaType, got, tt.want)
			}
		})
	}
}

// ─── parseClaudeMemoryFile: pure-function table-driven coverage ────────────

func TestParseClaudeMemoryFile(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantErr  bool
		wantName string
		wantDesc string
		wantType string
		wantBody string
	}{
		{
			name:     "well-formed frontmatter and body",
			raw:      "---\nname: sample\ndescription: A sample\nmetadata:\n  type: reference\n---\nBody text here.",
			wantName: "sample",
			wantDesc: "A sample",
			wantType: "reference",
			wantBody: "Body text here.",
		},
		{
			name:    "missing opening delimiter",
			raw:     "name: sample\nBody without frontmatter.",
			wantErr: true,
		},
		{
			name:    "missing closing delimiter",
			raw:     "---\nname: sample\nBody never closed.",
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			raw:     "---\nname: [unterminated\n---\nBody.",
			wantErr: true,
		},
		{
			name:    "missing required name field",
			raw:     "---\ndescription: no name here\n---\nBody.",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body, err := parseClaudeMemoryFile([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none (fm=%+v, body=%q)", fm, body)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fm.Name != tt.wantName {
				t.Fatalf("Name = %q, want %q", fm.Name, tt.wantName)
			}
			if fm.Description != tt.wantDesc {
				t.Fatalf("Description = %q, want %q", fm.Description, tt.wantDesc)
			}
			if fm.Metadata.Type != tt.wantType {
				t.Fatalf("Metadata.Type = %q, want %q", fm.Metadata.Type, tt.wantType)
			}
			if body != tt.wantBody {
				t.Fatalf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestClaudeMemorySlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"omnia-release-process", "omnia-release-process"},
		{"My Note!!", "my-note"},
		{"  Spaced Out  ", "spaced-out"},
		{"UPPER_CASE-Name", "upper-case-name"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := claudeMemorySlug(tt.in); got != tt.want {
			t.Fatalf("claudeMemorySlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ─── 10.4 Kill-switch/backward-compat: existing JSON import path untouched ─

func TestCmdImport_JSONPathUntouchedByClaudeMemoryDispatch(t *testing.T) {
	sourceCfg := testConfig(t)
	targetCfg := testConfig(t)
	mustSeedObservation(t, sourceCfg, "s-json", "proj-json", "pattern", "json-export-check", "exported via JSON", "project")

	exportPath := filepath.Join(t.TempDir(), "memories.json")
	withArgs(t, "omnia", "export", exportPath)
	captureOutput(t, func() { cmdExport(sourceCfg) })

	withArgs(t, "omnia", "import", exportPath)
	stdout, stderr := captureOutput(t, func() { cmdImport(targetCfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr from JSON import: %s", stderr)
	}
	if !strings.Contains(stdout, "Imported from "+exportPath) {
		t.Fatalf("expected the pre-existing JSON import report line, byte-for-byte unchanged, got: %s", stdout)
	}
	if strings.Contains(stdout, "dry-run") || strings.Contains(stdout, "Dry-run") {
		t.Fatalf("JSON import path must never mention dry-run/claude-memory report language, got: %s", stdout)
	}

	s, err := store.New(targetCfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	observations, err := s.AllObservations("proj-json", "", 10)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected the JSON import to still restore exactly 1 observation, got %d", len(observations))
	}
}

func TestCmdImport_DispatchesToClaudeMemorySubcommand(t *testing.T) {
	cfg := testConfig(t)
	withWriteHygieneAppConfig(t, writeHygieneEnabledFixture)
	dir := t.TempDir()
	writeClaudeMemoryFile(t, dir, "a.md", "name: dispatch-check\ndescription: Dispatch check memory", "Body.")

	withArgs(t, "omnia", "import", "claude-memory", dir, "--project", "importdispatch")
	stdout, stderr := captureOutput(t, func() { cmdImport(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "Imported from "+dir) {
		t.Fatalf("expected cmdImport to dispatch to the claude-memory path, got: %s", stdout)
	}
}
