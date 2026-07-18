// Package jira is a core.Source adapter that ingests CURRENT-STATE Jira Cloud
// issues (title, description, status, assignee, comments — NOT the full
// changelog/transition history) via the modern Jira Cloud REST API:
//
//	POST /rest/api/3/search/jql
//
// The old GET /search + startAt pagination was decommissioned by Atlassian in
// October 2025; this adapter paginates using the response body's
// nextPageToken instead (see fetchProject).
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/enrich"
	"github.com/velion/omnia/internal/meta"
	"github.com/velion/omnia/internal/source/atlassian"
	"github.com/velion/omnia/internal/source/atlassian/adf"
)

const (
	searchPath = "/rest/api/3/search/jql"

	// maxResultsPerPage bounds how many issues the search endpoint returns per
	// page. Well under Jira Cloud's documented cap; mirrors the conservative
	// per-page size used by the github/discord adapters (100).
	maxResultsPerPage = 100

	// cursorOverlap is subtracted from the stored cursor when computing the
	// "updated >=" JQL bound, covering Jira Cloud's search-index propagation
	// lag (a just-updated issue can briefly not yet be reflected in search
	// results). Re-fetching this small overlap window is safe because the
	// Engram sink upserts by topic_key (idempotent).
	cursorOverlap = 5 * time.Minute

	// maxPagesPerProject is a HARD CAP on how many search/jql pages
	// fetchProject will request for a single project key in one run,
	// independent of what the server's isLast/nextPageToken claims. Mirrors
	// internal/source/github's maxCommitsPerRepo cap pattern. Without this,
	// a misbehaving (or malicious) endpoint that always answers
	// isLast:false forever would page forever: collect.go runs the pipeline
	// under context.Background() with no deadline, so nothing else would
	// stop it (OOM / hung nightly cron). At maxResultsPerPage=100/page this
	// allows up to 5,000 issues per project per run — if that's not enough
	// for a single run, results are truncated and a warning is logged (see
	// fetchProject); the next run picks up from the cursor already
	// advanced for the issues that WERE ingested.
	maxPagesPerProject = 50

	maxCommentBodyLen = 2000
	maxChunkRunes     = 45000
	// maxBaseKeyLen ensures base + "-partNN" stays ≤ 120 chars after
	// normalization (NormalizeTopicKey caps at 120); 7 chars reserved for
	// "-part99".
	maxBaseKeyLen = 113

	// jqlTimeLayout is Jira JQL's date/time literal format for comparison
	// operators (e.g. `updated >= "2024-01-05 09:30"`). JQL does NOT accept
	// RFC3339 (no seconds, no "T"/timezone) — see
	// https://support.atlassian.com/jira-software-cloud/docs/jql-fields/#Updated
	jqlTimeLayout = "2006-01-02 15:04"
)

// projectRouter is a minimal interface to avoid circular imports (mirrors
// internal/source/github and internal/source/discord).
type projectRouter interface {
	ResolveJira(projectKey string) string
}

// Source fetches current-state Jira issues for a set of configured project keys.
type Source struct {
	client      *atlassian.Client
	projectKeys []string
	router      projectRouter
	state       core.StateStore
}

// New creates a Jira Source. client is the shared Atlassian Cloud transport
// (auth + retry), injected rather than built here — the same Client is also
// used by the Confluence adapter (design decision: one shared Cloud
// token/site for both sources).
func New(client *atlassian.Client, projectKeys []string, router projectRouter, state core.StateStore) *Source {
	return &Source{
		client:      client,
		projectKeys: projectKeys,
		router:      router,
		state:       state,
	}
}

func (s *Source) Name() string { return "jira" }

// FetchAll is a convenience method that calls Fetch with a zero time (returns
// everything the fixture/API has to offer, honoring per-project cursors).
func (s *Source) FetchAll(ctx context.Context) ([]core.Item, error) {
	return s.Fetch(ctx, time.Time{})
}

