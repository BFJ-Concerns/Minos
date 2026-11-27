package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
	"github.com/BFJ-Concerns/Minos/internal/product"
)

// A comment command is a pull-request conversation comment whose first
// non-empty line is the service's login as a mention followed by one of the
// commands below and nothing else, written by a user the forge shows with
// write access to the repository. Case and spacing do not matter; on GitHub
// the login may be written without its "[bot]" suffix.
//
//	@minos review        review the current head, never with no review
//	@minos full review   review the whole pull request
//	@minos skip          start no review until a later review command
//
// Commands are read from the forge on every pass, so a webhook that never
// arrived only delays one to the next sweep. A command is acknowledged with
// the commandAcknowledgement reaction on its comment until a run that
// started after it has finished, whether or not a newer command superseded
// it.
type commentCommand string

const (
	commandReview     commentCommand = "review"
	commandFullReview commentCommand = "full review"
	commandSkip       commentCommand = "skip"
)

const commandAcknowledgement = "eyes"

// skippedByCommandReason is the deferral reason for a pull request a skip
// command holds.
const skippedByCommandReason = "skipped by comment command"

func parseCommentCommand(body, botLogin string) (commentCommand, bool) {
	var line string
	for candidate := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(candidate); line != "" {
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	mention, addressed := strings.CutPrefix(fields[0], "@")
	if !addressed || !(strings.EqualFold(mention, botLogin) || strings.EqualFold(mention, strings.TrimSuffix(botLogin, "[bot]"))) {
		return "", false
	}
	switch command := commentCommand(strings.ToLower(strings.Join(fields[1:], " "))); command {
	case commandReview, commandFullReview, commandSkip:
		return command, true
	}
	return "", false
}

// issuedCommand is one command comment from a user who may command.
type issuedCommand struct {
	command commentCommand
	comment forge.PullRequestComment
	at      time.Time
}

// readIssuedCommands reads the pull request's command comments, oldest
// first, keeping those whose writer the forge shows with write access. Each
// writer's access is read once per pass.
func readIssuedCommands(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, pullRequest int64, botLogin string) ([]issuedCommand, error) {
	comments, err := adapter.PullRequestComments(ctx, repository, pullRequest)
	if err != nil {
		return nil, err
	}
	mayCommand := make(map[string]bool)
	var commands []issuedCommand
	for _, comment := range comments {
		if strings.EqualFold(comment.User, botLogin) {
			continue
		}
		command, ok := parseCommentCommand(comment.Body, botLogin)
		if !ok {
			continue
		}
		if comment.ID <= 0 {
			return nil, fmt.Errorf("command comment by %s carries no id", comment.User)
		}
		at, err := time.Parse(time.RFC3339, comment.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("command comment %d time %q: %w", comment.ID, comment.CreatedAt, err)
		}
		may, known := mayCommand[comment.User]
		if !known {
			if may, err = adapter.MayCommand(ctx, repository, comment.User); err != nil {
				return nil, fmt.Errorf("read %s's access: %w", comment.User, err)
			}
			mayCommand[comment.User] = may
		}
		if may {
			commands = append(commands, issuedCommand{command: command, comment: comment, at: at})
		}
	}
	slices.SortStableFunc(commands, func(a, b issuedCommand) int { return a.at.Compare(b.at) })
	return commands, nil
}

// commandEffect is what the commands in force ask of admission and of the
// run it starts.
type commandEffect struct {
	// skipped holds the pull request: a skip command is newer than every
	// review command.
	skipped bool
	// reviewRequested spends the current head's completion marker: a review
	// command newer than the last skip has not yet been answered.
	reviewRequested bool
	// fullOnly closes every push-inspection outcome but a full review: a
	// full review was asked for, or the head already carries a result, over
	// which an increment would be empty.
	fullOnly bool
	// acknowledge are the command comments no run has overtaken, those a
	// newer command superseded included: no run that left a result
	// started after them.
	acknowledge []forge.PullRequestComment
}

// headStatuses is one head's commit-status history.
type headStatuses struct {
	head     string
	statuses []forge.Status
}

// commandsInForce works out the commands' effect from current forge state.
// A review command is answered once a run that started after it has left a
// terminal Minos result: a request-changes or approving review on any head,
// or a clean or attention status on the current head or on the earlier
// head lastFinished names (its statuses read only where the pull request
// carries a command). On a head whose statuses are read, a result's run is taken to
// have started at the newest working status written before the result, so
// a run already under way when the command was written does not answer it;
// elsewhere, and where no working status was written, the result's own
// time stands in.
func commandsInForce(commands []issuedCommand, snapshot forge.Snapshot, lastFinished func() (headStatuses, error), botLogin, statusContext string) (commandEffect, error) {
	var effect commandEffect
	if len(commands) == 0 {
		return effect, nil
	}
	heads := []headStatuses{{head: snapshot.HeadSHA, statuses: snapshot.Statuses}}
	earlier, err := lastFinished()
	if err != nil {
		return commandEffect{}, fmt.Errorf("read the last finished head's statuses: %w", err)
	}
	if earlier.head != "" && earlier.head != snapshot.HeadSHA {
		heads = append(heads, earlier)
	}
	starts, headReviewed, err := answeringRunStarts(heads, snapshot.Reviews, botLogin, statusContext)
	if err != nil {
		return commandEffect{}, err
	}
	skipAt := -1
	for index, command := range commands {
		answered := slices.ContainsFunc(starts, command.at.Before)
		if !answered {
			effect.acknowledge = append(effect.acknowledge, command.comment)
		}
		switch {
		case command.command == commandSkip:
			skipAt = index
			effect.reviewRequested, effect.fullOnly = false, false
		case !answered:
			effect.reviewRequested = true
			effect.fullOnly = effect.fullOnly || command.command == commandFullReview || headReviewed
		}
	}
	effect.skipped = skipAt == len(commands)-1
	return effect, nil
}

// answeringRunStarts is when each run that left a terminal Minos result
// started, as commandsInForce reads it, and whether the first of heads, the
// current head, carries such a result.
func answeringRunStarts(heads []headStatuses, reviews []forge.Review, botLogin, statusContext string) ([]time.Time, bool, error) {
	working := make(map[string][]time.Time)
	var results []terminalResult
	for _, head := range heads {
		for _, status := range head.statuses {
			if status.Context != statusContext || status.Creator != botLogin {
				continue
			}
			state, recognised := product.StateForDescription(status.Description)
			_, terminal := product.CompletionMarker(string(status.State), status.Description)
			if !terminal && !(recognised && state == product.Working()) {
				continue
			}
			at, err := time.Parse(time.RFC3339, status.CreatedAt)
			if err != nil {
				return nil, false, fmt.Errorf("status %d time %q: %w", status.ID, status.CreatedAt, err)
			}
			if terminal {
				results = append(results, terminalResult{at: at, head: head.head})
			} else {
				working[head.head] = append(working[head.head], at)
			}
		}
	}
	for _, review := range reviews {
		if review.User != botLogin {
			continue
		}
		if _, terminal := terminalState(review); !terminal {
			continue
		}
		at, err := time.Parse(time.RFC3339, review.SubmittedAt)
		if err != nil {
			return nil, false, fmt.Errorf("review %d time %q: %w", review.ID, review.SubmittedAt, err)
		}
		results = append(results, terminalResult{at: at, head: review.CommitID})
	}
	starts := make([]time.Time, 0, len(results))
	headReviewed := false
	for _, result := range results {
		headReviewed = headReviewed || result.head == heads[0].head
		start := result.at
		if began, found := newestAtOrBefore(working[result.head], result.at); found {
			start = began
		}
		starts = append(starts, start)
	}
	return starts, headReviewed, nil
}

// terminalResult is when a terminal Minos result was written and on which
// head.
type terminalResult struct {
	at   time.Time
	head string
}

func newestAtOrBefore(times []time.Time, limit time.Time) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, at := range times {
		if !at.After(limit) && (!found || at.After(newest)) {
			newest, found = at, true
		}
	}
	return newest, found
}

