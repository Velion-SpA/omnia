package engram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/velion/omnia/internal/core"
)

const (
	maxContentLen = 50000
)

// sessionIDFor returns a per-project session ID so that observations from
// different projects are not mixed under a single shared session (W6).
func sessionIDFor(project string) string {
	return "omnia-" + project
}

// Client writes observations to the Engram daemon.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new Engram Client.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Health checks if the Engram daemon is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("engram health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("engram health returned %d", resp.StatusCode)
	}
	return nil
}

// EnsureSession creates or updates the ingestor session for the given project.
// Session creation is idempotent (INSERT OR IGNORE semantics in Engram).
// The directory parameter is used as the session working directory; pass the
// omnia repo path when available, or "/" as a neutral sentinel.
func (c *Client) EnsureSession(ctx context.Context, project, directory string) error {
	body := map[string]string{
		"id":        sessionIDFor(project),
		"project":   project,
		"directory": directory,
	}
	return c.post(ctx, "/sessions", body, nil)
}

// Write persists an Item as an Engram observation. Falls back to CLI if daemon is down.
//
// Provenance (bugfix, this change): item.Source used to be silently dropped
// here — the request body never included it, so every collector (github,
// discord, jira, confluence, ...) wrote observations with no write-time
// provenance signal. store.AddObservationParams (internal/store/store.go)
// already has a `Source string \`json:"source,omitempty"\`` field and
// already feeds it into classifyTrust to set trust_tag on save
// (internal/store/store.go:3277-3278, 3493, 3501) — the server side was
// never the problem, the client just never sent the value it had. Verified
// by reading internal/server/server.go's handleAddObservation, which JSON
// decodes the request body straight into store.AddObservationParams with no
// field allowlist, so this is a pure client-side fix with no server change
// required.
func (c *Client) Write(ctx context.Context, item core.Item) error {
	// Truncate content to Engram's max.
	content := item.Content
	if len([]rune(content)) > maxContentLen {
		content = string([]rune(content)[:maxContentLen])
	}

	obs := map[string]interface{}{
		"session_id": sessionIDFor(item.Project),
		"type":       item.Type,
		"title":      item.Title,
		"content":    content,
		"project":    item.Project,
		"scope":      "project",
		"topic_key":  item.TopicKey,
		"source":     item.Source,
	}

	if err := c.post(ctx, "/observations", obs, nil); err != nil {
		// Attempt CLI fallback.
		return c.cliWrite(item)
	}
	return nil
}

// observationResponse is the wire shape POST /observations and GET /search
// return.
//
// SyncID (bugfix, this change): added so this client is ready to decode the
// stable cross-replica identifier the moment the server emits it. It does
// NOT do that yet — verified by reading internal/server/server.go's
// handleAddObservation, which responds with only
// `map[string]any{"id": id, "status": "saved"}`
// (internal/server/server.go:484), and internal/store.SaveResult
// (internal/store/store.go:2943), which AddObservation/SaveObservation
// return and which has no SyncID field to source one from. Until that
// lands, this field always decodes to "". See this change's final report
// for the exact wiring request (touches internal/store and
// internal/server, both outside this package's ownership): SaveResult
// needs a SyncID field populated from the resolved row, and
// handleAddObservation needs to put it in the response body. That unblocks
// store.UpsertAnchor(ObsSyncID: ...) for callers like
// internal/source/repodoc (see its anchor.go), which need the sync_id of
// the observation they just wrote and currently have no way to get one —
// the integer `id` is NOT a substitute (ids are not stable across
// replicas).
type observationResponse struct {
	ID     int    `json:"id"`
	SyncID string `json:"sync_id"`
	Status string `json:"status"`
}

// WriteAndGetID saves an observation and returns its ID (used in tests/smoke).
//
// See Write's doc comment for the source-provenance fix and
// observationResponse's doc comment for the sync_id wiring gap this
// function still has: the returned int is the row id, not a sync_id, for
// the same reason Write's request now carries source but its response
// still cannot carry sync_id.
func (c *Client) WriteAndGetID(ctx context.Context, item core.Item) (int, error) {
	content := item.Content
	if len([]rune(content)) > maxContentLen {
		content = string([]rune(content)[:maxContentLen])
	}
	obs := map[string]interface{}{
		"session_id": sessionIDFor(item.Project),
		"type":       item.Type,
		"title":      item.Title,
		"content":    content,
		"project":    item.Project,
		"scope":      "project",
		"topic_key":  item.TopicKey,
		"source":     item.Source,
	}
	var result observationResponse
	if err := c.post(ctx, "/observations", obs, &result); err != nil {
		return 0, err
	}
	return result.ID, nil
}

// Delete hard-deletes an observation by ID.
func (c *Client) Delete(ctx context.Context, id int) error {
	rawURL := fmt.Sprintf("%s/observations/%d?hard=true", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("engram delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("engram delete returned %d", resp.StatusCode)
	}
	return nil
}

// Search queries Engram. S5: uses url.Values.Encode to safely encode query parameters.
func (c *Client) Search(ctx context.Context, query, project string, limit int) ([]observationResponse, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("project", project)
	params.Set("limit", fmt.Sprintf("%d", limit))
	rawURL := c.baseURL + "/search?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("engram search: %w", err)
	}
	defer resp.Body.Close()
	var results []observationResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return results, nil
}

func (c *Client) post(ctx context.Context, path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("engram POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("engram POST %s returned %d", path, resp.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// cliWrite is a fallback that writes an observation via the engram CLI binary.
// Note: content is passed as a positional argument, which means long content is
// visible in the process table (ps(1)). This is acceptable as a best-effort
// fallback when the daemon is unreachable; prefer the HTTP path in production.
//
// Provenance gap (bugfix, this change — deliberately NOT closed here): this
// fallback still cannot carry item.Source, unlike Write/WriteAndGetID's HTTP
// path above. Verified by reading cmd/omnia/main.go's cmdSave (the `omnia
// save` / `engram save` command this exec.Command invokes,
// cmd/omnia/main.go:1689-1725): its flag switch only recognizes --type,
// --project, --scope, and --topic — there is no --source flag to pass, and
// an unrecognized flag is silently ignored rather than erring, so adding
// "--source" here would look like a fix while doing nothing. cmd/omnia is
// outside this package's ownership; the wiring request is to add a --source
// flag to cmdSave that threads through to store.AddObservationParams.Source
// the same way --type/--project/--scope/--topic already do.
func (c *Client) cliWrite(item core.Item) error {
	cmd := exec.Command("engram", "save", item.Title, item.Content,
		"--type", item.Type,
		"--project", item.Project,
		"--scope", "project",
		"--topic", item.TopicKey,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("engram CLI fallback failed: %w\noutput: %s", err, out)
	}
	return nil
}
