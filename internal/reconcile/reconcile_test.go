package reconcile

import (
	"testing"
	"time"

	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
)

func snapshot() ForgeSnapshot {
	return ForgeSnapshot{
		Key:     ledger.Key{Forge: "forgejo", Owner: "BFJ", Repo: "Minos", PR: "42"},
		HeadSHA: "head-1", TargetSHA: "target-1", TargetKnown: true, Open: true,
		AuthorInScope: true, SkipDrafts: true, GoverningIdentity: "policy-1",
		DeploymentProfile: "profile-1",
	}
}

func TestDecisionPriority(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	base := snapshot()
	liveLease := &ledger.Lease{Key: base.Key, HeartbeatAt: now.Add(-time.Minute)}
	deadLease := &ledger.Lease{Key: base.Key, HeartbeatAt: now.Add(-2 * time.Hour)}
	cleanup := &ledger.Cleanup{Key: base.Key}
	future := now.Add(time.Hour)
	tests := []struct {
		name string
		edit func(*ForgeSnapshot)
		view View
		want DecisionKind
	}{
		{"cleanup first", nil, View{Cleanup: cleanup, Lease: liveLease, LivenessWindow: time.Hour}, CleanUp},
		{"live lease", nil, View{Lease: liveLease, LivenessWindow: time.Hour}, Live},
		{"dead lease", nil, View{Lease: deadLease, LivenessWindow: time.Hour}, Replace},
		{"closed", func(s *ForgeSnapshot) { s.Open = false }, View{}, Nothing},
		{"draft", func(s *ForgeSnapshot) { s.Draft = true }, View{}, Nothing},
		{"backoff", nil, View{Backoff: &ledger.Backoff{NextDueAt: future}}, Nothing},
		{"eligible", nil, View{}, Admit},
		{"out of scope", func(s *ForgeSnapshot) { s.AuthorInScope = false }, View{}, Nothing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := base
			if test.edit != nil {
				test.edit(&s)
			}
			if got := Decide(s, test.view, now).Kind; got != test.want {
				t.Fatalf("decision = %s, want %s", got, test.want)
			}
		})
	}
}

func TestOwnerlessWorkingResetFollowsEligibilityAndLedgerOwnership(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	base := snapshot()
	base.Product = product.Working()
	backoff := &ledger.Backoff{NextDueAt: now.Add(time.Hour)}
	tests := []struct {
		name  string
		edit  func(*ForgeSnapshot)
		view  View
		reset bool
	}{
		{name: "eligible without owner during backoff", view: View{Backoff: backoff}, reset: true},
		{name: "eligible without owner when retry is due", view: View{}, reset: true},
		{name: "live owner", view: View{Lease: &ledger.Lease{HeartbeatAt: now}, LivenessWindow: time.Hour}},
		{name: "closed", edit: func(value *ForgeSnapshot) { value.Open = false }},
		{name: "excluded draft", edit: func(value *ForgeSnapshot) { value.Draft = true }},
		{name: "ineligible author", edit: func(value *ForgeSnapshot) { value.AuthorInScope = false }},
		{name: "queued already", edit: func(value *ForgeSnapshot) { value.Product = product.Queued() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			if test.edit != nil {
				test.edit(&value)
			}
			if got := Decide(value, test.view, now).ResetWorking; got != test.reset {
				t.Fatalf("reset working=%v, want %v", got, test.reset)
			}
		})
	}
}

func TestStoppedAndCleanServedness(t *testing.T) {
	now := time.Now()
	for _, state := range []product.State{product.Stopped(), product.Blocked(), product.Partial(), product.Clean(), product.CleanLimited()} {
		t.Run(state.Name(), func(t *testing.T) {
			s := snapshot()
			s.Product = state
			if state == product.Blocked() {
				s.ProductBlockKind = FindingBlock
			}
			s.ProductHead = s.HeadSHA
			s.ProductTarget = s.TargetSHA
			s.ProductGoverning = s.GoverningIdentity
			s.ProductGoverningKnown = true
			if got := Decide(s, View{}, now).Kind; got != Nothing {
				t.Fatalf("unchanged served state = %s, want nothing", got)
			}
			s.HeadSHA = "head-2"
			if got := Decide(s, View{}, now).Kind; got != Admit {
				t.Fatalf("moved head = %s, want admit", got)
			}
		})
	}
}

