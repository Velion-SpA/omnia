# Omnia Cloud Dashboard — Design System

Status: **foundation, slice 0** of "Command Center v2". This is the first written
design brief for a system that has existed only as code since the original
Command Center redesign (see git history / `internal/dashboard/data.go` era). It
documents what already exists in `internal/ui/static/omnia.css` plus the
consolidation done in this slice. It does **not** redesign any page — see
"Non-goals" below.

## Identity

**Sobrio command-center.** Dark-only, by decision — there is no light theme and
none is planned. The aesthetic reads as an operations console: dense
information, monospace numerics, a single accent color used sparingly (status,
links, active state, primary actions), condensed display type for headings,
subtle scanline/grid texture on the app background.

- Dark base `#080a10`, calypso accent `#22d3ee`.
- Display type: **Barlow Condensed** (headings, tile values, wordmark).
- Body/UI type: **JetBrains Mono** (everything else — labels, tables, buttons,
  metadata).
- `html[data-theme="dark"]` is hardcoded in `internal/ui/layout.templ`. There is
  no theme toggle and no `prefers-color-scheme` branch — this is intentional,
  not an oversight.

## Token system

All tokens live in `internal/ui/static/omnia.css`, on `:root`. Tokens are
additive in this slice — introducing a scale does not force every existing
rule to adopt it immediately; existing literals keep rendering identically.
Future slices should prefer the token over a new hardcoded literal.

### Color

Two roles per palette entry: a **canonical** custom property (the source of
truth) and, where one existed before this slice, a same-value **alias** kept
only so nothing else has to change today.

| Role | Canonical | Alias (kept, do not add new uses) |
| --- | --- | --- |
| Base surfaces | `--bg`, `--surface`, `--surface-2`, `--surface-3` | — |
| Borders | `--border`, `--border-2`, `--border-strong`, `--border-accent` | — |
| Accent | `--calypso` | `--accent` → `var(--calypso)` |
| Accent tints | `--accent-dim`, `--accent-glow`, `--accent-mid`, `--calypso-dim` | *(not duplicates — different values/purposes; `--calypso-dim` is a darker solid shade, `--accent-dim/-glow/-mid` are translucent tints)* |
| Text (secondary) | `--text-2` | `--text-dim` → `var(--text-2)` |
| Text (tertiary) | `--text-faint` | `--text-3` → `var(--text-faint)` |
| Text (primary) | `--text` | — |
| Silver (muted UI chrome) | `--silver` | — |
| Semantic — ok | `--ok`, `--green` | *(same value, both still independently defined — not touched this slice, see "Known follow-ups")* |
| Semantic — warn | `--warn`, `--amber` | *(same value, both still independently defined — not touched this slice, see "Known follow-ups")* |
| Semantic — bad | `--bad` | — |
| Palette accents | `--violet`, `--pink`, `--blue` | — |

The six-color signal palette (calypso / violet / pink / blue / green / amber)
is used for type badges, memory-card accent bars and chart bars — one hue per
category, never combined for decoration.

### Type scale

```css
--fs-2xs: 8px;   /* pips, tiniest chip labels */
--fs-xs:  9px;   /* tile-label, feed-tag */
--fs-sm:  10px;  /* micro-label, table headers, badges */
--fs-base: 11px; /* body default, data rows */
--fs-md:  13px;  /* card copy, form controls */
--fs-lg:  15px;  /* card titles */
--fs-xl:  18px;  /* empty-state heading */
--fs-2xl: 22px;  /* section headers */
--fs-3xl: 28px;  /* metric values */
--fs-4xl: 44px;  /* hero tile values (Overview) */
```

### Spacing scale

```css
--space-1: 4px;
--space-2: 8px;
--space-3: 12px;
--space-4: 16px;
--space-5: 20px;
--space-6: 24px;
--space-8: 32px;
```

### Radius scale

```css
--radius-sm:   3px;
--radius:      5px;   /* existing canonical default — unchanged */
--radius-md:   8px;
--radius-lg:   12px;
--radius-pill: 9999px;
```

### Shadow scale

```css
--shadow-sm: 0 8px 20px rgba(4,9,18,0.3);
--shadow-md: 0 12px 30px rgba(4,9,18,0.35);
--shadow-lg: 0 20px 60px rgba(0,0,0,0.5);
```

### Motion / easing scale

```css
--ease-standard: cubic-bezier(0.16,1,0.3,1);
--dur-fast:   0.12s;
--dur-base:   0.15s;
--dur-slow:   0.3s;
--dur-reveal: 0.5s;
```

## Component inventory

- **Shell** — `internal/ui/layout.templ` (`ui.Layout`): nav, wordmark, clock,
  status chip, optional user/logout, page-main, footer. Shared by local + cloud.
- **Cards** — `.card` (default content card), `.card-elevated` (raised
  variant), `.panel` / `.panel.bracketed` / `.panel.accented` (Overview bento
  tiles), `.projcard`-style project/metric cards (`.stat-card`, `.metric-card`,
  `.project-card`).
- **Memory feed** — `ui.MemoryFeed` / `ui.Card` (`internal/ui/cards.templ`):
  the canonical memory-card renderer, shared by Browse and the cloud dashboard.
- **Tables** — `.data-table` (one definition as of this slice — see Cleanup).
- **Badges** — `.badge` + status/type/source modifiers (one base definition as
  of this slice — see Cleanup).
- **Buttons** — two families, unified authoring (see "Buttons" below).
- **Empty state** — `emptyState(msg)` (`internal/dashboard/layout.templ`), a
  centered icon + message block. Compact inline "No X yet" one-liners inside
  existing cards are a *different, smaller* pattern — see "Known follow-ups".
