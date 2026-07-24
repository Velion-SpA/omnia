package mcp

import (
	"context"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/project"
	"github.com/velion/omnia/internal/store"
)

// ─── Issue #140: mem_update must not silently reassign an observation's ────
// project by re-resolving it from cwd/git auto-detection, ignoring the
// process-level project override (cfg.DefaultProject) that mem_save already
// honors. These tests reproduce the bug and lock in the fixed semantics:
// mem_update unconditionally PRESERVES the observation's existing project —
// it has no explicit "project" argument at all (REQ-308 write-tool
// contract: only mem_save/mem_save_prompt expose that, for ambiguous-project
// recovery), and it never falls back to cwd/override auto-detection either.

// TestHandleUpdate_PreservesProjectUnderConflictingCwdOverride is the RED
// repro: cwd sits inside a DIFFERENT git repo than the configured process
// override. Before the fix, handleUpdate's response envelope re-resolved the
// project from that cwd via a bare resolveWriteProject() call (ignoring
// cfg.DefaultProject entirely), reporting a project unrelated to where the
// observation actually lives — the exact asymmetry with mem_save described
// in issue #140.
func TestHandleUpdate_PreservesProjectUnderConflictingCwdOverride(t *testing.T) {
	// cwd inside git repo "Y" — auto-detection from here would resolve to
	// the temp dir's basename, NOT the process override below.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.CreateSession("s-140-repro", "override-project-x", "/work/x"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-140-repro",
		Type:      "manual",
		Title:     "Original title",
		Content:   "Original content",
		Project:   "override-project-x",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Process-level override "X" (e.g. `omnia mcp --project override-project-x`).
	cfg := MCPConfig{DefaultProject: "override-project-x"}
	res, err := handleUpdate(s, cfg)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":    float64(id),
		"title": "Updated title",
	}}})
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	if body["project"] != "override-project-x" {
		t.Fatalf("envelope project must reflect the observation's actual project, got %v (want override-project-x)", body["project"])
	}
	if body["project_source"] != project.SourcePreserved {
		t.Fatalf("expected project_source=%q, got %v", project.SourcePreserved, body["project_source"])
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if obs.Project == nil || *obs.Project != "override-project-x" {
		t.Fatalf("observation must NOT be reassigned: expected stored project %q, got %v", "override-project-x", obs.Project)
	}
}

// TestHandleUpdate_ProjectArgumentIsIgnored locks in the write-tool contract
// (REQ-308, enforced separately by TestWriteSchema_ProjectFieldOnlyForAmbiguousRecovery):
// mem_update's schema does not accept "project" at all — only
// mem_save/mem_save_prompt expose it, for ambiguous-project recovery. Even
// if a caller sends a "project" argument anyway (the args map is not
// schema-validated at the handler level), the handler must ignore it rather
// than silently moving the observation.
func TestHandleUpdate_ProjectArgumentIsIgnored(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-140-ignored", "source-project", "/work/source"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-140-ignored",
		Type:      "manual",
		Title:     "Do not move me",
		Content:   "content",
		Project:   "source-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	res, err := handleUpdate(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      float64(id),
		"title":   "still not moved",
		"project": "some-other-project",
	}}})
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	if body["project"] != "source-project" {
		t.Fatalf("a stray 'project' argument must be ignored, got %v", body["project"])
	}
	if body["project_source"] != project.SourcePreserved {
		t.Fatalf("expected project_source=%q, got %v", project.SourcePreserved, body["project_source"])
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if obs.Project == nil || *obs.Project != "source-project" {
		t.Fatalf("observation must NOT move via a stray 'project' argument, got %v", obs.Project)
	}
}

// TestHandleSaveThenUpdate_ProjectResolutionSymmetryUnderProcessOverride is
// the mem_save/mem_update symmetry test: under the SAME process override
// and the SAME conflicting cwd, both tools must agree on which project a
// write belongs to.
func TestHandleSaveThenUpdate_ProjectResolutionSymmetryUnderProcessOverride(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "override-project-x"}

	saveRes, err := handleSave(s, cfg, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "seed memory",
		"content": "seed content",
	}}})
	if err != nil || saveRes.IsError {
		t.Fatalf("save: err=%v isError=%v", err, saveRes.IsError)
	}
	saveBody := callResultJSON(t, saveRes)
	if saveBody["project"] != "override-project-x" {
		t.Fatalf("mem_save must honor the process override, got %v", saveBody["project"])
	}
	savedID := int64(saveBody["id"].(float64))

	updateRes, err := handleUpdate(s, cfg)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      float64(savedID),
		"content": "revised content",
	}}})
	if err != nil || updateRes.IsError {
		t.Fatalf("update: err=%v isError=%v", err, updateRes.IsError)
	}
	updateBody := callResultJSON(t, updateRes)

	if updateBody["project"] != saveBody["project"] {
		t.Fatalf("mem_save/mem_update project resolution diverged under the same override: save=%v update=%v", saveBody["project"], updateBody["project"])
	}
}

// TestHandleCapturePassive_HonorsProcessOverride covers the same-shaped
// sibling bug found while auditing write tools for issue #140:
// handleCapturePassive is a genuine write path (it persists new
// observations via PassiveCapture) that also called the bare
// resolveWriteProject() and ignored cfg.DefaultProject, misdirecting new
// captures to whatever project cwd auto-detected to.
func TestHandleCapturePassive_HonorsProcessOverride(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "override-project-x"}

	res, err := handleCapturePassive(s, cfg, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "## Key Learnings:\n\n1. process override must win over cwd auto-detect\n",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	body := callResultJSON(t, res)
	if body["project"] != "override-project-x" {
		t.Fatalf("mem_capture_passive must honor the process override, got %v", body["project"])
	}

	obs, err := s.RecentObservations("override-project-x", "project", 5)
	if err != nil || len(obs) == 0 {
		t.Fatalf("expected captured observation under the override project, err=%v len=%d", err, len(obs))
	}
}
