// Package confluence is a core.Source adapter that ingests CURRENT-STATE
// Confluence Cloud pages (title, space, body content, last-modified) via the
// modern Confluence Cloud REST API v2:
//
//	GET /wiki/api/v2/spaces?keys={key}        (resolve space id from key)
//	GET /wiki/api/v2/spaces/{id}/pages        (list pages, body-format=storage)
//
// Confluence v2's `pages` endpoint filters by numeric space id, not the
// human-facing space key, so resolving the id is a required first call per
// configured space key (see resolveSpaceID). Page body content is stored as
// XHTML ("storage format"); it is converted to plain text/markdown via
// internal/source/atlassian/storage.ToText.
//
// # Sweep design (why fetchSpace is more than "page through and filter")
//
// The pages-list endpoint has NO server-side "modified since" filter (unlike
// Jira's JQL `updated >=` clause), so two problems have to be solved
// explicitly instead of being handled by the API:
//
//  1. Correctness of the client-side cursor filter: pages are requested
//     sorted NEWEST-EDITED-FIRST (`sort=-modified-date`). This is load-
//     bearing, not cosmetic — it is what makes "stop as soon as a page's
//     version.createdAt is not after the stored cursor" a SAFE early-stop
//     rule: every page after that point, in a newest-first list, is
//     guaranteed to be even older. Without this order, an old-but-just-
//     edited page could sort anywhere and be missed by an early stop.
//  2. Completeness for spaces larger than one run's page-cap budget: a
//     capped run must not restart at page 1 next time (the original bug —
//     see MUST-FIX 1 in the PR-C adversarial review) because the next run's
//     early-stop would then trigger near the top of the list and never
//     revisit the still-unswept tail, losing it forever. Instead, whenever a
//     run is capped mid-sweep, it persists the API's own opaque pagination
//     continuation link (`_links.next`) plus the sweep's running max
//     timestamp to the StateStore (keys `{spaceKey}#resume-link` /
//     `{spaceKey}#resume-max`, source "confluence"); the next run resumes
//     pagination from EXACTLY that link. The real per-space incremental
//     cursor (used for the early-stop optimization on later steady-state
//     runs) is only committed once a sweep actually finishes — by reaching
//     the early-stop boundary or by exhausting all pages — never from a
//     capped, still-incomplete run. maxPagesPerSpace remains a per-run
//     safety backstop (bounds wall-clock/memory per invocation against a
//     misbehaving/huge endpoint); it no longer causes permanent data loss,
//     just spreads a big sweep across more runs.
package confluence

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/Velion-SpA/omnia/internal/core"
	"github.com/Velion-SpA/omnia/internal/enrich"
	"github.com/Velion-SpA/omnia/internal/meta"
	"github.com/Velion-SpA/omnia/internal/source/atlassian"
	"github.com/Velion-SpA/omnia/internal/source/atlassian/storage"
)

const (
	spacesPath = "/wiki/api/v2/spaces"

	// maxResultsPerPage bounds how many pages the pages-list endpoint returns
	// per page. Mirrors the conservative per-page size used by the
	// github/jira adapters (100).
	maxResultsPerPage = 100

	// cursorOverlap is subtracted from the stored cursor before the
	// client-side "skip pages not newer than this" filter, covering any
	// propagation lag between a page edit and its version.createdAt becoming
	// visible. Re-fetching this small overlap window is safe because the
	// Engram sink upserts by topic_key (idempotent).
	cursorOverlap = 5 * time.Minute

	// maxPagesPerSpace is a per-run safety backstop on how many pages-list
	// requests fetchSpace will make for a single space key in one run,
	// independent of what the server's `_links.next` claims. Mirrors
	// internal/source/jira's maxPagesPerProject cap pattern (adversarial
	// review lesson from PR-B): without SOME cap, a misbehaving/malicious
	// endpoint that always returns a `_links.next` would page forever —
	// collect.go runs the pipeline under context.Background() with no
	// deadline, so nothing else would stop it (OOM / hung nightly cron). At
	// maxResultsPerPage=100/page this allows up to 5,000 pages/space/run. As
	// of the PR-C adversarial-review fix (MUST-FIX 1), hitting this cap no
	// longer loses data: fetchSpace persists a resume point (see the
	// package doc comment's "Sweep design" section) so the NEXT run
	// continues pagination from exactly where this one stopped, instead of
	// restarting at page 1.
	maxPagesPerSpace = 50

	// pagesSort requests pages-list results sorted NEWEST-EDITED-FIRST.
	// Load-bearing — see the package doc comment's "Sweep design" section
	// for why this ordering is required for the early-stop-on-cursor
	// optimization to be safe. Value per Confluence Cloud REST v2's
	// documented `sort` enum for the pages-list endpoints (id / -id /
	// created-date / -created-date / modified-date / -modified-date /
	// title / -title); "-modified-date" is Atlassian's field name for a
	// page's version.createdAt (its most recent edit time), matching the
	// exact field this adapter uses as the cursor (see confluencePage's
	// Version.CreatedAt doc comment). NOTE: not verified against a live
	// Confluence Cloud site as part of this fix — if a future API change
	// ever silently drops/ignores this parameter, the resume-link mechanism
	// above still guarantees no PERMANENT data loss (every page is still
	// eventually visited across enough runs), but the early-stop
	// optimization would then be unsafe until verified/corrected.
	pagesSort = "-modified-date"

	// resumeLinkKeySuffix / resumeMaxKeySuffix are appended to a space key
	// to form the StateStore keys (source "confluence") used to persist an
	// in-progress sweep's resumption point across runs. See the package doc
	// comment's "Sweep design" section.
	resumeLinkKeySuffix = "#resume-link"
	resumeMaxKeySuffix  = "#resume-max"

	maxChunkRunes = 45000
	// maxBaseKeyLen ensures base + "-partNN" stays <= 120 chars after
	// normalization (NormalizeTopicKey caps at 120); 7 chars reserved for
	// "-part99".
	maxBaseKeyLen = 113
)

