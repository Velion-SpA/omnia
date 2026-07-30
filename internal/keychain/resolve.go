package keychain

import (
	"context"
	"errors"
	"fmt"
)

// Resolver is the subset of Client's API the keychain-or-fail decision
// depends on. Declared as an interface (not the concrete *Client type) so
// internal/store and internal/embed — which must never import each other
// (an existing architecture guardrail) — can each inject a fake for tests
// while sharing this ONE decision helper instead of duplicating it (design
// ADR-3's "keychain-or-fail" contract, spec REQ-434, tasks.md 4.7 REFACTOR).
type Resolver interface {
	GetOrCreateHexKey(ctx context.Context, service, account string) (hexKey string, created bool, err error)
}

// ErrKeyUnavailable is returned (wrapped) when the keychain cannot be
// reached and the caller has NOT opted into allowPlaintextFallback — the
// fail-closed-by-default contract (spec REQ-434): callers MUST refuse to
// open the database in this case, never silently open as plaintext or
// generate a throwaway key.
var ErrKeyUnavailable = errors.New("keychain unavailable and allow_plaintext_fallback is not enabled")

// ResolveDecision is the outcome of Resolve. Exactly one of two shapes ever
// applies: HexKey set (Degraded false) means "open encrypted with this
// key"; Degraded true means "the caller explicitly allowed a plaintext
// fallback — open unencrypted," with Cause explaining why the keychain
// could not be reached (for the caller's own warning log/audit entry).
type ResolveDecision struct {
	HexKey   string
	Degraded bool
	Cause    error
}

// Resolve implements the memory-at-rest-security capability's single
// keychain-or-fail decision (design ADR-3), shared by internal/store's and
// internal/embed's independent driver-selection code:
//
//   - key resolves normally → ResolveDecision{HexKey: ...}, nil error.
//   - resolution fails, allowPlaintextFallback=false (the default) → a nil
//     ResolveDecision and an error wrapping ErrKeyUnavailable; the caller
//     MUST refuse to open (spec REQ-434 fail-closed).
//   - resolution fails, allowPlaintextFallback=true → ResolveDecision{
//     Degraded: true, Cause: err}, nil error; the caller opens unencrypted
//     but MUST log a loud warning and append exactly one audit entry —
//     Resolve itself does neither (internal/keychain has no
//     internal/audit/log dependency), so callers own that side effect,
//     keeping the "never silent" requirement visible at the call site.
func Resolve(ctx context.Context, r Resolver, service, account string, allowPlaintextFallback bool) (ResolveDecision, error) {
	hexKey, _, err := r.GetOrCreateHexKey(ctx, service, account)
	if err == nil {
		return ResolveDecision{HexKey: hexKey}, nil
	}
	if allowPlaintextFallback {
		return ResolveDecision{Degraded: true, Cause: err}, nil
	}
	return ResolveDecision{}, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
}
