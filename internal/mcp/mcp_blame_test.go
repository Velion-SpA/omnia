package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/store"
)

func TestHandleBlameOutsideGitRepoReturnsEmptyResult(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleBlame(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"file": t.TempDir() + "/outside.go", "line": float64(1)}}})
	if err != nil {
		t.Fatalf("handleBlame: %v", err)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "no repo context") || !strings.Contains(text, "hits") {
		t.Fatalf("unexpected result: %s", text)
	}
}

func TestHandleBlameReturnsGroupedPreviewOnlyHits(t *testing.T) {
	s := newMCPTestStore(t)
	repo, file := blameTestRepo(t)
	content := strings.Repeat("secret decision detail ", 40)
	if err := s.CreateSession("blame-session", "blame-project", repo); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{SessionID: "blame-session", Type: "decision", Title: "Use deterministic anchors", Content: content, Project: "blame-project", Scope: "project"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(obsID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if _, err := s.UpsertAnchor(store.UpsertAnchorParams{ObsSyncID: obs.SyncID, RepoRoot: repo, FilePath: "service.go", Symbol: "Run", LineStart: 1, LineEnd: 1, ContentHash: "hash"}); err != nil {
		t.Fatalf("UpsertAnchor: %v", err)
	}
	if hits, err := s.BlameLine(repo, "service.go", 1); err != nil || len(hits) != 1 {
		t.Fatalf("BlameLine setup: hits=%+v err=%v", hits, err)
	}

	res, err := handleBlame(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"file": file, "line": float64(1), "repo_root": repo}}})
	if err != nil || res.IsError {
		t.Fatalf("handleBlame: err=%v result=%s", err, callResultText(t, res))
	}
	raw := callResultText(t, res)
	if strings.Contains(raw, content) || strings.Contains(raw, `"content"`) {
		t.Fatalf("mem_blame leaked full observation content: %s", raw)
	}
	var out struct {
		Line int `json:"line"`
		Hits []struct {
			AnchorStatus string `json:"anchor_status"`
			Range        struct {
				File  string `json:"file"`
				Start int    `json:"start"`
				End   int    `json:"end"`
			} `json:"range"`
			Memories []struct {
				SyncID  string `json:"sync_id"`
				Type    string `json:"type"`
				Title   string `json:"title"`
				Preview string `json:"preview"`
			} `json:"memories"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Line != 1 || len(out.Hits) != 1 || out.Hits[0].AnchorStatus != store.AnchorStatusActive || out.Hits[0].Range.File != "service.go" || len(out.Hits[0].Memories) != 1 || out.Hits[0].Memories[0].SyncID != obs.SyncID || out.Hits[0].Memories[0].Title != obs.Title || out.Hits[0].Memories[0].Preview == "" {
		t.Fatalf("unexpected public response: %#v", out)
	}
}

func TestHandleBlameNoMatchSerializesEmptyHitsArray(t *testing.T) {
	s := newMCPTestStore(t)
	repo, file := blameTestRepo(t)
	res, err := handleBlame(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"file": file, "line": float64(1), "repo_root": repo}}})
	if err != nil || res.IsError {
		t.Fatalf("handleBlame: err=%v result=%s", err, callResultText(t, res))
	}
	if got := callResultText(t, res); !strings.Contains(got, `"hits":[]`) {
		t.Fatalf("expected explicit empty hits array, got %s", got)
	}
}

func TestHandleBlameRejectsFractionalLine(t *testing.T) {
	s := newMCPTestStore(t)
	repo, file := blameTestRepo(t)
	res, err := handleBlame(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"file": file, "line": float64(1.5), "repo_root": repo}}})
	if err != nil || !res.IsError || !strings.Contains(callResultText(t, res), "positive integer") {
		t.Fatalf("expected fractional line validation error, err=%v result=%s", err, callResultText(t, res))
	}
}

func TestNewServerWithConfigDoesNotRegisterBlameWhenDisabled(t *testing.T) {
	srv := NewServerWithConfig(newMCPTestStore(t), MCPConfig{CodeGraph: config.CodeGraphConfig{}}, nil)
	if _, found := srv.ListTools()["mem_blame"]; found {
		t.Fatal("mem_blame must not be registered while code graph is disabled")
	}
}

func blameTestRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	file := filepath.Join(repo, "service.go")
	if err := os.WriteFile(file, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "service.go"}, {"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out)), file
}