// projectRouter is a minimal interface to avoid circular imports (mirrors
// internal/source/jira and internal/source/github).
type projectRouter interface {
	ResolveConfluence(spaceKey string) string
}

// Source fetches current-state Confluence pages for a set of configured
// space keys.
type Source struct {
	client    *atlassian.Client
	spaceKeys []string
	router    projectRouter
	state     core.StateStore
}

// New creates a Confluence Source. client is the shared Atlassian Cloud
// transport (auth + retry), injected rather than built here — the same
// Client is also used by the Jira adapter (design decision: one shared Cloud
// token/site for both sources).
func New(client *atlassian.Client, spaceKeys []string, router projectRouter, state core.StateStore) *Source {
	return &Source{
		client:    client,
		spaceKeys: spaceKeys,
		router:    router,
		state:     state,
	}
}

func (s *Source) Name() string { return "confluence" }

// FetchAll is a convenience method that calls Fetch with a zero time
// (returns everything the fixture/API has to offer, honoring per-space
// cursors).
func (s *Source) FetchAll(ctx context.Context) ([]core.Item, error) {
	return s.Fetch(ctx, time.Time{})
}

// Fetch retrieves current-state pages for every configured space key,
// modified since the given time. When since is zero, each space's stored
// cursor is used as the lower bound; if no cursor exists, ALL pages for that
// space are fetched (mirrors internal/source/jira: no backfill-window
// concept — a fixed window could silently miss old-but-still-relevant
// pages).
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) {
	var items []core.Item
	for _, key := range s.spaceKeys {
		keyItems, err := s.fetchSpace(ctx, key, since)
		if err != nil {
			// Propagating the error (rather than swallowing it) is what makes
			// auth failures "loud": core.Pipeline.Run logs "source failed" and
			// continues with the remaining sources. Returning before any
			// SetCursor call for this space is flushed also means the cursor
			// is not advanced (state.Flush is only ever reached by the
			// pipeline after a successful Fetch + successful sink writes).
			return nil, fmt.Errorf("fetch space %s: %w", key, err)
		}
		items = append(items, keyItems...)
	}
	return items, nil
}

