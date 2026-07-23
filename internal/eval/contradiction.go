package eval

import (
	"fmt"

	"github.com/velion/omnia/internal/store"
)

// RelationsGetter is the minimal store capability contradiction.go needs:
// batch-load relations for a set of observation sync_ids (see
// internal/store/relations.go's GetRelationsForObservations). *store.Store
// satisfies this directly, so callers pass a real store; tests do the same
// against a self-contained temp-directory store rather than a fake, per the
// spec EVAL-4 requirement that the contradiction fixture be backed by a
// REAL store.RelationSupersedes relation, not a mocked one.
type RelationsGetter interface {
	GetRelationsForObservations(syncIDs []string) (map[string]store.ObservationRelations, error)
}

// ScoreContradiction scores one adversarial contradiction EvalCase (spec
// EVAL-4). c.ObservationID names the CURRENT (superseding) observation and
// c.SupersedesOf names the OLDER observation it replaces. ScoreContradiction
// fails fast — rather than silently scoring the case as plain Recall — when
// either:
//   - c.SupersedesOf is unset (this is not a contradiction case), or
//   - no judged store.RelationSupersedes relation actually links
//     c.ObservationID (as source) to *c.SupersedesOf (as target) in g.
//
// A hit is scored only when surfacedObservationID equals the CURRENT
// observation (c.ObservationID). Surfacing the older, superseded
// observation (*c.SupersedesOf) is a miss — matching spec EVAL-4's
// "superseded fact scores as a miss; only the newer one scores a hit".
//
// Direction note: this follows the SAME source/target convention already
// established by internal/store and internal/mcp (see
// internal/mcp/mcp_test.go TestHandleSearch_SupersededAnnotation and
// mcp.go's "supersedes:"/"superseded_by:" annotation labels): the relation
// SOURCE is the current, superseding observation and the relation TARGET is
// the stale one being superseded.
func ScoreContradiction(g RelationsGetter, c EvalCase, surfacedObservationID string) (hit bool, err error) {
	if c.SupersedesOf == nil || *c.SupersedesOf == "" {
		return false, fmt.Errorf("eval: ScoreContradiction: case %q has no SupersedesOf — not a contradiction case (spec EVAL-4)", c.ID)
	}

	relations, err := g.GetRelationsForObservations([]string{c.ObservationID})
	if err != nil {
		return false, fmt.Errorf("eval: ScoreContradiction: case %q: get relations: %w", c.ID, err)
	}

	found := false
	for _, rel := range relations[c.ObservationID].AsSource {
		if rel.Relation == store.RelationSupersedes && rel.TargetID == *c.SupersedesOf {
			found = true
			break
		}
	}
	if !found {
		return false, fmt.Errorf("eval: ScoreContradiction: case %q: no store.RelationSupersedes relation from observation %q to %q — fails fast rather than silently scoring as plain Recall (spec EVAL-4)", c.ID, c.ObservationID, *c.SupersedesOf)
	}

	return surfacedObservationID == c.ObservationID, nil
}
