package store

import (
	"strings"
	"testing"
)

// save_normalization_test.go — RED->GREEN tests closing the two
// save-normalization spec REQs (spec obs #1666 save-normalization domain)
// the sdd-verify report (obs #1682) found silently dropped between proposal
// and design: "Non-Blocking Junk Warnings" and "Warnings Are Itemized In The
// Envelope". Both are warn-only — a save must ALWAYS complete regardless of
// how many warnings fire (spec: "no rejection path exists").

// ─── Pure helper: detectSaveNormalizationWarnings ────────────────────────────

func TestDetectSaveNormalizationWarnings(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		minLength int
		maxLength int
		want      []string
	}{
		{
			name:      "empty content warns exactly once (short-circuits below-minimum/missing-keywords)",
			content:   "",
			minLength: 20,
			maxLength: 100,
			want:      []string{"empty content"},
		},
		{
			name:      "whitespace-only content counts as empty",
			content:   "   \n\t  ",
			minLength: 20,
			maxLength: 100,
			want:      []string{"empty content"},
		},
		{
			name:      "below minimum length only (Keywords present)",
			content:   "hi\nKeywords: x",
			minLength: 20,
			maxLength: 100,
			want:      []string{"content below minimum length (14 chars, minimum 20)"},
		},
		{
			name:      "missing Keywords section only (long enough content)",
			content:   strings.Repeat("word ", 6),
			minLength: 10,
			maxLength: 100,
			want:      []string{"missing Keywords section"},
		},
		{
			name:      "Keywords section present, case-insensitive, no warning",
			content:   strings.Repeat("word ", 6) + "\nKEYWORDS: foo bar",
			minLength: 10,
			maxLength: 100,
			want:      nil,
		},
		{
			name:      "oversized content (Keywords present, isolates the oversized warning)",
			content:   strings.Repeat("x", 25) + "\nKeywords: y",
			minLength: 1,
			maxLength: 20,
			want:      []string{"content exceeds maximum size (37 chars, maximum 20)"},
		},
		{
			name:      "multiple simultaneous warnings: below-minimum AND missing-keywords",
			content:   "hi",
			minLength: 20,
			maxLength: 100,
			want: []string{
				"content below minimum length (2 chars, minimum 20)",
				"missing Keywords section",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSaveNormalizationWarnings(tt.content, tt.minLength, tt.maxLength)
			if len(got) != len(tt.want) {
				t.Fatalf("detectSaveNormalizationWarnings() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("warning[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ─── SaveObservation-level: never blocks, itemized, kill-switch ─────────────

// TestSaveObservation_JunkWarningsNeverBlockSave is the "hygiene warns, never
// blocks" business rule (locked obs #1665): empty content must still save
// successfully and land a real row, with the warning surfaced on the result.
func TestSaveObservation_JunkWarningsNeverBlockSave(t *testing.T) {
	s := newWriteGateStore(t, gateEnabledDefaults)

	got := mustSave(t, s, AddObservationParams{Title: "Empty body test", Content: ""})

	if got.ID == 0 {
		t.Fatal("expected a saved row (ID != 0) even for empty content — hygiene must never block a save")
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "empty content" {
		t.Errorf("expected Warnings=[\"empty content\"], got %v", got.Warnings)
	}
	if got := countObservations(t, s, "wgproject"); got != 1 {
		t.Fatalf("expected the empty-content save to persist a row, got %d rows", got)
	}
}

// TestSaveObservation_JunkWarningsItemizedForMultipleConditions pins spec REQ
// "Warnings Are Itemized In The Envelope": two simultaneously-triggered
// conditions both appear as distinct entries.
func TestSaveObservation_JunkWarningsItemizedForMultipleConditions(t *testing.T) {
	s := newWriteGateStore(t, func(cfg *Config) {
		gateEnabledDefaults(cfg)
		cfg.MinContentLength = 20
	})

	got := mustSave(t, s, AddObservationParams{Title: "Short no-keywords note", Content: "too short"})

	if len(got.Warnings) != 2 {
		t.Fatalf("expected 2 itemized warnings, got %v", got.Warnings)
	}
}

// TestSaveObservation_OversizedContentStillSaves pins the "oversized warns
// but saves" scenario: content beyond the configured maximum size still
// completes (never rejected) and carries the oversized warning.
func TestSaveObservation_OversizedContentStillSaves(t *testing.T) {
	s := newWriteGateStore(t, func(cfg *Config) {
		gateEnabledDefaults(cfg)
		cfg.MaxObservationLength = 20
	})

	content := strings.Repeat("y", 40) + "\nKeywords: z"
	got := mustSave(t, s, AddObservationParams{Title: "Oversized note", Content: content})

	if got.ID == 0 {
		t.Fatal("expected oversized content to still save")
	}
	found := false
	for _, w := range got.Warnings {
		if strings.Contains(w, "exceeds maximum size") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an 'exceeds maximum size' warning, got %v", got.Warnings)
	}
}

// TestSaveObservation_WriteHygieneDisabledSkipsJunkWarnings is the
// kill-switch byte-for-byte case: with WriteHygieneEnabled=false, even
// maximally junky content (empty) produces zero warnings — no new v0.3.1
// behavior at all when the gate is off.
func TestSaveObservation_WriteHygieneDisabledSkipsJunkWarnings(t *testing.T) {
	s := newWriteGateStore(t, nil) // WriteHygieneEnabled left false

	got := mustSave(t, s, AddObservationParams{Title: "Empty body, gate off", Content: ""})

	if len(got.Warnings) != 0 {
		t.Errorf("expected zero warnings with write_hygiene disabled, got %v", got.Warnings)
	}
}
