package intent

// RoutingProfile is a per-intent ranking hint, expressed purely as DATA
// (plan P2: "Ship it as a new ranking profile per intent, not new ranking
// code — RankingConfig already carries per-component weights and
// ApplyTypeLens already does type boosting. This item is mostly a lookup
// table plus wiring.").
//
// Every field here is named and typed to match, field-for-field, the
// primitives the two existing consumers already accept, so wiring is a
// direct assignment rather than a translation layer:
//
//   - TypeLens matches internal/mcp.ApplyTypeLens's lensType string
//     argument (internal/mcp/type_lens.go) — the same vocabulary
//     InferLensType already returns ("doc", "decision", "architecture",
//     "bugfix", "pattern") or "" for no lens.
//   - RecencyWeight matches internal/config.RankingWeights.Recency
//     (float32 — internal/config/config.go's RankingConfig.Weights.Recency).
//   - RecencyHalfLifeDays matches internal/config.RankingConfig's own
//     RecencyHalfLifeDays field (float64, days-to-half-decay).
//
// This package intentionally does NOT import internal/config or
// internal/mcp (see intent.go's package doc — dependency-free leaf,
// mirroring internal/recall's precedent) so these are plain
// pointer-to-primitive fields, not the structs themselves. A wiring caller
// in internal/mcp (out of this package's file-ownership scope — see the
// wiring note this package's author reported alongside this change)
// overlays non-nil fields onto a config.RankingConfig it already holds.
type RoutingProfile struct {
	// TypeLens is the situational type to lift for this intent, or "" for
	// no lens hint. Wiring should pass this directly as ApplyTypeLens's
	// lensType argument, OVERRIDING whatever InferLensType would have
	// independently inferred from the raw query text — Classify already
	// read the same query with intent-level context (bilingual phrase
	// cues) that InferLensType's flatter, single-purpose table lacks.
	TypeLens string

	// RecencyWeight, when non-nil, overrides
	// config.RankingConfig.Weights.Recency for this turn only. nil means
	// "do not touch the configured weight". A pointer, not a bare float32,
	// because Identity's profile legitimately wants an explicit *zero*
	// ("recency: 0" per the plan's routing table) — a value
	// indistinguishable from "no override" if the field were a bare
	// float32 defaulting to its zero value.
	RecencyWeight *float32

	// RecencyHalfLifeDays, when non-nil, overrides
	// config.RankingConfig.RecencyHalfLifeDays (days until the recency
	// signal decays to 0.5) for this turn only. nil means "do not touch
	// the configured half-life". Same nil-means-absent reasoning as
	// RecencyWeight.
	RecencyHalfLifeDays *float64

	// Unverified marks that this intent's real routing target — P4's claim
	// lane (docs/conversational-retrieval-plan.md, "P4 — Claim lifecycle:
	// open items that can close") — does not exist yet. Plan's routing
	// table: open_items -> "claim lane (P4); until P4 ships, flag the
	// answer as unverified." Wiring should surface this to the caller
	// (e.g. an "unverified": true response field) rather than silently
	// answering an open-items question as if claim state were tracked.
	Unverified bool

	// MultiProjectHint marks that this intent's real routing target — P5's
	// multi-project mode (docs/conversational-retrieval-plan.md, "P5") —
	// does not exist yet. Plan's routing table: cross_project ->
	// "multi-project mode (P5)"; P5's own design note: "Hermes-side: P2's
	// cross_project intent releases the project latch for that turn only."
	// This flag is what that eventual wiring keys off.
	MultiProjectHint bool

	// Notes is a short human-readable explanation of why this profile
	// looks the way it does, for eval logging and debugging. Not consumed
	// by any ranking logic — safe to log or drop.
	Notes string
}

func float32ptr(v float32) *float32 { return &v }
func float64ptr(v float64) *float64 { return &v }

// profiles is the plan's routing table (P2), expressed as data. Delta and
// Unknown are deliberately absent from this map: ProfileFor returns the
// zero-value RoutingProfile for both, which is byte-for-byte "no override"
// — Delta because the plan says its existing signature/exact-match lane is
// "already correct" (no new routing needed), Unknown because that is this
// whole package's safety property (see intent.go's package doc).
var profiles = map[Intent]RoutingProfile{
	Identity: {
		// TypeLens is deliberately EMPTY, though the plan's routing table
		// originally called for "doc" here. Measured: forcing the doc lens on
		// identity questions cuts grounding from 0.625 to 0.375 over the
		// conversational corpus, while every other configuration — ranking
		// alone, InferLensType's own lens alone, intent routing without the
		// lens — holds 0.625.
		//
		// The cause is that ApplyTypeLens is a hard PARTITION, not the soft
		// boost its name suggests: it places every matching-type row above
		// every non-matching one. That is a win when the target type is a
		// small minority of the candidate set (Rationale/`decision` below
		// gains 0.750 -> 0.875 from exactly this). It backfires once the type
		// is a large share of what an identity query already retrieves —
		// after repodoc ingestion, doc chunks are a substantial fraction of
		// those candidates, so partitioning by type promotes the haystack
		// rather than the needle and discards the relevance order that was
		// already surfacing the right chunk.
		//
		// Relevance alone finds the right doc; it does not need the shove.
		// The deeper fix — making ApplyTypeLens a score boost rather than a
		// partition — is deferred: it is shared with the mem_search path and
		// carries regression risk that this one-field change does not.
		TypeLens:      "",
		RecencyWeight: float32ptr(0),
		Notes:         "identity wants a definition, not a recent one — recency contributes nothing (plan routing table: 'recency: 0'). No type lens: see the comment above, it measurably hurts once doc chunks are numerous.",
	},
	Status: {
		RecencyWeight:       float32ptr(3),
		RecencyHalfLifeDays: float64ptr(3),
		Notes:               "status wants the latest state; recency is decisive (plan routing table: 'recency weight high (half-life ~3 days)')",
	},
	OpenItems: {
		Unverified: true,
		Notes:      "open_items belongs in P4's claim lane, which does not exist yet; flag the answer as unverified until P4 ships (plan routing table)",
	},
	Rationale: {
		TypeLens: "decision",
		Notes:    "rationale wants the decision/architecture record. ApplyTypeLens accepts one lensType, not a set, so 'decision' is primary (matches InferLensType's own decision-signal table); 'architecture' is the documented alternate the plan lists (type lens -> decision/architecture) — a future ApplyTypeLens extension to accept multiple lens types would let both apply at once (see the wiring note reported alongside this change)",
	},
	CrossProject: {
		MultiProjectHint: true,
		Notes:            "cross_project belongs in P5's multi-project mode, which does not exist yet; MultiProjectHint is what that eventual project-latch-release wiring keys off (plan routing table)",
	},
}

// ProfileFor returns the ranking-profile hint for intent i (plan P2's
// routing table). Any Intent this package does not recognize — including
// Delta and Unknown, see profiles' doc comment — returns the zero-value
// RoutingProfile: no TypeLens, no weight overrides, Unverified and
// MultiProjectHint both false. That zero value IS the safety property the
// plan calls out: "Unclassified -> today's behaviour, byte-for-byte."
func ProfileFor(i Intent) RoutingProfile {
	return profiles[i]
}
