package incidents

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRetryBurstUpdatesOneIncident(t *testing.T) {
	store := NewFileStore(t.TempDir())
	key := Key{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: "17", Category: "stale-oauth"}
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		incident, err := store.Raise(context.Background(), Event{Key: key, Diagnostic: "Claude OAuth stale; re-authenticate deployment user", LogPath: filepath.Join("runs", "17", "run.log"), Attempt: i + 1, At: now.Add(time.Duration(i) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if incident.Updates != i+1 {
			t.Fatalf("update %d recorded count %d", i+1, incident.Updates)
		}
	}
	all, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Updates != 5 || all[0].Attempt != 5 {
		t.Fatalf("got %#v", all)
	}
}

func TestConcurrentRetryBurstRetainsEveryUpdate(t *testing.T) {
	store := NewFileStore(t.TempDir())
	key := Key{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: "19", Category: "engine"}
	var wait sync.WaitGroup
	errors := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Raise(context.Background(), Event{Key: key, Diagnostic: "engine unavailable", LogPath: "runs/19/run.log", At: time.Now()})
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Updates != 20 {
		t.Fatalf("concurrent burst was not deduplicated: %#v", all)
	}
}

func TestAcknowledgementAndRecoveryArePersisted(t *testing.T) {
	store := NewFileStore(t.TempDir())
	key := Key{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: "3", Category: "toolchain"}
	if _, err := store.Raise(context.Background(), Event{Key: key, Diagnostic: "git unavailable", LogPath: "runs/3/run.log", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acknowledge(context.Background(), key, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Recover(context.Background(), key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.AcknowledgedBy != "operator" || got.RecoveredAt == nil || got.State != StateRecovered {
		t.Fatalf("unexpected incident: %#v", got)
	}
}

func TestRecurrenceReopensRecoveredIncident(t *testing.T) {
	store := NewFileStore(t.TempDir())
	key := Key{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: "8", Category: "engine"}
	event := Event{Key: key, Diagnostic: "engine unavailable", LogPath: "runs/8/run.log", At: time.Now()}
	if _, err := store.Raise(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acknowledge(context.Background(), key, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Recover(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Raise(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != StateOpen || reopened.RecoveredAt != nil || reopened.AcknowledgedAt != nil || reopened.AcknowledgedBy != "" || reopened.Updates != 2 {
		t.Fatalf("incident did not reopen cleanly: %#v", reopened)
	}
}

func TestAttemptChangesUpdateOneLedgerAlignedIdentity(t *testing.T) {
	store := NewFileStore(t.TempDir())
	key := Key{Forge: "forgejo", Owner: "owner", Repo: "repo", PullRequest: "22", Category: "stale-oauth"}
	for _, attempt := range []int{1, 2, 3} {
		if _, err := store.Raise(context.Background(), Event{Key: key, Attempt: attempt, ObservedHead: "head", Diagnostic: "OAuth stale", LogPath: "runs/22/run.log", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	incidents, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 || incidents[0].Updates != 3 || incidents[0].Attempt != 3 {
		t.Fatalf("attempt entered dedupe identity: %#v", incidents)
	}
}
