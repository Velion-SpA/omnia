package engram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/velion/omnia/internal/core"
)

const (
	maxContentLen = 50000
	sessionID     = "omnia-ingestor"
)

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
func (c *Client) EnsureSession(ctx context.Context, project, directory string) error {
	body := map[string]string{
		"id":        sessionID,
		"project":   project,
		"directory": directory,
	}
	return c.post(ctx, "/sessions", body, nil)
}

// Write persists an Item as an Engram observation. Falls back to CLI if daemon is down.
func (c *Client) Write(ctx context.Context, item core.Item) error {
	// Truncate content to Engram's max.
	content := item.Content
	if len([]rune(content)) > maxContentLen {
		content = string([]rune(content)[:maxContentLen])
	}

	obs := map[string]interface{}{
		"session_id": sessionID,
		"type":       item.Type,
		"title":      item.Title,
		"content":    content,
		"project":    item.Project,
		"scope":      "project",
		"topic_key":  item.TopicKey,
	}

	if err := c.post(ctx, "/observations", obs, nil); err != nil {
		// Attempt CLI fallback.
		return c.cliWrite(item)
	}
	return nil
}

type observationResponse struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}

// WriteAndGetID saves an observation and returns its ID (used in tests/smoke).
func (c *Client) WriteAndGetID(ctx context.Context, item core.Item) (int, error) {
	content := item.Content
	if len([]rune(content)) > maxContentLen {
		content = string([]rune(content)[:maxContentLen])
	}
	obs := map[string]interface{}{
		"session_id": sessionID,
		"type":       item.Type,
		"title":      item.Title,
		"content":    content,
		"project":    item.Project,
		"scope":      "project",
		"topic_key":  item.TopicKey,
	}
	var result observationResponse
	if err := c.post(ctx, "/observations", obs, &result); err != nil {
		return 0, err
	}
	return result.ID, nil
}

// Delete hard-deletes an observation by ID.
func (c *Client) Delete(ctx context.Context, id int) error {
	url := fmt.Sprintf("%s/observations/%d?hard=true", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
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

// Search queries Engram.
func (c *Client) Search(ctx context.Context, query, project string, limit int) ([]observationResponse, error) {
	url := fmt.Sprintf("%s/search?q=%s&project=%s&limit=%d", c.baseURL, query, project, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
