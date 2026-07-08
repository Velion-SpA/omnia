# WhatsApp Source — Implementation Plan

## Overview

WhatsApp ingestion would use [whatsmeow](https://github.com/tulir/whatsmeow), a Go multi-device library that links Omnia as a secondary device using QR code pairing.

## Approach

1. **Pairing**: On first run, display a QR code in the terminal. The user scans it from WhatsApp on their phone to link Omnia as a secondary device.
2. **Session store**: whatsmeow requires a persistent session store (SQLite recommended via `whatsmeow/store/sqlstore`). Store at `~/.local/state/omnia/whatsapp.db`.
3. **Message ingestion**: Subscribe to `events.Message`. For each incoming message, buffer by chat+day and flush to daily digest observations — same format as Discord.
4. **History sync**: WhatsApp delivers a history sync on first link (up to ~90 days depending on backup settings). Consume via `events.HistorySync`.
5. **Chunking**: Apply the same 45k-rune chunking logic as Discord digests.

## Adapter scaffold

```go
// internal/source/whatsapp/whatsapp.go
type Source struct {
    client  *whatsmeow.Client
    project string
    state   core.StateStore
}

func (s *Source) Name() string { return "whatsapp" }
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]core.Item, error) { ... }
```

## Risks and notes

- **Terms of Service**: Using unofficial WhatsApp automation is a gray area. Accounts have been banned for automated activity. Omnia's use case (read-only archiving from a company account) is low-risk but not zero-risk.
- **No official API**: Meta's Business API does not support history retrieval. whatsmeow is reverse-engineered.
- **Phone dependency**: The linked device goes offline if the primary phone is off for >14 days (multi-device limitation).
- **Group chats**: Group messages require the bot to be a member. Add group JIDs to config.
