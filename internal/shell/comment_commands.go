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

// readIssuedCommands finds the command comments among the pull request's
// conversation comments, oldest first, keeping those whose writer the forge
// shows with write access. The comments come from the snapshot, which lists
// them once for the summary comment too; each writer's access is read once
// per pass.
func readIssuedCommands(ctx context.Context, adapter *forge.Adapter, repository forge.Repository, comments []forge.PullRequestComment, botLogin string) ([]issuedCommand, error) {
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
	// answered are the review commands this pass found answered, for the
	// record to keep.
	answered []int64
	// unacknowledged are the command comments the record does not show
	// acknowledged, superseded ones included.
	unacknowledged []forge.PullRequestComment
}

// terminalResult is one terminal Minos result on the forge: Token names
// that one publication, and Head is the head it judged.
type terminalResult struct {
	Token string
	Head  string
}

// pullRequestResults names the terminal Minos results a snapshot shows: the
// summary comment when its record is finished, every approving or
// request-changes review, and each clean or attention status on the current
// head. The summary comment is one comment edited in place, so its token
// carries the time of the edit as well as the head and state it records.
func pullRequestResults(snapshot forge.Snapshot, botLogin, statusContext string) []terminalResult {
	var results []terminalResult
	if record, found := snapshot.SummaryRecord(); found && record.State.Finished() {
		results = append(results, terminalResult{
			Token: "summary:" + record.Head + ":" + string(record.State) + ":" + snapshot.Summary.UpdatedAt,
			Head:  record.Head,
		})
	}
	for _, review := range snapshot.Reviews {
		if review.User != botLogin {
			continue
		}
		if _, terminal := terminalState(review); terminal {
			results = append(results, terminalResult{Token: "review:" + strconv.FormatInt(review.ID, 10), Head: review.CommitID})
		}
	}
	return append(results, statusResults(snapshot.HeadSHA, snapshot.Statuses, botLogin, statusContext)...)
}

// statusResults names the clean and attention Minos statuses among one
// head's statuses.
func statusResults(head string, statuses []forge.Status, botLogin, statusContext string) []terminalResult {
	var results []terminalResult
	for _, status := range statuses {
		if status.Context != statusContext || status.Creator != botLogin {
			continue
		}
		if _, terminal := product.CompletionMarker(string(status.State), status.Description); terminal {
			results = append(results, terminalResult{Token: "status:" + strconv.FormatInt(status.ID, 10), Head: head})
		}
	}
	return results
}

// commandsInForce works out the commands' effect from current forge state
// and the service's command record. A review command is answered by a
// terminal Minos result of a head a run was started on for it, published
// after that run started: one the forge did not show when the service read
// it back just after the start (commandRuns). A run already started when
// the command was written fixed its scope then and never answers it, and a
// result on a head no run was started on for the command, such as an older
// head the author resets the branch to, answers nothing. The reviews and
// the summary comment speak of every head; a status is read on its own
// head, so a started head other than the current one has its statuses read
// through readStatuses, and only for a command nothing else has answered.
// A failed read leaves the command waiting, which costs one more review
// and never a review command answered with none. Nothing is read for the
// commands a newer skip supersedes.
func commandsInForce(commands []issuedCommand, snapshot forge.Snapshot, record commandRecord, readStatuses func(head string) ([]forge.Status, error), botLogin, statusContext string) commandEffect {
	var effect commandEffect
	lastSkip := -1
	for index, command := range commands {
		if !slices.Contains(record.Acknowledged, command.comment.ID) {
			effect.unacknowledged = append(effect.unacknowledged, command.comment)
		}
		if command.command == commandSkip {
			lastSkip = index
		}
	}
	effect.skipped = lastSkip >= 0 && lastSkip == len(commands)-1
	if effect.skipped {
		return effect
	}
	results := pullRequestResults(snapshot, botLogin, statusContext)
	headReviewed := slices.ContainsFunc(results, func(result terminalResult) bool { return result.Head == snapshot.HeadSHA })
	earlier := map[string][]terminalResult{}
	resultsOn := func(head string) []terminalResult {
		if head == snapshot.HeadSHA {
			return nil
		}
		if read, done := earlier[head]; done {
			return read
		}
		statuses, err := readStatuses(head)
		if err != nil {
			log.Printf("read the statuses of %s for comment commands: %v", head, err)
		}
		earlier[head] = statusResults(head, statuses, botLogin, statusContext)
		return earlier[head]
	}
	for _, command := range commands[lastSkip+1:] {
		if slices.Contains(record.Answered, command.comment.ID) {
			continue
		}
		if runs, started := record.Runs[command.comment.ID]; started && runs.answeredBy(results, resultsOn) {
			effect.answered = append(effect.answered, command.comment.ID)
			continue
		}
		effect.waiting = append(effect.waiting, command.comment)
		effect.fullOnly = effect.fullOnly || command.command == commandFullReview || headReviewed
	}
	effect.reviewRequested = len(effect.waiting) > 0
	return effect
}

