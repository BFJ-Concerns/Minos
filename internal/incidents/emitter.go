package incidents

import "context"

// Emitter is the deliberately narrow integration seam for operational
// failure reporting. The coordination ledger can later implement the same
// identity-preserving operation without changing callers.
type Emitter interface {
	Emit(context.Context, Event) (Incident, error)
}

type StoreEmitter struct{ Store Store }

func (emitter StoreEmitter) Emit(ctx context.Context, event Event) (Incident, error) {
	return emitter.Store.Raise(ctx, event)
}
