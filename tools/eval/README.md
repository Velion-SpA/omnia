# Omnia recall eval harness

Two scripts that exercise omnia's recall surfaces against a disposable,
isolated corpus — used to catch recall regressions and to document known
recall limitations. Neither script touches a real `~/.omnia` install when
run correctly (see Isolation model below).

## Scripts

### `omnia_eval.py`

Seeds a small known corpus via `omnia save` and scores two recall surfaces:

- `omnia search` — keyword/FTS recall (ranked list), including a
  cross-project isolation check and a `--type` filter check.
- `omnia recall-fix` — signature-lane recall for recurring errors (the
  n-gram error-signature matcher, design #1399). Includes two cases marked
  `[LIMITATION]` that are *expected* to return nothing today:
  - a prose bug description with no machine error pattern (no signature
    gets attached at save time, so it's invisible to `recall-fix`), and
  - a distinctive error phrase diluted by unrelated noise tokens, which
    used to sink the match score below threshold (issue #84).

Metrics reported: **hit@1**, **hit@3**, **MRR** (mean reciprocal rank) over
the ranked-recall cases, plus average latency per surface. Isolation checks
and reproduced limitations are reported separately (they're not scored into
MRR — they're pass/fail booleans).

Run:

```sh
OMNIA_DATA_DIR=$(mktemp -d) OMNIA_BIN=/opt/homebrew/bin/omnia python3 tools/eval/omnia_eval.py
```

`OMNIA_BIN` defaults to `omnia` (resolved via `PATH`). `OMNIA_DATA_DIR` must
be set by the caller — this script does not create or default one; isolation
is the caller's responsibility (see below for why that matters).

### `omnia_semantic_eval.sh`

Compares FTS (CLI `omnia search`) recall against served semantic recall
(HTTP `/search`, isolated `omnia serve`) for a handful of paraphrase
queries that keyword search alone can't match. Seeds a tiny corpus, runs
`omnia embed`, starts an isolated server, and prints a side-by-side table.

Run:

```sh
tools/eval/omnia_semantic_eval.sh
# or, to control where the isolated home/config live and which binary to use:
EVAL_SCRATCH=/tmp/my-scratch OMNIA_BIN=/opt/homebrew/bin/omnia EVAL_PORT=7500 tools/eval/omnia_semantic_eval.sh
```

`EVAL_SCRATCH` defaults to a fresh `mktemp -d` (disposable). `OMNIA_BIN`
defaults to `omnia`. The script filters the update-checker banner with
`grep -Ev` on purpose (not `rg`) to keep the dependency footprint at
grep/curl/jq/sqlite3/python3, which is more likely to already be present
than ripgrep.

## Metrics glossary

- **hit@1 / hit@3**: fraction of ranked-recall cases where the expected
  memory appears in the top 1 / top 3 results.
- **MRR**: mean reciprocal rank — `1/rank` of the first expected hit,
  averaged across cases; 0 if the expected memory never appears.
- **Latency**: served semantic query latency is single-digit milliseconds
  (embedding lookup + RRF against an already-running process). CLI
  `search`/`recall-fix` latency is dominated by **process cold-start**
  (~250ms) — that's Go binary startup + SQLite open, not query cost. Don't
  compare CLI latency to served latency as if they measured the same thing.

## Isolation model — read before running `omnia embed`

`OMNIA_DATA_DIR` isolates the **main** database (`omnia.db` — memories,
projects, sessions) to the given directory. It does **not** isolate the
**embeddings vector store** (`embeddings.db`, default
`~/.local/share/omnia/embeddings.db`), which historically resolved
independently of the active data dir (tracked as issue #82).

Practical consequence: running `omnia embed` with an alternate
`OMNIA_DATA_DIR` embeds the alt corpus, but the vector store it prunes
against is still the **real** one — i.e. it can wipe every embedding that
doesn't correspond to an observation in the alt (tiny, disposable) main DB.
This is silent, tenant-crossing data loss, and it's why `omnia_eval.py`
requires the caller to pass a real `OMNIA_DATA_DIR` and does not touch
`omnia embed` at all.

**Full isolation today requires overriding `HOME`**, not just
`OMNIA_DATA_DIR` — that's what `omnia_semantic_eval.sh` does (it exports
`HOME="$EVAL_SCRATCH/eval-home"` so every path that falls back to
`~/.local/share/omnia/...` or `~/.config/omnia/...` lands under the scratch
dir instead of the real one).

**Warning:** never run `omnia embed` against an alternate `OMNIA_DATA_DIR`
on a machine that also has a real omnia instance, unless you have also
overridden `HOME` (or the fix for #82 has landed and embeddings are scoped
to the data dir). Doing so risks pruning the real instance's embeddings.

## Recall surface split (why `search` results differ from `mem_search`)

CLI `omnia search` and the HTTP `/search` endpoint are **FTS-only** today
(keyword/full-text ranking). Semantic (dense embedding + RRF) recall is
currently wired only into the MCP `mem_search` path used by agents — see
issue #86. That means a paraphrase query that a human runs via
`omnia search` or `curl /search` can miss, while the same paraphrase via
the MCP `mem_search` tool hits, because only the MCP path blends in the
semantic lane. `omnia_semantic_eval.sh` demonstrates this split directly by
comparing CLI FTS output against the served `/search` endpoint side by side
(the served endpoint is also FTS-only pending #86 — this script gives you
a harness to re-run once semantic is wired into `/search` too).

Semantic recall (whichever surface it's wired into) requires:

- Ollama running locally and reachable (default `http://localhost:11434`).
- The configured embedding model pulled (default
  `jina/jina-embeddings-v2-base-es`).
- `embeddings.enabled: true` and `recall.enabled: true` in config (see
  issue #83 for default-floor calibration and auto-detect behavior).

Without Ollama + the model available, both scripts still run — the
semantic-specific checks will simply show empty/FTS-equivalent results
instead of failing outright.
