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
	"bfj/minos/internal/product"
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
	Product               product.State
	ProductHead           string
	ProductTarget         string
	ProductGoverning      string
	ProductGoverningKnown bool
	ProductBlockKind      BlockKind
	ProductBlockIdentity  string
	ProductBlockKnown     bool
	GoverningIdentity     string
	DeploymentProfile     string
	RequiredChecks        []Check
	Mergeability          string
}

// BlockKind preserves the commissioned re-entry distinction without expanding
// the closed product-state vocabulary. The forge adapter derives this from the
// service-owned trailing marker attached to the substantive review.
type BlockKind string

const (
	FindingBlock    BlockKind = "finding"
	PermissionBlock BlockKind = "permission-policy"
)

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
	Reap    DecisionKind = "reap"
	Replace DecisionKind = "replace"
	CleanUp DecisionKind = "cleanup"
	Nothing DecisionKind = "nothing"
)

type Decision struct {
	Kind         DecisionKind
	Reason       string
	ResetWorking bool
}

func Decide(snapshot ForgeSnapshot, view View, now time.Time) Decision {
	resetWorking := view.Lease == nil && snapshot.Product == product.Working() && snapshot.Open && !snapshot.Merged && !(snapshot.Draft && snapshot.SkipDrafts) && snapshot.AuthorInScope
	decision := func(kind DecisionKind, reason string) Decision {
		return Decision{Kind: kind, Reason: reason, ResetWorking: resetWorking}
	}
	if view.Cleanup != nil {
		return decision(CleanUp, "post-merge branch cleanup is owed")
	}
	if view.Lease != nil {
		if now.Sub(view.Lease.HeartbeatAt) < view.LivenessWindow {
			return decision(Live, "current lease heartbeat is live")
		}
		if reason, served := productServed(snapshot); served {
			return decision(Reap, reason)
		}
		return decision(Replace, "lease heartbeat is stale")
	}
	if !snapshot.Open || snapshot.Merged {
		return decision(Nothing, "pull request is not open")
	}
	if snapshot.Draft && snapshot.SkipDrafts {
		return decision(Nothing, "drafts are excluded by policy")
	}
	if !snapshot.AuthorInScope {
		return decision(Nothing, "author or occasion is outside eligibility")
	}
	if view.Backoff != nil && now.Before(view.Backoff.NextDueAt) {
		return decision(Nothing, "operational backoff is not due")
	}
	fingerprint := WaitFingerprint(snapshot)
	if snapshot.Product == product.Waiting() {
		if waitCurrent(view.Wait, fingerprint, now) {
			return decision(Nothing, "wait fingerprint is current")
		}
		return decision(Admit, "wait fingerprint changed or failsafe expired")
	}
	if snapshot.Product == product.Blocked() && snapshot.ProductBlockKind == "" {
		return decision(Admit, "blocked result has no authoritative re-entry discriminator")
	}
	if reason, served := productServed(snapshot); served {
		return decision(Nothing, reason)
	}
	if snapshot.Product == product.Blocked() && snapshot.ProductBlockKind == PermissionBlock {
		return decision(Admit, "permission readiness or trusted policy changed")
	}
	if snapshot.Product == product.Merged() {
		return decision(Nothing, "pull request is already merged")
	}
	return decision(Admit, "eligible current state requires a lifecycle")
}

func productPairServed(snapshot ForgeSnapshot) bool {
	return snapshot.ProductHead == snapshot.HeadSHA && snapshot.ProductTarget != "" && snapshot.ProductTarget == snapshot.TargetSHA
}

func productServed(snapshot ForgeSnapshot) (string, bool) {
	if !productPairServed(snapshot) {
		return "", false
	}
	if snapshot.Product == product.Blocked() && snapshot.ProductBlockKind == FindingBlock {
		return "unchanged head and target were already served", true
	}
	if snapshot.Product == product.Blocked() && snapshot.ProductBlockKind == PermissionBlock && snapshot.ProductBlockKnown && snapshot.ProductBlockIdentity == BlockReentryIdentity(snapshot) {
		return "permission or policy block remains current", true
	}
	if snapshot.Product == product.Stopped() || snapshot.Product == product.Partial() {
		return "unchanged head and target were already served", true
	}
	if (snapshot.Product == product.Clean() || snapshot.Product == product.CleanLimited()) && snapshot.ProductGoverningKnown && snapshot.ProductGoverning == snapshot.GoverningIdentity {
		return "unchanged head, target, and governing inputs are clean", true
	}
	return "", false
}

// BlockReentryIdentity is the marker value for a permission or policy block.
// It covers both trusted repository policy and the deployment/readiness profile
// because either commissioned axis is sufficient to re-enter an unchanged PR.
func BlockReentryIdentity(snapshot ForgeSnapshot) string {
	sum := sha256.Sum256([]byte(snapshot.GoverningIdentity + "\n" + snapshot.DeploymentProfile))
	return hex.EncodeToString(sum[:])
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
