package keychain

import (
	"context"
	"errors"
	"testing"
)

type fakeResolver struct {
	hexKey string
	err    error
}

func (f *fakeResolver) GetOrCreateHexKey(ctx context.Context, service, account string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.hexKey, false, nil
}

func TestResolve_Success_ReturnsHexKeyNotDegraded(t *testing.T) {
	r := &fakeResolver{hexKey: "abc123"}
	decision, err := Resolve(context.Background(), r, "omnia", "db-key-v1", false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision.HexKey != "abc123" {
		t.Errorf("HexKey = %q, want %q", decision.HexKey, "abc123")
	}
	if decision.Degraded {
		t.Error("Degraded = true, want false")
	}
}

func TestResolve_Failure_FallbackDisabled_ReturnsErrKeyUnavailable(t *testing.T) {
	r := &fakeResolver{err: ErrUnavailable}
	_, err := Resolve(context.Background(), r, "omnia", "db-key-v1", false)
	if !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrKeyUnavailable", err)
	}
}

func TestResolve_Failure_FallbackEnabled_ReturnsDegradedDecisionNoError(t *testing.T) {
	cause := ErrUnavailable
	r := &fakeResolver{err: cause}
	decision, err := Resolve(context.Background(), r, "omnia", "db-key-v1", true)
	if err != nil {
		t.Fatalf("Resolve should not error when fallback is allowed: %v", err)
	}
	if !decision.Degraded {
		t.Error("Degraded = false, want true")
	}
	if !errors.Is(decision.Cause, cause) {
		t.Errorf("Cause = %v, want wrapping %v", decision.Cause, cause)
	}
	if decision.HexKey != "" {
		t.Errorf("HexKey = %q, want empty on degraded decision", decision.HexKey)
	}
}
