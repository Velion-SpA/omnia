# Memory Embeddings Model Specification

## Purpose

Defines the model-selection, Matryoshka-truncation, and eval-gated-swap contract for Omnia's local embeddings layer (`internal/embed`), so a candidate model (EmbeddingGemma-300m, served via Ollama like today's `jina/jina-embeddings-v2-base-es`) can be evaluated and optionally adopted without silently degrading recall or breaking the embeddings store's dimension invariants. `jina/jina-embeddings-v2-base-es` remains the shipped default throughout.

## Requirements

### Requirement: EMBM-1 Selectable Embedding Model, Ollama-Only
The system MUST allow the active embedding model to be chosen via `config.EmbeddingsConfig.Model` without code changes, and MUST support EmbeddingGemma-300m as a selectable value through the existing `internal/embed.Client`/`Embedder` interface. Adding a selectable model MUST NOT introduce a cgo dependency.

#### Scenario: Config selects EmbeddingGemma
- GIVEN `EmbeddingsConfig.Model` is set to the EmbeddingGemma-300m Ollama tag and `Dim` is 768
- WHEN Omnia builds its `embed.Client` from config
- THEN the client embeds and searches using that model with no source changes

#### Scenario: Unknown model fails fast
- GIVEN `EmbeddingsConfig.Model` names a model absent from Ollama
- WHEN `Client.Embed` is called
- THEN it returns an error rather than silently falling back to jina

### Requirement: EMBM-2 jina Remains Default Until Eval-Gated Swap
`applyDefaults` MUST keep `jina/jina-embeddings-v2-base-es` (dim 768) as the applied default when `Model`/`Dim` are unset. The system MUST NOT change this shipped default until a harness run of `sdd/omnia-eval-harness`'s retrieval-only recall@k (extending `RunModelAB`/`ABResult` over the existing `ab_pairs.json` ES/EN set) shows the candidate does not regress recall@k versus jina.

#### Scenario: Unset config yields jina default
- GIVEN a config with no `embeddings.model`/`embeddings.dim`
- WHEN `Load`/`applyDefaults` runs
- THEN `Model` is `jina/jina-embeddings-v2-base-es` and `Dim` is 768

#### Scenario: Swap blocked without recorded evidence
- GIVEN no recall@k comparison of EmbeddingGemma vs jina has been recorded
- WHEN a maintainer proposes changing the shipped default
- THEN the change is rejected until that comparison exists and shows no regression

### Requirement: EMBM-3 MRL-Capability Guard
The system MUST maintain an explicit per-model MRL-capability flag distinguishing Matryoshka-trained models (EmbeddingGemma-300m) from non-MRL models (jina-embeddings-v2-base-es, which has no MRL training). The system MUST reject a configured truncation dimension below a model's native output size for any model not flagged MRL-capable.

#### Scenario: Truncation rejected for jina
- GIVEN `Model` is jina (native 768d, non-MRL) and `Dim` is configured to 256
- WHEN Omnia validates the embeddings config
- THEN it rejects the configuration rather than silently truncating jina's output

#### Scenario: Truncation accepted for EmbeddingGemma
- GIVEN `Model` is EmbeddingGemma-300m (MRL-capable) and `Dim` is configured to 256
- WHEN Omnia validates the embeddings config
- THEN the configuration is accepted

### Requirement: EMBM-4 Matryoshka Dimension as Runtime Config
For a model flagged MRL-capable, the system MUST allow the effective stored/query dimension to be set independently of the model's native output size (e.g. 768/256/128), by truncating the native vector to the configured leading dimensions and re-normalizing to unit length before it is stored or compared.

#### Scenario: 256-dim config truncates and re-normalizes
- GIVEN `Dim` is 256 for an MRL-capable model with native 768d output
- WHEN a memory is embedded
- THEN the stored vector has length 256 and unit L2 norm

#### Scenario: Full-dimension config leaves vector untouched
- GIVEN `Dim` equals the model's native dimension
- WHEN a memory is embedded
- THEN no truncation is applied

### Requirement: EMBM-5 Dimension-Isolated Search
`internal/embed.Store`'s search path MUST continue to treat a stored row whose `Dim` differs from the query vector's length as non-comparable and exclude it from ranking, so brute-force cosine scoring never compares vectors of differing dimensionality.

#### Scenario: Mixed-dimension store returns only matching hits
- GIVEN the store holds rows at both 768d (pre-migration) and 256d (post-migration)
- WHEN `Search`/`SearchScoped` runs with a 256d query vector
- THEN only 256d rows are scored and ranked; 768d rows are skipped

### Requirement: EMBM-6 Full Re-embed on Model or Dimension Change
When the configured `Model` or effective `Dim` changes, the system MUST re-embed every live observation via `Reconcile` (triggered because a row's stored `Model`/`Dim` no longer matches the configured values, the same trigger already used for content-hash changes), and MUST prune rows for observations no longer live once reconciliation completes.

#### Scenario: Model change triggers full reconciliation
- GIVEN a store previously reconciled at `model=jina.., dim=768`
- WHEN the config `Model` changes to EmbeddingGemma-300m
- THEN the next `Reconcile` run re-embeds every live row (`Stats.Reused == 0`)

#### Scenario: Interrupted migration is resumable
- GIVEN a `Reconcile` run into the new model/dim is interrupted mid-run
- WHEN `Reconcile` runs again
- THEN it re-embeds only rows still mismatched on `Model`/`Dim` and reuses rows already migrated
