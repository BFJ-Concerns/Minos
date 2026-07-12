// Package product owns the closed, forge-agnostic vocabulary Minos exposes on
// a pull request. Infrastructure packages may render these meanings, but must
// not reinterpret or extend them.
package product

// State is one sanctioned value of the single Minos commit-status context.
// Its private representation prevents callers from minting product meanings.
type State struct {
	name        string
	forgeState  string
	description string
	meaning     string
}

var (
	queued = State{
		name: "queued", forgeState: "pending", description: "Waiting for review",
		meaning: "eligible work has no live owner yet",
	}
	working = State{
		name: "working", forgeState: "pending", description: "Reviewing changes",
		meaning: "a live owner is attempting the lifecycle",
	}
	waiting = State{
		name: "waiting", forgeState: "pending", description: "Waiting for checks",
		meaning: "resume only when the wait fingerprint changes or its configured failsafe expires",
	}
	blocked = State{
		name: "blocked", forgeState: "failure", description: "Changes need attention",
		meaning: "substantive findings, permissions, or policy require outside action",
	}
	partial = State{
		name: "partial", forgeState: "failure", description: "Review incomplete",
		meaning: "coverage was not adequate for clearance",
	}
	stopped = State{
		name: "stopped", forgeState: "failure", description: "Review stopped; findings remain",
		meaning: "no further repair will be chased on the unchanged head",
	}
	clean = State{
		name: "clean", forgeState: "success", description: "Changes approved",
		meaning: "review converged; final integration clearance still precedes service merge",
	}
	cleanLimited = State{
		name: "clean, limited", forgeState: "success", description: "Changes approved; verification limited",
		meaning: "convergence used the strongest available but degraded independence",
	}
	merged = State{
		name: "merged", forgeState: "success", description: "Merged",
		meaning: "the observed head was merged",
	}
)

var states = [...]State{queued, working, waiting, blocked, partial, stopped, clean, cleanLimited, merged}

func Queued() State       { return queued }
func Working() State      { return working }
func Waiting() State      { return waiting }
func Blocked() State      { return blocked }
func Partial() State      { return partial }
func Stopped() State      { return stopped }
func Clean() State        { return clean }
func CleanLimited() State { return cleanLimited }
func Merged() State       { return merged }

// States returns the complete product vocabulary in lifecycle order.
func States() []State {
	result := make([]State, len(states))
	copy(result, states[:])
	return result
}

func (state State) Name() string        { return state.name }
func (state State) ForgeState() string  { return state.forgeState }
func (state State) Description() string { return state.description }
func (state State) Meaning() string     { return state.meaning }

// Valid reports whether state is one of the package-owned product states.
func (state State) Valid() bool {
	for _, known := range states {
		if state == known {
			return true
		}
	}
	return false
}