- **Confirm dialog** — `ui.ConfirmDialog` (`internal/ui/confirm.templ`, new this
  slice): a reusable inline confirm card matching the existing
  soft/hard-delete pattern in `internal/dashboard/detail.templ`. Ready for
  later slices (Users/Access/Projects) to adopt for destructive actions.
- **Searchable selector** — `data-proj-select` + `internal/ui/static/admin.js`
  (`enhanceSelector`): today project-specific; generalizing it into a
  data-source-agnostic `searchableSelect` is a **known follow-up**, not done
  in this slice (see below).

### Buttons

There are two button *families*, not three: `.pill-btn*` (fully-rounded pill,
used on the local-dashboard content pages — Browse, Detail) and
`.shell-button*` (rounded-rectangle, used on cloud Admin pages and the login
screen). A third, `.btn` / `.btn-primary` / `.btn-danger`, existed in the CSS
but had **zero markup usages** anywhere in the codebase — pure dead weight —
and was deleted in this slice.

The two live families are now authored as **one system** in `omnia.css`: a
shared base ruleset (`.pill-btn, .shell-button`) carries the properties both
share (flex layout, cursor, transition, no underline), and each family's block
only carries what's genuinely different (shape/radius/padding/type). Primary
/ secondary / ghost / danger color logic is expressed once per shared token
(`--calypso`, `--bad`, `--surface-2`) rather than re-declared per family.

Unifying `.pill-btn` and `.shell-button` into a single **shape** was
deliberately **not** done — that changes what buttons look like on every page
that uses either family, which is a visual decision for Benja to make, not a
"cleanup". No markup changed in this slice; every existing button renders
pixel-identical to before.

## Responsive intent

**Desktop-first + tablet.** The dashboard is an operator tool used on real
monitors; there is currently **zero** `@media` coverage anywhere in the CSS.
That stays true after this slice — breakpoints are an explicit **non-goal**
here (see below) and are scheduled as their own follow-up once the page-level
redesigns (Browse, Projects, etc.) land, so breakpoints get designed against
the final markup instead of the current one.

## Cleanup done in this slice

- Deleted `internal/ui/overview.templ` (208 lines, dead — zero callers;
  `internal/dashboard/overview.templ` is the live Overview page and has fully
  diverged from it).
- Deleted `obsCard` / `obsSourceChip` in `internal/dashboard/browse.templ`
  (dead — `obsCardFeed` renders through the shared `ui.MemoryFeed` instead).
- Merged the 3 conflicting duplicate CSS definitions (`.card`, `.badge`,
  `.data-table`) into one definition each, keeping the previously-winning
  (later-in-file, cascade-computed) property values so nothing renders
  differently.
- Deleted the dead `.btn` / `.btn-primary` / `.btn-danger` block (zero
  markup usages).
- Threaded `ui.LayoutProps.Active` through `internal/dashboard`'s
  `layoutPropsForContext` / `layout()` so `aria-current="page"` renders on the
  active nav item dashboard-wide (it already worked on the cloud Admin pages
  via `adminLayoutProps`, which already set `Active`).

## Known follow-ups (identified, not done here — flagged for a deliberate decision)

- **`pico.min.css`**: no `pico-`-prefixed class is used anywhere in the
  templates, so there is no *direct* dependency. But pico also resets bare
  element selectors (`table`, `button`, `input`, headings, etc.) sitewide, and
  the review found at least one real reliance (the unclassed `<table>` at
  `internal/cloud/cloudserver/admin_ui.templ:46`). Removing the stylesheet
  needs a real visual pass across all 14 pages to be sure there's no *second*
  hidden reliance — that verification couldn't be done safely from static
  code review alone, so the `<link>` stays for now.
- **`--ok`/`--green` and `--warn`/`--amber`**: same-value duplicate pairs, same
  shape as the 3 pairs de-duplicated above. Not touched in this slice to keep
  the diff scoped to what was explicitly asked; same treatment (alias the
  less-used name) is a safe 10-minute follow-up.
- **Divergent `<h1>` styles**: three different hand-rolled page-title styles
  exist today (Browse: 20px/700/-0.02em; Activity/Sync: 20px/800/-0.02em;
  Admin: 24px/800/0.04em uppercase, matching `pageTitle`). Only the Admin case
  was routed through a shared helper this slice (see below) because it's the
  only one that is visually identical to the existing `pageTitle` helper.
  Forcing the other two through `pageTitle` as-is would change their type
  size/weight/case — a visual change, not a cleanup, and needs a decision on
  which style wins.
- **Compact empty-state one-liners**: `emptyState()` renders a large centered
  block with an icon (`padding: 4rem 0`); the ~6 other "No X yet" messages are
  small inline `<p>` tags living inside an already-rendered card. They are a
  *different* pattern, not an unadopted instance of the same one — swapping
  them to `emptyState()` would change their appearance. A smaller
  `emptyStateInline()` variant is the likely right shape for a follow-up.
- **`searchableSelect` generalization**: `admin.js`'s `enhanceSelector` /
  `data-proj-select` already contains all the generic combobox behavior
  (type-to-filter, keyboard nav, list-sourced-value-only). Generalizing its
  attribute names and endpoint contract so Access/Users pickers can reuse it
  is real but low-risk work — deferred here because nothing in this slice
  consumes it yet (those pages are out of scope), so there's no second call
  site to validate the generalization against.

## Non-goals for this slice

- No redesign of any existing page (Overview, Browse, Detail, Graph, Sync,
  Activity, Admin Users/Access/Teams/Profiles/Projects/Audit).
- No user CRUD.
- No responsive breakpoints.
- No login page → templ conversion.
- No visual change to any existing component's appearance.
