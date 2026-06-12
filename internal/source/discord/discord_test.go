package discord_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/velion/omnia/internal/config"
	discord "github.com/velion/omnia/internal/source/discord"
)

func TestFetchChannelFromFixture(t *testing.T) {
	msgData, err := os.ReadFile("../../testdata/discord_messages.json")
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
	src := discord.NewWithBaseURL(channels, "omnia", "fake-token", nil, srv.URL)

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
