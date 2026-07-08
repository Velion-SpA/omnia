# Jira Source — Implementation Plan

## Overview

Jira ingestion uses the [Jira REST API v3](https://developer.atlassian.com/cloud/jira/platform/rest/v3/) with email + API token authentication.

## Authentication

```
Authorization: Basic base64(email:api_token)
```

Set `JIRA_EMAIL` and `JIRA_API_TOKEN` environment variables or configure in `config.yaml`.

## Approach

1. **JQL query**: `updated >= -${backfill_days}d ORDER BY updated ASC` with pagination (`startAt`, `maxResults=50`).
2. **Fields**: `summary`, `description`, `status`, `issuetype`, `priority`, `assignee`, `reporter`, `labels`, `comment`, `updated`, `created`.
3. **Incremental state**: Cursor is the last `updated` timestamp per project key, stored in the state file.
4. **Observation format**: Same structure as GitHub issues — Source section, Body (description), Recent Comments, Participants, Keywords.
5. **Topic key**: `jira/{project-key}/issue-{ISSUE-123}` — upserts as issues evolve.

## Config

```yaml
sources:
  jira:
    enabled: true
    base_url: https://your-org.atlassian.net
    project_keys: ["ENG", "OPS"]
    project: omnia
```

## Adapter scaffold

```go
// internal/source/jira/jira.go
type Source struct {
    baseURL  string
    email    string
    token    string
    projects []string
    project  string
    state    core.StateStore
    client   *http.Client
}

func (s *Source) Name() string { return "jira" }
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) { ... }
```
