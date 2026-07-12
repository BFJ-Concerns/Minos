package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store, err := OpenWithClock(filepath.Join(t.TempDir(), "ledger.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testKey() Key { return Key{Forge: "forgejo", Owner: "BFJ", Repo: "Minos", PR: "42"} }

func testLease(key Key) Lease {
	return Lease{Key: key, ObservedHead: "head-1", ObservedTarget: "target-1", Unit: "minos-run-42", Workspace: "/tmp/work-42"}
}

func TestAcquireLeaseIsAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	const contenders = 12
	stores := make([]*Store, contenders)
	for i := range stores {
		var err error
		stores[i], err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[i].Close()
	}

	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			_, err := store.AcquireLease(context.Background(), testLease(testKey()), 3)
			results <- err
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrNotOwner) {
			t.Fatalf("unexpected acquire result: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful acquires = %d, want 1", successes)
	}
	leases, err := stores[0].ListLeases(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases = %v, err = %v", leases, err)
	}
}

func TestConcurrentFirstOpenBootstrapsBeforeAtomicAdmission(t *testing.T) {
	root := t.TempDir()
	for attempt := range 100 {
		path := filepath.Join(root, fmt.Sprintf("ledger-%03d.db", attempt))
		start := make(chan struct{})
		stores := make(chan *Store, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				store, err := Open(path)
				if err == nil {
					stores <- store
				}
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(stores)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("attempt %d first open: %v", attempt, err)
			}
		}
		opened := make([]*Store, 0, 2)
		for store := range stores {
			opened = append(opened, store)
		}
		if len(opened) != 2 {
			t.Fatalf("attempt %d opened %d stores, want 2", attempt, len(opened))
		}
		for _, store := range opened {
			_ = store.Close()
		}
	}
}

func TestAdmissionCapacityIsLeaseCardinality(t *testing.T) {
	store := testStore(t, time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC))
	first := testKey()
	if _, err := store.AcquireLease(t.Context(), testLease(first), 1); err != nil {
		t.Fatal(err)
	}
	second := Key{Forge: "forgejo", Owner: "BFJ", Repo: "Minos", PR: "43"}
	if _, err := store.AcquireLease(t.Context(), testLease(second), 1); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second acquire error = %v, want ErrCapacity", err)
	}
}