func (s *Source) fetchSpace(ctx context.Context, spaceKey string, since time.Time) ([]core.Item, error) {
	spaceSince := since
	if spaceSince.IsZero() && s.state != nil {
		if cursor, ok := s.state.GetCursor("confluence", spaceKey); ok {
			if t, err := time.Parse(time.RFC3339, cursor); err == nil {
				spaceSince = t.Add(-cursorOverlap)
			}
		}
	}

	project := s.router.ResolveConfluence(spaceKey)
	siteURL := s.client.BaseURL()

	// Resume an in-progress sweep, if a previous run persisted one (see the
	// package doc comment's "Sweep design" section). resumeMax carries
	// forward the sweep's running max timestamp: since pages are requested
	// newest-edited-first, that value is fixed by the very first page of
	// the very first run in the sweep and never needs to change afterward —
	// every later page in the sweep is, by construction, older.
	var latest time.Time
	var path string
	resuming := false
	if s.state != nil {
		if link, ok := s.state.GetCursor("confluence", spaceKey+resumeLinkKeySuffix); ok && link != "" {
			resuming = true
			path = link
			if v, ok := s.state.GetCursor("confluence", spaceKey+resumeMaxKeySuffix); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					latest = t
				}
			}
		}
	}

	if !resuming {
		spaceID, err := s.resolveSpaceID(ctx, spaceKey)
		if err != nil {
			return nil, err
		}
		path = fmt.Sprintf("%s/%s/pages?body-format=storage&limit=%d&sort=%s", spacesPath, spaceID, maxResultsPerPage, pagesSort)
	}

	var items []core.Item
	visited := make(map[string]bool)
	sweepComplete := false

	for page := 1; path != ""; page++ {
		var resp pagesResponse
		next, err := s.client.GetJSON(ctx, path, &resp)
		if err != nil {
			// An error mid-sweep (including auth failure) leaves any
			// already-persisted resume-link/resume-max untouched, so a
			// later retry (once the underlying problem is fixed) resumes
			// from the same point rather than losing sweep progress.
			return nil, err
		}

		// Defensive stop: an empty results page (with or without a next
		// link) has nothing left to offer — treat this as the natural end
		// of pagination (sweep complete) rather than following its next
		// link anyway, which would risk spinning on an empty-page loop
		// against a misbehaving server.
		if len(resp.Results) == 0 {
			sweepComplete = true
			break
		}

		reachedCursor := false
		for _, p := range resp.Results {
			createdAt := p.Version.CreatedAt.Time
			if !createdAt.IsZero() {
				// The cursor-filter and latest-tracking below only apply to
				// pages with a successfully parsed version timestamp. A zero
				// value here is indistinguishable between "field absent" and
				// "malformed" (confluenceTime.UnmarshalJSON never errors —
				// see its doc comment) from a genuinely ancient page, so a
				// degraded timestamp must NEVER be run through the "skip if
				// not newer than cursor" comparison below: zero.After(cursor)
				// is always false, which would silently and PERMANENTLY
				// exclude that page from every future run (a poison pill,
				// mirroring jira's MUST-FIX 2 but manifesting as a silent
				// drop instead of a hard abort). Falling through to the
				// unconditional formatPage call below instead guarantees the
				// page is still ingested even when a cursor already exists.
				if !spaceSince.IsZero() && !createdAt.After(spaceSince) {
					// EARLY STOP: because pages are sorted newest-edited-
					// first (pagesSort), reaching one that is not newer than
					// the (overlap-adjusted) cursor means every remaining
					// page in this and all further pages is also not newer
					// — the sweep has caught up to previously-ingested
					// content. Client-side, because v2's pages-list endpoint
					// has no "updated since" query filter.
					reachedCursor = true
					break
				}
				if createdAt.After(latest) {
					latest = createdAt
				}
			}
			items = append(items, formatPage(p, spaceKey, project, siteURL)...)
		}

		if reachedCursor {
			sweepComplete = true
			break
		}
		if next == "" {
			sweepComplete = true
			break
		}

		// Defensive guards against a misbehaving/malicious endpoint that
		// never actually terminates pagination — see maxPagesPerSpace's doc
		// comment. None of these are expected to fire against a
		// well-behaved Confluence Cloud instance.
		if visited[next] {
			log.Printf("confluence: space %s: _links.next %q repeated; stopping pagination to avoid an infinite loop after %d page(s) (results may be incomplete)", spaceKey, next, page)
			sweepComplete = true // a repeating link would only repeat forever; nothing to resume into
			break
		}
		if page >= maxPagesPerSpace {
			log.Printf("confluence: space %s: hit the %d-page cap for this run while the sweep is still catching up (a large space or a large batch of edits); persisting a resume point so the NEXT run continues pagination from EXACTLY here — no pages are skipped or lost, ingestion is just spread across multiple runs", spaceKey, maxPagesPerSpace)
			if s.state != nil {
				s.state.SetCursor("confluence", spaceKey+resumeLinkKeySuffix, next)
				if !latest.IsZero() {
					s.state.SetCursor("confluence", spaceKey+resumeMaxKeySuffix, latest.UTC().Format(time.RFC3339))
				}
			}
			// The real incremental cursor is deliberately NOT committed
			// here: this sweep is not finished, and doing so would tell a
			// later steady-state run "everything above this is already
			// ingested", masking the still-unswept tail.
			return items, nil
		}

		visited[next] = true
		path = next
	}

	if sweepComplete && s.state != nil {
		// This sweep finished (either by early-stopping at the cursor
		// boundary or by exhausting all pages) — clear any pending resume
		// point so the next run starts fresh at page 1 in steady state,
		// and commit the real incremental cursor.
		//
		// C1/C2 (mirrors jira/github/discord): SetCursor here is in-memory
		// only. The pipeline flushes state to disk only after all sink
		// writes for this source succeed, so a failed write leaves this
		// run's progress unpersisted and the next run safely re-fetches
		// (idempotent upsert).
		s.state.SetCursor("confluence", spaceKey+resumeLinkKeySuffix, "")
		if !latest.IsZero() {
			nv := latest.UTC().Format(time.RFC3339)
			if v, ok := s.state.GetCursor("confluence", spaceKey); !ok || nv > v {
				s.state.SetCursor("confluence", spaceKey, nv)
			}
		}
	}

	return items, nil
}

