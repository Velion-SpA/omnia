package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a tiny helper that creates a file with content under dir.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMigrateCopiesAndRenamesDB(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, ".engram")
	dst := filepath.Join(root, ".omnia")

	// Seed a legacy data dir: DB plus WAL/SHM sidecars, a sibling config, and a
	// nested directory — everything must survive the copy.
	writeFile(t, filepath.Join(src, "engram.db"), "MAIN")
	writeFile(t, filepath.Join(src, "engram.db-wal"), "WAL")
	writeFile(t, filepath.Join(src, "engram.db-shm"), "SHM")
	writeFile(t, filepath.Join(src, "cloud.json"), `{"server":"x"}`)
	writeFile(t, filepath.Join(src, "sessions", "s1.json"), "SESSION")

	if err := Migrate(src, dst); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// DB files renamed to omnia.db*.
	for suffix, want := range map[string]string{"": "MAIN", "-wal": "WAL", "-shm": "SHM"} {
		got, err := os.ReadFile(filepath.Join(dst, "omnia.db"+suffix))
		if err != nil {
			t.Fatalf("read omnia.db%s: %v", suffix, err)
		}
		if string(got) != want {
			t.Errorf("omnia.db%s = %q, want %q", suffix, got, want)
		}
		// Legacy name must NOT exist in the new dir.
		if _, err := os.Stat(filepath.Join(dst, "engram.db"+suffix)); !os.IsNotExist(err) {
			t.Errorf("engram.db%s should not exist in migrated dir", suffix)
		}
	}

	// Non-DB files copied verbatim, nested dirs preserved.
	if got, _ := os.ReadFile(filepath.Join(dst, "cloud.json")); string(got) != `{"server":"x"}` {
		t.Errorf("cloud.json not copied verbatim: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "sessions", "s1.json")); string(got) != "SESSION" {
		t.Errorf("nested session not copied: %q", got)
	}

	// Source left completely untouched (non-destructive backup).
	if got, _ := os.ReadFile(filepath.Join(src, "engram.db")); string(got) != "MAIN" {
		t.Errorf("source engram.db modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "omnia.db")); !os.IsNotExist(err) {
		t.Errorf("source dir must not gain omnia.db")
	}
}

func TestMigrateIdempotentWhenDstExists(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, ".engram")
	dst := filepath.Join(root, ".omnia")
	writeFile(t, filepath.Join(src, "engram.db"), "MAIN")
	writeFile(t, filepath.Join(dst, "omnia.db"), "EXISTING")

	if err := Migrate(src, dst); err != nil {
		t.Fatalf("Migrate (existing dst): %v", err)
	}
	// Existing dst must be left as-is, never clobbered.
	if got, _ := os.ReadFile(filepath.Join(dst, "omnia.db")); string(got) != "EXISTING" {
		t.Errorf("existing omnia.db clobbered: %q", got)
	}
}

func TestResolveExplicitWins(t *testing.T) {
	if got := resolveWithHome("/tmp/explicit", t.TempDir()); got != "/tmp/explicit" {
		t.Errorf("explicit dir = %q, want /tmp/explicit", got)
	}
}

func TestResolvePrefersOmniaWhenBothExist(t *testing.T) {
	home := t.TempDir()
	// Both dirs exist → the canonical ~/.omnia wins.
	writeFile(t, filepath.Join(home, ".omnia", "omnia.db"), "NEW")
	writeFile(t, filepath.Join(home, ".engram", "engram.db"), "OLD")
	if got := resolveWithHome("", home); got != filepath.Join(home, ".omnia") {
		t.Errorf("dir = %q, want ~/.omnia", got)
	}
}

func TestResolveUsesLegacyInPlaceWhenOnlyLegacy(t *testing.T) {
	home := t.TempDir()
	// Only a legacy ~/.engram exists → use it IN PLACE: no copy, no ~/.omnia.
	writeFile(t, filepath.Join(home, ".engram", "engram.db"), "OLD")

	got := resolveWithHome("", home)
	if got != filepath.Join(home, ".engram") {
		t.Errorf("dir = %q, want ~/.engram (in place)", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".omnia")); !os.IsNotExist(err) {
		t.Errorf("Resolve must not create ~/.omnia")
	}
	if _, err := os.Stat(filepath.Join(home, ".engram", "omnia.db")); !os.IsNotExist(err) {
		t.Errorf("Resolve must not copy or rename inside the legacy dir")
	}
}

func TestResolveDefaultsToOmniaWhenNeitherExists(t *testing.T) {
	home := t.TempDir()
	if got := resolveWithHome("", home); got != filepath.Join(home, ".omnia") {
		t.Errorf("dir = %q, want ~/.omnia", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".omnia")); !os.IsNotExist(err) {
		t.Errorf("Resolve must not create ~/.omnia")
	}
}

func TestResolveEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OMNIA_DATA_DIR", "/tmp/pinned")
	if got := resolveWithHome("", home); got != "/tmp/pinned" {
		t.Errorf("env dir = %q, want /tmp/pinned", got)
	}
}

// TestHomeDefaultIgnoresEnv locks the property ResolveEmbeddingsDBPath relies
// on (#82): HomeDefault reports the home-based data dir REGARDLESS of an
// OMNIA_DATA_DIR override, so an alternate data dir set via env can be told
// apart from the natural home default.
func TestHomeDefaultIgnoresEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OMNIA_DATA_DIR", filepath.Join(t.TempDir(), "somewhere-else"))
	if got, want := HomeDefault(), filepath.Join(home, ".omnia"); got != want {
		t.Errorf("HomeDefault() = %q, want %q (must ignore OMNIA_DATA_DIR)", got, want)
	}
}

func TestResolveLegacyEnvFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", "/tmp/legacy-pinned")
	if got := resolveWithHome("", home); got != "/tmp/legacy-pinned" {
		t.Errorf("legacy env dir = %q, want /tmp/legacy-pinned", got)
	}
}

func TestDBPathPrefersOmniaThenLegacy(t *testing.T) {
	dir := t.TempDir()
	// Neither file → canonical name.
	if got := DBPath(dir); got != filepath.Join(dir, "omnia.db") {
		t.Errorf("empty dir DBPath = %q, want omnia.db", got)
	}
	// Only legacy → legacy name (read compat).
	writeFile(t, filepath.Join(dir, "engram.db"), "OLD")
	if got := DBPath(dir); got != filepath.Join(dir, "engram.db") {
		t.Errorf("legacy-only DBPath = %q, want engram.db", got)
	}
	// Both present → omnia.db wins.
	writeFile(t, filepath.Join(dir, "omnia.db"), "NEW")
	if got := DBPath(dir); got != filepath.Join(dir, "omnia.db") {
		t.Errorf("both-present DBPath = %q, want omnia.db", got)
	}
}
