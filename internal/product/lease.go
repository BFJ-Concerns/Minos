package product

import "fmt"

// Lease captures only the product-visible presence invariant. The adapter owns
// the forge reaction verb; this value decides whether that reaction must exist.
type Lease struct {
	live bool
	eyes bool
}

func (lease Lease) Live() bool { return lease.live }
func (lease Lease) Eyes() bool { return lease.eyes }

// LeaseEvent names every transition which changes product-visible presence.
type LeaseEvent struct{ name string }

var (
	leaseAcquired = LeaseEvent{name: "acquired"}
	leaseExited   = LeaseEvent{name: "exited"}
	leaseWaiting  = LeaseEvent{name: "waiting"}
	leaseFailed   = LeaseEvent{name: "failed"}
	leaseReaped   = LeaseEvent{name: "reaped"}
)

func LeaseAcquired() LeaseEvent { return leaseAcquired }
func LeaseExited() LeaseEvent   { return leaseExited }
func LeaseWaiting() LeaseEvent  { return leaseWaiting }
func LeaseFailed() LeaseEvent   { return leaseFailed }
func LeaseReaped() LeaseEvent   { return leaseReaped }

func (event LeaseEvent) Name() string { return event.name }

// TransitionLease applies the eyes-lease lifecycle. Every release path returns
// the zero state, making "no live lease, no eyes" structural rather than advisory.
func TransitionLease(current Lease, event LeaseEvent) (Lease, error) {
	if current.eyes != current.live {
		return Lease{}, fmt.Errorf("invalid lease state: live ownership and eyes differ")
	}
	switch event {
	case leaseAcquired:
		if current.live {
			return Lease{}, fmt.Errorf("lease is already live")
		}
		return Lease{live: true, eyes: true}, nil
	case leaseExited, leaseWaiting, leaseFailed, leaseReaped:
		return Lease{}, nil
	default:
		return Lease{}, fmt.Errorf("unknown lease event")
	}
}
