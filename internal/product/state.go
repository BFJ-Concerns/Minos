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
	continuation = State{"continuation", "pending", "Review continuing in a fresh run"}
	clean        = State{"clean", "success", "Changes approved"}
	states       = [...]State{working, attention, incomplete, continuation, clean}
)

func Working() State      { return working }
func Attention() State    { return attention }
func Incomplete() State   { return incomplete }
func Continuation() State { return continuation }
func Clean() State        { return clean }

func States() []State                   { return append([]State(nil), states[:]...) }
func (state State) Name() string        { return state.name }
func (state State) ForgeState() string  { return state.forgeState }
func (state State) Description() string { return state.description }

// CompletionMarker recognises the terminal product states Minos publishes to
// the forge. Both fields are required so a stale or contradictory status does
// not suppress another review attempt.
func CompletionMarker(forgeState, description string) (State, bool) {
	for _, state := range [...]State{clean, attention} {
		if forgeState == state.forgeState && description == state.description {
			return state, true
		}
	}
	return State{}, false
}

func (state State) Valid() bool {
	for _, known := range states {
		if state == known {
			return true
		}
	}
	return false
}
