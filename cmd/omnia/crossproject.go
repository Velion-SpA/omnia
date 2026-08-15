package main

// crossproject.go — P5's I/O/HTTP wiring layer for cross-project retrieval
// (docs/conversational-retrieval-plan.md "P5 — Cross-project retrieval").
//
// internal/multiproject (Merge/Diversity) is the pure ranking core: given
// one Group of Item per project, it normalizes each project's relevance
// signal independently and merges the result so no project's absolute
// score scale dominates. That package's own doc explicitly defers "the
// fan-out layer" — converting store.SearchResult batches into Item, calling
// Merge, and mapping MergedItem back — to its caller. This file IS that
// fan-out layer: it fans a query out to every (or an explicit subset of)
// project via the SAME recallOrFTSSearchWithRelevance seam GET /search's
// single-project path already uses (issue #86), so the P5 path can never
// silently diverge from single-project retrieval's own scoped-search bugfix
// (engram obs #1436/#1437/#1438, internal/recall/service.go:81-88: a small
// project can get crowded out of a global top-k by a larger one unless the
// semantic leg is scoped PER project via embed.ScopedSearcher.SearchScoped
// — recallOrFTSSearchWithRelevance already does this whenever
// SearchOptions.Project is non-empty, so calling it once per project here
// gets that fix for free without reimplementing it).
//
// buildHTTPSearchFunc/buildHTTPAnswerFunc (recall.go) branch into this
// file's crossProjectSearchEnvelope/crossProjectAnswer at the very top of
// their returned closures, BEFORE any of their existing single-project
// code runs — see those two functions' own doc comments for why that
// ordering is what keeps the single-project path byte-for-byte unchanged.

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/velion/omnia/internal/config"
	"github.com/velion/omnia/internal/mcp"
	"github.com/velion/omnia/internal/multiproject"
	"github.com/velion/omnia/internal/recall"
	"github.com/velion/omnia/internal/server"
	"github.com/velion/omnia/internal/store"
)

// crossProjectDiversityWindow is the top-K window multiproject.Diversity
// measures over for the P5 envelope/response fields — the plan's own
// measurement gate wording ("project diversity in the top-4 ... must span
// >= 2 projects"), not a tunable: changing it would silently redefine what
// the plan's own gate means.
const crossProjectDiversityWindow = 4

// crossProjectDefaultLimit mirrors handleSearch's own `limit` query param
// default (10, internal/server/server.go) — used both as the per-project
// fetch size (each project's own "top-N", see multiproject.Group's doc) and
// the final merged-response size when the caller's SearchOptions.Limit is
// unset/non-positive.
const crossProjectDefaultLimit = 10

// crossProjectWorkerCap bounds how many per-project legs (one
// recallOrFTSSearchWithRelevance call each: a SQLite FTS5 query, plus,
// when semantic recall is configured, a scoped embed.ScopedSearcher.
// SearchScoped call and — unless P6's query-embedding cache already has
// this query's vector cached from an earlier leg — a network round trip to
// Ollama) run CONCURRENTLY during one P5 fan-out.
//
// This is a fixed-size worker pool (workers pull project indices off a
// channel), not one goroutine per project: the real store this shipped
// against has ~58 projects (2026-08 measurement, ~2500 observations). Firing
// all ~58 legs at once would (a) contend heavily for SQLite's single-writer/
// many-readers lock, since every leg hits the same on-disk database, and
// (b) can stack up to dozens of concurrent HTTP requests against a single
// Ollama instance (192.168.100.10) if the query embedding isn't cached yet
// — both well past GET /search's documented retrieval budget (plan P5's own
// "+15ms at N=5" line, which this file's own measurement note revises for
// the real N~58 case — see crossProjectSearch's doc for the measured
// numbers this default was picked against).
//
// GOMAXPROCS*2, clamped to [4,8]: fan-out legs are I/O-bound (disk +
// network), so some oversubscription past raw CPU count is fine and lets
// several legs overlap their I/O wait; the upper clamp keeps a busy
// multi-core box from still opening dozens of concurrent SQLite/Ollama
// connections. A package var (not a const) so it stays cheaply overridable
// — by a future config field, or a test — without editing this file.
var crossProjectWorkerCap = defaultCrossProjectWorkerCap()

