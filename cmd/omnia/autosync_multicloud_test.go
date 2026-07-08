package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Velion-SpA/omnia/internal/cloud/autosync"
	"github.com/Velion-SpA/omnia/internal/store"
)

// fakeCloudMutationServer is a minimal /sync/mutations/push+pull stub used to
// prove autosync delivers to a given cloud. It requires the given bearer token
// and counts pushed mutation entries.
type fakeCloudMutationServer struct {
	token  string
	pushed int32
}

func (f *fakeCloudMutationServer) newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/sync/mutations/push":
			var req struct {
				Entries []map[string]any `json:"entries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			atomic.AddInt32(&f.pushed, int32(len(req.Entries)))
			seqs := make([]int64, len(req.Entries))
			for i := range seqs {
				seqs[i] = int64(i + 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})
		case "/sync/mutations/pull":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  []any{},
				"has_more":   false,
				"latest_seq": 0,
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTryStartAutosyncDeliversToTwoConfiguredClouds proves the OBL-07
// acceptance criterion: with two clouds configured (the default plus a named
// alias registered in OBL-06's fan-out registry), a SINGLE local write is
// delivered to BOTH clouds by the background autosync manager set —
// autosync is no longer hard-wired to the default cloud only.
func TestTryStartAutosyncDeliversToTwoConfiguredClouds(t *testing.T) {
	cfg := testConfig(t)

	defaultCloud := &fakeCloudMutationServer{token: "default-token"}
	defaultSrv := defaultCloud.newServer(t)

	workCloud := &fakeCloudMutationServer{token: "work-token"}
	workSrv := workCloud.newServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "default-token")
	t.Setenv("ENGRAM_CLOUD_SERVER", defaultSrv.URL)

	if err := saveCloudConfigV2Entry(cfg, "work", workSrv.URL, "work-token", ""); err != nil {
		t.Fatalf("add work cloud: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	// Register "work" in the fan-out registry BEFORE the local write, exactly as
	// `omnia cloud add`/`omnia sync` do in production (reconcileCloudFanoutTargets),
	// so the write below fans out into the "work:proj-a" queue (OBL-06).
	if err := s.ReplaceCloudSyncTargets([]string{"work"}); err != nil {
		t.Fatalf("register work fan-out target: %v", err)
	}

	if err := s.CreateSession("multicloud-autosync-session", "proj-a", t.TempDir()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "multicloud-autosync-session",
		Type:      "bugfix",
		Title:     "multi-cloud autosync proof",
		Content:   "multi-cloud autosync proof mutation",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	oldNewAutosyncManager := newAutosyncManager
	t.Cleanup(func() { newAutosyncManager = oldNewAutosyncManager })
	newAutosyncManager = func(s *store.Store, transport autosync.CloudTransport, cfg autosync.Config) startableAutosyncManager {
		cfg.DebounceDuration = 5 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
		cfg.BaseBackoff = 20 * time.Millisecond
		cfg.MaxBackoff = 50 * time.Millisecond
		return autosync.New(s, transport, cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, stop := tryStartAutosync(ctx, s, cfg)
	if mgr == nil || stop == nil {
		t.Fatal("expected tryStartAutosync to start the default cloud manager")
	}
	defer stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&defaultCloud.pushed) > 0 && atomic.LoadInt32(&workCloud.pushed) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if atomic.LoadInt32(&defaultCloud.pushed) == 0 {
		t.Fatal("expected the default cloud to receive the pushed mutation")
	}
	if atomic.LoadInt32(&workCloud.pushed) == 0 {
		t.Fatal(`expected the additional configured cloud ("work") to receive the SAME local write`)
	}
}

// TestTryStartAutosyncSkipsUnconfiguredNamedCloud proves a named cloud missing
// a token/server is skipped (logged, not fatal) while the default cloud still
// starts normally — multi-cloud fan-out must never break single-cloud setups.
func TestTryStartAutosyncSkipsUnconfiguredNamedCloud(t *testing.T) {
	cfg := testConfig(t)

	defaultCloud := &fakeCloudMutationServer{token: "default-token"}
	defaultSrv := defaultCloud.newServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "default-token")
	t.Setenv("ENGRAM_CLOUD_SERVER", defaultSrv.URL)

	// "incomplete" cloud has a server but no token — must be skipped, not fatal.
	if err := saveCloudConfigV2Entry(cfg, "incomplete", "https://incomplete.example.test", "", ""); err != nil {
		t.Fatalf("add incomplete cloud: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	oldNewAutosyncManager := newAutosyncManager
	t.Cleanup(func() { newAutosyncManager = oldNewAutosyncManager })
	newAutosyncManager = func(s *store.Store, transport autosync.CloudTransport, cfg autosync.Config) startableAutosyncManager {
		cfg.DebounceDuration = 5 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
		return autosync.New(s, transport, cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, stop := tryStartAutosync(ctx, s, cfg)
	if mgr == nil || stop == nil {
		t.Fatal("expected the default cloud manager to still start despite an unconfigured named cloud")
	}
	stop()
}
