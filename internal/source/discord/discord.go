package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/enrich"
	"github.com/velion/omnia/internal/meta"
)

const (
	defaultDiscordAPI = "https://discord.com/api/v10"
	maxChunkRunes     = 45000
	pageSize          = 100
	maxRateLimitSleep = 60 * time.Second
	maxRetries        = 3

	// Discord message types included in digests.
	// Type 0 = DEFAULT, Type 19 = REPLY.
	msgTypeDefault = 0
	msgTypeReply   = 19
)

// projectRouter is a minimal interface to avoid circular imports.
type projectRouter interface {
	ResolveDiscord(channelID string, guildSlug string) string
}

// message represents a Discord message from the API.
type message struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Author    struct {
		Username string `json:"username"`
		ID       string `json:"id"`
	} `json:"author"`
	Type int `json:"type"`
}

// rateLimitResponse is the JSON body Discord sends with 429 responses.
type rateLimitResponse struct {
	RetryAfter float64 `json:"retry_after"` // seconds, may be fractional
}

// Source fetches Discord channel messages and produces daily digests.
type Source struct {
	channels []config.ChannelConfig
	router   projectRouter
	token    string
	client   *http.Client
	state    core.StateStore
	baseURL  string
}

// New creates a Discord Source. Token from DISCORD_BOT_TOKEN env or config.
func New(channels []config.ChannelConfig, router projectRouter, configToken string, state core.StateStore) *Source {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	if token == "" {
		token = configToken
	}
	return &Source{
		channels: channels,
		router:   router,
		token:    token,
		client:   &http.Client{Timeout: 30 * time.Second},
		state:    state,
		baseURL:  defaultDiscordAPI,
	}
}

// NewWithBaseURL creates a Source with a custom base URL (for testing).
func NewWithBaseURL(channels []config.ChannelConfig, router projectRouter, token string, state core.StateStore, baseURLOverride string) *Source {
	s := New(channels, router, token, state)
	s.baseURL = baseURLOverride
	return s
}

func (s *Source) Name() string { return "discord" }

// Fetch retrieves messages from all configured channels since the given time and groups them into daily digests.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) {
	var items []core.Item
	for _, ch := range s.channels {
		chItems, err := s.fetchChannel(ctx, ch, since)
		if err != nil {
			return nil, fmt.Errorf("fetch channel %s: %w", ch.Name, err)
		}
		items = append(items, chItems...)
	}
	return items, nil
}

func (s *Source) fetchChannel(ctx context.Context, ch config.ChannelConfig, since time.Time) ([]core.Item, error) {
	// Determine the "after" snowflake cursor from state or since timestamp.
	afterID := ""
	if s.state != nil {
		if cursor, ok := s.state.GetCursor("discord", ch.ID); ok {
			afterID = cursor
		} else {
			// Convert since time to a Discord snowflake approximation.
			afterID = timeToSnowflake(since)
		}
	} else {
		afterID = timeToSnowflake(since)
	}

	var allMessages []message
	for {
		msgs, err := s.fetchPage(ctx, ch.ID, afterID)
		if err != nil {
			// C4: SetCursor calls above are in-memory only. On failure we do NOT flush,
			// which is consistent with C2: the next run will re-fetch from the last
			// flushed cursor (safe because Engram upserts on topic_key).
			return nil, err
		}
		if len(msgs) == 0 {
			break
		}
		allMessages = append(allMessages, msgs...)
		afterID = msgs[len(msgs)-1].ID

		// C4: cursor is advanced in-memory page by page. If a later page fails the
		// in-memory progress is discarded and the run restarts from the last flushed
		// cursor. Re-ingestion is safe because Engram upserts on topic_key.
		if s.state != nil {
			s.state.SetCursor("discord", ch.ID, afterID)
		}

		if len(msgs) < pageSize {
			break
		}
	}

	if len(allMessages) == 0 {
		return nil, nil
	}

	// S2: filter out system messages; keep only user messages (type 0) and replies (type 19).
	var userMessages []message
	for _, m := range allMessages {
		if m.Type == msgTypeDefault || m.Type == msgTypeReply {
			userMessages = append(userMessages, m)
		}
	}
	if len(userMessages) == 0 {
		return nil, nil
	}

	// Group messages by day (UTC).
	byDay := make(map[string][]message)
	for _, m := range userMessages {
		day := m.Timestamp.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], m)
	}

	// Resolve project for this channel.
	project := s.router.ResolveDiscord(ch.ID, ch.Guild)

	var items []core.Item
	for day, msgs := range byDay {
		dayItems := buildDailyDigest(ch, day, msgs, project)
		items = append(items, dayItems...)
	}
	return items, nil
}

