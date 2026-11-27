package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// arrived only delays one to the next sweep. Every command, a superseded
// one included, is acknowledged with the commandAcknowledgement reaction on
// its comment.
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
	// waiting are the review commands not yet answered since the last skip;
	// a run started now is started for them.
	waiting []forge.PullRequestComment
	// unacknowledged are the command comments the record does not show
	// acknowledged, superseded ones included.
	unacknowledged []forge.PullRequestComment
	// results names every terminal Minos result now on the forge, as the
	// record keeps them.
	results []string
}

// headStatuses is one head's commit-status history.
type headStatuses struct {
	head     string
	statuses []forge.Status
}

// commandsInForce works out the commands' effect from current forge state
// and the service's command record. A review command is answered once a
// terminal Minos result appears that was not on the forge when the service
// first started a run for it: a request-changes or approving review on any
// head, or a clean or attention status on the current head or on the
// earlier head lastFinished names. While a command waits, every run the
// service starts is started for it, so a result absent at that first start
// is the answer of a run that honoured it; a run already started when the
// command was written, which fixed its scope then, never answers it.
func commandsInForce(commands []issuedCommand, snapshot forge.Snapshot, lastFinished func() (headStatuses, error), record commandRecord, botLogin, statusContext string) (commandEffect, error) {
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
	results, headReviewed := terminalResults(heads, snapshot.Reviews, botLogin, statusContext)
	effect.results = results
	skipAt := -1
	for index, command := range commands {
		if !slices.Contains(record.Acknowledged, command.comment.ID) {
			effect.unacknowledged = append(effect.unacknowledged, command.comment)
		}
		if command.command == commandSkip {
			skipAt = index
			effect.waiting, effect.fullOnly = nil, false
			continue
		}
		known, started := record.Started[command.comment.ID]
		answered := started && slices.ContainsFunc(results, func(result string) bool { return !slices.Contains(known, result) })
		if !answered {
			effect.waiting = append(effect.waiting, command.comment)
			effect.fullOnly = effect.fullOnly || command.command == commandFullReview || headReviewed
		}
	}
	effect.skipped = skipAt == len(commands)-1
	effect.reviewRequested = len(effect.waiting) > 0
	return effect, nil
}

// terminalResults names each terminal Minos result: a clean or attention
// status on one of heads, or an approving or request-changes review on any
// head. It also reports whether the first of heads, the current head,
// carries one.
func terminalResults(heads []headStatuses, reviews []forge.Review, botLogin, statusContext string) ([]string, bool) {
	var results []string
	headReviewed := false
	for index, head := range heads {
		for _, status := range head.statuses {
			if status.Context != statusContext || status.Creator != botLogin {
				continue
			}
			if _, terminal := product.CompletionMarker(string(status.State), status.Description); terminal {
				results = append(results, "status:"+strconv.FormatInt(status.ID, 10))
				headReviewed = headReviewed || index == 0
			}
		}
	}
	for _, review := range reviews {
		if review.User != botLogin {
			continue
		}
		if _, terminal := terminalState(review); terminal {
			results = append(results, "review:"+strconv.FormatInt(review.ID, 10))
			headReviewed = headReviewed || review.CommitID == heads[0].head
		}
	}
	return results, headReviewed
}

// commandRecordPath is where the service keeps its command record for one
// pull request. The record is a cache beside the runs: losing it costs one
// more review of each command already answered and a repeated reaction
// check, and nothing else.
func commandRecordPath(cfg ServiceConfig, forgeName string, guard forge.Guard) string {
	name := reviewedChangePrefix(forgeName, guard.Repository) + strconv.FormatInt(guard.PullRequest, 10) + ".json"
	return filepath.Join(cfg.Runs.Dir, ".commands", name)
}

// commandRecord is what the service keeps of a pull request's commands.
type commandRecord struct {
	// Started maps each review command a run was started for to the
	// terminal results on the forge when the first such run started.
	Started map[int64][]string `json:"started"`
	// Acknowledged are the command comments whose reaction was written.
	Acknowledged []int64 `json:"acknowledged"`
}

// readCommandRecord reads the record. An unreadable record reads as empty,
// as a lost one does.
func readCommandRecord(cfg ServiceConfig, forgeName string, guard forge.Guard) commandRecord {
	path := commandRecordPath(cfg, forgeName, guard)
	record := commandRecord{Started: make(map[int64][]string)}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("read command record: %v", err)
		}
		return record
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		log.Printf("decode command record %s: %v", path, err)
		return commandRecord{Started: make(map[int64][]string)}
	}
	if record.Started == nil {
		record.Started = make(map[int64][]string)
	}
	return record
}

// writeCommandRecord replaces the record. A failed write is logged: the
// record is a cache.
func writeCommandRecord(cfg ServiceConfig, forgeName string, guard forge.Guard, record commandRecord) {
	err := func() error {
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		path := commandRecordPath(cfg, forgeName, guard)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		staging := path + ".next"
		if err := os.WriteFile(staging, append(encoded, '\n'), 0o600); err != nil {
			return err
		}
		return os.Rename(staging, path)
	}()
	if err != nil {
		log.Printf("write command record: %v", err)
	}
}

func removeCommandRecord(cfg ServiceConfig, forgeName string, guard forge.Guard) error {
	if err := os.Remove(commandRecordPath(cfg, forgeName, guard)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove command record: %w", err)
	}
	return nil
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

// acknowledgeCommands reacts on each comment and returns the ids whose
// reaction was written. The acknowledgement is feedback to the commenter,
// so a failed write is logged, admission goes on, and the next pass tries
// again.
func acknowledgeCommands(ctx context.Context, adapter *forge.Adapter, guard forge.Guard, comments []forge.PullRequestComment) []int64 {
	var acknowledged []int64
	for _, comment := range comments {
		result := adapter.AcknowledgeComment(ctx, guard, comment.ID, commandAcknowledgement)
		if result.Outcome == forge.WriteApplied {
			acknowledged = append(acknowledged, comment.ID)
			continue
		}
		log.Printf("acknowledge command comment %d on %s/%s#%d: %s: %s",
			comment.ID, guard.Repository.Owner, guard.Repository.Name, guard.PullRequest, result.Outcome, result.Reason)
	}
	return acknowledged
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
