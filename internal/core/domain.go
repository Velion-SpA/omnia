package core

// Item type taxonomy — stable identifiers for consumer filtering.
//
// GitHub:
//
//	github-pr            — a pull request observation
//	github-issue         — an issue observation (not a PR)
//	github-commit-digest — future: daily commit digest
//
// Discord:
//
//	discord-digest       — daily message digest for a channel
//
// Future sources:
//
//	jira-issue           — Jira ticket observation
//	whatsapp-digest      — WhatsApp daily chat digest
//
// The type field is the positive-filter axis for consumers:
// e.g. "give me only github-pr items" → filter type == "github-pr".
// The omnia-meta Kind field maps to these types as follows:
//
//	pull_request   → github-pr
//	issue          → github-issue
//	message_digest → discord-digest

import "time"

// Item is a normalized piece of knowledge to be stored in Engram.
type Item struct {
	Type      string
	Title     string
	Content   string
	Project   string
	TopicKey  string
	Source    string
	FetchedAt time.Time
}