func TestStaleLeaseReapsServedTerminalStateWithoutReplacement(t *testing.T) {
	now := time.Date(2026, 7, 12, 21, 0, 0, 0, time.UTC)
	deadLease := &ledger.Lease{HeartbeatAt: now.Add(-2 * time.Hour)}
	view := View{Lease: deadLease, LivenessWindow: time.Hour}
	for _, state := range []product.State{product.Stopped(), product.Partial(), product.Clean(), product.CleanLimited()} {
		t.Run(state.Name(), func(t *testing.T) {
			s := snapshot()
			s.Product = state
			s.ProductHead = s.HeadSHA
			s.ProductTarget = s.TargetSHA
			s.ProductGoverning = s.GoverningIdentity
			s.ProductGoverningKnown = true
			if got := Decide(s, view, now).Kind; got != Reap {
				t.Fatalf("stale lease over served terminal state = %s, want reap", got)
			}
			s.TargetSHA = "target-2"
			if got := Decide(s, view, now).Kind; got != Replace {
				t.Fatalf("stale lease over moved terminal state = %s, want replace", got)
			}
		})
	}
}

func TestStaleLeaseReplacesCleanStateWhenGoverningInputsMoved(t *testing.T) {
	now := time.Date(2026, 7, 12, 21, 0, 0, 0, time.UTC)
	s := snapshot()
	s.Product = product.Clean()
	s.ProductHead = s.HeadSHA
	s.ProductTarget = s.TargetSHA
	s.ProductGoverning = "policy-0"
	s.ProductGoverningKnown = true
	view := View{Lease: &ledger.Lease{HeartbeatAt: now.Add(-2 * time.Hour)}, LivenessWindow: time.Hour}
	if got := Decide(s, view, now).Kind; got != Replace {
		t.Fatalf("stale lease over superseded clean governance = %s, want replace", got)
	}
}

func TestStaleLeaseMirrorsServedBlockedReentryRules(t *testing.T) {
	now := time.Date(2026, 7, 12, 22, 0, 0, 0, time.UTC)
	view := View{Lease: &ledger.Lease{HeartbeatAt: now.Add(-2 * time.Hour)}, LivenessWindow: time.Hour}
	tests := []struct {
		name string
		edit func(*ForgeSnapshot)
		want DecisionKind
	}{
		{"served finding block", nil, Reap},
		{"finding block moved head", func(s *ForgeSnapshot) { s.HeadSHA = "head-2" }, Replace},
		{"served permission block", func(s *ForgeSnapshot) {
			s.ProductBlockKind = PermissionBlock
			s.ProductBlockIdentity = BlockReentryIdentity(*s)
			s.ProductBlockKnown = true
		}, Reap},
		{"permission block moved head", func(s *ForgeSnapshot) {
			s.ProductBlockKind = PermissionBlock
			s.ProductBlockIdentity = BlockReentryIdentity(*s)
			s.ProductBlockKnown = true
			s.HeadSHA = "head-2"
		}, Replace},
		{"permission block changed governing identity", func(s *ForgeSnapshot) {
			s.ProductBlockKind = PermissionBlock
			s.ProductBlockIdentity = BlockReentryIdentity(*s)
			s.ProductBlockKnown = true
			s.GoverningIdentity = "policy-2"
		}, Replace},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot()
			s.Product = product.Blocked()
			s.ProductBlockKind = FindingBlock
			s.ProductHead = s.HeadSHA
			s.ProductTarget = s.TargetSHA
			if test.edit != nil {
				test.edit(&s)
			}
			if got := Decide(s, view, now).Kind; got != test.want {
				t.Fatalf("decision=%s, want %s", got, test.want)
			}
		})
	}
}

