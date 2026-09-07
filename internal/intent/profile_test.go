package intent

import "testing"

// TestProfileFor_UnknownIsZeroValue pins the safety property from
// profile.go's doc comment: Unknown (and Delta, whose plan-documented
// routing is "no override — existing lane already correct") must map to
// the zero-value RoutingProfile, so a caller applying an unrecognized or
// deliberately-unrouted query's profile changes nothing.
func TestProfileFor_UnknownIsZeroValue(t *testing.T) {
	for _, i := range []Intent{Unknown, Delta, Intent("bogus")} {
		got := ProfileFor(i)
		want := RoutingProfile{}
		if got != want {
			t.Errorf("ProfileFor(%q) = %+v; want zero-value RoutingProfile %+v", i, got, want)
		}
	}
}

// TestProfileFor_Identity pins the identity routing row: recency weight 0,
// and NO type lens.
//
// The plan's original routing table said "type lens: doc" here, and
// measurement overruled it: forcing that lens cuts identity grounding from
// 0.625 to 0.375 over the conversational corpus, because ApplyTypeLens
// partitions rather than boosts and doc chunks are numerous after repodoc
// ingestion. See ProfileFor's own comment for the full reasoning — this
// assertion exists so the "doc" lens cannot be restored from the plan text
// without re-measuring first.
func TestProfileFor_Identity(t *testing.T) {
	p := ProfileFor(Identity)
	if p.TypeLens != "" {
		t.Errorf("ProfileFor(Identity).TypeLens = %q; want %q (no lens — it measurably hurts grounding)", p.TypeLens, "")
	}
	if p.RecencyWeight == nil {
		t.Fatal("ProfileFor(Identity).RecencyWeight is nil; want an explicit override to 0")
	}
	if *p.RecencyWeight != 0 {
		t.Errorf("ProfileFor(Identity).RecencyWeight = %v; want 0", *p.RecencyWeight)
	}
	if p.RecencyHalfLifeDays != nil {
		t.Errorf("ProfileFor(Identity).RecencyHalfLifeDays = %v; want nil (only the weight is overridden)", *p.RecencyHalfLifeDays)
	}
	if p.Unverified || p.MultiProjectHint {
		t.Errorf("ProfileFor(Identity) unexpectedly sets Unverified=%v MultiProjectHint=%v", p.Unverified, p.MultiProjectHint)
	}
}

// TestProfileFor_Status pins the plan's routing-table row: "status ->
// ranking weight recency high (half-life ~3 days)".
func TestProfileFor_Status(t *testing.T) {
	p := ProfileFor(Status)
	if p.RecencyWeight == nil || *p.RecencyWeight <= 1 {
		t.Errorf("ProfileFor(Status).RecencyWeight = %v; want a non-nil override greater than the default 1.0 weight", p.RecencyWeight)
	}
	if p.RecencyHalfLifeDays == nil {
		t.Fatal("ProfileFor(Status).RecencyHalfLifeDays is nil; want a short half-life override")
	}
	if *p.RecencyHalfLifeDays <= 0 || *p.RecencyHalfLifeDays > 7 {
		t.Errorf("ProfileFor(Status).RecencyHalfLifeDays = %v; want roughly ~3 days per the plan, in any case well under the 14-day config default", *p.RecencyHalfLifeDays)
	}
	if p.TypeLens != "" {
		t.Errorf("ProfileFor(Status).TypeLens = %q; want no type lens hint", p.TypeLens)
	}
}

// TestProfileFor_OpenItems pins the plan's routing-table row: "open_items
// -> claim lane (P4); until P4 ships, flag the answer as unverified."
func TestProfileFor_OpenItems(t *testing.T) {
	p := ProfileFor(OpenItems)
	if !p.Unverified {
		t.Error("ProfileFor(OpenItems).Unverified = false; want true (P4's claim lane does not exist yet)")
	}
	if p.RecencyWeight != nil || p.RecencyHalfLifeDays != nil || p.TypeLens != "" {
		t.Errorf("ProfileFor(OpenItems) sets a ranking override (%+v); want none until P4 ships", p)
	}
}

// TestProfileFor_Rationale pins the plan's routing-table row: "rationale ->
// type lens: decision/architecture".
func TestProfileFor_Rationale(t *testing.T) {
	p := ProfileFor(Rationale)
	if p.TypeLens != "decision" && p.TypeLens != "architecture" {
		t.Errorf("ProfileFor(Rationale).TypeLens = %q; want %q or %q", p.TypeLens, "decision", "architecture")
	}
}

// TestProfileFor_CrossProject pins the plan's routing-table row:
// "cross_project -> multi-project mode (P5)".
func TestProfileFor_CrossProject(t *testing.T) {
	p := ProfileFor(CrossProject)
	if !p.MultiProjectHint {
		t.Error("ProfileFor(CrossProject).MultiProjectHint = false; want true (P5's multi-project mode does not exist yet)")
	}
}

// TestProfileFor_AllSevenIntentsHaveNotes documents that every real
// (non-Unknown) intent's profile choice is explained, since Notes is the
// eval/debug trail a reviewer follows back to the plan's routing table.
// Delta is exempt: its profile IS the zero value (see the map's own doc
// comment), which is itself the documented decision ("no new routing
// needed" — the explanation lives in code comments, not a runtime Notes
// string nobody reads for a no-op).
func TestProfileFor_AllSevenIntentsHaveNotes(t *testing.T) {
	for _, i := range []Intent{Identity, Status, OpenItems, Rationale, CrossProject} {
		if ProfileFor(i).Notes == "" {
			t.Errorf("ProfileFor(%q).Notes is empty; every real intent's profile should explain itself", i)
		}
	}
}