func defaultCrossProjectWorkerCap() int {
	return crossProjectWorkerCapFor(runtime.GOMAXPROCS(0))
}

// crossProjectWorkerCapFor is the pure clamp formula defaultCrossProjectWorkerCap
// applies to the live runtime.GOMAXPROCS(0) — split out as its own function
// (rather than left inline) purely so the [4,8] clamp is unit-testable
// against arbitrary gomaxprocs inputs without depending on how many CPUs
// the test machine actually has.
func crossProjectWorkerCapFor(gomaxprocs int) int {
	n := gomaxprocs * 2
	if n < 4 {
		n = 4
	}
	if n > 8 {
		n = 8
	}
	return n
}

// effectiveCrossProjectWorkers returns how many worker goroutines
// runCrossProjectFanout actually starts for numProjects projects: never
// more than crossProjectWorkerCap (the concurrency bound), and never more
// than numProjects itself (starting more workers than there are jobs would
// just leave the extras blocked forever reading from an already-drained,
// closed channel). Split out from runCrossProjectFanout's inline `if
// workers > len(projects) { workers = len(projects) }` so this second half
// of the worker-pool-sizing logic is independently unit-testable too.
func effectiveCrossProjectWorkers(workerCap, numProjects int) int {
	if numProjects <= 0 {
		return 0
	}
	if workerCap > numProjects {
		return numProjects
	}
	return workerCap
}

// crossProjectLeg is one project's fan-out outcome: exactly what
// recallOrFTSSearchWithRelevance returns for that project, plus which
// project it was.
type crossProjectLeg struct {
	project   string
	results   []store.SearchResult
	relevance map[int64]float64
	semantic  map[int64]float64
	fusionRan bool
	err       error
}

// searchResultToItem converts one hydrated store.SearchResult plus its raw
// per-leg relevance signal into the multiproject.Item shape
// crossProjectSearch feeds into multiproject.Group/Merge. Preempt collapses
// the topic_key exact-match sentinel (r.Rank == cliExactSentinelRank) and a
// signature-match row (r.SignatureMatch) into one boolean via OR — the SAME
// rule mcp.RankResults/MinMaxNormalizeRelevance already enforce for the
// single-project case (see internal/mcp/recall_ranking.go's RankResults doc
// and multiproject.Item.Preempt's own doc for why both conditions must
// pre-empt identically: either sentinel's relevance is an outlier by
// construction, not a real bm25/RRF score). Extracted into its own function
// (rather than left inline in crossProjectSearch's loop) so this
// easy-to-get-backwards boolean-OR is independently unit-testable — see
// crossproject_test.go.
func searchResultToItem(r store.SearchResult, relevance float64) multiproject.Item {
	return multiproject.Item{
		ID:        r.ID,
		Relevance: relevance,
		UpdatedAt: r.UpdatedAt,
		Preempt:   r.Rank == cliExactSentinelRank || r.SignatureMatch,
	}
}

// resolveFanoutProjects returns the project name list a P5 fan-out should
// search: every project with at least one observation (all_projects=1) via
// store.ListProjectsWithStats, or the caller's explicit subset (2+ repeated
// project= params) — either way, deduplicated by store.NormalizeProject
// (see dedupeNormalizedProjects's own doc for why this is required, not
// optional).
func resolveFanoutProjects(s *store.Store, allProjects bool, explicit []string) ([]string, error) {
	var raw []string
	if allProjects {
		stats, err := s.ListProjectsWithStats()
		if err != nil {
			return nil, err
		}
		raw = make([]string, 0, len(stats))
		for _, p := range stats {
			raw = append(raw, p.Name)
		}
	} else {
		raw = explicit
	}
	return dedupeNormalizedProjects(raw), nil
}

