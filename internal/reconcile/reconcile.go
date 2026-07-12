// Package reconcile decides what Minos should do from current forge facts and
// narrow coordination state. It performs no I/O: webhooks and sweeps must feed
// this same decision function rather than maintaining separate transition logic.
package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"bfj/minos/internal/ledger"
)

type ProductState string

const (
	ProductNone         ProductState = ""
	ProductQueued       ProductState = "queued"
	ProductWorking      ProductState = "working"
	ProductWaiting      ProductState = "waiting"
	ProductBlocked      ProductState = "blocked"
	ProductPartial      ProductState = "partial"
	ProductStopped      ProductState = "stopped"
	ProductClean        ProductState = "clean"
	ProductCleanLimited ProductState = "clean, limited"
	ProductMerged       ProductState = "merged"
)

type Check struct {
	Identity   string
	Conclusion string
}

// ForgeSnapshot is the consumer-owned authoritative snapshot contract. The
// shell adaptation may temporarily report TargetKnown=false, but the interface
// makes the commissioned target revision explicit for the wave-two adapter.
type ForgeSnapshot struct {
	Key                   ledger.Key
	HeadSHA               string
	TargetBranch          string
	TargetSHA             string
	TargetKnown           bool
	Open                  bool
	Merged                bool
	Draft                 bool
	AuthorInScope         bool
	SkipDrafts            bool
	Occasion              string
	Product               ProductState
	ProductHead           string
	ProductTarget         string
	ProductGoverning      string
	ProductGoverningKnown bool
	GoverningIdentity     string
	DeploymentProfile     string
	RequiredChecks        []Check
	Mergeability          string
}

type View struct {
	Lease          *ledger.Lease
	Wait           *ledger.Wait
	Backoff        *ledger.Backoff
	Cleanup        *ledger.Cleanup
	LivenessWindow time.Duration
}

type DecisionKind string

const (
	Admit   DecisionKind = "admit"
	Live    DecisionKind = "live"
	Replace DecisionKind = "replace"
	CleanUp DecisionKind = "cleanup"
	Nothing DecisionKind = "nothing"
)

type Decision struct {
	Kind   DecisionKind
	Reason string
}

func Decide(snapshot ForgeSnapshot, view View, now time.Time) Decision {
	if view.Cleanup != nil {
		return Decision{Kind: CleanUp, Reason: "post-merge branch cleanup is owed"}
	}
	if view.Lease != nil {
		if now.Sub(view.Lease.HeartbeatAt) < view.LivenessWindow {
			return Decision{Kind: Live, Reason: "current lease heartbeat is live"}
		}
		return Decision{Kind: Replace, Reason: "lease heartbeat is stale"}
	}
	if !snapshot.Open || snapshot.Merged {
		return Decision{Kind: Nothing, Reason: "pull request is not open"}
	}
	if snapshot.Draft && snapshot.SkipDrafts {
		return Decision{Kind: Nothing, Reason: "drafts are excluded by policy"}
	}
	if view.Backoff != nil && now.Before(view.Backoff.NextDueAt) {
		return Decision{Kind: Nothing, Reason: "operational backoff is not due"}
	}
	fingerprint := WaitFingerprint(snapshot)
	if snapshot.Product == ProductWaiting {
		if waitCurrent(view.Wait, fingerprint, now) {
			return Decision{Kind: Nothing, Reason: "wait fingerprint is current"}
		}
		return Decision{Kind: Admit, Reason: "wait fingerprint changed or failsafe expired"}
	}
	servedPair := snapshot.ProductHead == snapshot.HeadSHA && snapshot.ProductTarget != "" && snapshot.ProductTarget == snapshot.TargetSHA
	switch snapshot.Product {
	case ProductStopped, ProductBlocked, ProductPartial:
		if servedPair {
			return Decision{Kind: Nothing, Reason: "unchanged head and target were already served"}
		}
	case ProductClean, ProductCleanLimited:
		if servedPair && snapshot.ProductGoverningKnown && snapshot.ProductGoverning == snapshot.GoverningIdentity {
			return Decision{Kind: Nothing, Reason: "unchanged head, target, and governing inputs are clean"}
		}
	case ProductMerged:
		return Decision{Kind: Nothing, Reason: "pull request is already merged"}
	}
	if snapshot.AuthorInScope {
		return Decision{Kind: Admit, Reason: "eligible current state requires a lifecycle"}
	}
	return Decision{Kind: Nothing, Reason: "author or occasion is outside eligibility"}
}

func waitCurrent(wait *ledger.Wait, fingerprint string, now time.Time) bool {
	if wait == nil || wait.Fingerprint != fingerprint {
		return false
	}
	return wait.FailsafeAt == nil || now.Before(*wait.FailsafeAt)
}

// WaitFingerprint uses a canonical ordering so the same forge facts produce
// the same identity regardless of adapter response order.
func WaitFingerprint(snapshot ForgeSnapshot) string {
	checks := append([]Check(nil), snapshot.RequiredChecks...)
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Identity == checks[j].Identity {
			return checks[i].Conclusion < checks[j].Conclusion
		}
		return checks[i].Identity < checks[j].Identity
	})
	parts := []string{
		"head=" + snapshot.HeadSHA,
		"target=" + snapshot.TargetSHA,
		"governing=" + snapshot.GoverningIdentity,
		"profile=" + snapshot.DeploymentProfile,
	}
	for _, check := range checks {
		parts = append(parts, "check="+check.Identity+"="+check.Conclusion)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}
