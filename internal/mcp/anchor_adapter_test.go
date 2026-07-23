package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/store"
)

// newTestAnchorObservation seeds a minimal bugfix observation to link test
// anchors to, returning its full store.Observation.
func newTestAnchorObservation(t *testing.T, s *store.Store) *store.Observation {
	t.Helper()
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Auth middleware bugfix",
		Content:   "Fixed a bug in the auth middleware.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	return obs
}

// fakeAnchorCapturer scripts anchor.Probe.Capture's outcome per call, so
// tests never need to spawn real git or touch anchor.Probe's unexported
// runGit field.
type fakeAnchorCapturer struct {
	// byFile is consulted first, keyed by the input's File.
	byFile map[string]fakeCaptureResult
	// fallback is used when the file isn't in byFile.
	fallback fakeCaptureResult
	calls    []CodeAnchorInput
}

type fakeCaptureResult struct {
	anc anchor.Anchor
	err error
}

func (f *fakeAnchorCapturer) Capture(_ context.Context, _, file, symbol string, start, end int) (anchor.Anchor, error) {
	f.calls = append(f.calls, CodeAnchorInput{File: file, Symbol: symbol, LineStart: start, LineEnd: end})
	if r, ok := f.byFile[file]; ok {
		return r.anc, r.err
	}
	return f.fallback.anc, f.fallback.err
}

func TestAnchorCaptureAdapter_CapturesAndPersistsAnchors(t *testing.T) {
	s := newMCPTestStore(t)
	obs := newTestAnchorObservation(t, s)

	fake := &fakeAnchorCapturer{
		byFile: map[string]fakeCaptureResult{
			"internal/auth/middleware.go": {
				anc: anchor.Anchor{
					File: "internal/auth/middleware.go", Symbol: "Middleware",
					LineStart: 10, LineEnd: 20,
					BlameSHA: "d8d74ffa1489ad18fa062f76a5455cf41f22ea9d", BlameAt: "2024-01-01T00:00:00Z",
					ContentHash: "hash1", RepoRoot: "/repo",
				},
			},
		},
	}
	adapter := &AnchorCaptureAdapter{Probe: fake, Store: s}

	captured, errs := adapter.Capture(context.Background(), "/repo", obs.SyncID, []CodeAnchorInput{
		{File: "internal/auth/middleware.go", Symbol: "Middleware", LineStart: 10, LineEnd: 20},
	})
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if captured != 1 {
		t.Fatalf("expected 1 anchor captured, got %d", captured)
	}

	anchors, err := s.ListActiveAnchors("engram")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("expected 1 persisted anchor, got %d", len(anchors))
	}
	if anchors[0].ObsSyncID != obs.SyncID {
		t.Errorf("expected anchor linked to %q, got %q", obs.SyncID, anchors[0].ObsSyncID)
	}
	if anchors[0].FilePath != "internal/auth/middleware.go" {
		t.Errorf("unexpected file_path: %q", anchors[0].FilePath)
	}
}

func TestAnchorCaptureAdapter_SkipsFailedCaptureWithoutPanicking(t *testing.T) {
	s := newMCPTestStore(t)
	obs := newTestAnchorObservation(t, s)

	fake := &fakeAnchorCapturer{fallback: fakeCaptureResult{err: anchor.ErrGitNotInstalled}}
	adapter := &AnchorCaptureAdapter{Probe: fake, Store: s}

	captured, errs := adapter.Capture(context.Background(), "/repo", obs.SyncID, []CodeAnchorInput{
		{File: "foo.go", Symbol: "Foo", LineStart: 1, LineEnd: 3},
	})
	if captured != 0 {
		t.Errorf("expected 0 captured, got %d", captured)
	}
	if len(errs) != 1 || !errors.Is(errs[0], anchor.ErrGitNotInstalled) {
		t.Errorf("expected a wrapped ErrGitNotInstalled, got %v", errs)
	}

	anchors, err := s.ListActiveAnchors("engram")
	if err != nil {
		t.Fatalf("ListActiveAnchors: %v", err)
	}
	if len(anchors) != 0 {
		t.Errorf("expected no anchors persisted on capture failure, got %d", len(anchors))
	}
}

func TestAnchorCaptureAdapter_EmptyInputsIsNoOp(t *testing.T) {
	s := newMCPTestStore(t)
	fake := &fakeAnchorCapturer{}
	adapter := &AnchorCaptureAdapter{Probe: fake, Store: s}

	captured, errs := adapter.Capture(context.Background(), "/repo", "obs-whatever", nil)
	if captured != 0 || len(errs) != 0 {
		t.Fatalf("expected no-op for empty inputs, got captured=%d errs=%v", captured, errs)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected no Capture calls for empty inputs")
	}
}