// Fetch retrieves current-state issues for every configured project key,
// updated since the given time. When since is zero, each project's stored
// cursor is used as the lower bound; if no cursor exists, ALL issues for
// that project key are fetched (Jira has no backfill-window concept — a
// fixed window could silently miss old-but-still-open tickets).
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) {
	var items []core.Item
	for _, key := range s.projectKeys {
		keyItems, err := s.fetchProject(ctx, key, since)
		if err != nil {
			// Propagating the error (rather than swallowing it) is what makes
			// auth failures "loud": core.Pipeline.Run logs "source failed" and
			// continues with the remaining sources. Returning before any
			// SetCursor call for this project is flushed also means the
			// cursor is not advanced (state.Flush is only ever reached by the
			// pipeline after a successful Fetch + successful sink writes).
			return nil, fmt.Errorf("fetch project %s: %w", key, err)
		}
		items = append(items, keyItems...)
	}
	return items, nil
}

func (s *Source) fetchProject(ctx context.Context, projectKey string, since time.Time) ([]core.Item, error) {
	projSince := since
	if projSince.IsZero() && s.state != nil {
		if cursor, ok := s.state.GetCursor("jira", projectKey); ok {
			if t, err := time.Parse(time.RFC3339, cursor); err == nil {
				projSince = t.Add(-cursorOverlap)
			}
		}
	}

	jql := buildJQL(projectKey, projSince)
	project := s.router.ResolveJira(projectKey)
	siteURL := s.client.BaseURL()

	var items []core.Item
	var latest time.Time
	pageToken := ""
	seenTokens := make(map[string]bool)

	for page := 1; ; page++ {
		resp, err := s.searchPage(ctx, jql, pageToken)
		if err != nil {
			return nil, err
		}
		for _, iss := range resp.Issues {
			items = append(items, formatIssue(iss, projectKey, project, siteURL)...)
			if iss.Fields.Updated.After(latest) {
				latest = iss.Fields.Updated.Time
			}
		}

		if resp.IsLast || len(resp.Issues) == 0 {
			break
		}

		// Defensive guards against a misbehaving/malicious endpoint that
		// never actually terminates pagination — see maxPagesPerProject's
		// doc comment. None of these are expected to fire against a
		// well-behaved Jira Cloud instance.
		if resp.NextPageToken == "" {
			log.Printf("jira: project %s: isLast=false but no nextPageToken in the response; stopping pagination defensively after %d page(s) (results may be incomplete)", projectKey, page)
			break
		}
		if seenTokens[resp.NextPageToken] {
			log.Printf("jira: project %s: nextPageToken %q repeated; stopping pagination to avoid an infinite loop after %d page(s) (results may be incomplete)", projectKey, resp.NextPageToken, page)
			break
		}
		if page >= maxPagesPerProject {
			log.Printf("jira: project %s: hit the %d-page cap; results for this run are TRUNCATED — the cursor still advances past the issues fetched so far, and the next run continues from there", projectKey, maxPagesPerProject)
			break
		}

		seenTokens[resp.NextPageToken] = true
		pageToken = resp.NextPageToken
	}

	// C1/C2 (mirrors github/discord): SetCursor here is in-memory only. The
	// pipeline flushes state to disk only after all sink writes for this
	// source succeed, so a failed write leaves this run's progress
	// unpersisted and the next run safely re-fetches (idempotent upsert).
	if s.state != nil && !latest.IsZero() {
		nv := latest.UTC().Format(time.RFC3339)
		if v, ok := s.state.GetCursor("jira", projectKey); !ok || nv > v {
			s.state.SetCursor("jira", projectKey, nv)
		}
	}

	return items, nil
}

// buildJQL scopes the query to projectKey and, when since is non-zero, adds
// an "updated >=" lower bound. When since is zero (first run, no cursor) the
// query has no time bound at all — every issue in the project is fetched.
func buildJQL(projectKey string, since time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "project = %q", projectKey)
	if !since.IsZero() {
		fmt.Fprintf(&sb, " AND updated >= \"%s\"", since.UTC().Format(jqlTimeLayout))
	}
	sb.WriteString(" ORDER BY updated ASC")
	return sb.String()
}

