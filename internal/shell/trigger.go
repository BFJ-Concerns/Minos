package shell

import (
	"path"
)

type TriggerRule struct {
	Run     string   `toml:"run"`
	On      []string `toml:"on"`
	Authors []string `toml:"authors"`
	Actors  []string `toml:"actors"`
	Drafts  *bool    `toml:"drafts"`
}

type TriggerDecision struct {
	Kind RunKind
	Rule TriggerRule
}

func EvaluateTriggers(facts Facts, repo RepoConfig) (TriggerDecision, bool) {
	for _, rule := range repo.Triggers {
		kind, err := ParseRunKind(rule.Run)
		if err != nil {
			continue
		}
		if !matchesAny(rule.On, facts.Occasion) {
			continue
		}
		if rule.Drafts != nil && facts.Draft != *rule.Drafts {
			continue
		}
		if len(rule.Authors) > 0 && !matchesGlobAny(rule.Authors, facts.Author) {
			continue
		}
		if len(rule.Actors) > 0 && !matchesGlobAny(rule.Actors, facts.Actor) {
			continue
		}
		return TriggerDecision{Kind: kind, Rule: rule}, true
	}
	return TriggerDecision{}, false
}

func matchesAny(candidates []string, value string) bool {
	for _, candidate := range candidates {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesGlobAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == "*" {
			return true
		}
		if ok, err := path.Match(pattern, value); err == nil && ok {
			return true
		}
	}
	return false
}

func NewHeadOccasion(occasion string) bool {
	switch occasion {
	case "pr-opened", "pr-reopened", "pr-synchronized":
		return true
	default:
		return false
	}
}
