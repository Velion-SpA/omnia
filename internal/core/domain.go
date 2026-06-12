package core

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