// spacesResponse is the subset of GET /wiki/api/v2/spaces?keys={key} needed
// to resolve a space key to its numeric id.
type spacesResponse struct {
	Results []struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"results"`
}

// resolveSpaceID looks up the numeric space id for spaceKey via
// GET /wiki/api/v2/spaces?keys={key} — required because v2's pages-list
// endpoint (GET /wiki/api/v2/spaces/{id}/pages) filters by id, not key.
func (s *Source) resolveSpaceID(ctx context.Context, spaceKey string) (string, error) {
	path := fmt.Sprintf("%s?keys=%s", spacesPath, url.QueryEscape(spaceKey))
	var resp spacesResponse
	if _, err := s.client.GetJSON(ctx, path, &resp); err != nil {
		return "", fmt.Errorf("resolve space id for key %s: %w", spaceKey, err)
	}
	for _, r := range resp.Results {
		if strings.EqualFold(r.Key, spaceKey) {
			return r.ID, nil
		}
	}
	if len(resp.Results) > 0 {
		// Defensive fallback: the `keys` filter should already narrow the
		// response to an exact match, but if the API ever returns a
		// near-match set without an exact Key echo, prefer the first result
		// over a hard failure. This is loud, not silent: picking the wrong
		// space id here would ingest the WRONG space's content under the
		// configured key with no other signal, so an operator needs to see
		// this in logs to catch a misconfigured/renamed space key.
		log.Printf("confluence: space key %q: the spaces-lookup endpoint returned %d result(s) but none had an exact (case-insensitive) key match; falling back to the first result's id %q — verify sources.atlassian.confluence.space_keys is spelled exactly as it appears in Confluence, this may otherwise ingest the WRONG space", spaceKey, len(resp.Results), resp.Results[0].ID)
		return resp.Results[0].ID, nil
	}
	return "", fmt.Errorf("confluence: space key %q not found", spaceKey)
}

// pagesResponse is the subset of GET /wiki/api/v2/spaces/{id}/pages needed
// here. Pagination (`_links.next`) is extracted by the shared
// atlassian.Client itself (GetJSON's return value), not by this struct.
type pagesResponse struct {
	Results []confluencePage `json:"results"`
}

// confluencePage is the subset of a Confluence Cloud v2 page needed for
// CURRENT-STATE ingestion (title, body, last-modified). No comments (unlike
// Jira's current-state depth, Confluence's design scope is page content
// only).
type confluencePage struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	CreatedAt confluenceTime `json:"createdAt"` // page's original creation time
	Version   struct {
		Number int `json:"number"`
		// CreatedAt is this VERSION's creation time — i.e. the timestamp of
		// the most recent edit. Confluence v2 has no separate "lastModified"
		// field; version.createdAt IS the last-modified timestamp (design
		// decision, confirms the design's open question). This field drives
		// both the rendered "Last Modified" line and the cursor.
		CreatedAt confluenceTime `json:"createdAt"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

// formatPage builds one or more Items for a Confluence page. Mirrors
// internal/source/jira's formatIssue: chunks content exceeding
// maxChunkRunes into multiple Items with topic_key suffixes "-part1",
// "-part2", etc., each continuation chunk carrying a brief context header.
//
// Author is deliberately omitted: Confluence v2's page object exposes only
// an opaque authorId (account ID), not a human-readable display name;
// resolving one requires a separate GET /wiki/api/v2/users/{id} call per
// page/author, which is not "cheaply available" the way Jira's inline
// assignee/comment-author displayName fields are (documented deviation from
// the design's "author if cheaply available" language).
func formatPage(p confluencePage, spaceKey, project, siteURL string) []core.Item {
	pageURL := fmt.Sprintf("%s/wiki%s", siteURL, p.Links.WebUI)
	title := p.Title

	var sb strings.Builder
	sb.WriteString("## Source\n\n")
	sb.WriteString(fmt.Sprintf("- Page ID: %s\n", p.ID))
	sb.WriteString(fmt.Sprintf("- Space: %s\n", spaceKey))
	sb.WriteString(fmt.Sprintf("- URL: %s\n", pageURL))
	sb.WriteString(fmt.Sprintf("- Version: %d\n", p.Version.Number))
	if !p.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- Created: %s\n", p.CreatedAt.Format(time.RFC3339)))
	}
	if !p.Version.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- Last Modified: %s\n", p.Version.CreatedAt.Format(time.RFC3339)))
	}

	sb.WriteString("\n## Content\n\n")
	sb.WriteString(storage.ToText(p.Body.Storage.Value))
	sb.WriteString("\n")

	keywords := enrich.ExtractKeywords([]string{spaceKey}, nil)
	if len(keywords) > 0 {
		sb.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))
	}

	fullContent := sb.String()

	rawTopicKey := fmt.Sprintf("confluence/%s/page-%s", spaceKey, p.ID)
	normalizedBase := enrich.NormalizeTopicKey(rawTopicKey)
	if len([]rune(normalizedBase)) > maxBaseKeyLen {
		normalizedBase = string([]rune(normalizedBase)[:maxBaseKeyLen])
	}

	contextHeader := fmt.Sprintf("<!-- %s | %s -->\n\n", sanitizeCommentText(p.Title), p.Version.CreatedAt.Format("2006-01-02"))

	ingestedAt := time.Now().UTC()
	m := meta.Meta{
		SchemaVersion: meta.SchemaVersion,
		Source:        "confluence",
		Kind:          "page",
		Layer:         "ingested",
		Project:       project,
		SourceID:      p.ID,
		URL:           pageURL,
		CreatedAt:     p.CreatedAt.Time,
		UpdatedAt:     p.Version.CreatedAt.Time,
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
			Type:      "confluence-page",
			Title:     itemTitle,
			Content:   content,
			Project:   project,
			TopicKey:  topicKey,
			Source:    "confluence",
			FetchedAt: time.Now(),
		})
	}
	return items
}

