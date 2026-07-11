package shell

import (
	"sort"
	"strconv"
	"strings"
)

type sweepAction struct {
	Run        RunKind
	ApplyReady bool
}

type sweepSnapshot struct {
	facts          Facts
	statuses       []Status
	reviews        []Review
	combinedStatus string
}

type sweepPriority int

const (
	sweepDrain sweepPriority = iota
	sweepWiden
)

type sweepCandidate struct {
	snapshot sweepSnapshot
	priority sweepPriority
}

// decideSweepAction derives only from forge-owned machine state. Review state,
// head identity, labels, and statuses are control facts; review prose remains
// deliberately outside this path.
func decideSweepAction(repo RepoConfig, facts Facts, statuses []Status, reviews []Review, combinedStatus, readyActor string) (sweepAction, bool) {
	if facts.HasLabel(LabelFlakyTests) {
		if decision, ok := reconcileDecision(repo, facts, statuses, readyActor); ok && decision == RunFlaky {
			return sweepAction{Run: decision}, true
		}
	}

	if review, ok := currentHeadBotReview(reviews, facts.HeadSHA); ok {
		switch strings.ToUpper(review.State) {
		case "REQUEST_CHANGES":
			fixContext, _ := StatusContext(RunFix)
			if _, exists := statusForContext(statuses, fixContext); !exists && !facts.HasLabel(LabelStandingFindings) && guardsPass(repo, RunFix, facts, review.User) {
				return sweepAction{Run: RunFix}, true
			}
		case "APPROVED":
			if repo.Policy.AutoMerge && !facts.HasLabel(LabelReady) && !facts.HasLabel(LabelFlakyTests) && combinedHeadIsGreenOrAbsent(combinedStatus) {
				return sweepAction{ApplyReady: true}, true
			}
		}
	}

	if decision, ok := reconcileDecision(repo, facts, statuses, readyActor); ok {
		return sweepAction{Run: decision}, true
	}
	return sweepAction{}, false
}

func currentHeadBotReview(reviews []Review, headSHA string) (Review, bool) {
	var newest Review
	found := false
	for _, review := range reviews {
		if review.CommitID != headSHA || review.User != serviceBotLogin {
			continue
		}
		if !found || review.ID > newest.ID {
			newest = review
			found = true
		}
	}
	return newest, found
}

func combinedHeadIsGreenOrAbsent(state string) bool {
	return state == "" || strings.EqualFold(state, "success")
}

func classifySweepPriority(snapshot sweepSnapshot) sweepPriority {
	for _, label := range []string{
		LabelReviewing, LabelFixing, LabelFinishing, LabelRepairingFlaky,
		LabelStandingFindings, LabelConverged, LabelPartialCoverage, LabelReady, LabelFlakyTests,
	} {
		if snapshot.facts.HasLabel(label) {
			return sweepDrain
		}
	}
	for _, status := range snapshot.statuses {
		if strings.HasPrefix(status.Context, "pump19/") {
			return sweepDrain
		}
	}
	if _, ok := currentHeadBotReview(snapshot.reviews, snapshot.facts.HeadSHA); ok {
		return sweepDrain
	}
	return sweepWiden
}

func rankSweepCandidates(candidates []sweepCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		leftPR := candidates[i].snapshot.facts.PR
		rightPR := candidates[j].snapshot.facts.PR
		left, leftErr := strconv.ParseUint(leftPR, 10, 64)
		right, rightErr := strconv.ParseUint(rightPR, 10, 64)
		if leftErr == nil && rightErr == nil && left != right {
			return left < right
		}
		return leftPR < rightPR
	})
}
