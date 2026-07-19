package store

import (
	"strings"
	"testing"
)

// ─── NormalizeErrorSignature ─────────────────────────────────────────────────
//
// RED (design obs #1498 / audit #1497, slice 1, part B): deterministic
// normalization of raw error text (Go panics, JS/TS stack traces, or a bare
// query string) into a stable signature so that two occurrences of the SAME
// underlying bug — differing only in file paths, line/col numbers, memory
// addresses, timestamps, or incidental counters — collapse to identical
// strings, while genuinely different errors remain distinguishable.

func TestNormalizeErrorSignature_GoPanicSameBugDifferentPathsLinesAddresses(t *testing.T) {
	variantA := `panic: runtime error: index out of range [5] with length 3

goroutine 1 [running]:
main.processItems(...)
	/Users/benja/dev/omnia/internal/store/store.go:3144 +0x1a4
main.main()
	/Users/benja/dev/omnia/cmd/server/main.go:42 +0x65`

	variantB := `panic: runtime error: index out of range [12] with length 7

goroutine 7 [running]:
main.processItems(...)
	/home/ci/build/omnia/internal/store/store.go:3199 +0x2b3
main.main()
	/home/ci/build/omnia/cmd/server/main.go:88 +0x9f`

	sigA := NormalizeErrorSignature(variantA)
	sigB := NormalizeErrorSignature(variantB)

	if sigA == "" {
		t.Fatalf("expected non-empty signature for variant A")
	}
	if sigA != sigB {
		t.Fatalf("expected same-bug variants to normalize identically:\n  A: %q\n  B: %q", sigA, sigB)
	}
}

func TestNormalizeErrorSignature_GoDifferentBugsDiffer(t *testing.T) {
	indexOutOfRange := `panic: runtime error: index out of range [5] with length 3

goroutine 1 [running]:
main.processItems(...)
	/Users/benja/dev/omnia/internal/store/store.go:3144 +0x1a4`

	nilPointer := `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x47d123]

goroutine 5 [running]:
main.(*Server).Handle(...)
	/Users/benja/dev/omnia/internal/server/server.go:88 +0x44`

	sig1 := NormalizeErrorSignature(indexOutOfRange)
	sig2 := NormalizeErrorSignature(nilPointer)

	if sig1 == sig2 {
		t.Fatalf("expected genuinely different Go panics to normalize differently, both got %q", sig1)
	}
}

func TestNormalizeErrorSignature_JSTSSameBugDifferentPathsLinesCols(t *testing.T) {
	variantA := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)
    at async handleRequest (/Users/benja/dev/omnia/src/server.ts:112:5)`

	variantB := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/home/ci/workspace/omnia/src/services/orderService.ts:53:11)
    at async handleRequest (/home/ci/workspace/omnia/src/server.ts:130:9)`

	sigA := NormalizeErrorSignature(variantA)
	sigB := NormalizeErrorSignature(variantB)

	if sigA == "" {
		t.Fatalf("expected non-empty signature for variant A")
	}
	if sigA != sigB {
		t.Fatalf("expected same-bug JS/TS variants to normalize identically:\n  A: %q\n  B: %q", sigA, sigB)
	}
}

func TestNormalizeErrorSignature_JSDifferentErrorsDiffer(t *testing.T) {
	typeErr := `TypeError: Cannot read properties of undefined (reading 'items')
    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)`

	refErr := `ReferenceError: fetchData is not defined
    at Object.<anonymous> (/Users/benja/dev/omnia/src/index.js:5:3)`

	sig1 := NormalizeErrorSignature(typeErr)
	sig2 := NormalizeErrorSignature(refErr)

	if sig1 == sig2 {
		t.Fatalf("expected TypeError and ReferenceError to normalize differently, both got %q", sig1)
	}
}

func TestNormalizeErrorSignature_EmptyInputReturnsEmpty(t *testing.T) {
	if got := NormalizeErrorSignature(""); got != "" {
		t.Fatalf("expected empty signature for empty input, got %q", got)
	}
	if got := NormalizeErrorSignature("   \n\t  "); got != "" {
		t.Fatalf("expected empty signature for whitespace-only input, got %q", got)
	}
}

func TestNormalizeErrorSignature_ProseWithoutErrorIsStable(t *testing.T) {
	// Prose without any error-shaped tokens should still normalize
	// deterministically (lowercased, whitespace-collapsed) rather than
	// crashing or returning something time/random dependent.
	a := NormalizeErrorSignature("  Hello   World  ")
	b := NormalizeErrorSignature("hello world")

	if a == "" {
		t.Fatalf("expected a stable low-signal (non-empty) value for prose input, got empty")
	}
	if a != b {
		t.Fatalf("expected equivalent prose to normalize identically:\n  a: %q\n  b: %q", a, b)
	}

	// Deterministic: calling twice with the same input yields the same result.
	if again := NormalizeErrorSignature("  Hello   World  "); again != a {
		t.Fatalf("expected deterministic output, got %q then %q", a, again)
	}
}

func TestNormalizeErrorSignature_StripsHexUUIDAndTimestamps(t *testing.T) {
	a := "connection failed at 2026-07-19T15:04:05Z ref=0x1a2b3c id=550e8400-e29b-41d4-a716-446655440000"
	b := "connection failed at 2026-07-20T09:12:45Z ref=0x9f8e7d id=6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	sigA := NormalizeErrorSignature(a)
	sigB := NormalizeErrorSignature(b)

	if sigA == "" {
		t.Fatalf("expected non-empty signature")
	}
	if sigA != sigB {
		t.Fatalf("expected hex/uuid/timestamp variants to normalize identically:\n  a: %q\n  b: %q", sigA, sigB)
	}
}

