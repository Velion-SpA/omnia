package discord_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Velion-SpA/omnia/internal/config"
	discord "github.com/Velion-SpA/omnia/internal/source/discord"
)

// stubDiscordRouter satisfies the discord source's projectRouter interface.
type stubDiscordRouter struct {
	project string
}

func (r *stubDiscordRouter) ResolveDiscord(_, _ string) string { return r.project }

// stubDiscordState is a minimal StateStore for discord tests.
type stubDiscordState struct {
	cursors map[string]string
}

func newDiscordStubState() *stubDiscordState {
	return &stubDiscordState{cursors: make(map[string]string)}
}

func (s *stubDiscordState) GetCursor(source, key string) (string, bool) {
	v, ok := s.cursors[source+":"+key]
	return v, ok
}

func (s *stubDiscordState) SetCursor(source, key, value string) error {
	s.cursors[source+":"+key] = value
	return nil
}

func (s *stubDiscordState) Flush() error { return nil }

func TestFetchChannelFromFixture(t *testing.T) {
	msgData, err := os.ReadFile("../../../testdata/discord_messages.json")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/messages") {
			w.Write(msgData)
		}
	}))
	defer srv.Close()

	channels := []config.ChannelConfig{{ID: "123", Name: "dev-ops", Guild: "test-guild"}}
	src := discord.NewWithBaseURL(channels, &stubDiscordRouter{"omnia"}, "fake-token", nil, srv.URL)

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one daily digest item")
	}

	item := items[0]
	if item.Type != "discord-digest" {
		t.Errorf("type = %q, want discord-digest", item.Type)
	}
	if !strings.Contains(item.Title, "dev-ops") {
		t.Errorf("title %q should contain channel name", item.Title)
	}
	if !strings.Contains(item.Content, "Participants") {
		t.Errorf("content should have Participants section")
	}
	if !strings.Contains(item.Content, "Keywords:") {
		t.Errorf("content should have Keywords section")
	}
}

// TestDiscordMultiPagePagination verifies that the source fetches across multiple
// pages using the "after" cursor and that in-memory cursors advance per page (C4).
//
// Discord's API returns messages newest-first. The source code reverses each page to
// chronological order and then advances the afterID cursor to msgs[last].ID (highest ID
// in the page). The stub server simulates this by returning messages in descending ID
// order (newest-first, matching real Discord behavior) and serves page 2 only when the
// "after" query param equals the highest ID from page 1.
func TestDiscordMultiPagePagination(t *testing.T) {
	// Page IDs: page1 covers 1000–1099 (100 messages, triggers another fetch because
	// len == pageSize). Page 2 covers 2000–2004 (5 messages, terminates pagination).
	// Discord returns newest-first, so we return pages in descending ID order.
	const numPage1 = 100

	// makeMessagesDesc builds a slice of messages with IDs [start+count-1 .. start]
	// (descending, newest-first, simulating Discord API order).
	makeMessagesDesc := func(start, count int) []map[string]interface{} {
		msgs := make([]map[string]interface{}, count)
		for i := 0; i < count; i++ {
			id := start + count - 1 - i // descending
			msgs[i] = map[string]interface{}{
				"id":        fmt.Sprintf("%d", id),
				"content":   fmt.Sprintf("message %d", id),
				"timestamp": time.Date(2024, 1, 15, 10, i%60, 0, 0, time.UTC).Format(time.RFC3339),
				"author":    map[string]string{"username": "alice", "id": "1"},
				"type":      0,
			}
		}
		return msgs
	}

	page1 := makeMessagesDesc(1000, numPage1) // IDs 1099..1000 (newest-first)
	page2 := makeMessagesDesc(2000, 5)        // IDs 2004..2000 (newest-first)

	// After fetchPage reverses page1 to chronological order, msgs[last].ID = "1099"
	// (the highest ID), which becomes the next afterID cursor.
	page1HighestID := fmt.Sprintf("%d", 1000+numPage1-1) // "1099"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		after := r.URL.Query().Get("after")
		w.Header().Set("Content-Type", "application/json")
		switch after {
		case "", "0":
			// "0" is the snowflake for time.Time{} (zero time), treated as "from the beginning".
			json.NewEncoder(w).Encode(page1)
		case page1HighestID:
			json.NewEncoder(w).Encode(page2)
		default:
			json.NewEncoder(w).Encode([]interface{}{})
		}
	}))
	defer srv.Close()

	st := newDiscordStubState()
	channels := []config.ChannelConfig{{ID: "555", Name: "general", Guild: "my-guild"}}
	src := discord.NewWithBaseURL(channels, &stubDiscordRouter{"omnia"}, "fake-token", st, srv.URL)

	items, err := src.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one digest item")
	}

	// Verify that messages from both pages appear in the digest(s).
	totalMsgLines := 0
	for _, item := range items {
		totalMsgLines += strings.Count(item.Content, "message ")
	}
	wantTotal := numPage1 + 5
	if totalMsgLines != wantTotal {
		t.Errorf("expected %d message lines across digests, got %d", wantTotal, totalMsgLines)
	}

	// In-memory cursor should equal the highest ID seen from page 2 (2004).
	lastPage2ID := fmt.Sprintf("%d", 2000+5-1) // "2004"
	if v, ok := st.GetCursor("discord", "555"); !ok || v != lastPage2ID {
		t.Errorf("discord cursor = %q ok=%v, want %q", v, ok, lastPage2ID)
	}
}
