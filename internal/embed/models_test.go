package embed

import "testing"

// TestLookupModel_JinaIsNotMRL locks EMBM-3's non-MRL side: jina-embeddings-
// v2-base-es has no Matryoshka training, so LookupModel must report MRL:
// false for it — this is what makes truncating its output an error rather
// than a silent degradation.
func TestLookupModel_JinaIsNotMRL(t *testing.T) {
	info, ok := LookupModel("jina/jina-embeddings-v2-base-es")
	if !ok {
		t.Fatal("LookupModel(jina): got not found, want found")
	}
	if info.MRL {
		t.Error("jina MRL: got true, want false (jina has no Matryoshka training)")
	}
	if info.NativeDim != 768 {
		t.Errorf("jina NativeDim: got %d, want 768", info.NativeDim)
	}
}

// TestLookupModel_EmbeddingGemmaIsMRL locks EMBM-3's MRL side for the
// candidate model: embeddinggemma:300m (tag confirmed against the Ollama
// registry manifest during apply) is Matryoshka-trained, so truncating its
// native 768-dim output to 512/256/128 is a valid, supported operation
// (EMBM-4).
func TestLookupModel_EmbeddingGemmaIsMRL(t *testing.T) {
	info, ok := LookupModel("embeddinggemma:300m")
	if !ok {
		t.Fatal("LookupModel(embeddinggemma:300m): got not found, want found")
	}
	if !info.MRL {
		t.Error("embeddinggemma MRL: got false, want true (Matryoshka-trained)")
	}
	if info.NativeDim != 768 {
		t.Errorf("embeddinggemma NativeDim: got %d, want 768", info.NativeDim)
	}
}

// TestLookupModel_UnknownModelNotFound proves an unregistered model name
// (EMBM-1: the active model must stay selectable without code changes) is
// reported as "not found" rather than panicking or defaulting to some
// capability guess — callers (ValidateEmbeddings, Client.Embed) must treat
// "not found" as "skip capability-specific behavior," never as an error on
// its own.
func TestLookupModel_UnknownModelNotFound(t *testing.T) {
	if _, ok := LookupModel("some-unregistered-model"); ok {
		t.Error("LookupModel(unknown): got found, want not found")
	}
}