// ─── isBugfixFamilyType ───────────────────────────────────────────────────────

func TestIsBugfixFamilyType(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"bug", true},
		{"bugfix", true},
		{"fix", true},
		{"incident", true},
		{"hotfix", true},
		{"BugFix", true}, // case-insensitive
		{"decision", false},
		{"architecture", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isBugfixFamilyType(c.typ); got != c.want {
			t.Errorf("isBugfixFamilyType(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
}

// ─── normalizeOutcome ─────────────────────────────────────────────────────────

func TestNormalizeOutcome(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"  ", "", false},
		{"unknown", "", false}, // "unknown" is stored as NULL (equivalent to unset)
		{"worked", OutcomeWorked, false},
		{"WORKED", OutcomeWorked, false},
		{"did_not_work", OutcomeDidNotWork, false},
		{"nonsense", "", true},
	}
	for _, c := range cases {
		got, err := normalizeOutcome(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeOutcome(%q): expected error, got nil", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeOutcome(%q): unexpected error: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeOutcome(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// ─── extractErrorText (fix for the "signature lane almost never fires" defect) ─
//
// The original save path computed error_signature from the ENTIRE memory
// content (full What/Why/Where/Learned prose), which almost never equals a
// fresh, bare error occurrence's normalized signature under Search's exact
// equality lookup. extractErrorText narrows the save-time input to just the
// error-shaped line(s) within the content, so the stored signature is close
// in shape to what a future bare-error query will normalize to.

func TestExtractErrorText_PullsErrorLineOutOfFullProseMemory(t *testing.T) {
	content := "**What**: Fixed a crash in the checkout pipeline when the cart items slice was shorter than expected.\n" +
		"**Why**: A customer reported the app crashing intermittently during checkout.\n" +
		"**Where**: internal/checkout/pipeline.go\n" +
		"**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems() when a coupon removed an item mid-request."

	got := extractErrorText(content)

	if got == "" {
		t.Fatalf("expected extractErrorText to find the quoted panic line, got empty string")
	}
	if got == content {
		t.Fatalf("expected extractErrorText to narrow to the error-shaped line(s), got the full prose back unchanged")
	}
	if !strings.Contains(got, "panic: runtime error: index out of range") {
		t.Fatalf("expected extracted text to contain the panic line, got %q", got)
	}
	lower := strings.ToLower(got)
	if strings.Contains(lower, "customer reported") {
		t.Fatalf("expected extracted text to EXCLUDE unrelated prose (Why line), got %q", got)
	}
	if strings.Contains(lower, "fixed a crash in the checkout pipeline") {
		t.Fatalf("expected extracted text to EXCLUDE unrelated prose (What line), got %q", got)
	}
}

func TestExtractErrorText_PureProseReturnsEmpty(t *testing.T) {
	content := "**What**: Refactored the auth module to use constructor injection.\n" +
		"**Why**: Improve testability of the login flow.\n" +
		"**Where**: internal/auth\n" +
		"**Learned**: Constructor injection makes mocking trivial in unit tests."

	if got := extractErrorText(content); got != "" {
		t.Fatalf("expected empty extraction for pure prose with no error-shaped line, got %q", got)
	}
}

func TestExtractErrorText_EmptyInputReturnsEmpty(t *testing.T) {
	if got := extractErrorText(""); got != "" {
		t.Fatalf("expected empty extraction for empty input, got %q", got)
	}
}

// ─── ExtractErrorText (exported wrapper, #1399 slice 2 — forced activation) ───
//
// Slice 2's `omnia recall-fix` CLI command (cmd/omnia) and the PostToolUse
// hook need to run a fresh tool-error occurrence through the exact SAME
// error-shaped-line extraction used at save time, before handing the result
// to Store.Search's signature lane — otherwise a noisy raw stdout/stderr
// blob (unrelated log lines, prompts, etc.) would pollute the query
// signature. extractErrorText itself is unexported (an internal save-time
// helper); ExtractErrorText is a thin exported delegate so callers outside
// internal/store can reuse the identical logic instead of re-implementing
// it (and risking drift between the save-time and query-time extraction).

func TestExtractErrorText_ExportedWrapperDelegatesToInternalHelper(t *testing.T) {
	content := "**Learned**: `panic: runtime error: index out of range [7] with length 2` happening in validateCartItems()."

	want := extractErrorText(content)
	got := ExtractErrorText(content)

	if got != want {
		t.Fatalf("ExtractErrorText(%q) = %q, want it to delegate to extractErrorText and return %q", content, got, want)
	}
	if got == "" {
		t.Fatalf("expected a non-empty extraction for content containing a panic line")
	}
}

func TestExtractErrorText_ExportedWrapperEmptyForPureProse(t *testing.T) {
	if got := ExtractErrorText("**What**: Refactored the auth module.\n**Why**: Testability."); got != "" {
		t.Fatalf("expected empty extraction for pure prose with no error-shaped line, got %q", got)
	}
}

func TestExtractErrorText_RecognizesJSStackFrameLines(t *testing.T) {
	content := "**What**: Investigated a checkout crash.\n" +
		"**Learned**: TypeError: Cannot read properties of undefined (reading 'items')\n" +
		"    at processOrder (/Users/benja/dev/omnia/src/services/orderService.ts:47:19)"

	got := extractErrorText(content)
	if !strings.Contains(got, "TypeError") {
		t.Fatalf("expected extracted text to include the TypeError line, got %q", got)
	}
	if !strings.Contains(got, "processOrder") {
		t.Fatalf("expected extracted text to include the stack frame line, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "investigated a checkout crash") {
		t.Fatalf("expected extracted text to exclude the unrelated What line, got %q", got)
	}
}
