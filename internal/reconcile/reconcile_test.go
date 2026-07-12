package reconcile

import (
	"testing"
	"time"

	"bfj/minos/internal/ledger"
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

func TestStoppedAndCleanServedness(t *testing.T) {
	now := time.Now()
	for _, state := range []ProductState{ProductStopped, ProductBlocked, ProductPartial, ProductClean, ProductCleanLimited} {
		t.Run(string(state), func(t *testing.T) {
			s := snapshot()
			s.Product = state
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

func TestCleanReAdmitsWhenProductGoverningIdentityIsUnknown(t *testing.T) {
	s := snapshot()
	s.Product = ProductClean
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
	s.Product = ProductWaiting
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

func TestWaitFingerprintIsOrderIndependent(t *testing.T) {
	a := snapshot()
	a.RequiredChecks = []Check{{Identity: "test", Conclusion: "success"}, {Identity: "build", Conclusion: "pending"}}
	b := snapshot()
	b.RequiredChecks = []Check{{Identity: "build", Conclusion: "pending"}, {Identity: "test", Conclusion: "success"}}
	if WaitFingerprint(a) != WaitFingerprint(b) {
		t.Fatal("fingerprint depends on check response order")
	}
}
