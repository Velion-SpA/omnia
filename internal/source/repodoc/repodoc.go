// Package repodoc ingests a git repository's OWN documentation —
// README/VISION/ARCHITECTURE/CONTRIBUTING, docs/**, adr/** — as Engram
// memories, satisfying core.Source (internal/core/ports.go).
//
// openspec/specs/** was in the plan's original scope list but was measured
// out of the DEFAULT allowlist (see allowlist.go's DefaultAllowlist doc
// comment for the corpus-growth numbers that forced this) — it remains
// available as a New(...) allowlist override, just not on by default. The
// default is also capped to H1/H2 headings (DefaultMaxHeadingDepth,
// chunk.go); deeper sections are folded into their nearest kept ancestor
// rather than becoming their own chunk, for the same corpus-growth reason.
// docs/** was further narrowed from recursive to top-level-only
// (docs/*.md) after the openspec removal alone still measured at 60.96% of
// this repo's ~479 delta memories — see allowlist.go's DefaultAllowlist doc
// comment for that number and the "deep docs is reference material, not
// identity" reasoning. Separately, and for a correctness reason rather than
// a size reason, DefaultExcludedPathSegments (allowlist.go) hard-excludes
// any file under a legacy/archive/deprecated/beta path segment from EVERY
// allowlist, including an explicit recursive override — read that doc
// comment before touching it; it protects against ranking a
// self-declared-obsolete document as identity evidence, which plan P1
// exists specifically to prevent.
//
// Measured after all four narrowings (openspec removal, H1/H2 depth cap,
// non-recursive docs, path-segment exclusion), Preview() against this repo
// still returns a corpus above the plan's 30% guidance — see this change's
// final report for the exact number and a breakdown of the heaviest
// remaining files; getting further under 30% specifically for this repo
// was deliberately NOT pursued beyond these four changes, since this
// default has to be reasonable for arbitrary repos, not tuned until one
// specific repo's number looks good.
//
// # Motivating decision (plan P1)
//
// Omnia's memory records CHANGE, never STATE: the save protocol has no
// trigger that ever fires for "what this project IS", so a question like
// "what is Omnia" has nothing to retrieve and the LLM confabulates (the
// brief's "Omnia es un cliente de línea de comandos para GitLab" case). The
// rejected fix is to hand-write an identity record — that does not scale
// and is not understanding, and a wrong answer can only be fixed by editing
// prose nobody re-reads. The accepted fix is to give the agent access to
// the EVIDENCE instead: ingest the repo's own documentation, so the source
// of truth stays the repo (a wrong answer is fixed by fixing the README)
// and refreshes automatically when the repo changes.
//
// # Why doc ingestion beats a written identity record
//
// Every chunk this package emits carries a git staleness anchor (see
// anchor.go). A stale QUOTED document is detectable — the existing
// memory_anchors / structural-forgetting mechanism (internal/anchor,
// internal/store/anchors.go) already knows how to notice that the anchored
// line range changed and downrank/flag the memory. A stale DERIVED
// definition (hand-written prose describing what the project is) is not
// detectable by that mechanism at all — nothing links it back to the code
// or docs it was derived from, so it silently rots.
//
// # Pipeline shape
//
// Source -> Item -> Sink (core.Pipeline, internal/core/pipeline.go).
// Item.TopicKey drives upsert at the sink, which gives re-ingestion
// idempotency for free: running Fetch again after a doc changed produces
// the same TopicKey for unchanged headings and a fresh one only for
// headings that moved/were renamed — never a duplicate observation for an
// unchanged heading.
//
// # Ownership boundary
//
// This package only produces core.Item values; it does not decide how they
// are wired into `omnia collect`, does not touch internal/config, and does
// not write memory_anchors rows itself (that needs internal/store, which
// needs an obs_sync_id this package never sees — Source.Fetch returns
// before any Sink.Write happens). See this change's final report for the
// exact wiring request that connects this package to the rest of Omnia.
package repodoc

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/velion/omnia/internal/anchor"
	"github.com/velion/omnia/internal/core"
	"github.com/velion/omnia/internal/enrich"
	"github.com/velion/omnia/internal/meta"
)

