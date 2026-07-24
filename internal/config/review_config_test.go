package config_test

import (
	"testing"

	"github.com/velion/omnia/internal/config"
)

// review_config_test.go — RED→GREEN tests for omnia-0.3.1-write-hygiene
// PR11 (spaced-review / Play G, design D11's mem_context due-count nudge).
// ReviewConfig carries only DueNudge — no numeric field needs a default, so
// like TypeLensConfig (see type_lens_config_test.go's own doc) its zero
// value (false) IS the default: no explicit-vs-absent probe is needed.

// TestReview_DefaultsDisabled locks the same backward-compatible rollback
// guarantee every other write-hygiene/Context-Economy gate shares: a config
// with no `review` section at all must default to Review.DueNudge=false.
func TestReview_DefaultsDisabled(t *testing.T) {
	path := writeTempConfig(t, "engram:\n  base_url: http://127.0.0.1:7437\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Review.DueNudge {
		t.Error("Review.DueNudge: got true, want false by default")
	}
}

// TestReview_ParsesOverrides is the full review.due_nudge yaml roundtrip.
func TestReview_ParsesOverrides(t *testing.T) {
	path := writeTempConfig(t, ""+
		"review:\n"+
		"  due_nudge: true\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Review.DueNudge {
		t.Error("Review.DueNudge: got false, want true")
	}
}