func TestCleanReAdmitsWhenProductGoverningIdentityIsUnknown(t *testing.T) {
	s := snapshot()
	s.Product = product.Clean()
	s.ProductHead = s.HeadSHA
	s.ProductTarget = s.TargetSHA
	s.ProductGoverning = "unknown"
	s.ProductGoverningKnown = false
	if got := Decide(s, View{}, time.Now()).Kind; got != Admit {
		t.Fatalf("decision=%s, want admit", got)
	}
}

func TestWaitFingerprintCurrency(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	s := snapshot()
	s.Product = product.Waiting()
	s.RequiredChecks = []Check{{Identity: "build", Conclusion: "pending"}}
	fingerprint := WaitFingerprint(s)
	failsafe := now.Add(time.Hour)
	view := View{Wait: &ledger.Wait{Fingerprint: fingerprint, FailsafeAt: &failsafe}}
	if got := Decide(s, view, now).Kind; got != Nothing {
		t.Fatalf("current wait = %s, want nothing", got)
	}
	s.RequiredChecks[0].Conclusion = "success"
	if got := Decide(s, view, now).Kind; got != Admit {
		t.Fatalf("changed check = %s, want admit", got)
	}
	s.RequiredChecks[0].Conclusion = "pending"
	if got := Decide(s, view, failsafe).Kind; got != Admit {
		t.Fatalf("expired failsafe = %s, want admit", got)
	}
}

func TestChangedWaitStillRequiresCurrentEligibility(t *testing.T) {
	s := snapshot()
	s.Product = product.Waiting()
	s.AuthorInScope = false
	view := View{Wait: &ledger.Wait{Fingerprint: "prior"}}
	if got := Decide(s, view, time.Now()).Kind; got != Nothing {
		t.Fatalf("changed wait for ineligible author=%s, want nothing", got)
	}
}

func TestPermissionPolicyBlockReentryUsesGoverningInputs(t *testing.T) {
	s := snapshot()
	s.Product = product.Blocked()
	s.ProductBlockKind = PermissionBlock
	s.ProductHead = s.HeadSHA
	s.ProductTarget = s.TargetSHA
	s.ProductBlockIdentity = BlockReentryIdentity(s)
	s.ProductBlockKnown = true
	if got := Decide(s, View{}, time.Now()).Kind; got != Nothing {
		t.Fatalf("current permission block=%s, want nothing", got)
	}
	s.GoverningIdentity = "policy-2"
	if got := Decide(s, View{}, time.Now()).Kind; got != Admit {
		t.Fatalf("changed permission policy=%s, want admit", got)
	}
}

func TestPermissionPolicyBlockReentersOnDeploymentReadinessChange(t *testing.T) {
	s := snapshot()
	s.Product = product.Blocked()
	s.ProductBlockKind = PermissionBlock
	s.ProductHead = s.HeadSHA
	s.ProductTarget = s.TargetSHA
	s.ProductBlockIdentity = BlockReentryIdentity(s)
	s.ProductBlockKnown = true
	s.DeploymentProfile = "profile-2"
	if got := Decide(s, View{}, time.Now()).Kind; got != Admit {
		t.Fatalf("changed deployment readiness=%s, want admit", got)
	}
}

func TestBlockedWithoutDiscriminatorReentersConservatively(t *testing.T) {
	s := snapshot()
	s.Product = product.Blocked()
	s.ProductHead = s.HeadSHA
	s.ProductTarget = s.TargetSHA
	if got := Decide(s, View{}, time.Now()).Kind; got != Admit {
		t.Fatalf("undiscriminated block=%s, want admit", got)
	}
}

func TestWaitFingerprintIsOrderIndependent(t *testing.T) {
	a := snapshot()
	a.RequiredChecks = []Check{{Identity: "test", Conclusion: "success"}, {Identity: "build", Conclusion: "pending"}}
	b := snapshot()
	b.RequiredChecks = []Check{{Identity: "build", Conclusion: "pending"}, {Identity: "test", Conclusion: "success"}}
	if WaitFingerprint(a) != WaitFingerprint(b) {
		t.Fatal("fingerprint depends on check response order")
	}
}