const (
	// itemType is the Item.Type this package emits. Plan P1 requires a new
	// observation type, deliberately NOT "manual" and NOT "digest", so
	// downstream ranking (internal/mcp/type_lens.go, a different package)
	// can lens/weight/exclude doc-derived memories independently of both.
	itemType = "doc"

	// itemSource is the provenance tag. It is not a new vocabulary entry —
	// store.TrustTagIngestDoc ("ingest:doc", internal/store/provenance.go)
	// already existed before this package did, confirming the plan's claim
	// that "the vocabulary already exists in mem_save".
	itemSource = "ingest:doc"

	// maxChunkRunes bounds a single Item's content. internal/source/github
	// and internal/source/discord both use 45000 for the same constant,
	// but a doc section is read start-to-end by a human or an LLM forming
	// an answer, not paged through like a PR comment thread — a tighter
	// budget keeps one retrieved chunk closer to "one coherent unit of
	// evidence" than "one arbitrary slice of a wall of text".
	maxChunkRunes = 12000

	// maxBaseKeyLen leaves room for a "-partNN" suffix on the topic_key,
	// mirroring internal/source/github's maxBaseKeyLen budget (its
	// normalized-topic-key cap is a self-imposed 120; repodoc's topic keys
	// are not run through enrich.NormalizeTopicKey — see buildItems — so
	// this is just a sane length ceiling, not a hard schema limit).
	maxBaseKeyLen = 200

	// stateSource is the core.StateStore source key this package's cursors
	// are namespaced under (state.GetCursor/SetCursor's first argument).
	stateSource = "repodoc"

	// minChunkBudget is the floor buildItems will not shrink a chunk
	// content budget below, even if a very long file path + heading path
	// makes the meta/anchor overhead unusually large. Prevents a
	// pathological input from producing a negative or zero chunk size.
	minChunkBudget = 1000
)

// Source implements core.Source over a local git working tree's own
// documentation. See the package doc comment for the design rationale.
type Source struct {
	repoRoot             string // absolute path to the git working tree root
	project              string // Engram project name every emitted Item carries
	allowlist            []string
	state                core.StateStore
	probe                anchorProbe // git-backed staleness anchor capture; nil disables it gracefully
	maxHeadingDepth      int         // see FoldSectionsByDepth (chunk.go); defaults to DefaultMaxHeadingDepth
	excludedPathSegments []string    // see DefaultExcludedPathSegments (allowlist.go); defaults to it, independent of allowlist
}

// anchorProbe is the subset of *internal/anchor.Probe this package depends
// on. Declaring it as an interface (rather than depending on the concrete
// type directly) lets tests inject a fake without spawning real git
// processes — mirroring internal/anchor.Probe's own runGit injection
// pattern (anchor.go: "tests inject a fake to avoid spawning real git
// processes").
type anchorProbe interface {
	Capture(ctx context.Context, dir, file, symbol string, start, end int) (anchor.Anchor, error)
}

// New creates a repodoc Source rooted at repoRoot, which MUST be an
// absolute path to a git working tree's top level: Fetch walks it directly
// off disk (the "local-first" hard constraint — no GitHub API, no network,
// no token, so it works for private and unpushed repos exactly like any
// other file on the machine).
//
// allowlist, when nil or empty, defaults to DefaultAllowlist. state is used
// to record a per-file git-blob-hash cursor (see hash.go and Fetch) so a
// repeat run with nothing changed is a no-op; pass nil to disable cursor
// tracking (every run re-ingests every matched file — still idempotent via
// TopicKey upsert at the sink, just wasteful).
//
// Anchor capture (git blame per chunk, see anchor.go) is enabled by default
// via anchor.NewProbe(); call SetProbe(nil) to disable it explicitly (e.g.
// when repoRoot is known not to be a git repository — Fetch already
// degrades gracefully on a Capture error, so this is an optimization, not a
// correctness requirement).
func New(repoRoot, project string, allowlist []string, state core.StateStore) *Source {
	if len(allowlist) == 0 {
		allowlist = DefaultAllowlist
	}
	return &Source{
		repoRoot:             repoRoot,
		project:              project,
		allowlist:            allowlist,
		state:                state,
		probe:                anchor.NewProbe(),
		maxHeadingDepth:      DefaultMaxHeadingDepth,
		excludedPathSegments: DefaultExcludedPathSegments,
	}
}

// SetProbe overrides the anchor probe (test injection point), or disables
// anchor capture entirely when p is nil.
func (s *Source) SetProbe(p anchorProbe) { s.probe = p }

