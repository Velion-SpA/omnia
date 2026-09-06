package store

import "testing"

// TestListSyncStatesForAlias_IncludesPerProjectRows locks the fix for the
// defect where a successful manual sync never cleared a degraded status.
//
// `omnia sync --cloud --project X` writes the PER-PROJECT key
// ("personal:omnia-test"); only whole-cloud operations touch the bare alias
// key ("personal"). `omnia cloud status` used to read the bare key alone, so
// no amount of successful per-project syncing could ever change what it
// printed. Enumerating the alias is what makes the two surfaces able to agree.
func TestListSyncStatesForAlias_IncludesPerProjectRows(t *testing.T) {
	s := newTestStore(t)

	for _, key := range []string{"personal", "personal:omnia-test", "personal:vel-voice"} {
		if err := s.MarkSyncHealthy(key); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	// A different cloud must not leak in, and must not be matched merely
	// because its name starts with the alias we asked for.
	for _, key := range []string{"work", "personalx", "personalx:other"} {
		if err := s.MarkSyncHealthy(key); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	states, err := s.ListSyncStatesForAlias("personal")
	if err != nil {
		t.Fatalf("ListSyncStatesForAlias: %v", err)
	}

	got := make(map[string]bool, len(states))
	for _, st := range states {
		got[st.TargetKey] = true
	}
	for _, want := range []string{"personal", "personal:omnia-test", "personal:vel-voice"} {
		if !got[want] {
			t.Errorf("missing target_key %q; got %v", want, got)
		}
	}
	for _, unwanted := range []string{"work", "personalx", "personalx:other"} {
		if got[unwanted] {
			t.Errorf("target_key %q leaked into alias listing; got %v", unwanted, got)
		}
	}
	if len(states) != 3 {
		t.Errorf("len(states) = %d, want 3; got %v", len(states), got)
	}
}

// TestListSyncStatesForAlias_EscapesLikeWildcards guards the LIKE prefix
// match. An alias containing % or _ would otherwise be a wildcard and pull in
// every unrelated cloud's rows — turning the status surface into a liar in the
// opposite direction from the bug it was fixing.
func TestListSyncStatesForAlias_EscapesLikeWildcards(t *testing.T) {
	s := newTestStore(t)

	for _, key := range []string{"%", "%:trap", "unrelated", "unrelated:proj"} {
		if err := s.MarkSyncHealthy(key); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	states, err := s.ListSyncStatesForAlias("%")
	if err != nil {
		t.Fatalf("ListSyncStatesForAlias: %v", err)
	}
	for _, st := range states {
		if st.TargetKey != "%" && st.TargetKey != "%:trap" {
			t.Errorf("wildcard alias matched unrelated key %q", st.TargetKey)
		}
	}
	if len(states) != 2 {
		t.Errorf("len(states) = %d, want 2 (the literal %%-prefixed rows only)", len(states))
	}
}

// TestListSyncStatesForAlias_BlankAliasUsesDefaultTarget locks that a blank
// alias follows the same normalization contract as GetSyncState — it means the
// default cloud target, NOT "enumerate every cloud in the store".
func TestListSyncStatesForAlias_BlankAliasUsesDefaultTarget(t *testing.T) {
	s := newTestStore(t)
	for _, key := range []string{DefaultSyncTargetKey, DefaultSyncTargetKey + ":proj", "personal"} {
		if err := s.MarkSyncHealthy(key); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	states, err := s.ListSyncStatesForAlias("   ")
	if err != nil {
		t.Fatalf("ListSyncStatesForAlias: %v", err)
	}
	for _, st := range states {
		if st.TargetKey != DefaultSyncTargetKey && st.TargetKey != DefaultSyncTargetKey+":proj" {
			t.Errorf("blank alias matched %q, want only the default target and its projects", st.TargetKey)
		}
	}
	if len(states) != 2 {
		t.Errorf("len(states) = %d, want 2", len(states))
	}
}