// dedupeNormalizedProjects collapses names to their store.NormalizeProject
// form and drops duplicates, keeping the first-seen raw spelling's
// normalized value for each.
//
// This is a correctness fix, not a cosmetic one: found live against the
// ~58-project store this shipped against, which has real case-variant
// fragmentation in its raw project column ("workly"/"Workly",
// "velion"/"Velion"/"01.- velion"/"01.- Velion", "habitia"/"Habitia",
// "nudge"/"NUDGE"/"nudge sistema" — store.ListProjectsWithStats groups by
// the RAW column value, so every one of those pairs comes back as two
// separate names). store.Search and recall.Service both normalize AND
// lowercase-compare the project filter before querying
// (store.go's `opts.Project, _ = NormalizeProject(opts.Project)` +
// `LOWER(project) = ?`), so a "workly" leg and a "Workly" leg of the fan-out
// query the EXACT SAME underlying rows. Without this dedup, every such pair
// wastes a full extra fan-out leg AND — worse — surfaces the same
// observation twice in the merged response, once per raw-name Group,
// because multiproject.Merge has no cross-group ID dedup (each Item
// originates from exactly one project's search, by its own doc's
// assumption that the fan-out layer already gave it disjoint per-project
// candidate sets). See this package's final report for the P5 corpus's own
// "cross-project-fragmentation" case, which asks exactly this question
// ("which projects got tangled together by the naming fragmentation") —
// the live store's fragmentation is a genuine, pre-existing data property,
// not a bug this file introduces; failing to dedupe by normalized name
// before fanning out was the bug.
func dedupeNormalizedProjects(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		norm, _ := store.NormalizeProject(n)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out
}

// runCrossProjectFanout runs one recallOrFTSSearchWithRelevance leg per
// project in projects, using a fixed pool of crossProjectWorkerCap worker
// goroutines pulling project indices off a shared channel (see
// crossProjectWorkerCap's own doc for why this is a bounded pool, not one
// goroutine per project). Each leg gets its own opts.Project — every other
// SearchOptions field (Type/Scope/Limit) is shared unchanged across all
// legs.
func runCrossProjectFanout(ctx context.Context, s *store.Store, recallSvc *recall.Service, query string, opts store.SearchOptions, projects []string) []crossProjectLeg {
	legs := make([]crossProjectLeg, len(projects))
	if len(projects) == 0 {
		return legs
	}

	jobs := make(chan int, len(projects))
	for i := range projects {
		jobs <- i
	}
	close(jobs)

	workers := effectiveCrossProjectWorkers(crossProjectWorkerCap, len(projects))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				legs[i] = runCrossProjectLeg(ctx, s, recallSvc, query, opts, projects[i])
			}
		}()
	}
	wg.Wait()
	return legs
}

// runCrossProjectLeg runs recallOrFTSSearchWithRelevance for exactly one
// project leg, recovering from any panic raised inside it (e.g.
// embed.ScopedSearcher.SearchScoped choking on a malformed/nil response
// from Ollama, or a nil-map/index-out-of-range bug surfacing only for one
// particular project's data) into a normal crossProjectLeg.err instead.
//
// This recover is required, not defensive dressing: Go's net/http recovers
// a panic in the goroutine handling an HTTP request, but NEVER in a
// goroutine that handler itself spawns (documented net/http behavior).
// runCrossProjectFanout spawns crossProjectWorkerCap worker goroutines per
// fan-out request — without this recover, ANY one leg panicking crashes the
// entire omnia server process for every in-flight request, not just the
// one that triggered it. The single-project search path has no equivalent
// failure mode (it runs synchronously in the request goroutine, where
// net/http's own recover already applies), so this gap is specific to the
// P5 fan-out's own concurrency.
//
// The resulting error shape matches how an ordinary (non-panic)
// recallOrFTSSearchWithRelevance error is already handled: crossProjectSearch
// skips any leg with a non-nil err (see that function's own doc) — a
// recovered panic degrades that ONE project's contribution to the merge,
// exactly like a normal per-leg error would, rather than being surfaced
// any differently.
func runCrossProjectLeg(ctx context.Context, s *store.Store, recallSvc *recall.Service, query string, opts store.SearchOptions, project string) (leg crossProjectLeg) {
	leg.project = project
	defer func() {
		if r := recover(); r != nil {
			leg.results = nil
			leg.relevance = nil
			leg.semantic = nil
			leg.fusionRan = false
			leg.err = fmt.Errorf("cross-project fan-out: leg %q panicked: %v", project, r)
			log.Printf("[crossproject] recovered panic in fan-out leg project=%q query=%q: %v\n%s", project, query, r, debug.Stack())
		}
	}()

	legOpts := opts
	legOpts.Project = project
	leg.results, leg.relevance, leg.semantic, leg.fusionRan, leg.err = recallOrFTSSearchWithRelevance(ctx, s, recallSvc, query, legOpts)
	return leg
}

