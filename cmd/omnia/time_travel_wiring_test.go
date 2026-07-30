package main

import (
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func TestApplyTimeTravelConfig(t *testing.T) {
	cfg := store.Config{}
	app := config.Config{TimeTravel: config.TimeTravelConfig{Enabled: true, MaxRevisionsPerMemory: 7}}
	applyTimeTravelConfig(&cfg, &app)
	if !cfg.TimeTravelEnabled || cfg.HistoryRevisionCap != 7 {
		t.Fatalf("store config = enabled:%v cap:%d, want true/7", cfg.TimeTravelEnabled, cfg.HistoryRevisionCap)
	}
}

func TestCmdSearchAsOfUsesRecordedStateAndDisclosesLimitation(t *testing.T) {
	cfg := testConfig(t)
	cfg.TimeTravelEnabled = true
	s, _ := store.New(cfg)
	_ = s.CreateSession("s1", "omnia", t.TempDir())
	id, _ := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "decision", Title: "old title", Content: "old body", Project: "omnia", Scope: "project"})
	_, _ = s.DB().Exec(`UPDATE observations SET created_at='2023-01-01', updated_at='2023-01-01', review_after='2025-01-01' WHERE id=?`, id)
	old, _ := s.GetObservation(id)
	title, content := "current title", "current searchable"
	_, _ = s.UpdateObservation(id, store.UpdateObservationParams{Title: &title, Content: &content})
	_ = s.Close()
	loadConfig := loadAppConfigWithRecallAutodetect
	loadAppConfigWithRecallAutodetect = func() (*config.Config, error) {
		return &config.Config{Recall: config.RecallConfig{Ranking: config.RankingConfig{RecencyHalfLifeDays: 365}}}, nil
	}
	t.Cleanup(func() { loadAppConfigWithRecallAutodetect = loadConfig })
	withArgs(t, "omnia", "search", "searchable", "--as-of", "2024-01-01", "--explain")
	stdout, _ := captureOutput(t, func() { cmdSearch(cfg) })
	if !strings.Contains(stdout, "old body") || !strings.Contains(stdout, "Search limitation") || !strings.Contains(stdout, "recency=0.5") {
		t.Fatalf("recorded-time search output:\n%s", stdout)
	}
	s, _ = store.New(cfg)
	_ = s.DeleteObservation(id, false)
	var deletedAt string
	if err := s.DB().QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, id).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	withArgs(t, "omnia", "search", "searchable", "--as-of", deletedAt)
	stdout, _ = captureOutput(t, func() { cmdSearch(cfg) })
	if !strings.Contains(stdout, "No memories") || !strings.Contains(stdout, "Search limitation") {
		t.Fatalf("zero-hit recorded-time search output:\n%s", stdout)
	}
	loadAppConfigWithRecallAutodetect = loadConfig
	withArgs(t, "omnia", "search", "searchable")
	liveSearch, _ := captureOutput(t, func() { cmdSearch(cfg) })
	withArgs(t, "omnia", "search", "searchable", "--as-of", time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	futureSearch, _ := captureOutput(t, func() { cmdSearch(cfg) })
	if futureSearch != liveSearch || strings.Contains(futureSearch, "Recorded-time view") {
		t.Fatalf("future search differs from live:\n%s\n--- live ---\n%s", futureSearch, liveSearch)
	}
	withArgs(t, "omnia", "context", "--scope", "personal", "--as-of", old.UpdatedAt)
	stdout, _ = captureOutput(t, func() { cmdContext(cfg) })
	if !strings.Contains(stdout, "No previous") || !strings.Contains(stdout, "Recorded-time view") {
		t.Fatalf("zero-hit recorded-time context output:\n%s", stdout)
	}
}

// TestApplyEncryptionConfig (v0.4 memory-at-rest-security, PR4, spec REQ-430):
// confirms config.yaml's encryption.* thread into store.Config BEFORE
// storeNew constructs the *store.Store, mirroring applyTimeTravelConfig's
// own test above and write_hygiene_wiring_test.go's established pattern.
func TestApplyEncryptionConfig(t *testing.T) {
	cfg := store.Config{}
	app := config.Config{Encryption: config.EncryptionConfig{
		Enabled: true, KeychainService: "custom-service", AllowPlaintextFallback: true,
	}}
	applyEncryptionConfig(&cfg, &app)
	if !cfg.EncryptionEnabled {
		t.Error("EncryptionEnabled = false, want true")
	}
	if cfg.EncryptionKeychainService != "custom-service" {
		t.Errorf("EncryptionKeychainService = %q, want %q", cfg.EncryptionKeychainService, "custom-service")
	}
	if !cfg.EncryptionAllowPlaintextFallback {
		t.Error("EncryptionAllowPlaintextFallback = false, want true")
	}
}
