package token

import (
	"strings"
	"testing"
	"time"
)

// TestEstimateTokens_Golden pins the char/4 heuristic against a fixed table
// of inputs, including the documented CJK under-count (spec token-estimation
// REQ3: heuristic, documented accuracy, not exact tokenizer parity).
func TestEstimateTokens_Golden(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "empty string",
			in:   "",
			want: 0,
		},
		{
			name: "short ASCII, non-multiple-of-4 runes",
			// 11 runes: (11+3)/4 = 3
			in:   "hello world",
			want: 3,
		},
		{
			name: "ASCII exact multiple of 4 runes",
			// 8 runes: (8+3)/4 = 2
			in:   "abcdefgh",
			want: 2,
		},
		{
			name: "single rune",
			// 1 rune: (1+3)/4 = 1
			in:   "a",
			want: 1,
		},
		{
			name: "CJK under-count (documented heuristic limitation)",
			// 4 CJK runes, 12 bytes: (4+3)/4 = 1 — a real BPE tokenizer would
			// count closer to 4-8 tokens here. This under-count is the
			// documented accepted approximation (spec REQ3).
			in:   "你好世界",
			want: 1,
		},
		{
			name: "whitespace-only string still estimates by rune count",
			// 5 runes: (5+3)/4 = 2
			in:   "     ",
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.in)
			if got != tt.want {
				t.Fatalf("EstimateTokens(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestEstimateTokens_Deterministic verifies identical input always yields an
// identical estimate (spec token-estimation REQ2).
func TestEstimateTokens_Deterministic(t *testing.T) {
	in := "the quick brown fox jumps over the lazy dog 你好世界"

	first := EstimateTokens(in)
	second := EstimateTokens(in)

	if first != second {
		t.Fatalf("EstimateTokens not deterministic: first=%d second=%d", first, second)
	}
}

// TestEstimateTokens_BoundedHugeInput verifies multi-MB content completes
// without panic and within a bounded time budget (spec token-estimation
// REQ4 — bounded edge-case behavior).
func TestEstimateTokens_BoundedHugeInput(t *testing.T) {
	huge := strings.Repeat("a", 5_000_000) // 5,000,000 runes (ASCII)

	done := make(chan int, 1)
	go func() {
		done <- EstimateTokens(huge)
	}()

	select {
	case got := <-done:
		want := (5_000_000 + 3) / 4
		if got != want {
			t.Fatalf("EstimateTokens(huge) = %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EstimateTokens did not return within bounded time for multi-MB input")
	}
}

// trimItem is a minimal test fixture for TrimToBudget — a named item with a
// fixed reported size, so tests can assert whole-item-only inclusion without
// depending on any real store/mcp type.
type trimItem struct {
	name string
	size int
}

func trimItemSize(it trimItem) int { return it.size }

// TestTrimToBudget_Table covers the top-N-complete trim contract (spec
// injection-budget REQ2, exercised here at the generic-helper level): items
// are kept whole in order until the budget is exhausted, never partially
// truncated.
func TestTrimToBudget_Table(t *testing.T) {
	tests := []struct {
		name      string
		items     []trimItem
		budget    int
		wantNames []string
		wantUsed  int
	}{
		{
			name: "whole items kept in order, remainder dropped entirely (not truncated)",
			items: []trimItem{
				{name: "a", size: 10},
				{name: "b", size: 10},
				{name: "c", size: 10},
			},
			budget:    25,
			wantNames: []string{"a", "b"}, // "c" would push 20+10=30 > 25 → dropped whole
			wantUsed:  20,
		},
		{
			name: "budget of zero returns nil, 0",
			items: []trimItem{
				{name: "a", size: 1},
			},
			budget:    0,
			wantNames: nil,
			wantUsed:  0,
		},
		{
			name: "negative budget returns nil, 0",
			items: []trimItem{
				{name: "a", size: 1},
			},
			budget:    -5,
			wantNames: nil,
			wantUsed:  0,
		},
		{
			name: "huge budget keeps every item",
			items: []trimItem{
				{name: "a", size: 100},
				{name: "b", size: 200},
				{name: "c", size: 300},
			},
			budget:    1_000_000,
			wantNames: []string{"a", "b", "c"},
			wantUsed:  600,
		},
		{
			name: "exact-fit boundary: item that exactly fills remaining budget is included",
			items: []trimItem{
				{name: "a", size: 5},
				{name: "b", size: 5},
			},
			budget:    10,
			wantNames: []string{"a", "b"},
			wantUsed:  10,
		},
		{
			name: "budget smaller than first item excludes everything",
			items: []trimItem{
				{name: "a", size: 50},
				{name: "b", size: 1},
			},
			budget:    10,
			wantNames: nil,
			wantUsed:  0,
		},
		{
			name:      "empty items returns nil, 0",
			items:     nil,
			budget:    100,
			wantNames: nil,
			wantUsed:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, used := TrimToBudget(tt.items, trimItemSize, tt.budget)

			if used != tt.wantUsed {
				t.Fatalf("used = %d, want %d", used, tt.wantUsed)
			}

			gotNames := make([]string, len(kept))
			for i, it := range kept {
				gotNames[i] = it.name
			}

			if len(gotNames) != len(tt.wantNames) {
				t.Fatalf("kept = %v (len %d), want %v (len %d)", gotNames, len(gotNames), tt.wantNames, len(tt.wantNames))
			}
			for i := range tt.wantNames {
				if gotNames[i] != tt.wantNames[i] {
					t.Fatalf("kept[%d] = %q, want %q (full got=%v want=%v)", i, gotNames[i], tt.wantNames[i], gotNames, tt.wantNames)
				}
			}

			if tt.budget <= 0 && kept != nil {
				t.Fatalf("TrimToBudget with budget=%d must return nil kept slice, got %v", tt.budget, kept)
			}
		})
	}
}
