// Package atlassian provides the shared HTTP transport for both the Jira and
// Confluence adapters — one Atlassian Cloud site, one email+API-token pair
// (HTTP Basic auth), injected into both adapters (design decision: shared
// Cloud client, orthogonal to the per-source project Router).
//
// Client is pure transport: it knows nothing about Jira issues or
// Confluence pages, only how to authenticate, retry rate limits, and follow
// a cursor-style "_links.next" pagination link.
package atlassian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrAuthFailed is returned (wrapped) when the Atlassian API responds with
// 401 or 403, so adapters can detect it and skip that source loudly (log +
// continue with other sources) rather than aborting the whole collect run.
var ErrAuthFailed = errors.New("atlassian: authentication failed (401 or 403) - check email/token")

const (
	// maxRetries bounds the number of 429 retry attempts GetJSON will make
	// before giving up. This is a HARD CAP independent of ctx: without it, a
	// server that keeps answering 429 (with a zero, missing, or unparsable
	// Retry-After) would otherwise be retried in an unbounded loop — forever
	// if ctx is never cancelled, which a long-running `omnia collect` might
	// not do for a long time.
	maxRetries = 5

	// minRetryAfter is the floor applied to a parsed Retry-After value, and
	// the value used when the header is missing, zero, negative, or
	// unparsable. Taking "Retry-After: 0" literally would produce a
	// zero-delay busy-loop against a rate-limited endpoint.
	minRetryAfter = 1 * time.Second

	// maxResponseBodyBytes caps how much of a response body GetJSON will
	// buffer into memory. A hostile or misbehaving server sending a huge
	// body must not be able to exhaust memory.
	maxResponseBodyBytes = 10 * 1024 * 1024 // 10 MiB
)

// Client is a shared Atlassian Cloud HTTP client using Basic auth
// (email + API token).
type Client struct {
	baseURL    string
	email      string
	token      string
	httpClient *http.Client
}

// New creates an Atlassian Cloud client for the given site base URL (e.g.
// "https://yoursite.atlassian.net" in production, or an httptest.Server URL
// in tests).
func New(baseURL, email, token string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		email:      email,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the client's configured site base URL (no trailing slash),
// e.g. for building human-facing browse links (adapters need the same site
// URL the client authenticates against; exposing it here avoids threading a
// second copy of the site URL through adapter constructors).
func (c *Client) BaseURL() string { return c.baseURL }

// linksEnvelope captures the subset of Atlassian REST v2 response shapes
// needed to discover a "next page" link (Confluence's `_links.next`).
type linksEnvelope struct {
	Links struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// GetJSON performs an authenticated GET against path (either a full URL, or
// a path resolved against the client's base URL), decodes the JSON response
// body into out, and returns the "next page" path/URL if the response
// carries a `_links.next` field (empty string when there is no next page).
//
// On 401/403 it returns an error wrapping ErrAuthFailed. On 429 it honors
// the Retry-After header (clamped to a minRetryAfter floor so a zero,
// missing, or unparsable header can't cause a zero-delay retry) and retries
// using a BOUNDED loop, up to maxRetries attempts total. Once that cap is
// exceeded it returns an error instead of continuing to retry — this caps
// both wall-clock time and outbound request volume against a server that
// never stops answering 429, independent of ctx cancellation.
func (c *Client) GetJSON(ctx context.Context, path string, out any) (string, error) {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

// PostJSON performs an authenticated POST with a JSON-encoded body against
// path, decodes the JSON response into out, and returns the "next page"
// path/URL exactly like GetJSON (present for response shapes that carry
// `_links.next`; Jira's search/jql response instead carries its
// nextPageToken inside the decoded body, which callers read from out).
//
// Added for the Jira adapter: Jira's modern Cloud search endpoint
// (POST /rest/api/3/search/jql) requires a JSON request body, which a
// GET-only client cannot send. PostJSON shares GetJSON's auth, 401/403,
// bounded 429 retry, and response-body-cap behavior via doJSON rather than
// duplicating that loop per adapter.
func (c *Client) PostJSON(ctx context.Context, path string, body any, out any) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request body: %w", err)
	}
	return c.doJSON(ctx, http.MethodPost, path, payload, out)
}

// doJSON is the shared retry/decode loop behind GetJSON and PostJSON. See
// GetJSON's doc comment for the retry/auth/cap semantics; they are identical
// regardless of method.
func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, out any) (string, error) {
	url := c.resolveURL(path)

	var lastStatus int
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := c.doRequest(ctx, method, url, body)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return "", fmt.Errorf("%w (status %d)", ErrAuthFailed, resp.StatusCode)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastStatus = resp.StatusCode
			sleep := retryAfterDuration(resp, minRetryAfter)
			resp.Body.Close()
			if attempt == maxRetries-1 {
				// Last allowed attempt already failed: don't sleep just to
				// throw the wait away, fall through to the final error.
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(sleep):
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("%s %s returned %d", method, url, resp.StatusCode)
		}

		return c.decodeBody(resp, url, out)
	}

	return "", fmt.Errorf("%s %s: exceeded %d retries, last status %d", method, url, maxRetries, lastStatus)
}

// doRequest builds and issues one authenticated request. A fresh
// *http.Request is constructed per call (per net/http guidance, Request
// values should not be reused/mutated across multiple client.Do calls).
// body is nil for GET; for POST it is sent as the request body with a JSON
// Content-Type.
func (c *Client) doRequest(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	return resp, nil
}

// decodeBody reads (up to maxResponseBodyBytes) and decodes a 200 response
// body into out, and extracts the `_links.next` pagination path if present.
func (c *Client) decodeBody(resp *http.Response, url string, out any) (string, error) {
	defer resp.Body.Close()

	// Cap how much of the body we buffer: read one byte past the limit so
	// we can detect truncation and distinguish "exactly at the cap" from
	// "over the cap" without ever holding more than maxResponseBodyBytes+1
	// bytes in memory.
	limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return "", fmt.Errorf("GET %s: response body exceeds %d byte limit", url, maxResponseBodyBytes)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	// Best-effort extraction of the pagination link. Unmarshal errors are
	// ignored: not every Atlassian response shape carries a `_links` field
	// (out already got the real payload above).
	var links linksEnvelope
	_ = json.Unmarshal(body, &links)
	return links.Links.Next, nil
}

// resolveURL returns path unchanged if it is already an absolute URL,
// otherwise joins it onto the client's base URL.
func (c *Client) resolveURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.baseURL + path
}

// retryAfterDuration parses the Retry-After header (seconds), clamped to a
// [floor, ...] range: a missing, zero, negative, or unparsable header falls
// back to floor rather than to a longer default — the header only ever
// extends the wait, it never shortens it below floor. Mirrors
// internal/source/github's retryAfterDuration, adapted for Atlassian (no
// X-RateLimit-Reset header equivalent).
func retryAfterDuration(resp *http.Response, floor time.Duration) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return floor
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil || secs <= 0 {
		return floor
	}
	d := time.Duration(secs) * time.Second
	if d < floor {
		return floor
	}
	return d
}