func (s *Source) fetchPage(ctx context.Context, channelID, afterID string) ([]message, error) {
	url := fmt.Sprintf("%s/channels/%s/messages?limit=%d", s.baseURL, channelID, pageSize)
	if afterID != "" {
		url += "&after=" + afterID
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bot "+s.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET discord messages: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			// W2: parse Retry-After from header or JSON body; cap at maxRateLimitSleep.
			sleep := parseDiscordRetryAfter(resp)
			resp.Body.Close()

			if attempt >= maxRetries {
				return nil, fmt.Errorf("discord rate limited (429) after %d retries", maxRetries)
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(sleep):
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("discord GET messages returned %d", resp.StatusCode)
		}

		var msgs []message
		if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode discord messages: %w", err)
		}
		resp.Body.Close()

		// Discord returns newest-first; reverse to chronological order.
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
		return msgs, nil
	}

	return nil, fmt.Errorf("discord GET messages: exceeded retry limit")
}

// parseDiscordRetryAfter extracts the retry delay from a 429 response.
// It checks the Retry-After header first, then falls back to the JSON body's
// retry_after field. The result is capped at maxRateLimitSleep.
func parseDiscordRetryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		var secs float64
		if _, err := fmt.Sscanf(v, "%f", &secs); err == nil && secs > 0 {
			d := time.Duration(secs * float64(time.Second))
			if d > maxRateLimitSleep {
				return maxRateLimitSleep
			}
			return d
		}
	}

	// Try JSON body.
	var body rateLimitResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.RetryAfter > 0 {
		d := time.Duration(body.RetryAfter * float64(time.Second))
		if d > maxRateLimitSleep {
			return maxRateLimitSleep
		}
		return d
	}

	// Fallback: 5 seconds.
	return 5 * time.Second
}

// buildDailyDigest formats a set of messages into one or more Engram observations.
// The resolved project is passed directly as a string.
func buildDailyDigest(ch config.ChannelConfig, day string, msgs []message, project string) []core.Item {
	guildLabel := ch.Guild
	if guildLabel == "" {
		guildLabel = "unknown-guild"
	}

	// Build participants.
	seen := make(map[string]bool)
	var participants []string
	for _, m := range msgs {
		if !seen[m.Author.Username] {
			seen[m.Author.Username] = true
			participants = append(participants, m.Author.Username)
		}
	}

	// Build message lines.
	var lines strings.Builder
	for _, m := range msgs {
		ts := m.Timestamp.UTC().Format("15:04")
		text := strings.ReplaceAll(m.Content, "\n", " ")
		lines.WriteString(fmt.Sprintf("[%s] %s: %s\n", ts, m.Author.Username, text))
	}

	// Build content.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Channel\n\n#%s | %s | %s\n\n", ch.Name, guildLabel, day))
	sb.WriteString("## Messages\n\n")
	sb.WriteString(lines.String())
	sb.WriteString(fmt.Sprintf("\n## Participants\n\n%s\n", strings.Join(participants, ", ")))
	keywords := enrich.ExtractKeywords(participants, []string{ch.Name, guildLabel})
	sb.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))

	fullContent := sb.String()

	// S4b: reserve space for "-partNN" suffix within the 120-char normalized limit.
	const maxDiscordBaseKeyLen = 113
	rawBase := fmt.Sprintf("discord/%s/%s/%s", guildLabel, ch.Name, day)
	normalizedBase := enrich.NormalizeTopicKey(rawBase)
	if len([]rune(normalizedBase)) > maxDiscordBaseKeyLen {
		normalizedBase = string([]rune(normalizedBase)[:maxDiscordBaseKeyLen])
	}

	// S4a: context header for continuation chunks.
	contextHeader := fmt.Sprintf("<!-- #%s | %s | %s -->\n\n", ch.Name, guildLabel, day)

	// Build meta struct for this digest.
	ingestedAt := time.Now().UTC()
	m := meta.Meta{
		SchemaVersion: meta.SchemaVersion,
		Source:        "discord",
		Kind:          "message_digest",
		Layer:         "ingested",
		Project:       project,
		IngestedAt:    ingestedAt,
	}

	// C3: split human-readable content into chunks, reserving budget for the meta block.
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

		// Append meta block to this chunk, ensuring it starts on its own line.
		if !strings.HasSuffix(content, "\n") {
			content = content + "\n"
		}
		content = content + meta.Render(m)

		title := fmt.Sprintf("[#%s] Daily digest %s%s", ch.Name, day, suffix)
		items = append(items, core.Item{
			Type:      "discord-digest",
			Title:     title,
			Content:   content,
			Project:   project,
			TopicKey:  enrich.NormalizeTopicKey(topicKey),
			Source:    "discord",
			FetchedAt: time.Now(),
		})
	}
	return items
}

// timeToSnowflake converts a time to an approximate Discord snowflake ID.
// Formula: ((timestamp_ms - discord_epoch) << 22)
func timeToSnowflake(t time.Time) string {
	const discordEpoch = 1420070400000 // 2015-01-01 in ms
	ms := t.UnixMilli()
	snowflake := (ms - discordEpoch) << 22
	if snowflake < 0 {
		snowflake = 0
	}
	return fmt.Sprintf("%d", snowflake)
}
