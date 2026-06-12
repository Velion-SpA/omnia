package enrich

import (
	"strings"
	"unicode"
)

// TruncateContent ensures content does not exceed maxLen runes, appending a truncation note.
func TruncateContent(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "\n\n[content truncated]"
}

// NormalizeTopicKey converts a raw string to a valid Engram topic key:
// lowercase, spaces to hyphens, max 120 chars.
func NormalizeTopicKey(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r == ' ' || r == '_':
			b.WriteRune('-')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '/':
			b.WriteRune(r)
		}
	}
	result := b.String()
	if len(result) > 120 {
		result = result[:120]
	}
	return result
}

// ChunkContent splits content into chunks of at most chunkSize runes.
// Returns one or more chunks.
func ChunkContent(content string, chunkSize int) []string {
	runes := []rune(content)
	if len(runes) <= chunkSize {
		return []string{content}
	}
	var chunks []string
	for len(runes) > 0 {
		end := chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}

// ExtractKeywords returns a deduplicated, lowercased list of keywords from the provided slices.
func ExtractKeywords(sources ...[]string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, src := range sources {
		for _, kw := range src {
			k := strings.ToLower(strings.TrimSpace(kw))
			if k == "" {
				continue
			}
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				result = append(result, k)
			}
		}
	}
	return result
}