// SetMaxHeadingDepth overrides the heading-depth ceiling chunkFile folds
// deeper sections into (see FoldSectionsByDepth, chunk.go). n <= 0 disables
// the limit entirely — the config override path DefaultMaxHeadingDepth's
// doc comment describes, mirroring SetProbe's own "call the setter to
// deviate from New's default" convention rather than growing New's
// parameter list.
func (s *Source) SetMaxHeadingDepth(n int) { s.maxHeadingDepth = n }

// SetExcludedPathSegments overrides the hard path-segment exclusion filter
// (see DefaultExcludedPathSegments, allowlist.go) — pass nil to disable it
// entirely, or a different word list to protect additional/different
// conventions (e.g. a repo that uses "obsolete" or "old" instead of
// "legacy"). Read DefaultExcludedPathSegments' doc comment before calling
// this with nil: unlike the allowlist and heading-depth overrides, which
// exist purely to manage corpus SIZE, this filter exists to protect
// CORRECTNESS — it stops repodoc from ranking a document that has already
// declared itself obsolete as evidence for "what is this project".
// Disabling it does not just risk a bigger corpus, it risks a confidently
// wrong one.
func (s *Source) SetExcludedPathSegments(segments []string) { s.excludedPathSegments = segments }

func (s *Source) Name() string { return "repodoc" }

// Fetch walks the allowlist, skips any file whose current content hash
// matches its stored cursor, chunks the rest by markdown heading, and
// returns one Item per chunk (or more, for a heading section long enough to
// need sub-chunking — see buildItems).
//
// since is accepted to satisfy core.Source but is not used: unlike
// GitHub/Discord there is no remote "updated since" API to page through,
// and a file's mtime is not trustworthy as a staleness signal — checkouts,
// rebases, and CI clones all touch mtimes without touching content. The
// per-file git-blob-hash cursor (see hash.go) is strictly more precise and
// is the only signal Fetch actually uses.
func (s *Source) Fetch(ctx context.Context, _ time.Time) ([]core.Item, error) {
	files, err := s.listCandidateFiles()
	if err != nil {
		return nil, fmt.Errorf("repodoc: list files: %w", err)
	}

	var items []core.Item
	for _, relPath := range files {
		raw, err := os.ReadFile(filepath.Join(s.repoRoot, relPath))
		if err != nil {
			return nil, fmt.Errorf("repodoc: read %s: %w", relPath, err)
		}

		blobHash := gitBlobHash(raw)
		if s.state != nil {
			if cursor, ok := s.state.GetCursor(stateSource, relPath); ok && cursor == blobHash {
				continue // unchanged since the last run — no-op by design
			}
		}

		items = append(items, s.chunkFile(ctx, relPath, string(raw), true)...)

		if s.state != nil {
			if err := s.state.SetCursor(stateSource, relPath, blobHash); err != nil {
				return nil, fmt.Errorf("repodoc: set cursor for %s: %w", relPath, err)
			}
		}
	}
	return items, nil
}

// Stats summarizes a repodoc run's corpus growth for operator visibility.
//
// This exists because naive ingestion would swamp a small project's delta
// log: this repo's docs/ alone is ~40 files against hundreds of delta
// memories, and the plan's explicit regression gate is that doc ingestion
// must not degrade the pre-existing coding-agent search corpus. The
// intended usage is: run Preview, compare ChunksTotal (or a per-file entry
// in ByFile) against the project's existing total observation count, and if
// doc chunks would exceed roughly 30% of the total, the allowlist is too
// wide (plan P1's "Corpus growth" measurement) — this package cannot know
// the project's total observation count itself (that lives in
// internal/store, outside this package), so Stats reports its half of that
// ratio and leaves the comparison to the operator/caller.
type Stats struct {
	// FilesMatched is every allowlisted file found, regardless of cursor state.
	FilesMatched int
	// FilesChanged is the subset of FilesMatched a real Fetch would actually
	// re-chunk right now (no cursor, or cursor mismatch).
	FilesChanged int
	// FilesSkipped is FilesMatched - FilesChanged: cursor hit, no-op.
	FilesSkipped int
	// ChunksTotal is the number of Items a real Fetch would emit right now
	// — the exact same count, computed the exact same way, minus git
	// anchor capture (see Preview's doc comment for why that is skipped).
	ChunksTotal int
	// ByFile maps each changed file's relative path to its own chunk count,
	// for spotting a single outlier file inflating the total.
	ByFile map[string]int
}

