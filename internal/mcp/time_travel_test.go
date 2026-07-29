package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/store"
)

func TestRecordedTimeReadHandlers(t *testing.T) {
	cfg, _ := store.DefaultConfig()
	cfg.DataDir, cfg.TimeTravelEnabled = t.TempDir(), true
	s, _ := store.New(cfg)
	defer s.Close()
	_ = s.CreateSession("s1", "omnia", t.TempDir())
	id, _ := s.AddObservation(store.AddObservationParams{SessionID: "s1", Type: "decision", Title: "old title", Content: "old body", Project: "omnia", Scope: "project"})
	_, _ = s.DB().Exec(`UPDATE observations SET created_at='2023-01-01', updated_at='2023-01-01', review_after='2025-01-01' WHERE id=?`, id)
	old, _ := s.GetObservation(id)
	asOf := "2024-01-01"
	title, content := "current title", "current searchable body"
	_, _ = s.UpdateObservation(id, store.UpdateObservationParams{Title: &title, Content: &content})
	source, _ := s.GetObservation(id)
	targetID, _ := s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "replacement",
		Content: "replacement body", Project: "omnia", Scope: "project",
	})
	target, _ := s.GetObservation(targetID)
	_, _ = s.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-recorded-time", SourceID: source.SyncID, TargetID: target.SyncID,
	})
	_, _ = s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID: "rel-recorded-time", Relation: store.RelationSupersedes,
		MarkedByActor: "test", MarkedByKind: "agent",
	})
	anchorID, _ := s.UpsertAnchor(store.UpsertAnchorParams{
		ObsSyncID: source.SyncID, FilePath: "current.go", LineStart: 1, LineEnd: 2,
		BlameSHA: "aaaaaaaa", ContentHash: "hash",
	})
	_ = s.MarkAnchorStale(anchorID, nil)
	_ = s.UpdateAnchorStaleReceipt(anchorID, "bbbbbbbb")
	req := func(args map[string]any) mcppkg.CallToolRequest {
		return mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}}
	}
	activity := NewSessionActivity(time.Minute)
	cases := []struct {
		name string
		call server.ToolHandlerFunc
		args map[string]any
		want string
	}{
		{"get", handleGetObservation(s, MCPConfig{TimeTravelEnabled: true}), map[string]any{"id": float64(id), "as_of": asOf}, "old body"},
		{"search", handleSearch(s, MCPConfig{TimeTravelEnabled: true}, activity), map[string]any{"query": "searchable", "project": "omnia", "as_of": asOf}, "Search limitation"},
		{"context", handleContext(s, MCPConfig{TimeTravelEnabled: true}, activity), map[string]any{"project": "omnia", "as_of": asOf}, "old title"},
		{"zero context", handleContext(s, MCPConfig{TimeTravelEnabled: true}, activity), map[string]any{"project": "omnia", "scope": "personal", "as_of": asOf}, "Recorded-time view"},
		{"invalid get", handleGetObservation(s, MCPConfig{TimeTravelEnabled: true}), map[string]any{"id": float64(id), "as_of": "not-a-time"}, "invalid as_of timestamp"},
		{"lexical explain", handleSearch(s, MCPConfig{TimeTravelEnabled: true, Recall: &recall.Service{}, RecallRanking: config.RankingConfig{RecencyHalfLifeDays: 365}}, activity), map[string]any{"query": "searchable", "project": "omnia", "as_of": asOf, "explain": true}, `"recency":0.5`},
	}
	for _, tt := range cases {
		res, err := tt.call(context.Background(), req(tt.args))
		text := callResultText(t, res)
		if err != nil || !strings.Contains(text, tt.want) {
			t.Fatalf("%s missing %q: %v", tt.name, tt.want, err)
		}
		if tt.name == "context" && strings.Contains(text, "Memory stats:") {
			t.Fatalf("historical context leaked current stats: %s", text)
		}
		if tt.name == "lexical explain" && (!strings.Contains(text, `"fusion":null`) || !strings.Contains(text, `"state":"active"`)) {
			t.Fatalf("historical explain/lifecycle used live semantics: %s", text)
		}
	}
	res, _ := handleGetObservation(s, MCPConfig{})(context.Background(), req(map[string]any{"id": float64(id), "as_of": old.UpdatedAt}))
	live, _ := handleGetObservation(s, MCPConfig{TimeTravelEnabled: true})(context.Background(), req(map[string]any{"id": float64(id)}))
	if text := callResultText(t, res); text != callResultText(t, live) || !strings.Contains(text, content) || strings.Contains(text, "Recorded-time view") {
		t.Fatalf("disabled as_of changed live output: %s", text)
	}
	future, _ := handleGetObservation(s, MCPConfig{TimeTravelEnabled: true})(context.Background(),
		req(map[string]any{"id": float64(id), "as_of": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}))
	if futureText := callResultText(t, future); futureText != callResultText(t, live) {
		t.Fatalf("future as_of changed live output:\n%s\n--- live ---\n%s", futureText, callResultText(t, live))
	}
	nudgeNow := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	nudgeActivity := NewSessionActivity(time.Minute)
	nudgeActivity.now = func() time.Time { return nudgeNow }
	for range 6 {
		nudgeActivity.RecordToolCall(defaultSessionID("omnia"))
	}
	nudgeNow = nudgeNow.Add(2 * time.Minute)
	historicalSearch, _ := handleSearch(s, MCPConfig{
		TimeTravelEnabled: true, StructuralForgetting: config.StructuralForgettingConfig{Enabled: true},
	}, nudgeActivity)(context.Background(), req(map[string]any{
		"query": "searchable", "project": "omnia", "as_of": old.UpdatedAt,
	}))
	historicalText := callResultText(t, historicalSearch)
	for _, leak := range []string{"supersedes:", "anchor current.go", "No mem_save calls"} {
		if strings.Contains(historicalText, leak) {
			t.Fatalf("historical search leaked current annotation %q: %s", leak, historicalText)
		}
	}
	var startedAt string
	_ = s.DB().QueryRow(`SELECT started_at FROM time_travel_metadata WHERE id = 1`).Scan(&startedAt)
	start, _ := time.Parse(time.RFC3339Nano, startedAt)
	zero, _ := handleSearch(s, MCPConfig{TimeTravelEnabled: true}, activity)(context.Background(), req(map[string]any{
		"query": "no-candidate", "project": "omnia", "as_of": start.Add(-time.Second).Format(time.RFC3339Nano),
	}))
	if text := callResultText(t, zero); !strings.Contains(text, startedAt) {
		t.Fatalf("zero-hit pre-boundary search missing persisted boundary %s: %s", startedAt, text)
	}
}

func TestAsOfSchemaFeatureGate(t *testing.T) {
	s := newMCPTestStore(t)
	allow := map[string]bool{"mem_search": true, "mem_context": true, "mem_get_observation": true}
	for _, enabled := range []bool{false, true} {
		tools := NewServerWithConfig(s, MCPConfig{TimeTravelEnabled: enabled}, allow).ListTools()
		for name := range allow {
			_, advertised := tools[name].Tool.InputSchema.Properties["as_of"]
			if advertised != enabled {
				t.Fatalf("%s as_of advertised=%v with feature=%v", name, advertised, enabled)
			}
		}
	}
}
