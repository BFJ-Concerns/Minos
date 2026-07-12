package shell

import "path"

// EligibilityRule selects which PR authors the deployment serves. Lifecycle
// stages and forge actors are intentionally absent: current forge state decides
// what an eligible PR needs.
type EligibilityRule struct {
	Authors []string `toml:"authors"`
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