// searchRequest is the POST /rest/api/3/search/jql request body.
type searchRequest struct {
	JQL           string   `json:"jql"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
	Fields        []string `json:"fields"`
}

// searchResponse is the subset of the search/jql response shape needed here.
type searchResponse struct {
	Issues        []jiraIssue `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

// jiraIssue is the subset of a Jira Cloud issue needed for CURRENT-STATE
// ingestion (title, description, status, assignee, comments) — no
// changelog/transition history.
type jiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"` // ADF; converted via adf.ToMarkdown
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
		Assignee *struct {
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Comment struct {
			Comments []jiraComment `json:"comments"`
		} `json:"comment"`
		Created jiraTime `json:"created"`
		Updated jiraTime `json:"updated"`
	} `json:"fields"`
}

type jiraComment struct {
	Body   json.RawMessage `json:"body"` // ADF; converted via adf.ToMarkdown
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created jiraTime `json:"created"`
}

// searchPage issues one POST /rest/api/3/search/jql request and decodes the
// response. Pagination is driven entirely by the body's nextPageToken/isLast
// fields (NOT the shared client's `_links.next` return value, which Jira's
// search response does not use).
func (s *Source) searchPage(ctx context.Context, jql, pageToken string) (*searchResponse, error) {
	req := searchRequest{
		JQL:           jql,
		MaxResults:    maxResultsPerPage,
		NextPageToken: pageToken,
		Fields:        []string{"summary", "description", "status", "assignee", "comment", "created", "updated"},
	}
	var resp searchResponse
	if _, err := s.client.PostJSON(ctx, searchPath, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// formatIssue builds one or more Items for a Jira issue. Mirrors
// internal/source/github's formatIssue: chunks content exceeding
// maxChunkRunes into multiple Items with topic_key suffixes "-part1",
// "-part2", etc., each continuation chunk carrying a brief context header.
func formatIssue(iss jiraIssue, projectKey, project, siteURL string) []core.Item {
	url := fmt.Sprintf("%s/browse/%s", siteURL, iss.Key)
	title := fmt.Sprintf("[%s] %s (%s)", iss.Key, iss.Fields.Summary, iss.Fields.Status.Name)

	assignee := ""
	if iss.Fields.Assignee != nil {
		assignee = iss.Fields.Assignee.DisplayName
	}

	// Build participants: assignee first (if any), then comment authors,
	// deduplicated. Jira's current-state ingest depth does not include the
	// reporter, so — unlike github's Author (issue creator) — Author here is
	// the assignee: the only person field this depth fetches.
	var participants []string
	seen := map[string]bool{}
	if assignee != "" {
		participants = append(participants, assignee)
		seen[assignee] = true
	}
	for _, c := range iss.Fields.Comment.Comments {
		name := c.Author.DisplayName
		if name != "" && !seen[name] {
			participants = append(participants, name)
			seen[name] = true
		}
	}

	var sb strings.Builder
	sb.WriteString("## Source\n\n")
	sb.WriteString(fmt.Sprintf("- Key: %s\n", iss.Key))
	sb.WriteString(fmt.Sprintf("- URL: %s\n", url))
	sb.WriteString(fmt.Sprintf("- Status: %s\n", iss.Fields.Status.Name))
	if assignee != "" {
		sb.WriteString(fmt.Sprintf("- Assignee: %s\n", assignee))
	}
	if !iss.Fields.Created.IsZero() {
		sb.WriteString(fmt.Sprintf("- Created: %s\n", iss.Fields.Created.Format(time.RFC3339)))
	}
	if !iss.Fields.Updated.IsZero() {
		sb.WriteString(fmt.Sprintf("- Updated: %s\n", iss.Fields.Updated.Format(time.RFC3339)))
	}

	sb.WriteString("\n## Description\n\n")
	description := adf.ToMarkdown(iss.Fields.Description)
	sb.WriteString(enrich.TruncateContent(description, 5000))
	sb.WriteString("\n")

	if len(iss.Fields.Comment.Comments) > 0 {
		sb.WriteString("\n## Recent Comments\n\n")
		for _, c := range iss.Fields.Comment.Comments {
			body := adf.ToMarkdown(c.Body)
			truncBody := enrich.TruncateContent(body, maxCommentBodyLen)
			sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n",
				c.Author.DisplayName, c.Created.Format("2006-01-02 15:04"), truncBody))
		}
	}

	if len(participants) > 0 {
		sb.WriteString(fmt.Sprintf("\n## Participants\n\n%s\n", strings.Join(participants, ", ")))
	}

	keywords := enrich.ExtractKeywords([]string{iss.Fields.Status.Name, projectKey}, participants)
	if len(keywords) > 0 {
		sb.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))
	}

	fullContent := sb.String()

	rawTopicKey := fmt.Sprintf("jira/%s/issue-%s", projectKey, iss.Key)
	normalizedBase := enrich.NormalizeTopicKey(rawTopicKey)
	if len([]rune(normalizedBase)) > maxBaseKeyLen {
		normalizedBase = string([]rune(normalizedBase)[:maxBaseKeyLen])
	}

	contextHeader := fmt.Sprintf("<!-- %s | %s -->\n\n", iss.Key, iss.Fields.Updated.Format("2006-01-02"))

	ingestedAt := time.Now().UTC()
	m := meta.Meta{
		SchemaVersion: meta.SchemaVersion,
		Source:        "jira",
		Kind:          "issue",
		Layer:         "ingested",
		Project:       project,
		SourceID:      iss.Key,
		Status:        iss.Fields.Status.Name,
		Author:        assignee,
		Participants:  participants,
		URL:           url,
		CreatedAt:     iss.Fields.Created.Time,
		UpdatedAt:     iss.Fields.Updated.Time,
		IngestedAt:    ingestedAt,
	}

	metaSize := len([]rune(meta.Render(m)))
	chunkBudget := maxChunkRunes - metaSize
	chunks := enrich.ChunkContent(fullContent, chunkBudget)

	var items []core.Item
	total := len(chunks)
	for i, chunk := range chunks {
		topicKey := normalizedBase
		suffix := ""
		content := chunk
		if total > 1 {
			topicKey = fmt.Sprintf("%s-part%d", normalizedBase, i+1)
			suffix = fmt.Sprintf(" (part %d/%d)", i+1, total)
			if i > 0 {
				content = contextHeader + chunk
			}
			m.ChunkCurrent = i + 1
			m.ChunkTotal = total
		} else {
			m.ChunkCurrent = 0
			m.ChunkTotal = 0
		}

		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += meta.Render(m)

		itemTitle := title
		if suffix != "" {
			itemTitle = title + suffix
		}
		items = append(items, core.Item{
			Type:      "jira-issue",
			Title:     itemTitle,
			Content:   content,
			Project:   project,
			TopicKey:  topicKey,
			Source:    "jira",
			FetchedAt: time.Now(),
		})
	}
	return items
}

