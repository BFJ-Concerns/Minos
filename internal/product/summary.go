package product

import (
	"fmt"
	"regexp"
	"strings"
)

// The summary comment is the one comment Minos keeps on a pull request:
// created at the first claim and edited in place ever after. Its first line
// is the summary record, an HTML comment a reader of the pull request does
// not see, which binds a state to a head. Everything below that line is
// presentation and is never read back.
//
// The record is the head's completion marker. It names the head it speaks
// of, so a head that has moved has spent it with no write: a record for
// another head says nothing about this one.

// SummaryState is what the summary comment says of the head it names.
type SummaryState string

const (
	// SummaryInspecting: the head is claimed and push inspection is
	// deciding how much review it needs.
	SummaryInspecting SummaryState = "inspecting"
	// SummaryReviewing: a review of the head is running.
	SummaryReviewing SummaryState = "reviewing"
	// SummaryClean: the head was judged and nothing blocks it.
	SummaryClean SummaryState = "clean"
	// SummaryChangesRequested: the head was judged and a gating finding
	// stands, this run's or an earlier one's.
	SummaryChangesRequested SummaryState = "changes-requested"
	// SummaryIncomplete: a run on the head stopped short of judging it.
	SummaryIncomplete SummaryState = "incomplete"
)

var summaryStates = [...]SummaryState{SummaryInspecting, SummaryReviewing, SummaryClean, SummaryChangesRequested, SummaryIncomplete}

func SummaryStates() []SummaryState { return append([]SummaryState(nil), summaryStates[:]...) }

// NamedSummaryState finds the state a command argument or a record names.
func NamedSummaryState(name string) (SummaryState, bool) {
	for _, state := range summaryStates {
		if string(state) == name {
			return state, true
		}
	}
	return "", false
}

// Finished reports whether the state records a judged head. Only a finished
// state completes a head: inspecting, reviewing and incomplete leave it
// eligible.
func (state SummaryState) Finished() bool {
	return state == SummaryClean || state == SummaryChangesRequested
}

// Completion is the terminal product state a finished summary state stands
// for, the same one a terminal review or status of the head stands for.
func (state SummaryState) Completion() (State, bool) {
	switch state {
	case SummaryClean:
		return clean, true
	case SummaryChangesRequested:
		return attention, true
	}
	return State{}, false
}

// SummaryRecord is the summary comment's first line. Head, Target and State
// say where the head under review stands. FinishedHead and FinishedState
// name the last head a run finished on and how, kept while State is not
// itself finished, so the earlier finished head survives the claim that
// starts work on a later one; a record whose own State is finished is its
// own last finished head and carries neither.
type SummaryRecord struct {
	Head          string       `json:"head"`
	Target        string       `json:"target"`
	State         SummaryState `json:"state"`
	FinishedHead  string       `json:"finished_head,omitempty"`
	FinishedState SummaryState `json:"finished_state,omitempty"`
}

// Completion reads the record as the completion marker of head: the
// terminal state it records, when it speaks of that head and its state is
// finished.
func (record SummaryRecord) Completion(head string) (State, bool) {
	if record.Head == "" || record.Head != head {
		return State{}, false
	}
	return record.State.Completion()
}

// LastFinished names the last head a run finished on, and how.
func (record SummaryRecord) LastFinished() (string, SummaryState, bool) {
	if record.State.Finished() {
		return record.Head, record.State, record.Head != ""
	}
	return record.FinishedHead, record.FinishedState, record.FinishedHead != "" && record.FinishedState.Finished()
}

// Next is the record that replaces this one when a run writes state for
// head. It is the one place the last finished head is carried forward, so
// no writer handles it. The zero record, a pull request with no summary
// comment yet, carries nothing.
func (record SummaryRecord) Next(head, target string, state SummaryState) SummaryRecord {
	next := SummaryRecord{Head: head, Target: target, State: state}
	if !state.Finished() {
		if finishedHead, finishedState, found := record.LastFinished(); found {
			next.FinishedHead, next.FinishedState = finishedHead, finishedState
		}
	}
	return next
}

// SummaryOpening is how the summary record, and so the summary comment,
// opens. Each forge's adaptation finds the service's summary comment by
// this opening and its author (summary_comment in
// scripts/adaptations/*/common.sh).
const SummaryOpening = "<!-- Minos summary:"

// summaryLine is the summary record's grammar, and summaryToken one token
// of it alone.
var (
	summaryLine  = regexp.MustCompile(`^` + SummaryOpening + `(( ` + recordToken + `)+) -->$`)
	summaryToken = regexp.MustCompile(`^` + recordToken + `$`)
)

// Line renders the record, refusing one that is incomplete or that the
// grammar cannot carry.
func (record SummaryRecord) Line() (string, error) {
	if _, known := NamedSummaryState(string(record.State)); !known {
		return "", fmt.Errorf("unknown summary state %q", record.State)
	}
	tokens := []string{"head=" + record.Head, RecordTargetKey + "=" + record.Target, "state=" + string(record.State)}
	if record.FinishedHead != "" || record.FinishedState != "" {
		if record.State.Finished() || !record.FinishedState.Finished() {
			return "", fmt.Errorf("summary record carries a last finished head it cannot hold")
		}
		tokens = append(tokens, "finished-head="+record.FinishedHead, "finished-state="+string(record.FinishedState))
	}
	for _, token := range tokens {
		if !summaryToken.MatchString(token) {
			return "", fmt.Errorf("invalid summary record token %q", token)
		}
	}
	return SummaryOpening + " " + strings.Join(tokens, " ") + " -->", nil
}

// ComposeSummary is the summary comment's whole body: the record, then the
// text a reader sees.
func ComposeSummary(record SummaryRecord, text string) (string, error) {
	line, err := record.Line()
	if err != nil {
		return "", err
	}
	return line + "\n" + strings.Trim(text, "\r\n") + "\n", nil
}

// ParseSummary reads the record a summary comment's body opens with. A key
// it does not know is passed over, so a record may gain one. A body that
// does not open with a complete record is not a summary comment.
func ParseSummary(body string) (SummaryRecord, bool) {
	line, _, _ := strings.Cut(body, "\n")
	match := summaryLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if match == nil {
		return SummaryRecord{}, false
	}
	var record SummaryRecord
	for _, token := range strings.Fields(match[1]) {
		key, value, _ := strings.Cut(token, "=")
		switch key {
		case "head":
			record.Head = value
		case RecordTargetKey:
			record.Target = value
		case "state":
			record.State = SummaryState(value)
		case "finished-head":
			record.FinishedHead = value
		case "finished-state":
			record.FinishedState = SummaryState(value)
		}
	}
	if _, err := record.Line(); err != nil || record.Head == "" || record.Target == "" {
		return SummaryRecord{}, false
	}
	return record, true
}
