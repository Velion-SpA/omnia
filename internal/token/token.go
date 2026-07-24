// Package token provides a deterministic, dependency-free token-count
// estimate for memory content, and a generic "top-N-complete" trim helper
// built on top of it (spec token-estimation, design D1/PR1 for Omnia v0.3 —
// Context Economy).
//
// This package is the shared foundation every injection-economy feature
// (budget, diversity, gating, lens) is built on. It imports only the Go
// standard library — no CGO, no external tokenizer, no dependency on any
// other internal/* package — so it stays a reusable leaf importable by
// internal/store, internal/mcp, and cmd/omnia without introducing an import
// cycle.
//
// # Accuracy
//
// EstimateTokens uses a char/4 heuristic: (rune count + 3) / 4. This is a
// documented APPROXIMATION, not exact BPE tokenizer parity:
//
//   - English prose and source code: roughly ±25% of true token counts for
//     common BPE tokenizers (a reasonable rule of thumb given ~4 chars per
//     token in English).
//   - CJK (Chinese/Japanese/Korean) and other dense-script text: this
//     heuristic systematically UNDER-counts, since a single CJK rune is
//     often its own token (or close to it) in real tokenizers, not one
//     quarter of a token. Callers budgeting for heavy CJK content should
//     apply extra headroom.
//   - Whitespace/punctuation-dense text (tables, ASCII art, deeply indented
//     code): the heuristic OVER-counts, since real tokenizers often fold
//     runs of whitespace and punctuation into fewer tokens. The budget
//     simply ends up conservative there, which is the safe direction.
//
// This divergence from true tokenizer output is an accepted, documented
// limitation (spec token-estimation REQ3) — not a defect to be fixed by
// adding a real tokenizer dependency.
package token

import "unicode/utf8"

// EstimateTokens returns a deterministic, heuristic estimate of how many
// LLM tokens the given string will consume: (rune count + 3) / 4.
//
// EstimateTokens is pure and side-effect-free: the same input always
// produces the same output (spec token-estimation REQ2), it never panics,
// and it never invokes CGO or an external process (spec token-estimation
// REQ1). An empty string returns 0. Multi-megabyte input is handled in
// bounded, linear time — no unbounded resource use (spec token-estimation
// REQ4).
//
// See the package doc comment for the heuristic's documented accuracy
// caveats, in particular CJK under-counting.
func EstimateTokens(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

// TrimToBudget trims items to fit within budget using "top-N-complete"
// semantics: items are kept whole, in order, until the next item would push
// the running total over budget; that item and every item after it are
// dropped entirely. No item is ever partially included (spec
// injection-budget REQ2).
//
// sizeOf reports the size (in whatever unit budget is expressed in —
// typically estimated tokens via EstimateTokens) of a single item.
//
// If budget <= 0 or items is empty, TrimToBudget returns (nil, 0): nothing
// fits, and no item is ever force-included over budget. An item that
// exactly fills the remaining budget is included (the boundary is
// inclusive: used+size <= budget keeps the item).
//
// TrimToBudget is a pure generic helper: it never mutates or aliases items
// (the returned slice has fresh backing storage), and makes no assumption
// about T beyond what sizeOf needs.
func TrimToBudget[T any](items []T, sizeOf func(T) int, budget int) (kept []T, used int) {
	if budget <= 0 || len(items) == 0 {
		return nil, 0
	}

	kept = make([]T, 0, len(items))
	for _, item := range items {
		size := sizeOf(item)
		if used+size > budget {
			break
		}
		kept = append(kept, item)
		used += size
	}

	if len(kept) == 0 {
		return nil, 0
	}
	return kept, used
}
