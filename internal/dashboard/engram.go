// Package dashboard provides the Omnia web UI served by "omnia dashboard".
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Observation is the Engram observation as returned by the HTTP API.
type Observation struct {
	ID            int    `json:"id"`
	SyncID        string `json:"sync_id"`
	SessionID     string `json:"session_id"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	Project       string `json:"project"`
	Scope         string `json:"scope"`
	TopicKey      string `json:"topic_key"`
	RevisionCount int    `json:"revision_count"`
	DuplicateCount int   `json:"duplicate_count"`
	LastSeenAt    string `json:"last_seen_at"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Rank          float64 `json:"rank"`
}

// engramClient talks to the Engram HTTP API (read + write).
// It is intentionally separate from internal/sink/engram to avoid coupling the
// dashboard to the ingestion pipeline's internal types.
type engramClient struct {
	baseURL    string
	httpClient *http.Client
}

func newEngramClient(baseURL string) *engramClient {
	return &engramClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Health returns nil when the Engram daemon responds OK.
func (c *engramClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %d", resp.StatusCode)
	}
	return nil
}

// Search calls GET /search and returns matching observations.
// NOTE: Engram has no "list all" endpoint. We use a keyword search with a broad
// term as the closest available approximation for browsing. Limit should be set
// generously (e.g. 200) so the caller can paginate or filter client-side.
// This is the key API limitation of v1 — future Omnia index will provide proper
// listing without FTS dependency.
func (c *engramClient) Search(ctx context.Context, query, project string, limit int) ([]Observation, error) {
	params := url.Values{}
	params.Set("q", query)
	if project != "" {
		params.Set("project", project)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))
	rawURL := c.baseURL + "/search?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned %d", resp.StatusCode)
	}
	var obs []Observation
	if err := json.NewDecoder(resp.Body).Decode(&obs); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	if obs == nil {
		obs = []Observation{}
	}
	return obs, nil
}

// GetObservation calls GET /observations/{id}.
func (c *engramClient) GetObservation(ctx context.Context, id int) (*Observation, error) {
	rawURL := fmt.Sprintf("%s/observations/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get observation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("observation %d not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get observation returned %d", resp.StatusCode)
	}
	var obs Observation
	if err := json.NewDecoder(resp.Body).Decode(&obs); err != nil {
		return nil, fmt.Errorf("decode observation: %w", err)
	}
	return &obs, nil
}

// PatchObservation calls PATCH /observations/{id} with the provided fields.
// Only non-empty string fields are sent.
func (c *engramClient) PatchObservation(ctx context.Context, id int, title, content, obsType string) error {
	body := map[string]string{}
	if title != "" {
		body["title"] = title
	}
	if content != "" {
		body["content"] = content
	}
	if obsType != "" {
		body["type"] = obsType
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("%s/observations/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, rawURL, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch observation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("patch returned %d", resp.StatusCode)
	}
	return nil
}

// DeleteObservation calls DELETE /observations/{id}. If hard is true, appends ?hard=true.
func (c *engramClient) DeleteObservation(ctx context.Context, id int, hard bool) error {
	rawURL := fmt.Sprintf("%s/observations/%d", c.baseURL, id)
	if hard {
		rawURL += "?hard=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete observation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete returned %d", resp.StatusCode)
	}
	return nil
}

// formatAge returns a human-readable age string from an Engram timestamp string
// (format: "2006-01-02 15:04:05"). Engram stores timestamps in UTC without a
// timezone suffix, so we parse them as UTC explicitly.
func formatAge(ts string) string {
	if ts == "" {
		return "unknown"
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2, 2006")
	}
}
