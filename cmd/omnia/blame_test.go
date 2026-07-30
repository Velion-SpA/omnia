package main

import (
	"strings"
	"testing"

	"github.com/velion/omnia/internal/store"
)

func TestCmdBlameDisabledDoesNotOpenAnchorStore(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "omnia", "blame", "outside.go:1")
	stdout, stderr := captureOutput(t, func() { cmdBlame(cfg) })
	if stderr != "" || !strings.Contains(stdout, "capability disabled") {
		t.Fatalf("expected structured disabled response, stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdBlameOutsideGitRepoReturnsEmptyReason(t *testing.T) {
	cfg := testConfig(t)
	old := loadCodeGraphConfig
	loadCodeGraphConfig = func() (bool, error) { return true, nil }
	t.Cleanup(func() { loadCodeGraphConfig = old })
	withArgs(t, "omnia", "blame", t.TempDir()+"/outside.go:1")
	stdout, stderr := captureOutput(t, func() { cmdBlame(cfg) })
	if stderr != "" || !strings.Contains(stdout, "no repo context") || !strings.Contains(stdout, "hits: 0") {
		t.Fatalf("expected empty no-repo response, stdout=%q stderr=%q", stdout, stderr)
	}
}

var _ = store.AnchorStatusActive