// Preview reports what a real Fetch call would emit right now, WITHOUT
// writing to the state store and WITHOUT shelling out to git for staleness
// anchors — this is repodoc's dry-run entry point (plan P1's "Design risk"
// requirement: "support a dry-run"). Skipping anchor capture keeps Preview
// cheap and side-effect-free by design, so an operator can run it freely,
// on every project, as often as they want, to answer "is the allowlist too
// wide" before committing to a real ingest.
func (s *Source) Preview(ctx context.Context) (Stats, error) {
	stats := Stats{ByFile: map[string]int{}}
	files, err := s.listCandidateFiles()
	if err != nil {
		return stats, fmt.Errorf("repodoc: list files: %w", err)
	}
	stats.FilesMatched = len(files)

	for _, relPath := range files {
		raw, err := os.ReadFile(filepath.Join(s.repoRoot, relPath))
		if err != nil {
			return stats, fmt.Errorf("repodoc: read %s: %w", relPath, err)
		}
		if s.state != nil {
			if cursor, ok := s.state.GetCursor(stateSource, relPath); ok && cursor == gitBlobHash(raw) {
				stats.FilesSkipped++
				continue
			}
		}
		stats.FilesChanged++
		items := s.chunkFile(ctx, relPath, string(raw), false)
		stats.ByFile[relPath] = len(items)
		stats.ChunksTotal += len(items)
	}
	return stats, nil
}

