# Jira Source — Implementation Notes

## Overview

Jira ingestion uses the modern [Jira Cloud REST API v3](https://developer.atlassian.com/cloud/jira/platform/rest/v3/) search endpoint with email + API token authentication. Ingest depth is CURRENT STATE only (title, description, status, assignee, comments) — not the full changelog/transition history.

## Authentication

```
Authorization: Basic base64(email:api_token)
```

Auth is shared with the Confluence adapter via one `internal/source/atlassian.Client` (one Atlassian Cloud site, one email + API token pair). Configure `sources.atlassian.email` / `sources.atlassian.token` (or the equivalent env vars, per the deploy convention) — there is no separate `JIRA_EMAIL` / `JIRA_API_TOKEN` pair.

## Approach

1. **Search endpoint**: `POST /rest/api/3/search/jql`, **not** `GET /search`. The old `GET /search` + `startAt` pagination was **decommissioned by Atlassian in October 2025** — any integration still using it will fail.
2. **Pagination**: driven by `nextPageToken` in the JSON response body (and the `isLast` flag), not `startAt`/`maxResults` offsets. Each page's request repeats the same JQL with the previous response's `nextPageToken`.
3. **JQL query**: `project = "<KEY>" AND updated >= "<cursor>" ORDER BY updated ASC` when a stored cursor exists; on the first run for a project key (no cursor yet), the `updated >=` clause is omitted entirely so **all** issues for that project are fetched. Note JQL date/time literals use `"yyyy-MM-dd HH:mm"` (no seconds, no `T`/timezone) — different from RFC3339.
4. **Fields requested**: `summary`, `description`, `status`, `assignee`, `comment`, `created`, `updated`. Description and comment bodies are Atlassian Document Format (ADF) JSON, converted to markdown via `internal/source/atlassian/adf.ToMarkdown`.
5. **Incremental state**: cursor is the max `updated` timestamp (RFC3339) seen per project key, stored in the state file under source `"jira"`, key `{projectKey}`. A small overlap window is subtracted from the cursor before building the next JQL query, to cover Jira Cloud's search-index propagation lag; re-fetching the overlap is safe because the Engram sink upserts by topic_key.
6. **Observation format**: same structure as GitHub issues — Source section, Description, Recent Comments, Participants, Keywords.
7. **Topic key**: `jira/{project-key}/issue-{ISSUE-123}` — upserts as issues evolve.
8. **Auth failures** (401/403) surface as an error wrapping `atlassian.ErrAuthFailed`; the collector logs it loudly and continues with other sources without advancing the Jira cursor. 429s are retried by the shared `atlassian.Client` with a bounded retry loop honoring `Retry-After`.

## Config

```yaml
sources:
  atlassian:
    site_url: https://your-org.atlassian.net
    email: bot@your-org.com
    token: ${ATLASSIAN_API_TOKEN}
    jira:
      enabled: true
      project_keys: ["ENG", "OPS"]
      project: omnia
```

## Adapter shape

```go
// internal/source/jira/jira.go
type Source struct {
    client      *atlassian.Client // shared Atlassian Cloud transport (auth + retry)
    projectKeys []string
    router      projectRouter // minimal ResolveJira(projectKey) interface
    state       core.StateStore
}

func (s *Source) Name() string { return "jira" }
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) { ... }
```
