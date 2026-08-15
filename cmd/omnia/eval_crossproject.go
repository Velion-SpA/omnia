package main

// eval_crossproject.go — the `omnia eval --profile conversational
// --multi-project` mode (P5, docs/conversational-retrieval-plan.md "P5 —
// Cross-project retrieval"): runs ONLY the corpus's cross_project cases
// through GET /search?all_projects=1&envelope=1 and prints accuracy@1 PLUS
// project diversity in the top-4, PER CASE — the "not just an aggregate"
// requirement, since KindSegment/ConversationalReport (conversational_
// scoring.go) only ever fold cases into one per-kind aggregate and have no
// slot for a per-case diversity report at all.
//
// This is a parallel, standalone mode rather than a new --target value or a
// widening of RunOnceConversational/ConversationalReport: it reuses
// eval.ScoreConversationalCase for the accuracy@1 verdict (so hit/miss
// logic can never drift from the normal conversational scoring path) but
// prints per-case, not through the aggregate ConversationalReport/
// KindSegment machinery, which has no per-case output shape to reuse
// without widening those types for a one-off diagnostic report.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/velion/omnia/internal/eval"
	"github.com/velion/omnia/internal/store"
)

// crossProjectSearchHTTPResponse mirrors internal/server.searchEnvelopeJSON's
// wire shape (GET /search?envelope=1) — a local shadow struct decoded over
// HTTP, exactly like this file's sibling conversational fetchers
// (conversationalHTTPFetcher's plain []store.SearchResult decode,
// answerHTTPResponse's shadow of AnswerResponse) rather than importing
// internal/server's Go type directly.
type crossProjectSearchHTTPResponse struct {
	Results           []store.SearchResult `json:"results"`
	DiversityDistinct int                  `json:"diversity_distinct,omitempty"`
	DiversityCounts   map[string]int       `json:"diversity_counts,omitempty"`
}

// conversationalCrossProjectHTTPFetcher returns an eval.ConversationalFetcher
// that queries GET /search?all_projects=1&envelope=1 on a running Omnia
// server (P5's fan-out wiring, cmd/omnia/crossproject.go) — the conversational-
// harness sibling of conversationalHTTPFetcher, widened to (a) request the
// cross-project fan-out and (b) decode the envelope shape instead of the
// bare array, since diversity_distinct/diversity_counts only exist on the
// envelope response.
func conversationalCrossProjectHTTPFetcher(client *http.Client, baseURL string) eval.ConversationalFetcher {
	return func(ctx context.Context, c eval.ConversationalCase) (eval.RetrievedCase, error) {
		u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/search")
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalCrossProjectHTTPFetcher: parse base url %q: %w", baseURL, err)
		}
		q := u.Query()
		q.Set("q", c.Query)
		q.Set("limit", strconv.Itoa(rankCandidateDepth))
		q.Set("all_projects", "1")
		q.Set("envelope", "1")
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalCrossProjectHTTPFetcher: build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalCrossProjectHTTPFetcher: GET %s: %w", u.String(), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalCrossProjectHTTPFetcher: GET %s: status %d", u.String(), resp.StatusCode)
		}

		var envelope crossProjectSearchHTTPResponse
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return eval.RetrievedCase{}, fmt.Errorf("conversationalCrossProjectHTTPFetcher: decode response: %w", err)
		}

		rc := eval.RetrievedCase{
			DiversityDistinct: envelope.DiversityDistinct,
			DiversityCounts:   envelope.DiversityCounts,
		}
		if len(envelope.Results) == 0 {
			return rc, nil
		}
		rc.Retrieved = assembleGroundingContext(envelope.Results)
		rc.SurfacedObservationID = envelope.Results[0].SyncID
		rc.Tokens = eval.TokenBreakdown{Retrieval: estimateTokenCount(envelope.Results[0].Content)}
		rc.RankedObservationIDs = rankedSyncIDs(envelope.Results)
		return rc, nil
	}
}

// runCrossProjectMeasurement is `omnia eval --profile conversational
// --multi-project`'s entry point: loads the corpus, keeps only its
// cross_project cases, fetches each ONCE through
// conversationalCrossProjectHTTPFetcher, scores it via
// eval.ScoreConversationalCase (the same scoring primitive the normal
// conversational run uses), and prints a per-case accuracy@1 + diversity
// line followed by the aggregate accuracy@1 — the P5 measurement gate's
// "before: ~0, after: ?" numbers plus "project diversity in the top-4 must
// span >= 2 projects" per case, not just on average.
//
// A single pass, not eval.RunConversationalHarness's [MinRuns,MaxRuns]
// reproducibility loop: this mode's entire point is to show EACH case's own
// outcome (accuracy@1 is deterministic given a fixed store/config, so
// repeating the same HTTP calls N times would only re-print identical rows,
// not add information — unlike the coding/conversational profiles' own
// runs, which exist to catch NON-determinism, not to average out real
// per-case variance).
func runCrossProjectMeasurement(ctx context.Context, opts conversationalRunOptions) error {
	if strings.ToLower(strings.TrimSpace(opts.Target)) == "answer" {
		return fmt.Errorf("--multi-project --target answer is not supported: GET /answer has no ranked-observation-ID list to score accuracy@1 against (only GET /search's envelope does) — use --target http (the default) instead")
	}
	if strings.TrimSpace(opts.HTTPBaseURL) == "" {
		return fmt.Errorf("--http-base-url is required for --multi-project (calls GET /search?all_projects=1 on a running server)")
	}

	cases, err := loadConversationalCorpus(opts.CorpusPath)
	if err != nil {
		return fmt.Errorf("load conversational corpus: %w", err)
	}

	var crossCases []eval.ConversationalCase
	for _, c := range cases {
		if c.Kind == eval.KindCrossProject {
			crossCases = append(crossCases, c)
		}
	}
	if len(crossCases) == 0 {
		return fmt.Errorf("no %q cases found in the loaded corpus", eval.KindCrossProject)
	}

	fetch := conversationalCrossProjectHTTPFetcher(&http.Client{Timeout: 30 * time.Second}, opts.HTTPBaseURL)

	fmt.Printf("Cross-Project Fan-out Measurement (P5, docs/conversational-retrieval-plan.md) — %d case(s)\n", len(crossCases))
	fmt.Println("GET /search?all_projects=1&envelope=1, per-case accuracy@1 + diversity (not just an aggregate)")
	fmt.Println()

	var rankedCases, hits int
	for _, c := range crossCases {
		rc, err := fetch(ctx, c)
		if err != nil {
			return fmt.Errorf("case %q: fetch: %w", c.ID, err)
		}
		if _, err := eval.ScoreConversationalCase(c, rc); err != nil {
			return fmt.Errorf("case %q: score: %w", c.ID, err)
		}

		hit := false
		if len(rc.RankedObservationIDs) > 0 && c.ObservationID != "" {
			rankedCases++
			hit = rc.RankedObservationIDs[0] == c.ObservationID
			if hit {
				hits++
			}
		}

		fmt.Printf("  %-32s hit@1=%-5v diversity_distinct=%d  counts=%v\n", c.ID, hit, rc.DiversityDistinct, rc.DiversityCounts)
	}

	fmt.Println()
	accuracy := 0.0
	if rankedCases > 0 {
		accuracy = float64(hits) / float64(rankedCases)
	}
	fmt.Printf("cross_project accuracy@1: %.3f (%d/%d rankable cases)\n", accuracy, hits, rankedCases)
	return nil
}