// lastFinishedHeadStatuses reads the status history of the head the
// service's reviewed-change record says a run last finished on. Without the
// record there is no earlier head to read, and a command answered there is
// taken as waiting until the current head's next result answers it.
func lastFinishedHeadStatuses(ctx context.Context, cfg ServiceConfig, forgeName string, adapter *forge.Adapter, guard forge.Guard) (headStatuses, error) {
	raw, err := os.ReadFile(reviewedChangePath(cfg, forgeName, guard))
	if os.IsNotExist(err) {
		return headStatuses{}, nil
	}
	if err != nil {
		return headStatuses{}, err
	}
	var record reviewedChange
	if json.Unmarshal(raw, &record) != nil || record.Head == "" || record.Head == guard.HeadSHA {
		return headStatuses{}, nil
	}
	statuses, err := adapter.CommitStatuses(ctx, guard.Repository, record.Head)
	if err != nil {
		return headStatuses{}, err
	}
	return headStatuses{head: record.Head, statuses: statuses}, nil
}

// acknowledgeCommands reacts on each command comment not yet overtaken. The
// acknowledgement is feedback to the commenter and nothing reads it back,
// so a failed write is logged and admission goes on.
func acknowledgeCommands(ctx context.Context, adapter *forge.Adapter, guard forge.Guard, comments []forge.PullRequestComment) {
	for _, comment := range comments {
		if result := adapter.AcknowledgeComment(ctx, guard, comment.ID, commandAcknowledgement); result.Outcome != forge.WriteApplied {
			log.Printf("acknowledge command comment %d on %s/%s#%d: %s: %s",
				comment.ID, guard.Repository.Owner, guard.Repository.Name, guard.PullRequest, result.Outcome, result.Reason)
		}
	}
}

// commandedOutcomes narrows the repository's push-inspection outcomes for a
// run a review command asked for: no-review is closed, and every outcome
// but full where the effect says so.
func commandedOutcomes(configured []string, effect commandEffect) []string {
	if effect.fullOnly {
		return []string{"full"}
	}
	return slices.DeleteFunc(slices.Clone(configured), func(outcome string) bool { return outcome == "no-review" })
}
