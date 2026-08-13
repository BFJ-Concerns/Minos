// Package product owns the small status vocabulary Minos exposes on a pull request.
package product

type State struct {
	name        string
	forgeState  string
	description string
}

var (
	working      = State{"working", "pending", "Reviewing changes"}
	attention    = State{"attention", "failure", "Changes need attention"}
	incomplete   = State{"incomplete", "error", "Review incomplete"}
	held         = State{"held", "pending", "Blocked on target branch"}
	continuation = State{"continuation", "pending", "Review continuing in a fresh run"}
	clean        = State{"clean", "success", "Changes approved"}
	merged       = State{"merged", "success", "Merged"}
	states       = [...]State{working, attention, incomplete, held, continuation, clean, merged}
)

func Working() State      { return working }
func Attention() State    { return attention }
func Incomplete() State   { return incomplete }
func Held() State         { return held }
func Continuation() State { return continuation }
func Clean() State        { return clean }
func Merged() State       { return merged }

func States() []State                   { return append([]State(nil), states[:]...) }
func (state State) Name() string        { return state.name }
func (state State) ForgeState() string  { return state.forgeState }
func (state State) Description() string { return state.description }
func (state State) Valid() bool {
	for _, known := range states {
		if state == known {
			return true
		}
	}
	return false
}