// jiraTime parses Jira Cloud's REST timestamp format, e.g.
// "2024-01-05T10:00:00.000+0000" — note the timezone offset has NO colon,
// which fails Go's default time.Time JSON unmarshaling (that expects
// RFC3339's "+00:00" form). Falls back to strict RFC3339 for forward
// compatibility in case a future API response uses the colon form.
type jiraTime struct {
	time.Time
}

// UnmarshalJSON never returns an error — see the type doc comment. A single
// malformed timestamp degrades to the zero value (logged) instead of
// aborting the decode.
func (jt *jiraTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000-0700", s); err == nil {
		jt.Time = t
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		jt.Time = t
		return nil
	}
	// A single malformed timestamp must never poison the whole page decode:
	// encoding/json aborts json.Unmarshal ENTIRELY the moment any field's
	// UnmarshalJSON returns an error. That would abort searchPage ->
	// fetchProject -> Fetch for this whole project — and since Fetch
	// returns on the first project error, every remaining configured
	// project key too — and because the cursor is never advanced on error,
	// the SAME bad record would re-fail every future run forever,
	// permanently blocking ingestion (a poison pill). Degrade to the zero
	// value instead: the issue's other fields (summary/description/status/
	// assignee/comments) are still ingested; a zero CreatedAt/UpdatedAt is
	// simply omitted from the rendered content and meta block (both only
	// emit non-zero times) and can never win the max-`updated` cursor
	// comparison, so the cursor still advances past the surrounding valid
	// records.
	log.Printf("jira: could not parse timestamp %q; degrading to zero value (this field will be omitted for the affected issue/comment)", s)
	jt.Time = time.Time{}
	return nil
}
