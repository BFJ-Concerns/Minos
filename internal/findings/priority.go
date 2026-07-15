package findings

import (
	"encoding/json"
	"fmt"
)

// Priority is one fixed Minos review priority. Its representation is private so
// callers cannot invent repository-specific priority classes.
type Priority struct{ value string }

var (
	P0 = Priority{value: "P0"}
	P1 = Priority{value: "P1"}
	P2 = Priority{value: "P2"}
	P3 = Priority{value: "P3"}
)

var priorities = [...]Priority{P0, P1, P2, P3}

func ParsePriority(value string) (Priority, error) {
	for _, priority := range priorities {
		if value == priority.value {
			return priority, nil
		}
	}
	return Priority{}, fmt.Errorf("invalid priority %q: want P0, P1, P2, or P3", value)
}

func (priority Priority) String() string { return priority.value }

func (priority Priority) Valid() bool {
	_, err := ParsePriority(priority.value)
	return err == nil
}

func (priority Priority) MarshalText() ([]byte, error) {
	if !priority.Valid() {
		return nil, fmt.Errorf("invalid priority %q", priority.value)
	}
	return []byte(priority.value), nil
}

func (priority *Priority) UnmarshalText(data []byte) error {
	parsed, err := ParsePriority(string(data))
	if err != nil {
		return err
	}
	*priority = parsed
	return nil
}

func (priority Priority) MarshalJSON() ([]byte, error) {
	text, err := priority.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (priority *Priority) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("priority must be a string: %w", err)
	}
	return priority.UnmarshalText([]byte(value))
}

// AtOrAbove applies the fixed P0-to-P3 order inclusively.
func AtOrAbove(priority, threshold Priority) bool {
	return priority.Valid() && threshold.Valid() && priorityRank(priority) <= priorityRank(threshold)
}

func priorityRank(priority Priority) int {
	for rank, candidate := range priorities {
		if priority == candidate {
			return rank
		}
	}
	return len(priorities)
}

// MoreSevere returns the more severe of two valid priorities.
func MoreSevere(left, right Priority) (Priority, error) {
	if !left.Valid() || !right.Valid() {
		return Priority{}, fmt.Errorf("cannot compare invalid priorities %q and %q", left, right)
	}
	if AtOrAbove(left, right) {
		return left, nil
	}
	return right, nil
}

type Assurance struct{ value string }

var AgentJudgement = Assurance{value: "agent-judgement"}

func ParseAssurance(value string) (Assurance, error) {
	if value != AgentJudgement.value {
		return Assurance{}, fmt.Errorf("invalid assurance %q: want agent-judgement", value)
	}
	return AgentJudgement, nil
}

func (assurance Assurance) String() string { return assurance.value }
func (assurance Assurance) Valid() bool    { return assurance == AgentJudgement }

func (assurance Assurance) MarshalJSON() ([]byte, error) {
	if !assurance.Valid() {
		return nil, fmt.Errorf("invalid assurance %q", assurance.value)
	}
	return json.Marshal(assurance.value)
}

func (assurance *Assurance) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("assurance must be a string: %w", err)
	}
	parsed, err := ParseAssurance(value)
	if err != nil {
		return err
	}
	*assurance = parsed
	return nil
}

type DispositionMode string

const (
	DestinationMode      DispositionMode = "destination"
	PublishThroughP3Mode DispositionMode = "publish-through-p3"
)

func (mode DispositionMode) Valid() bool {
	return mode == DestinationMode || mode == PublishThroughP3Mode
}

type CandidateOutcome string

const (
	CandidateVerified               CandidateOutcome = "verified"
	CandidateSuppressed             CandidateOutcome = "suppressed"
	CandidateVerificationUnresolved CandidateOutcome = "verification-unresolved"
)

func (outcome CandidateOutcome) Valid() bool {
	return outcome == CandidateVerified || outcome == CandidateSuppressed || outcome == CandidateVerificationUnresolved
}

type PublicationDisposition string

const (
	PublicationPublished PublicationDisposition = "published"
	PublicationWithheld  PublicationDisposition = "withheld"
	PublicationDisclosed PublicationDisposition = "disclosed"
)

func (disposition PublicationDisposition) Valid() bool {
	return disposition == PublicationPublished || disposition == PublicationWithheld || disposition == PublicationDisclosed
}

type RepairDisposition string

const (
	RepairNotEligible  RepairDisposition = "not-eligible"
	RepairNotTriggered RepairDisposition = "not-triggered"
	RepairSelected     RepairDisposition = "selected"
	RepairRepaired     RepairDisposition = "repaired"
	RepairBlocked      RepairDisposition = "blocked"
	RepairFruitless    RepairDisposition = "fruitless"
	RepairAnnexeRouted RepairDisposition = "annexe-routed"
	RepairDeferred     RepairDisposition = "deferred"
)

func (disposition RepairDisposition) Valid() bool {
	switch disposition {
	case RepairNotEligible, RepairNotTriggered, RepairSelected, RepairRepaired, RepairBlocked, RepairFruitless, RepairAnnexeRouted, RepairDeferred:
		return true
	default:
		return false
	}
}

func (disposition RepairDisposition) Terminal() bool {
	switch disposition {
	case RepairNotEligible, RepairNotTriggered, RepairRepaired, RepairBlocked, RepairFruitless, RepairAnnexeRouted, RepairDeferred:
		return true
	default:
		return false
	}
}

type DeliveryState string

const (
	DeliveryNotRequired DeliveryState = "not-required"
	DeliveryPending     DeliveryState = "pending"
	DeliveryConfirmed   DeliveryState = "confirmed"
)

func (state DeliveryState) Valid() bool {
	return state == DeliveryNotRequired || state == DeliveryPending || state == DeliveryConfirmed
}
