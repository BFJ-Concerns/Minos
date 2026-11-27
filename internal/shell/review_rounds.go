package shell

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

func requestChangesRoundAdvice(ctx context.Context, adapter *forge.Adapter, guard forge.Guard) string {
	threshold := defaultRequestChangesRounds
	if configured, err := strconv.Atoi(os.Getenv("MINOS_REQUEST_CHANGES_ROUNDS")); err == nil && configured > 0 {
		threshold = configured
	}
	snapshot, err := adapter.SnapshotWithoutStatuses(ctx, guard.Repository, guard.PullRequest)
	if err != nil {
		log.Printf("forge review: count request-changes rounds: %v", err)
		return ""
	}
	round := requestChangesRound(snapshot, guard.HeadSHA)
	if round < threshold {
		return ""
	}
	if round == 1 {
		return "\n\nThis is the first round of requested changes on this pull request. Consider stepping back and reconsidering the approach before making further changes."
	}
	return fmt.Sprintf("\n\nThis is round %d of requested changes on this pull request. Consider stepping back and reconsidering the approach before making further changes.", round)
}

// One round is one distinct head with a submitted request for changes from
// the review account. The first review of a head fixes that head's position
// in the sequence, so a retry or a return to an earlier head keeps its number.
func requestChangesRound(snapshot forge.Snapshot, head string) int {
	firstByHead := make(map[string]int64)
	for _, review := range snapshot.Reviews {
		state, terminal := terminalState(review)
		if !terminal || state != product.Attention() || review.User != snapshot.AuthenticatedUser || review.CommitID == "" {
			continue
		}
		first, seen := firstByHead[review.CommitID]
		if !seen || review.ID < first {
			firstByHead[review.CommitID] = review.ID
		}
	}
	current, reviewed := firstByHead[head]
	round := 1
	for earlierHead, first := range firstByHead {
		if earlierHead != head && (!reviewed || first < current) {
			round++
		}
	}
	return round
}
