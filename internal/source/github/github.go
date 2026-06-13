package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/enrich"
)

const (
	defaultBaseURL    = "https://api.github.com"
	maxComments       = 10
	maxCommentBodyLen = 2000
	maxChunkRunes     = 45000
	// maxBaseKeyLen ensures base + "-partNN" stays ≤ 120 chars after normalization.
	// We reserve 7 chars for "-part99" to cover up to 99 parts safely.
	maxBaseKeyLen = 113
)

// issue represents a GitHub issue or PR from the API.
type issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	Body      string    `json:"body"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
	Head *struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base *struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Draft  bool `json:"draft"`
	Merged bool `json:"merged"`
}

// comment represents a single GitHub comment.
type comment struct {
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// Source fetches GitHub issues and PRs.
type Source struct {
	repos       []string
	project     string
	token       string
	backfillDays int
	client      *http.Client
	state       core.StateStore
	baseURL     string
}

// New creates a GitHub Source. Token resolution: GITHUB_TOKEN env → configToken → `gh auth token`.
func New(repos []string, project string, configToken string, state core.StateStore) *Source {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = configToken
	}
	if token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	}
	return &Source{
		repos:        repos,
		project:      project,
		token:        token,
		backfillDays: 30,
		client:       &http.Client{Timeout: 30 * time.Second},
		state:        state,
		baseURL:      defaultBaseURL,
	}
}

// NewWithBaseURL creates a Source with a custom base URL (for testing).
func NewWithBaseURL(repos []string, project, configToken string, state core.StateStore, baseURLOverride string) *Source {
	s := New(repos, project, configToken, state)
	s.baseURL = baseURLOverride
	return s
}

// SetBackfillDays overrides the default 30-day backfill window used when no cursor exists.
func (s *Source) SetBackfillDays(days int) {
	if days > 0 {
		s.backfillDays = days
	}
}

// FetchAll is a convenience method that calls Fetch with a zero time (returns everything from fixture server).
func (s *Source) FetchAll(ctx context.Context) ([]core.Item, error) {
	return s.Fetch(ctx, time.Time{})
}

func (s *Source) Name() string { return "github" }

// Fetch retrieves all issues and PRs updated since the given time.
// When since is zero, each repo's stored cursor is used as the lower bound;
// if no cursor exists, the source falls back to now minus backfillDays.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) {
	var items []core.Item
	for _, repo := range s.repos {
		// C1: resolve per-repo since from state cursor when no explicit override.
		repoSince := since
		if repoSince.IsZero() && s.state != nil {
			if cursor, ok := s.state.GetCursor("github", repo); ok {
				t, err := time.Parse(time.RFC3339, cursor)
				if err == nil {
					repoSince = t
				}
			}
		}
		if repoSince.IsZero() {
			repoSince = time.Now().AddDate(0, 0, -s.backfillDays)
		}

		repoItems, err := s.fetchRepo(ctx, repo, repoSince)
		if err != nil {
			return nil, fmt.Errorf("fetch repo %s: %w", repo, err)
		}
		items = append(items, repoItems...)
	}
	return items, nil
}

func (s *Source) fetchRepo(ctx context.Context, repo string, since time.Time) ([]core.Item, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format %q, expected owner/repo", repo)
	}
	owner, repoName := parts[0], parts[1]

	sinceStr := since.UTC().Format(time.RFC3339)
	url := fmt.Sprintf("%s/repos/%s/%s/issues?state=all&since=%s&per_page=100&sort=updated",
		s.baseURL, owner, repoName, sinceStr)

	var allIssues []issue
	for url != "" {
		issues, nextURL, err := s.fetchPage(ctx, url)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, issues...)
		url = nextURL
		if len(issues) == 0 {
			break
		}
	}

	var items []core.Item
	for _, iss := range allIssues {
		comments, _ := s.fetchComments(ctx, owner, repoName, iss.Number)
		// C3: formatIssue may produce multiple chunked items.
		issueItems := formatIssue(iss, comments, owner, repoName, s.project)
		items = append(items, issueItems...)

		// Update cursor to the latest updated_at seen for this repo.
		if s.state != nil {
			cursorKey := repo
			if v, ok := s.state.GetCursor("github", cursorKey); !ok || iss.UpdatedAt.UTC().Format(time.RFC3339) > v {
				s.state.SetCursor("github", cursorKey, iss.UpdatedAt.UTC().Format(time.RFC3339))
			}
		}
	}
	return items, nil
}

func (s *Source) fetchPage(ctx context.Context, url string) ([]issue, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	// W1: distinguish rate limit (X-RateLimit-Remaining: 0) from forbidden (bad token/scope).
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			// Honor Retry-After if present; fall back to X-RateLimit-Reset delta.
			sleep := retryAfterDuration(resp, 60*time.Second)
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(sleep):
			}
			// Retry once after sleeping.
			return s.fetchPage(ctx, url)
		}
		return nil, "", fmt.Errorf("forbidden: check token scope/repo access (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}

	var issues []issue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, "", fmt.Errorf("decode issues: %w", err)
	}

	// Parse Link header for pagination.
	nextURL := parseLinkNext(resp.Header.Get("Link"))
	return issues, nextURL, nil
}

// retryAfterDuration parses the Retry-After header (seconds) or derives a wait
// from X-RateLimit-Reset (Unix timestamp). Falls back to fallback.
func retryAfterDuration(resp *http.Response, fallback time.Duration) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		var secs int
		if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > fallback {
				return fallback
			}
			return d
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		var resetUnix int64
		if _, err := fmt.Sscanf(v, "%d", &resetUnix); err == nil {
			d := time.Until(time.Unix(resetUnix, 0))
			if d > fallback {
				return fallback
			}
			if d > 0 {
				return d
			}
		}
	}
	return fallback
}

func (s *Source) fetchComments(ctx context.Context, owner, repo string, number int) ([]comment, error) {
	// S1: direction=asc so comments appear in chronological order.
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=%d&sort=created&direction=asc",
		s.baseURL, owner, repo, number, maxComments)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var comments []comment
	json.NewDecoder(resp.Body).Decode(&comments)
	return comments, nil
}

// formatIssue builds one or more Items for an issue/PR.
// C3: if the formatted content exceeds 45k runes it is split into multiple
// Items with topic_key suffixes "-part1", "-part2", etc. Each continuation chunk
// carries a brief context header so the content is useful in isolation (S4a).
func formatIssue(iss issue, comments []comment, owner, repo, project string) []core.Item {
	isPR := iss.PullRequest != nil
	itemType := "github-issue"
	topicPrefix := "issue"
	if isPR {
		itemType = "github-pr"
		topicPrefix = "pr"
	}

	rawTopicKey := fmt.Sprintf("github/%s-%s/%s-%d", owner, repo, topicPrefix, iss.Number)
	title := fmt.Sprintf("[%s#%d] %s (%s)", repo, iss.Number, iss.Title, iss.State)

	// Build labels list.
	var labels []string
	for _, l := range iss.Labels {
		labels = append(labels, l.Name)
	}

	// Build participants.
	participants := []string{iss.User.Login}
	seen := map[string]bool{iss.User.Login: true}
	for _, a := range iss.Assignees {
		if !seen[a.Login] {
			participants = append(participants, a.Login)
			seen[a.Login] = true
		}
	}
	for _, c := range comments {
		if !seen[c.User.Login] {
			participants = append(participants, c.User.Login)
			seen[c.User.Login] = true
		}
	}

	var sb strings.Builder
	sb.WriteString("## Source\n\n")
	sb.WriteString(fmt.Sprintf("- Repository: %s/%s\n", owner, repo))
	sb.WriteString(fmt.Sprintf("- URL: %s\n", iss.HTMLURL))
	sb.WriteString(fmt.Sprintf("- Author: %s\n", iss.User.Login))
	sb.WriteString(fmt.Sprintf("- Created: %s\n", iss.CreatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- Updated: %s\n", iss.UpdatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- State: %s\n", iss.State))
	if len(labels) > 0 {
		sb.WriteString(fmt.Sprintf("- Labels: %s\n", strings.Join(labels, ", ")))
	}
	if isPR && iss.Head != nil && iss.Base != nil {
		sb.WriteString(fmt.Sprintf("- Branch: %s → %s\n", iss.Head.Ref, iss.Base.Ref))
		merged := "no"
		if iss.Merged {
			merged = "yes"
		}
		sb.WriteString(fmt.Sprintf("- Merged: %s\n", merged))
		if iss.Draft {
			sb.WriteString("- Draft: yes\n")
		}
	}

	sb.WriteString("\n## Body\n\n")
	body := enrich.TruncateContent(iss.Body, 5000)
	sb.WriteString(body)
	sb.WriteString("\n")

	if len(comments) > 0 {
		sb.WriteString("\n## Recent Comments\n\n")
		for _, c := range comments {
			// C3 / S1: cap each comment body to prevent unbounded growth.
			truncBody := enrich.TruncateContent(c.Body, maxCommentBodyLen)
			sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n",
				c.User.Login, c.CreatedAt.Format("2006-01-02 15:04"), truncBody))
		}
	}

	if len(participants) > 0 {
		sb.WriteString(fmt.Sprintf("\n## Participants\n\n%s\n", strings.Join(participants, ", ")))
	}

	keywords := enrich.ExtractKeywords(labels, []string{repo, owner}, participants)
	if len(keywords) > 0 {
		sb.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))
	}

	fullContent := sb.String()

	// S4b: truncate the base key so base + "-partNN" stays within 120 chars after
	// normalization (NormalizeTopicKey caps at 120). We truncate the raw key before
	// normalization using the same budget.
	normalizedBase := enrich.NormalizeTopicKey(rawTopicKey)
	if len([]rune(normalizedBase)) > maxBaseKeyLen {
		normalizedBase = string([]rune(normalizedBase)[:maxBaseKeyLen])
	}

	// S4a: build continuation header for chunks beyond the first.
	contextHeader := fmt.Sprintf("<!-- %s/%s | %s#%d | %s -->\n\n",
		owner, repo, topicPrefix, iss.Number, iss.UpdatedAt.Format("2006-01-02"))

	// C3: chunk using enrich.ChunkContent; pass a header for continuation chunks.
	chunks := enrich.ChunkContent(fullContent, maxChunkRunes)

	var items []core.Item
	for i, chunk := range chunks {
		topicKey := normalizedBase
		suffix := ""
		content := chunk
		if len(chunks) > 1 {
			topicKey = fmt.Sprintf("%s-part%d", normalizedBase, i+1)
			suffix = fmt.Sprintf(" (part %d/%d)", i+1, len(chunks))
			if i > 0 {
				content = contextHeader + chunk
			}
		}
		itemTitle := title
		if suffix != "" {
			itemTitle = title + suffix
		}
		items = append(items, core.Item{
			Type:      itemType,
			Title:     itemTitle,
			Content:   content,
			Project:   project,
			TopicKey:  topicKey,
			Source:    "github",
			FetchedAt: time.Now(),
		})
	}
	return items
}

// parseLinkNext extracts the "next" URL from a GitHub Link header.
func parseLinkNext(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		url := strings.Trim(strings.TrimSpace(segments[0]), "<>")
		rel := strings.TrimSpace(segments[1])
		if rel == `rel="next"` {
			return url
		}
	}
	return ""
}
