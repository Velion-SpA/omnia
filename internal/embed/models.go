package embed

// ModelInfo captures embedding-model capability metadata needed to guard
// Matryoshka (MRL) dimension truncation (EMBM-3): only models explicitly
// trained with Matryoshka Representation Learning support truncating their
// native output to a smaller leading-dimension prefix and re-normalizing
// into a still-valid, still-comparable embedding. jina-embeddings-v2-base-es
// has NO such training — truncating it would silently degrade every
// stored/query vector with no error surfaced.
type ModelInfo struct {
	// NativeDim is the model's untruncated output dimension.
	NativeDim int
	// MRL reports whether the model was trained with Matryoshka
	// Representation Learning, i.e. whether truncating its native output to
	// a smaller leading-dimension prefix and re-normalizing yields a valid
	// embedding rather than a silently degraded one.
	MRL bool
}

// knownModels is Omnia's embedding-model capability registry (EMBM-3). An
// unregistered model name is NOT an error on its own (EMBM-1: the active
// model must stay selectable via config without code changes) — LookupModel
// reports "not found" and callers (config.ValidateEmbeddings, Client.Embed)
// treat that as "skip the MRL guard," relying on the store's existing
// dimension-mismatch checks (EMBM-5) as the remaining safety net.
var knownModels = map[string]ModelInfo{
	// jina/jina-embeddings-v2-base-es: today's shipped default (EMBM-2). No
	// MRL training — a configured Dim below 768 must be rejected, never
	// silently truncated.
	"jina/jina-embeddings-v2-base-es": {NativeDim: 768, MRL: false},
	// embeddinggemma:300m: Google's 300M-parameter multilingual embedding
	// model served via Ollama. Tag confirmed against the live Ollama
	// registry manifest (https://ollama.com/library/embeddinggemma/tags —
	// "embeddinggemma:300m" resolves to a real image, layered on
	// "embeddinggemma:300m-bf16") during the omnia-embeddings-gemma apply,
	// not assumed. Native output is 768-dim and IS Matryoshka-trained,
	// truncatable to 512/256/128 per Google's model card — this is the
	// EMBM-1/EMBM-4 candidate model. jina stays the shipped default
	// regardless (EMBM-2) until an eval-gated swap is recorded.
	"embeddinggemma:300m": {NativeDim: 768, MRL: true},
}

// LookupModel returns the capability metadata for a known embedding model
// name. The second return value reports whether the model is registered at
// all — "not found" must be handled as "skip capability-specific behavior,"
// not as an error, so an operator can still select a model this registry
// hasn't been taught about yet.
func LookupModel(name string) (ModelInfo, bool) {
	info, ok := knownModels[name]
	return info, ok
}