// crossProjectSearch is P5's core wiring: fan out one scoped search per
// project (runCrossProjectFanout), rank each project's own results
// in-place via mcp.RankResults (the same stage 1 of RankPipeline the
// single-project path runs — this is what makes each project's slice
// "already ranked" the way multiproject.Group's own doc expects), convert
// the top per-project results into multiproject.Item/Group, call
// multiproject.Merge, then map the merged, cross-project-ranked ID order
// back to each item's hydrated store.SearchResult.
//
// Per multiproject.Item.Relevance's own doc ("an RRF fusion score, a
// negated FTS5 bm25 rank ... the same contract mcp.RankResults' relevance
// parameter already documents"), each Item's Relevance is the RAW,
// per-leg relevance signal (recallOrFTSSearchWithRelevance's own return
// value) — NOT the RankScore mcp.RankResults computes from it. RankResults
// is used here only to pick each project's internal ORDER (so the "top-N"
// slice taken per project is the right N rows), never as the value fed
// into the cross-project normalization itself.
//
// It deliberately does NOT re-run the LATER RankPipeline stages
// (ApplyLearnedRanker/ApplyStalenessDownrank/ApplyTypeLens/ApplyMMR) over
// the merged output: every one of those stages re-scores or reorders using
// a row's raw relevance value, which is exactly the per-project-
// absolute-scale signal multiproject.Merge's own normalization exists to
// neutralize (package doc: "the most active project does not dominate").
// Running them again after Merge would re-introduce the crowd-out effect
// P5 exists to fix. The one exception the caller may apply on the
// returned, already-final order is mcp.ApplyTokenBudget: it only ever
// drops trailing rows, never reorders, so it is safe post-merge.
//
// Returns the merged+hydrated top results (capped to opts.Limit, or
// crossProjectDefaultLimit when unset), each result's raw relevance
// (needed by GET /answer's confidence classification, mirroring the
// single-project path's own use of the relevance map), each result's
// [0,1] cross-project-normalized score (multiproject.MergedItem.
// NormalizedScore, keyed by ID — this is what explain=1 uses in place of
// mcp.MinMaxNormalizeRelevance's single-project batch normalization),
// whether fusion ran for AT LEAST ONE leg (an approximation for a
// response-level "fusionRan" signal — individual legs can differ, e.g. one
// project's recall.Service call falls back to FTS5 mid-query while
// another's fuses cleanly; BuildResultReceipt is called per-row in the
// caller, at which point per-row precision would require per-row
// provenance multiproject.Item does not carry, so this is a deliberate
// simplification — see this package's final report), and the
// multiproject.DiversityReport over the merged list's top-4 (P5's own
// measurement gate).
//
// Measured against the live ~58-project store (2026-08, `omnia serve` +
// GET /search?all_projects=1): see the P5 wiring engram observation
// (topic_key architecture/multiproject-fanout-wiring) for the exact
// p50/worst-case numbers that validated crossProjectWorkerCap's default.
// semantic (the 3rd return value, engram obs #2585/#2612) is each returned
// result's raw semantic cosine score, keyed by Observation.ID, merged from
// whichever leg's crossProjectLeg.semantic actually produced that ID —
// present only for rows a leg's fusion carried a cosine for, mirroring
// relevance/rawRelevance's own per-leg merge and recall.Result.
// SemanticScore's nil-means-absent contract. This is the data P5's own
// measurement (engram obs #2612) found missing: every project's rank-1 RRF
// score is ~equal, so raw cosine is the only candidate signal that could
// tell "this project's top hit is a real match" apart from "this project's
// top hit just happened to rank first locally" — this return value only
// carries it out to the caller; it does not change ranking, Merge, or
// diversity in any way this slice.
func crossProjectSearch(ctx context.Context, s *store.Store, recallSvc *recall.Service, appCfg *config.Config, query string, opts store.SearchOptions, allProjects bool, explicitProjects []string, now time.Time) (results []store.SearchResult, relevance map[int64]float64, normalizedScore map[int64]float64, semantic map[int64]float64, fusionRan bool, diversity multiproject.DiversityReport, err error) {
	projectNames, err := resolveFanoutProjects(s, allProjects, explicitProjects)
	if err != nil {
		return nil, nil, nil, nil, false, multiproject.DiversityReport{}, err
	}

	legLimit := opts.Limit
	if legLimit <= 0 {
		legLimit = crossProjectDefaultLimit
	}
	legOpts := opts
	legOpts.Limit = legLimit

	legs := runCrossProjectFanout(ctx, s, recallSvc, query, legOpts, projectNames)

	groups := make([]multiproject.Group, 0, len(legs))
	hydrated := make(map[int64]store.SearchResult)
	rawRelevance := make(map[int64]float64)
	rawSemantic := make(map[int64]float64)

	for _, leg := range legs {
		if leg.err != nil || len(leg.results) == 0 {
			continue
		}
		if leg.fusionRan {
			fusionRan = true
		}

		// Stage 1 of RankPipeline only (see this function's own doc for why
		// stages 2-6 do not run here): picks each project's internal order
		// so the "top-N" below is the right N rows. A no-op, same-order
		// slice when appCfg.Recall.Ranking.Enabled is false (RankResults'
		// own contract).
		ranked := mcp.RankResults(leg.results, leg.relevance, appCfg.Recall.Ranking, now)

		items := make([]multiproject.Item, 0, len(ranked))
		for _, r := range ranked {
			hydrated[r.ID] = r
			rel := leg.relevance[r.ID]
			rawRelevance[r.ID] = rel
			// Only copy an entry when this leg actually had a cosine for
			// this ID — mirroring leg.semantic's own "absent means no
			// signal" contract rather than defaulting a lexical-only hit to
			// a misleading 0.0.
			if v, ok := leg.semantic[r.ID]; ok {
				rawSemantic[r.ID] = v
			}
			items = append(items, searchResultToItem(r, rel))
		}
		groups = append(groups, multiproject.Group{Project: leg.project, Items: items})
	}

	// k=0 selects multiproject.DefaultPerProjectCap (3). That cap is what
	// prevents an active project from flooding the merged list — it is NOT a
	// scoring adjustment. Merge ranks on RAW relevance now: per-group min-max
	// normalization used to sit here and was removed after it drove
	// cross_project accuracy@1 from 0.250 to 0.000 at this store's ~49-project
	// scale (most projects contribute a single hit, min==max, so every one of
	// them normalized to a tied 1.0 and the UpdatedAt tie-break — recency, not
	// relevance — decided nearly every position). See multiproject.Merge's own
	// doc and engram obs #2607.
	merged := multiproject.Merge(groups, 0)
	diversity = multiproject.Diversity(merged, crossProjectDiversityWindow)

	finalLimit := opts.Limit
	if finalLimit <= 0 {
		finalLimit = crossProjectDefaultLimit
	}

	results = make([]store.SearchResult, 0, min(len(merged), finalLimit))
	relevance = make(map[int64]float64, len(results))
	semantic = make(map[int64]float64)
	for _, mi := range merged {
		if len(results) >= finalLimit {
			break
		}
		r, ok := hydrated[mi.ID]
		if !ok {
			continue
		}
		results = append(results, r)
		relevance[mi.ID] = rawRelevance[mi.ID]
		if v, ok := rawSemantic[mi.ID]; ok {
			semantic[mi.ID] = v
		}
	}

	// explain=1's score display is normalized SEPARATELY from ranking, over
	// the final returned batch, using the same mcp.MinMaxNormalizeRelevance
	// the single-project path uses. Two distinct concerns that were
	// previously conflated in one number:
	//
	//   - RANKING is now raw relevance (see Merge above) — normalizing per
	//     group destroyed the ordering signal.
	//   - DISPLAY needs a [0,1] value that means the same thing here as on
	//     the single-project path, or `explain` silently changes units
	//     depending on whether all_projects was set. MergedItem.Score is raw
	//     relevance in arbitrary units and would do exactly that.
	//
	// Preempting rows are excluded from the normalization batch, the same
	// rule RankResults and MinMaxNormalizeRelevance both document: their
	// relevance is an outlier by construction and would pin the batch max.
	nonSentinel := make([]store.SearchResult, 0, len(results))
	for _, r := range results {
		if r.Rank != cliExactSentinelRank && !r.SignatureMatch {
			nonSentinel = append(nonSentinel, r)
		}
	}
	normalizedScore = mcp.MinMaxNormalizeRelevance(nonSentinel, relevance)

	return results, relevance, normalizedScore, semantic, fusionRan, diversity, nil
}

