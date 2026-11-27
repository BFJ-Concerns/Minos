package shell

import (
	"context"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

// writeSummary makes the summary comment say state of the guarded head,
// above text, the presentation the run composed. It binds the two: the
// record the comment opens with is written here and nowhere else, taking
// the head and target from the guard and carrying the last finished head
// on from the record the comment holds now. A run therefore never states
// a completion marker itself; it names a state and the head it holds, and
// a moved head is refused by the guard before the comment is touched.
func writeSummary(ctx context.Context, adapter *forge.Adapter, guard forge.Guard, state product.SummaryState, text string) (forge.WriteResult, error) {
	existing, err := adapter.SummaryComment(ctx, guard.Repository, guard.PullRequest)
	if err != nil {
		return forge.WriteResult{}, err
	}
	var held product.SummaryRecord
	if existing != nil {
		// A comment whose record no longer parses is still the comment to
		// edit; it carries nothing forward.
		held, _ = product.ParseSummary(existing.Body)
	}
	body, err := product.ComposeSummary(held.Next(guard.HeadSHA, guard.TargetSHA, state), text)
	if err != nil {
		return forge.WriteResult{}, err
	}
	return adapter.WriteSummary(ctx, guard, body), nil
}