// sanitizeCommentText makes s safe to embed as literal text inside an HTML
// comment (`<!-- ... -->`), as used by formatPage's continuation-chunk
// context header. Two things must never survive: a literal "-->" sequence
// (which would prematurely close the comment and let an attacker-controlled
// page title inject content into the surrounding markdown/HTML rendering
// context — the SHOULD-FIX from the PR-C adversarial review), and embedded
// newlines (which would break the single-line "<!-- KEY | DATE -->"
// convention). Every run of 2+ consecutive hyphens is broken up, not just
// the literal "-->" substring: HTML comment syntax also technically
// disallows a bare "--" anywhere inside a comment, and breaking every dash
// run is the simplest rule that provably leaves no "-->" substring behind
// regardless of how the title is worded around it (e.g. "----" or a title
// ending in "-" immediately before the header's own "-->").
func sanitizeCommentText(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "- ")
	}
	return s
}

// confluenceTime parses Confluence Cloud v2's RFC3339 timestamps (e.g.
// "2023-04-13T22:33:31.116Z") but NEVER returns an error on an unparsable
// value — see UnmarshalJSON's doc comment for why.
type confluenceTime struct {
	time.Time
}

// UnmarshalJSON never returns an error: encoding/json aborts json.Unmarshal
// ENTIRELY the moment any field's UnmarshalJSON returns an error, which
// would abort the whole pages-list decode for one malformed timestamp on one
// page — aborting fetchSpace/Fetch for this space, and (since Fetch returns
// on the first space error) every remaining configured space key too. Because
// the cursor is never advanced on error, the SAME bad record would re-fail
// every future run forever — a permanent ingestion block (mirrors
// internal/source/jira's jiraTime, the adversarial-review-hardened pattern
// from PR-B). Degrade to the zero value instead: the page's other fields
// (title/body) still ingest normally; a zero CreatedAt/UpdatedAt is simply
// omitted from the rendered content and meta block (both only emit non-zero
// times) and can never win the max-version.createdAt cursor comparison, so
// the cursor still advances past the surrounding valid records.
func (ct *confluenceTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		ct.Time = t
		return nil
	}
	log.Printf("confluence: could not parse timestamp %q; degrading to zero value (this field will be omitted for the affected page)", s)
	ct.Time = time.Time{}
	return nil
}
