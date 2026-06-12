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
)

const (
	defaultDiscordAPI = "https://discord.com/api/v10"
	maxChunkRunes     = 45000
	pageSize          = 100
)

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

// Source fetches Discord channel messages and produces daily digests.
type Source struct {
	channels []config.ChannelConfig
	project  string
	token    string
	client   *http.Client
	state    core.StateStore
	baseURL  string
}

// New creates a Discord Source. Token from DISCORD_BOT_TOKEN env or config.
func New(channels []config.ChannelConfig, project, configToken string, state core.StateStore) *Source {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	if token == "" {
		token = configToken
	}
	return &Source{
		channels: channels,
		project:  project,
		token:    token,
		client:   &http.Client{Timeout: 30 * time.Second},
		state:    state,
		baseURL:  defaultDiscordAPI,
	}
}

// NewWithBaseURL creates a Source with a custom base URL (for testing).
func NewWithBaseURL(channels []config.ChannelConfig, project, token string, state core.StateStore, baseURLOverride string) *Source {
	s := New(channels, project, token, state)
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
			return nil, err
		}
		if len(msgs) == 0 {
			break
		}
		allMessages = append(allMessages, msgs...)
		afterID = msgs[len(msgs)-1].ID

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

	// Group messages by day (UTC).
	byDay := make(map[string][]message)
	for _, m := range allMessages {
		day := m.Timestamp.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], m)
	}

	var items []core.Item
	for day, msgs := range byDay {
		dayItems := buildDailyDigest(ch, day, msgs, s.project)
		items = append(items, dayItems...)
	}
	return items, nil
}

func (s *Source) fetchPage(ctx context.Context, channelID, afterID string) ([]message, error) {
	url := fmt.Sprintf("%s/channels/%s/messages?limit=%d", s.baseURL, channelID, pageSize)
	if afterID != "" {
		url += "&after=" + afterID
	}

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
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		// Basic backoff — in production a real Retry-After parse would go here.
		return nil, fmt.Errorf("discord rate limited (429)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord GET messages returned %d", resp.StatusCode)
	}

	var msgs []message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("decode discord messages: %w", err)
	}

	// Discord returns newest-first; reverse to chronological order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// buildDailyDigest formats a set of messages into one or more Engram observations.
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

	// Chunk if needed.
	baseTopicKey := fmt.Sprintf("discord/%s/%s/%s", guildLabel, ch.Name, day)
	chunks := enrich.ChunkContent(fullContent, maxChunkRunes)

	var items []core.Item
	for i, chunk := range chunks {
		topicKey := baseTopicKey
		suffix := ""
		if len(chunks) > 1 {
			topicKey = fmt.Sprintf("%s-part%d", baseTopicKey, i+1)
			suffix = fmt.Sprintf(" (part %d/%d)", i+1, len(chunks))
		}
		title := fmt.Sprintf("[#%s] Daily digest %s%s", ch.Name, day, suffix)
		items = append(items, core.Item{
			Type:      "discord-digest",
			Title:     title,
			Content:   chunk,
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
