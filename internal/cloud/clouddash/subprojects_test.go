package clouddash

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeProjectLinksStore is a minimal cloudstore.CloudStore double exposing
// only the Slice 5a capability under test here (ListProjectParents) — mirrors
// fakeCloudStore's role in source_test.go but scoped to this one seam.
type fakeProjectLinksStore struct {
	parents map[string]string
	err     error
}

func (f fakeProjectLinksStore) ListProjectParents(context.Context) (map[string]string, error) {
	return f.parents, f.err
}

func TestNewSubProjectResolver_ChildrenOf_ReturnsSortedDirectChildren(t *testing.T) {
	store := fakeProjectLinksStore{parents: map[string]string{
		"workly-marketing": "workly",
		"workly-videos":    "workly",
		"velion-web":       "velion",
	}}
	resolver := NewSubProjectResolver(store)
	if resolver == nil {
		t.Fatal("expected a non-nil resolver for a store implementing CloudProjectLinks")
	}

	got := resolver.ChildrenOf(context.Background(), "workly")
	want := []string{"workly-marketing", "workly-videos"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChildrenOf(workly) = %v, want %v", got, want)
	}
}

func TestNewSubProjectResolver_ChildrenOf_EmptyForNonParent(t *testing.T) {
	store := fakeProjectLinksStore{parents: map[string]string{"workly-marketing": "workly"}}
	resolver := NewSubProjectResolver(store)

	if got := resolver.ChildrenOf(context.Background(), "workly-marketing"); len(got) != 0 {
		t.Fatalf("expected no children for a leaf/child project, got %v", got)
	}
}

func TestNewSubProjectResolver_ParentOf(t *testing.T) {
	store := fakeProjectLinksStore{parents: map[string]string{"workly-marketing": "workly"}}
	resolver := NewSubProjectResolver(store)

	if got := resolver.ParentOf(context.Background(), "workly-marketing"); got != "workly" {
		t.Fatalf("ParentOf(workly-marketing) = %q, want workly", got)
	}
	if got := resolver.ParentOf(context.Background(), "workly"); got != "" {
		t.Fatalf("ParentOf(workly) = %q, want empty (workly is unlinked itself)", got)
	}
}

func TestNewSubProjectResolver_StoreErrorDegradesToEmpty(t *testing.T) {
	store := fakeProjectLinksStore{err: errors.New("boom")}
	resolver := NewSubProjectResolver(store)

	if got := resolver.ChildrenOf(context.Background(), "workly"); len(got) != 0 {
		t.Fatalf("expected empty children on store error, got %v", got)
	}
	if got := resolver.ParentOf(context.Background(), "workly"); got != "" {
		t.Fatalf("expected empty parent on store error, got %q", got)
	}
}
