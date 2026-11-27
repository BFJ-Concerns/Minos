package shell

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// AdmissionRules determines which open pull requests the repository reviews.
// Its zero value admits ready pull requests from the same repository.
type AdmissionRules struct {
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude-labels"`
	Authors       []string `toml:"authors"`
	BaseBranches  []string `toml:"base-branches"`
	Drafts        string   `toml:"drafts"`
	TitlePattern  string   `toml:"title-pattern"`
	Paths         []string `toml:"paths"`
	AllowForks    bool     `toml:"allow-forks"`
}

func (rules AdmissionRules) validate() error {
	switch rules.Drafts {
	case "", "ready", "any", "only":
	default:
		return fmt.Errorf("drafts must be ready, any or only")
	}
	for _, filter := range []struct {
		key    string
		values []string
	}{
		{"labels", rules.Labels}, {"exclude-labels", rules.ExcludeLabels},
		{"authors", rules.Authors}, {"base-branches", rules.BaseBranches},
	} {
		for _, value := range filter.values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s cannot contain a blank value", filter.key)
			}
		}
	}
	if _, err := regexp.Compile(rules.TitlePattern); err != nil {
		return fmt.Errorf("title-pattern: %w", err)
	}
	for _, pattern := range rules.Paths {
		if err := validRepositoryPath(pattern); err != nil {
			return fmt.Errorf("paths pattern %q %w", pattern, err)
		}
		if strings.HasSuffix(pattern, "/") {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("paths pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// metadataDeferrals uses forge facts, independently of the incoming event.
func (rules AdmissionRules) metadataDeferrals(snapshot forge.Snapshot) []string {
	var reasons []string
	switch {
	case snapshot.Draft && (rules.Drafts == "" || rules.Drafts == "ready"):
		reasons = append(reasons, "draft")
	case !snapshot.Draft && rules.Drafts == "only":
		reasons = append(reasons, "admission requires a draft")
	}
	if snapshot.HeadRepository == "" || snapshot.TargetRepository == "" {
		reasons = append(reasons, "admission source repository unavailable")
	} else if !rules.AllowForks && !strings.EqualFold(snapshot.HeadRepository, snapshot.TargetRepository) {
		reasons = append(reasons, "admission excludes forks")
	}
	if slices.ContainsFunc(rules.ExcludeLabels, func(label string) bool { return slices.Contains(snapshot.Labels, label) }) {
		reasons = append(reasons, "admission excludes a pull request label")
	}
	if len(rules.Labels) > 0 && !slices.ContainsFunc(rules.Labels, func(label string) bool { return slices.Contains(snapshot.Labels, label) }) {
		reasons = append(reasons, "admission label did not match")
	}
	if len(rules.Authors) > 0 && !slices.ContainsFunc(rules.Authors, func(author string) bool { return strings.EqualFold(author, snapshot.Author) }) {
		reasons = append(reasons, "admission author did not match")
	}
	if len(rules.BaseBranches) > 0 && !slices.Contains(rules.BaseBranches, snapshot.TargetBranch) {
		reasons = append(reasons, "admission base branch did not match")
	}
	if rules.TitlePattern != "" {
		matched, _ := regexp.MatchString(rules.TitlePattern, snapshot.Title) // Validated on load.
		if !matched {
			reasons = append(reasons, "admission title did not match")
		}
	}
	return reasons
}

func (rules AdmissionRules) matchesPaths(paths []string) bool {
	for _, pattern := range rules.Paths {
		for _, changed := range paths {
			if strings.HasSuffix(pattern, "/") {
				if strings.HasPrefix(changed, pattern) {
					return true
				}
				continue
			}
			if matched, _ := path.Match(pattern, changed); matched {
				return true
			}
		}
	}
	return false
}
