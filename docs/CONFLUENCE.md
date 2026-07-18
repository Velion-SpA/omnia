# Confluence Source — Implementation Notes

## Overview

Confluence ingestion uses the [Confluence Cloud REST API v2](https://developer.atlassian.com/cloud/confluence/rest/v2/intro/) with email + API token authentication (same credentials as Jira). Ingest depth is CURRENT STATE only (title, space, body content, last-modified) — page comments are out of scope for this depth.

## Authentication

```
Authorization: Basic base64(email:api_token)
```

Auth is shared with the Jira adapter via one `internal/source/atlassian.Client` (one Atlassian Cloud site, one email + API token pair). Configure `sources.atlassian.email` / `sources.atlassian.token` — there is no separate Confluence-only credential pair.

## Approach

1. **Space id resolution**: v2's pages-list endpoint filters by numeric space *id*, not the human-facing space *key* configured in `space_keys`. So each configured space key requires one `GET /wiki/api/v2/spaces?keys={key}` call to resolve its id before paging pages. This runs once per space key per collect run (not once per page).
2. **Pages endpoint**: `GET /wiki/api/v2/spaces/{id}/pages?body-format=storage&limit=100`. `body-format=storage` requests the page body as Confluence storage-format XHTML (not the rendered view or the older wiki markup).
3. **Pagination**: driven entirely by the shared `atlassian.Client.GetJSON`'s `_links.next` return value (Confluence v2's native pagination link), not an offset/cursor parameter built by this adapter. A hard cap (`maxPagesPerSpace = 50`) plus a visited-link guard stop a misbehaving/malicious endpoint from paging forever — mirrors the Jira adapter's `maxPagesPerProject` guard (adversarial-review-hardened pattern from PR-B).
4. **Body conversion**: storage-format XHTML is converted to plain text/markdown via `internal/source/atlassian/storage.ToText` (pure function, `golang.org/x/net/html` tokenizer).
5. **Incremental state / cursor field**: the cursor is the max `version.createdAt` timestamp seen per space key. Confluence v2 has no separate "lastModified" field — `version.createdAt` (the timestamp of a page's most recent revision) **is** the last-modified timestamp; this confirms the design phase's open question. Cursor is stored in the state file under source `"confluence"`, key `{spaceKey}`.
6. **Time filtering is client-side**: unlike Jira's JQL `updated >=` clause, Confluence v2's pages-list endpoint has no "updated since" query filter. The adapter fetches pages normally and skips (client-side) any page whose `version.createdAt` is not after the (overlap-adjusted) cursor. A small overlap window (5 minutes) is subtracted from the cursor before filtering to cover any propagation lag; re-fetching the overlap is safe because the Engram sink upserts by topic_key.
7. **Author omitted**: Confluence v2's page object exposes only an opaque `authorId` (account ID), not a human-readable display name. Resolving a name requires a separate `GET /wiki/api/v2/users/{id}` call per page/author — not "cheaply available" the way Jira's inline assignee/comment-author `displayName` fields are. This is a deliberate, documented scope cut; a future pass could batch-resolve author names if this proves valuable.
8. **Observation format**: Source section (Page ID, Space, URL, Version, Created, Last Modified) + Content section (converted body) + Keywords.
9. **Topic key**: `confluence/{space-key}/page-{id}` — upserts as pages evolve.
10. **Auth failures** (401/403) surface as an error wrapping `atlassian.ErrAuthFailed`; the collector logs it loudly and continues with other sources without advancing the Confluence cursor. 429s are retried by the shared `atlassian.Client` with a bounded retry loop honoring `Retry-After`.

## Config

```yaml
sources:
  atlassian:
    site_url: https://your-org.atlassian.net
    email: bot@your-org.com
    token: ${ATLASSIAN_API_TOKEN}
    confluence:
      enabled: true
      space_keys: ["DOCS", "ENG"]
      project: omnia
```

## Adapter shape

```go
// internal/source/confluence/confluence.go
type Source struct {
    client    *atlassian.Client // shared Atlassian Cloud transport (auth + retry)
    spaceKeys []string
    router    projectRouter // minimal ResolveConfluence(spaceKey) interface
    state     core.StateStore
}

func (s *Source) Name() string { return "confluence" }
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) { ... }
```
