package diagnostic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/velion/omnia/internal/store"
)

// newDiagnosticTestStoreAt is newDiagnosticTestStore but pointed at an
// explicit dataDir (instead of a bare t.TempDir()), so StoreExposureCheck
// tests can exercise real path/permission evaluation via Store.DataDir()
// without inventing a Scope override field.
func newDiagnosticTestStoreAt(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = dataDir
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStoreExposureCheck_FlagsCloudSyncPath (omnia-provenance-foundation,
// phase 7): the store dir resolving inside a recognized cloud-backup/sync
// folder (iCloud Drive, Dropbox, OneDrive) must be flagged at warning
// severity — a background sync agent could silently replicate the raw
// SQLite file to a third-party service.
func TestStoreExposureCheck_FlagsCloudSyncPath(t *testing.T) {
	cloudLikeDir := filepath.Join(t.TempDir(), "Mobile Documents", "com~apple~CloudDocs", "omnia")
	s := newDiagnosticTestStoreAt(t, cloudLikeDir)

	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store:   s,
		Project: "engram",
	}, CheckStoreExposure)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusWarning {
		t.Fatalf("status = %s, want %s; report=%+v", report.Status, StatusWarning, report)
	}
	found := false
	for _, f := range report.Checks[0].Findings {
		if f.ReasonCode == "store_path_cloud_synced" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected store_path_cloud_synced finding, got %+v", report.Checks[0].Findings)
	}
}

// TestStoreExposureCheck_FlagsLoosePermissions (omnia-provenance-foundation,
// phase 7): a store directory that is group- or world-readable/writable must
// be flagged at warning severity. New() only locks down a FRESH create, so
// this test loosens the dir's mode AFTER creation to simulate a pre-existing,
// looser install.
func TestStoreExposureCheck_FlagsLoosePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "loose-store")
	s := newDiagnosticTestStoreAt(t, dataDir)
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("chmod loose dir: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store:   s,
		Project: "engram",
	}, CheckStoreExposure)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusWarning {
		t.Fatalf("status = %s, want %s; report=%+v", report.Status, StatusWarning, report)
	}
	found := false
	for _, f := range report.Checks[0].Findings {
		if f.ReasonCode == "store_dir_group_or_world_readable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected store_dir_group_or_world_readable finding, got %+v", report.Checks[0].Findings)
	}
}

// TestStoreExposureCheck_OKWhenLockedDownAndNotCloudSynced
// (omnia-provenance-foundation, phase 7): a fresh, owner-only store dir
// outside any recognized cloud-sync folder must report no findings.
func TestStoreExposureCheck_OKWhenLockedDownAndNotCloudSynced(t *testing.T) {
	s := newDiagnosticTestStore(t)

	report, err := NewRunner().RunOne(context.Background(), Scope{
		Store:   s,
		Project: "engram",
	}, CheckStoreExposure)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusOK {
		t.Fatalf("status = %s, want %s; report=%+v", report.Status, StatusOK, report)
	}
}
