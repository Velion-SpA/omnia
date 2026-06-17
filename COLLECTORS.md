# Omnia — External Source Collectors

Status and next steps for pulling external activity into Omnia/Engram.

Omnia already has a pluggable collection pipeline (`internal/core` Source/Sink/StateStore,
`internal/source/*`, `internal/sink/engram`). The nightly `sync` cron (02:00, launchd
`com.velion.omnia`) fetches from each enabled source and upserts the results into Engram
as observations (idempotent by `topic_key`). This doc tracks each source's state and what
you need to decide/do to turn the rest on.

---

## GitHub — commits (NEW, off by default)

**What changed:** the GitHub source already pulled issues and PRs (via `/issues`). It now
also pulls **commits** with their authors — `sha`, message, the GitHub login *and* the git
name/email, date, and URL — as `github-commit` observations. This is exactly the "commits,
who made them" you asked for.

- Code: `internal/source/github/github.go` (`commitResp`, `fetchCommitItems`, `fetchCommits`,
  `formatCommit`). Config: `internal/config/config.go` (`GitHubConfig.IncludeCommits`,
  `MaxCommitsPerRepo`). Wiring: `cmd/omnia/main.go` `runSync`.
- Idempotent: `topic_key = github/<owner>-<repo>/commit-<sha>`, so re-runs never duplicate.
- Separate per-repo cursor (`<repo>#commits`) so it doesn't interfere with the issues/PRs cursor.
- Verified end-to-end (dry-run, real API) against `saluvita`; unit-tested.

**Why it's OFF by default:** the first run backfills up to `max_commits_per_repo` (default
**300**) per repo over `backfill_days`. Across all your repos that can be a *lot* of new
observations in Engram at once. I didn't want to flood Engram while you slept. You decide
when to flip it.

**To turn it on:**

First, deploy the new binary (the prod binary at `~/.local/bin/omnia` predates this code).
With `include_commits` still false this is a no-op behaviourally — the cron keeps doing exactly
what it does today (issues/PRs only) until you flip the flag:

```bash
cp ~/.local/bin/omnia ~/.local/bin/omnia.bak-$(date +%Y%m%d-%H%M%S)   # backup
go build -o ~/.local/bin/omnia ./cmd/omnia                            # from the repo root
```

Then edit `~/.config/omnia/config.yaml`:

```yaml
sources:
  github:
    enabled: true              # already true
    include_commits: true      # <-- add this
    max_commits_per_repo: 100  # optional; default 300. Lower = gentler first backfill
```

Then either wait for the 02:00 cron or run it now. **Preview first (no writes):**

```bash
omnia --dry-run --source github sync   # prints what it WOULD ingest
omnia --source github sync             # actually ingests
```

Tip: to keep the first backfill small, set a short window once: `omnia --since 2026-06-10T00:00:00Z --source github sync`.

---

## GitHub — issues / PRs (already live)

`enabled: true` and running on the 02:00 cron. No action needed. Commits ride the same cron.

---

## Discord (implemented, disabled — needs a bot)

`internal/source/discord/discord.go` is **already complete**: it reads channel message
history over the Discord REST API and stores one `message_digest` observation per channel
per day, with rate-limit handling, pagination, snowflake cursors, and project routing. It
is **read-only** — it never posts. Nothing to build; it just needs credentials.

**To turn it on:**

1. Create a bot app at <https://discord.com/developers/applications> → New Application → Bot.
2. Copy the **bot token**.
3. Invite the bot to your server with the minimum scopes: **View Channel** + **Read Message
   History** only (OAuth2 URL generator → scope `bot`, those two permissions).
4. Enable the **Message Content Intent** in the bot settings (required to read message bodies).
5. Get the channel IDs (Discord → Settings → Advanced → Developer Mode → right-click channel → Copy ID).
6. Configure:

```yaml
sources:
  discord:
    enabled: true
    token: ""                       # better: export DISCORD_BOT_TOKEN in the launchd plist
    channels:
      - { id: "123...", name: "general", guild: "my-server" }
    project: omnia                   # or route per-channel via the `routes:` map
```

**Care notes (your "con cuidado"):** keep the bot scoped to only the channels you want, with
read-only permissions. Don't commit the token to git — prefer the `DISCORD_BOT_TOKEN` env var
in the plist's `EnvironmentVariables`. The collector only reads; it can't send or moderate.

> Note: you said "bot **helper** de discord". This source *ingests* messages (read-only). If
> what you want is a bot that *replies/assists* in Discord, that's a separate build — tell me
> and I'll spec it.

---

## WhatsApp (NOT implemented — decision needed) ⚠️

You asked for your self-chat ("notes to self"). I deliberately did **not** build this yet,
because the only ways to read a personal WhatsApp account are risky. Here are the real options:

| Option | How | Risk |
|---|---|---|
| **A. Manual export → import** (recommended) | In WhatsApp: open the self-chat → ⋯ → Export chat → share the `.txt`/`.zip`. Then `omnia import whatsapp <file>`. | **None to your account.** Manual, you control when. |
| B. Unofficial lib (Baileys / whatsapp-web.js) | Scrapes WhatsApp Web via a linked session (scan a QR, keep it alive). | **Can get your personal number BANNED.** Violates WhatsApp ToS. Fragile (breaks on WA updates). |
| C. WhatsApp Cloud / Business API (official) | Meta's official API. | Only for **business** numbers, not your personal account/self-chat. Doesn't fit. |

**My recommendation: Option A.** It's safe, and the self-chat export is exactly the "stuff I
send myself" you described. I can implement `omnia import whatsapp <export>` quickly — but the
export's text format varies by locale (es-CL) and iOS vs Android, so **I need one sample
export from you to calibrate the parser**. Drop a small exported chat in the repo (or paste a
few lines) and I'll wire it up.

If you want Option B despite the ban risk, say so explicitly and I'll build it isolated behind
a disabled flag — but I won't point it at your real number without your clear go-ahead.

---

## Cron — no new job needed

The existing `com.velion.omnia` launchd job (02:00, `omnia --source github sync`) already
runs the GitHub source; commits flow through it once `include_commits: true`. The embed job
(02:15) re-embeds afterward. I did **not** touch either cron. When Discord is configured,
the same `sync` job (run without `--source github`, i.e. `omnia sync`) will run both sources;
we can update the plist's `ProgramArguments` to drop the `--source github` filter then.

---

## Ideas for more sources (not built — tell me if you use these)

- **Linear / Jira** — `meta.go` already reserves a `jira` source kind. Easy to add if you use one.
- **Google Calendar** — events as observations (needs OAuth setup).
- **Local notes / Obsidian vault** — ingest markdown files on a path.
- **Per-commit diffs/stats** — current commit ingestion stores message + author; we could add
  files-changed/+−lines (one extra API call per commit; heavier).

---

## Decisions I need from you

1. **GitHub commits:** OK to flip `include_commits: true`? Any repos to *exclude*? Good
   `max_commits_per_repo` for the first backfill?
2. **Discord:** do you want message ingestion (built, needs bot token + channels), a reply
   bot (separate build), or both?
3. **WhatsApp:** Option A (safe export-import — send me a sample) or Option B (unofficial,
   ban risk — explicit go-ahead required)?
4. **Any other source** from the ideas list?

Nothing here is live yet except what was already running (GitHub issues/PRs). The new commit
code is committed but gated off until you decide.
