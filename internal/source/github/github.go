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
	defaultBaseURL = "https://api.github.com"
	maxComments    = 10
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
	repos   []string
	project string
	token   string
	client  *http.Client
	state   core.StateStore
	baseURL string
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
		repos:   repos,
		project: project,
		token:   token,
		client:  &http.Client{Timeout: 30 * time.Second},
		state:   state,
		baseURL: defaultBaseURL,
	}
}

// NewWithBaseURL creates a Source with a custom base URL (for testing).
func NewWithBaseURL(repos []string, project, configToken string, state core.StateStore, baseURLOverride string) *Source {
	s := New(repos, project, configToken, state)
	s.baseURL = baseURLOverride
	return s
}

// FetchAll is a convenience method that calls Fetch with a zero time (returns everything from fixture server).
func (s *Source) FetchAll(ctx context.Context) ([]core.Item, error) {
	return s.Fetch(ctx, time.Time{})
}

func (s *Source) Name() string { return "github" }

// Fetch retrieves all issues and PRs updated since the given time.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) {
	var items []core.Item
	for _, repo := range s.repos {
		repoItems, err := s.fetchRepo(ctx, repo, since)
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
		item := formatIssue(iss, comments, owner, repoName, s.project)
		items = append(items, item)

		// Update cursor.
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

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 403 {
		return nil, "", fmt.Errorf("rate limited (status %d)", resp.StatusCode)
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

func (s *Source) fetchComments(ctx context.Context, owner, repo string, number int) ([]comment, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=%d&sort=created&direction=desc",
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

func formatIssue(iss issue, comments []comment, owner, repo, project string) core.Item {
	isPR := iss.PullRequest != nil
	itemType := "github-issue"
	topicPrefix := "issue"
	if isPR {
		itemType = "github-pr"
		topicPrefix = "pr"
	}

	topicKey := fmt.Sprintf("github/%s-%s/%s-%d", owner, repo, topicPrefix, iss.Number)
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
			sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n",
				c.User.Login, c.CreatedAt.Format("2006-01-02 15:04"), c.Body))
		}
	}

	if len(participants) > 0 {
		sb.WriteString(fmt.Sprintf("\n## Participants\n\n%s\n", strings.Join(participants, ", ")))
	}

	keywords := enrich.ExtractKeywords(labels, []string{repo, owner}, participants)
	if len(keywords) > 0 {
		sb.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))
	}

	return core.Item{
		Type:      itemType,
		Title:     title,
		Content:   sb.String(),
		Project:   project,
		TopicKey:  enrich.NormalizeTopicKey(topicKey),
		Source:    "github",
		FetchedAt: time.Now(),
	}
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