func TestReplacementFencesOldTokenAndReleaseIsTokenGuarded(t *testing.T) {
	store := testStore(t, time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC))
	key := testKey()
	first, err := store.AcquireLease(t.Context(), testLease(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	replacement := testLease(key)
	replacement.ObservedHead = "head-2"
	second, err := store.ReplaceLease(t.Context(), first.Token, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if second.Token <= first.Token {
		t.Fatalf("replacement token %d is not greater than %d", second.Token, first.Token)
	}
	if owns, err := store.Owns(t.Context(), key, first.Token); err != nil || owns {
		t.Fatalf("old token owns = %v, err = %v", owns, err)
	}
	if released, err := store.ReleaseLease(t.Context(), key, first.Token); err != nil || released {
		t.Fatalf("old-token release = %v, err = %v", released, err)
	}
	if owns, err := store.Owns(t.Context(), key, second.Token); err != nil || !owns {
		t.Fatalf("new token owns = %v, err = %v", owns, err)
	}
}

func TestWaitOutlivesReleasedLease(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key := testKey()
	lease, err := store.AcquireLease(t.Context(), testLease(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	failsafe := now.Add(2 * time.Hour)
	if set, err := store.SetWait(t.Context(), Wait{Key: key, Fingerprint: "fingerprint", FailsafeAt: &failsafe}, lease.Token); err != nil || !set {
		t.Fatalf("set wait=%v err=%v", set, err)
	}
	if _, err := store.ReleaseLease(t.Context(), key, lease.Token); err != nil {
		t.Fatal(err)
	}
	wait, found, err := store.Wait(t.Context(), key)
	if err != nil || !found || wait.Fingerprint != "fingerprint" || wait.FailsafeAt == nil || !wait.FailsafeAt.Equal(failsafe) {
		t.Fatalf("wait = %#v, found = %v, err = %v", wait, found, err)
	}
}

func TestBackoffIsExponentialWithoutTerminalCap(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	lease, err := store.AcquireLease(t.Context(), testLease(testKey()), 1)
	if err != nil {
		t.Fatal(err)
	}
	wants := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, want := range wants {
		backoff, err := store.RecordFailure(t.Context(), testKey(), lease.Token)
		if err != nil {
			t.Fatal(err)
		}
		if got := backoff.NextDueAt.Sub(backoff.LastFailureAt); got != want {
			t.Fatalf("attempt %d delay = %s, want %s", i+1, got, want)
		}
	}
}

func TestPacingMutationsRejectStaleToken(t *testing.T) {
	store := testStore(t, time.Now())
	key := testKey()
	first, err := store.AcquireLease(t.Context(), testLease(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	failsafe := time.Now().Add(time.Hour)
	if set, err := store.SetWait(t.Context(), Wait{Key: key, Fingerprint: "current", FailsafeAt: &failsafe}, first.Token); err != nil || !set {
		t.Fatalf("initial wait set=%v err=%v", set, err)
	}
	if _, err := store.RecordFailure(t.Context(), key, first.Token); err != nil {
		t.Fatal(err)
	}
	replacement := testLease(key)
	replacement.ObservedHead = "head-2"
	second, err := store.ReplaceLease(t.Context(), first.Token, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.SetWait(t.Context(), Wait{Key: key, Fingerprint: "stale"}, first.Token); err != nil || set {
		t.Fatalf("stale wait set=%v err=%v", set, err)
	}
	if cleared, err := store.ClearWait(t.Context(), key, first.Token); err != nil || cleared {
		t.Fatalf("stale wait clear=%v err=%v", cleared, err)
	}
	if _, err := store.RecordFailure(t.Context(), key, first.Token); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("stale failure err=%v, want ErrNotOwner", err)
	}
	if cleared, err := store.ClearBackoff(t.Context(), key, first.Token); err != nil || cleared {
		t.Fatalf("stale backoff clear=%v err=%v", cleared, err)
	}
	wait, found, err := store.Wait(t.Context(), key)
	if err != nil || !found || wait.Fingerprint != "current" {
		t.Fatalf("wait=%#v found=%v err=%v", wait, found, err)
	}
	backoff, found, err := store.Backoff(t.Context(), key)
	if err != nil || !found || backoff.Attempt != 1 {
		t.Fatalf("backoff=%#v found=%v err=%v", backoff, found, err)
	}
	if set, err := store.SetWait(t.Context(), Wait{Key: key, Fingerprint: "successor"}, second.Token); err != nil || !set {
		t.Fatalf("successor wait set=%v err=%v", set, err)
	}
}

func TestClearanceRequiresCurrentObservedPair(t *testing.T) {
	store := testStore(t, time.Now())
	key := testKey()
	lease, err := store.AcquireLease(t.Context(), testLease(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.SetClearance(t.Context(), key, lease.Token, "wrong", "target-1"); err != nil || set {
		t.Fatalf("wrong-pair clearance = %v, err = %v", set, err)
	}
	if set, err := store.SetClearance(t.Context(), key, lease.Token, "head-1", "target-1"); err != nil || !set {
		t.Fatalf("current-pair clearance = %v, err = %v", set, err)
	}
}

func TestOwnerCanAdvanceObservedPairAndInvalidatesClearance(t *testing.T) {
	store := testStore(t, time.Now())
	key := testKey()
	lease, err := store.AcquireLease(t.Context(), testLease(key), 1)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.SetClearance(t.Context(), key, lease.Token, "head-1", "target-1"); err != nil || !set {
		t.Fatalf("set=%v err=%v", set, err)
	}
	if updated, err := store.UpdateObservedPair(t.Context(), key, lease.Token, "wrong", "target-1", "head-2", "target-2"); err != nil || updated {
		t.Fatalf("wrong-pair update=%v err=%v", updated, err)
	}
	if updated, err := store.UpdateObservedPair(t.Context(), key, lease.Token, "head-1", "target-1", "head-2", "target-2"); err != nil || !updated {
		t.Fatalf("current-pair update=%v err=%v", updated, err)
	}
	current, found, err := store.Lease(t.Context(), key)
	if err != nil || !found || current.ObservedHead != "head-2" || current.ObservedTarget != "target-2" || current.ClearanceHead != "" {
		t.Fatalf("lease=%#v found=%v err=%v", current, found, err)
	}
}

func TestIncidentIdentityDeduplicatesRetries(t *testing.T) {
	store := testStore(t, time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC))
	incident := Incident{Key: testKey(), Category: "engine-auth", ObservedHead: "head-1", LogLocation: "/logs/attempt"}
	first, err := store.UpsertIncident(t.Context(), incident)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertIncident(t.Context(), incident)
	if err != nil {
		t.Fatal(err)
	}
	if first.RetryCount != 1 || second.RetryCount != 2 || !first.FirstAt.Equal(second.FirstAt) {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if acked, err := store.AckIncident(t.Context(), testKey(), "engine-auth"); err != nil || !acked {
		t.Fatalf("acked=%v err=%v", acked, err)
	}
}

func TestCleanupObligationPersistsUntilGuardedSuccess(t *testing.T) {
	store := testStore(t, time.Now())
	cleanup := Cleanup{Key: testKey(), MergedHead: "head-1", Branch: "feature"}
	if err := store.AddCleanup(t.Context(), cleanup); err != nil {
		t.Fatal(err)
	}
	if err := store.BumpCleanupAttempt(t.Context(), cleanup.Key); err != nil {
		t.Fatal(err)
	}
	values, err := store.ListCleanup(t.Context())
	if err != nil || len(values) != 1 || values[0].Attempts != 1 {
		t.Fatalf("cleanup=%v err=%v", values, err)
	}
	if err := store.RemoveCleanup(t.Context(), cleanup.Key); err != nil {
		t.Fatal(err)
	}
	values, err = store.ListCleanup(t.Context())
	if err != nil || len(values) != 0 {
		t.Fatalf("remaining=%v err=%v", values, err)
	}
}

func TestCorruptLedgerRefusesToOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	if err := os.WriteFile(path, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrLedger) {
		t.Fatalf("Open error = %v, want ErrLedger", err)
	}
}