// listCandidateFiles walks repoRoot and returns every regular file whose
// repo-root-relative path matches the allowlist AND is not hard-excluded by
// excludedPathSegments (see DefaultExcludedPathSegments, allowlist.go —
// this second check runs independently of and in addition to the
// allowlist, deliberately: it must keep excluding legacy/archive/
// deprecated/beta content even when the allowlist itself is widened),
// sorted for deterministic output — both the tests and the corpus-growth
// report depend on stable ordering across runs. ".git" is always skipped:
// never a documentation source, and can be enormous.
func (s *Source) listCandidateFiles() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(s.repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if matches(s.allowlist, relSlash) && !isExcludedPath(relSlash, s.excludedPathSegments) {
			out = append(out, relSlash)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// chunkFile parses one file's content into heading Sections and builds
// Items for each. computeAnchor controls whether git anchor capture runs
// (Fetch: true: Preview: false — see Preview's doc comment).
func (s *Source) chunkFile(ctx context.Context, relPath, content string, computeAnchor bool) []core.Item {
	docTitle := DocTitle(content, s.docTitleFallback(relPath))
	sections := FoldSectionsByDepth(ChunkMarkdown(content), s.maxHeadingDepth)

	var items []core.Item
	for _, sec := range sections {
		items = append(items, s.buildItems(ctx, relPath, docTitle, sec, computeAnchor)...)
	}
	return items
}

// docTitleFallback picks a sane title for a document that has no top-level
// (H1) heading outside a fence — DocTitle's fallback path. A README is
// singled out because it is exactly the file class most likely to hit that
// path: the common convention is a centered HTML banner or badge block
// instead of a real "# Project Name" H1 (this repo's own README.md is one),
// so the bare filename "README" would be a useless, repeated-165-times
// title. The project name is the obvious better candidate — it is what a
// user is actually asking about ("what is Omnia"), so it doubles as a
// helpful lexical match for identity-intent queries instead of a
// directionless filename echo. Every other document class (ARCHITECTURE.md,
// CONTRIBUTING.md, docs/*.md, ...) keeps the plain filename-derived title:
// those names already describe their own topic well enough, and inventing a
// project-name substitute for them would just repeat the project name
// across unrelated chunks instead of README's.
func (s *Source) docTitleFallback(relPath string) string {
	base := filepath.Base(relPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.EqualFold(name, "README") && s.project != "" {
		return s.project
	}
	return name
}

// buildItems turns a single heading Section into one or more core.Item
// values (more than one only when the section's rendered content exceeds
// maxChunkRunes, mirroring internal/source/github's -partN convention).
//
// Identity/upsert (plan P1): TopicKey = "repodoc:{project}:{path}#{slug}".
// This is a plain, deterministic function of (project, file path, heading
// slug) — re-running Fetch after a file changes produces the SAME TopicKey
// for every heading that did not move, so the sink's topic_key upsert makes
// re-ingestion a revision, never a duplicate, by construction. It is NOT
// passed through enrich.NormalizeTopicKey (unlike github/discord's raw
// human strings): that helper silently DROPS ':' and '#', which would
// destroy the very separators this scheme relies on for uniqueness.
func (s *Source) buildItems(ctx context.Context, relPath, docTitle string, sec Section, computeAnchor bool) []core.Item {
	headingPathStr := strings.Join(sec.HeadingPath, " > ")
	if headingPathStr == "" {
		headingPathStr = "(introduction)"
	}

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Document: %s\n", docTitle))
	body.WriteString(fmt.Sprintf("Section: %s\n\n", headingPathStr))
	body.WriteString(sec.Body)
	if !strings.HasSuffix(body.String(), "\n") {
		body.WriteString("\n")
	}
	keywords := enrich.ExtractKeywords(sec.HeadingPath, []string{docTitle, filepath.Base(relPath)})
	if len(keywords) > 0 {
		body.WriteString(fmt.Sprintf("\nKeywords: %s\n", strings.Join(keywords, ", ")))
	}
	fullContent := body.String()

	rawTopicBase := fmt.Sprintf("repodoc:%s:%s#%s", s.project, relPath, sec.Slug)
	topicBase := rawTopicBase
	if len([]rune(topicBase)) > maxBaseKeyLen {
		topicBase = string([]rune(topicBase)[:maxBaseKeyLen])
	}

	contextHeader := fmt.Sprintf("<!-- %s | %s -->\n\n", relPath, headingPathStr)

	var anchorBlock string
	blameAt := ""
	if computeAnchor && s.probe != nil {
		// Graceful degradation (internal/anchor.go's REQ-002 doctrine,
		// inherited here): a Capture failure (git missing, repoRoot not a
		// git repository, ...) just means "no staleness anchor for this
		// chunk" — it must never fail the surrounding ingest.
		if a, err := s.probe.Capture(ctx, s.repoRoot, relPath, headingPathStr, sec.LineStart, sec.LineEnd); err == nil {
			anchorBlock = renderAnchorBlock(AnchorInfo{
				RepoRoot:    a.RepoRoot,
				File:        a.File,
				Symbol:      a.Symbol,
				LineStart:   a.LineStart,
				LineEnd:     a.LineEnd,
				BlameSHA:    a.BlameSHA,
				BlameAt:     a.BlameAt,
				ContentHash: a.ContentHash,
			})
			blameAt = a.BlameAt
		}
	}

	m := meta.Meta{
		SchemaVersion: meta.SchemaVersion,
		Source:        "repodoc",
		Kind:          "doc_chunk",
		Layer:         "ingested",
		Project:       s.project,
		SourceID:      fmt.Sprintf("%s#%s", relPath, sec.Slug),
		IngestedAt:    time.Now().UTC(),
	}
	if blameAt != "" {
		if t, err := time.Parse(time.RFC3339, blameAt); err == nil {
			m.UpdatedAt = t
		}
	}

	// C3 (mirroring internal/source/github's own comment for the identical
	// pattern): reserve budget for the meta + anchor blocks using a
	// representative render before the per-chunk fields (chunk N/Total) are
	// set, since those blocks are appended to every part identically.
	metaSize := len([]rune(meta.Render(m))) + len([]rune(anchorBlock))
	budget := maxChunkRunes - metaSize
	if budget < minChunkBudget {
		budget = minChunkBudget
	}
	chunks := enrich.ChunkContent(fullContent, budget)

	title := fmt.Sprintf("%s — %s", docTitle, headingPathStr)

	var items []core.Item
	total := len(chunks)
	for i, chunk := range chunks {
		topicKey := topicBase
		itemTitle := title
		content := chunk
		if total > 1 {
			topicKey = fmt.Sprintf("%s-part%d", topicBase, i+1)
			itemTitle = fmt.Sprintf("%s (part %d/%d)", title, i+1, total)
			if i > 0 {
				content = contextHeader + chunk
			}
			m.ChunkCurrent = i + 1
			m.ChunkTotal = total
		} else {
			m.ChunkCurrent = 0
			m.ChunkTotal = 0
		}

		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if anchorBlock != "" {
			content += anchorBlock
		}
		content += meta.Render(m)

		items = append(items, core.Item{
			Type:      itemType,
			Title:     itemTitle,
			Content:   content,
			Project:   s.project,
			TopicKey:  topicKey,
			Source:    itemSource,
			FetchedAt: time.Now(),
		})
	}
	return items
}