// answeredBy reports whether a result published since the command's first
// run started answers it: one on a head a run was started on for it that
// the record does not know from a start.
func (runs commandRuns) answeredBy(results []terminalResult, resultsOn func(head string) []terminalResult) bool {
	answers := func(result terminalResult) bool {
		return slices.Contains(runs.Heads, result.Head) && !slices.Contains(runs.Known, result.Token)
	}
	if slices.ContainsFunc(results, answers) {
		return true
	}
	for _, head := range runs.Heads {
		if slices.ContainsFunc(resultsOn(head), answers) {
			return true
		}
	}
	return false
}

// commandRecordPath is where the service keeps its command record for one
// pull request. The record is a cache beside the runs: losing it costs one
// more review of each command already answered and a repeated reaction
// check, and nothing else.
func commandRecordPath(cfg ServiceConfig, forgeName string, guard forge.Guard) string {
	return filepath.Join(commandRecordDir(cfg), reviewedChangePrefix(forgeName, guard.Repository)+strconv.FormatInt(guard.PullRequest, 10)+".json")
}

func commandRecordDir(cfg ServiceConfig) string {
	return filepath.Join(cfg.Runs.Dir, ".commands")
}

// commandRecord is what the service keeps of a pull request's commands.
type commandRecord struct {
	// Runs holds, for each review command a run was started for, what
	// those runs were started on.
	Runs map[int64]commandRuns `json:"runs"`
	// Answered are the review commands a result has answered. An answer
	// is kept once seen, so it stands when the summary comment that gave
	// it is next rewritten for another head.
	Answered []int64 `json:"answered"`
	// Acknowledged are the command comments whose reaction was written.
	Acknowledged []int64 `json:"acknowledged"`
}

// commandRuns is what the record keeps of the runs started for one review
// command.
type commandRuns struct {
	// Heads are the heads those runs were started on.
	Heads []string `json:"heads"`
	// Known are the terminal results the forge showed when the service read
	// it back just after each of those runs started. A result there was
	// published before the run and answers nothing.
	Known []string `json:"known"`
}

// readCommandRecord reads the record. An unreadable record reads as empty,
// as a lost one does.
func readCommandRecord(cfg ServiceConfig, forgeName string, guard forge.Guard) commandRecord {
	path := commandRecordPath(cfg, forgeName, guard)
	record := commandRecord{Runs: make(map[int64]commandRuns)}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("read command record: %v", err)
		}
		return record
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		log.Printf("decode command record %s: %v", path, err)
		return commandRecord{Runs: make(map[int64]commandRuns)}
	}
	if record.Runs == nil {
		record.Runs = make(map[int64]commandRuns)
	}
	return record
}

// updateCommandRecord applies change to the record as it stands on disk.
// The record is read, changed and replaced under the admission lock, so a
// webhook's pass and the sweep's over one pull request never put back a
// copy without the other's change. A failed update is logged: the record
// is a cache.
func updateCommandRecord(cfg ServiceConfig, forgeName string, guard forge.Guard, change func(*commandRecord)) {
	err := func() error {
		unlock, err := lockAdmission(cfg.Runs.Dir)
		if err != nil {
			return err
		}
		defer unlock()
		record := readCommandRecord(cfg, forgeName, guard)
		change(&record)
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

// recordCommandedRun records a run started on head for the waiting review
// commands, with the terminal results the forge shows now, read back after
// the start: whatever a predecessor published before it ended is known and
// answers nothing. When the read-back fails or the head has already moved,
// nothing is recorded and the commands stay waiting, which costs one more
// review and never a review command answered with none.
func recordCommandedRun(ctx context.Context, cfg ServiceConfig, forgeName string, adapter *forge.Adapter, guard forge.Guard, waiting []forge.PullRequestComment) {
	if len(waiting) == 0 {
		return
	}
	snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
	if err != nil {
		log.Printf("%s/%s#%d: read back the results at a commanded run's start: %v", guard.Repository.Owner, guard.Repository.Name, guard.PullRequest, err)
		return
	}
	if snapshot.HeadSHA != guard.HeadSHA {
		return
	}
	var known []string
	for _, result := range pullRequestResults(snapshot, cfg.BotLogin(forgeName), cfg.Service.StatusContext) {
		known = append(known, result.Token)
	}
	updateCommandRecord(cfg, forgeName, guard, func(record *commandRecord) {
		for _, comment := range waiting {
			runs := record.Runs[comment.ID]
			if !slices.Contains(runs.Heads, guard.HeadSHA) {
				runs.Heads = append(runs.Heads, guard.HeadSHA)
			}
			for _, token := range known {
				if !slices.Contains(runs.Known, token) {
					runs.Known = append(runs.Known, token)
				}
			}
			record.Runs[comment.ID] = runs
		}
	})
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