// crossProjectSearchEnvelope is buildHTTPSearchFunc's (recall.go) P5
// branch: runs crossProjectSearch, then applies the SAME degradation-
// envelope (#226) and explain (P0 item 3) shaping the single-project path
// applies, minus the pipeline stages crossProjectSearch's own doc explains
// are unsafe to re-run post-merge. Structural-forgetting staleness downrank
// is also skipped here (a deliberate scope simplification, not an
// oversight — see this file's final report) since anchor loading is keyed
// per-observation and adds another per-leg fan-out concern for a signal
// that ApplyStalenessDownrank would immediately have to re-partition by its
// own relevance-adjacent logic; explain=1 rows report stalenessPenalty=0
// accordingly, matching BuildResultReceipt's own pre-PR2 default for
// callers with no anchor lookup wired.
func crossProjectSearchEnvelope(ctx context.Context, s *store.Store, recallSvc *recall.Service, appCfg *config.Config, readWatermarks mcp.WatermarkReader, query string, req server.SearchRequest) (server.SearchEnvelope, error) {
	now := time.Now()
	results, relevance, normalizedScore, semantic, fusionRan, diversity, err := crossProjectSearch(ctx, s, recallSvc, appCfg, query, req.SearchOptions, req.AllProjects, req.Projects, now)
	if err != nil {
		return server.SearchEnvelope{}, err
	}

	// P0 item 3: same per-request token-budget override the single-project
	// path applies. ApplyTokenBudget only trims trailing rows (see
	// crossProjectSearch's own doc), so it is safe to run on the merged,
	// already-final order.
	budget := appCfg.Injection.Budget
	if req.MaxTokens > 0 {
		budget.Enabled = true
		budget.MaxTokens = req.MaxTokens
	}
	results = mcp.ApplyTokenBudget(results, budget)

	envelope := server.SearchEnvelope{
		Results:           results,
		DiversityDistinct: diversity.Distinct,
		DiversityCounts:   diversity.Counts,
	}

	health := mcp.EvaluateRecallHealth(ctx, recallSvc != nil, readWatermarks)
	if health.Degraded {
		envelope.RecallDegraded = true
		envelope.RecallDegradedReason = health.Reason
		if health.EmbeddingsStale {
			envelope.EmbeddingsStale = true
			envelope.EmbeddingsBehindBy = health.BehindBy
			envelope.NewestEmbeddedAt = health.NewestEmbeddedAt
		}
	}

	if req.Explain {
		envelope.ScoreBreakdown = make(map[string]map[string]any, len(results))
		for _, r := range results {
			envelope.ScoreBreakdown[strconv.FormatInt(r.ID, 10)] = mcp.BuildResultReceipt(
				r, fusionRan, appCfg.Recall.Ranking, relevance, normalizedScore, semantic, now, 0,
			)
		}
	}

	return envelope, nil
}

