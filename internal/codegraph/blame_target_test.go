package codegraph

import "testing"

// ─── ParseBlameTarget: optional line (issue #225) ────────────────────────────

func TestParseBlameTargetAcceptsBareFileAsLineZero(t *testing.T) {
	file, line, err := ParseBlameTarget("internal/store/anchors.go")
	if err != nil {
		t.Fatalf("a bare path must be accepted: %v", err)
	}
	if file != "internal/store/anchors.go" || line != 0 {
		t.Fatalf("expected (path, 0), got (%q, %d)", file, line)
	}
}

func TestParseBlameTargetStillParsesFileLine(t *testing.T) {
	file, line, err := ParseBlameTarget("internal/store/anchors.go:421")
	if err != nil {
		t.Fatalf("ParseBlameTarget: %v", err)
	}
	if file != "internal/store/anchors.go" || line != 421 {
		t.Fatalf("expected (path, 421), got (%q, %d)", file, line)
	}
}

// A path can legitimately contain a colon; only a trailing numeric suffix is a
// line. "weird:name.go" must be read as a filename, not as a broken line spec.
func TestParseBlameTargetTreatsNonNumericSuffixAsPartOfPath(t *testing.T) {
	file, line, err := ParseBlameTarget("pkg/weird:name.go")
	if err != nil {
		t.Fatalf("expected a colon in a filename to be tolerated: %v", err)
	}
	if file != "pkg/weird:name.go" || line != 0 {
		t.Fatalf("expected the whole string as the path, got (%q, %d)", file, line)
	}
}

func TestParseBlameTargetRejectsZeroAndNegativeLines(t *testing.T) {
	for _, bad := range []string{"a.go:0", "a.go:-3"} {
		if _, _, err := ParseBlameTarget(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

// ParseFileLine keeps its strict contract — callers that require a line still get one.
func TestParseFileLineStillRequiresALine(t *testing.T) {
	if _, _, err := ParseFileLine("internal/store/anchors.go"); err == nil {
		t.Fatal("ParseFileLine must still reject a bare path")
	}
}