// crossProjectAnswer is buildHTTPAnswerFunc's (recall.go) P5 branch: runs
// crossProjectSearch, then applies the SAME confidence-classification
// shaping the single-project path applies (mcp.AssembleAnswerContext ->
// mcp.ClassifyAnswerConfidence), reading topScore/hasTopScore off
// crossProjectSearch's returned raw relevance map exactly like
// buildHTTPAnswerFunc's own single-project loop does.
func crossProjectAnswer(ctx context.Context, s *store.Store, recallSvc *recall.Service, appCfg *config.Config, readWatermarks mcp.WatermarkReader, query string, req server.AnswerRequest) (server.AnswerResponse, error) {
	now := time.Now()
	// semantic (crossProjectSearch's 4th return value) is deliberately
	// discarded here — same rationale as buildHTTPAnswerFunc's
	// single-project path (recall.go): wiring the per-hit cosine into
	// confidence/ranking is later, gated consumer work, not this plumbing
	// slice.
	results, relevance, _, _, fusionRan, diversity, err := crossProjectSearch(ctx, s, recallSvc, appCfg, query, req.SearchOptions, req.AllProjects, req.Projects, now)
	if err != nil {
		return server.AnswerResponse{}, err
	}

	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = appCfg.Answer.MaxChars
	}
	assembled := mcp.AssembleAnswerContext(results, maxChars)

	// Global (not per-project) diagnostic probe, matching buildHTTPAnswerFunc's
	// own answerFTSDiag call: this only feeds ClassifyAnswerConfidence's
	// FTSRelaxed/FTSRelaxStep signal, not the actual results.
	diag := answerFTSDiag(s, query, req.SearchOptions)
	health := mcp.EvaluateRecallHealth(ctx, recallSvc != nil, readWatermarks)

	var topScore float64
	var hasTopScore bool
	for _, r := range results {
		if r.Rank == cliExactSentinelRank || r.SignatureMatch {
			continue
		}
		if sc, ok := relevance[r.ID]; ok {
			topScore, hasTopScore = sc, true
		}
		break
	}

	confidence := mcp.ClassifyAnswerConfidence(mcp.AnswerConfidenceSignals{
		HitCount:         len(results),
		TopScore:         topScore,
		HasTopScore:      hasTopScore,
		FTSRelaxed:       diag.Relaxed,
		FTSRelaxStep:     diag.Step,
		FusionRan:        fusionRan,
		RecallDegraded:   health.Degraded,
		SourcesAssembled: len(assembled.Sources),
	}, appCfg.Answer.NoneScoreFloor, appCfg.Answer.ConfidenceThreshold)

	sources := make([]server.AnswerSource, 0, len(assembled.Sources))
	for _, src := range assembled.Sources {
		sources = append(sources, server.AnswerSource{
			SyncID:    src.SyncID,
			Title:     src.Title,
			Type:      src.Type,
			UpdatedAt: src.UpdatedAt,
		})
	}

	return server.AnswerResponse{
		Context:           assembled.Context,
		Confidence:        string(confidence),
		Sources:           sources,
		Degraded:          health.Degraded,
		DiversityDistinct: diversity.Distinct,
		DiversityCounts:   diversity.Counts,
	}, nil
}
