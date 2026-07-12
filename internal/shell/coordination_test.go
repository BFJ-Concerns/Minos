package shell

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bfj/minos/internal/ledger"
	"bfj/minos/internal/reconcile"
)

func coordinationConfig(t *testing.T) (ServiceConfig, RepoConfig, Facts) {
	t.Helper()
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	adapt := filepath.Join(root, "adapt")
	if err := os.MkdirAll(adapt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adapt, "add-reaction"), "#!/bin/sh\nexit 0\n")
	writeScript(t, filepath.Join(adapt, "remove-reaction"), "#!/bin/sh\nexit 0\n")
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := "[service]\nbot-login=\"Minos\"\n[listener]\nbind=\":0\"\n[forges.local]\nadaptation=\"" + adapt + "\"\napi-base=\"http://forge.invalid\"\nwebhook-secret-file=\"" + secret + "\"\ncredential-file=\"" + secret + "\"\n[runs]\ndir=\"" + runs + "\"\nmax-concurrent=2\n[sweep]\nliveness-threshold=\"6s\"\n"
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(service), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := RepoConfig{Forge: "local", Owner: "owner", Repo: "subject"}
	repo.Adaptation.Skill = "skill"
	repo.Eligibility = []EligibilityRule{{Authors: []string{"*"}}}
	facts := Facts{Forge: "local", Owner: "owner", Repo: "subject", PR: "7", HeadSHA: "head-1", BaseRef: "main", Author: "alice", Open: true}
	return cfg, repo, facts
}

func presenceProbe(t *testing.T, cfg ServiceConfig) (reaction, removals string) {
	t.Helper()
	reaction = filepath.Join(t.TempDir(), "eyes-present")
	removals = filepath.Join(t.TempDir(), "removals")
	adapt := cfg.Forges["local"].Adaptation
	writeScript(t, filepath.Join(adapt, "add-reaction"), "#!/bin/sh\ntouch "+strconv.Quote(reaction)+"\n")
	writeScript(t, filepath.Join(adapt, "remove-reaction"), "#!/bin/sh\nrm -f "+strconv.Quote(reaction)+"\nprintf 'removed\\n' >> "+strconv.Quote(removals)+"\n")
	return reaction, removals
}

func assertPresenceClosed(t *testing.T, reaction, removals string) {
	t.Helper()
	if _, err := os.Stat(reaction); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eyes reaction remains: %v", err)
	}
	data, err := os.ReadFile(removals)
	if err != nil || !strings.Contains(string(data), "removed") {
		t.Fatalf("reaction removal was not observed: data=%q err=%v", data, err)
	}
}

func TestDuplicateLaunchCollapsesAtLeaseAcquire(t *testing.T) {
	cfg, repo, facts := coordinationConfig(t)
	original := systemdRunCommand
	defer func() { systemdRunCommand = original }()
	var launches atomic.Int32
	systemdRunCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		launches.Add(1)
		return exec.CommandContext(ctx, "true")
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- SpawnRun(context.Background(), cfg, repo, facts, "target-1", "pr-opened")
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("detached launches=%d, want 1", got)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	leases, err := store.ListLeases(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases=%v err=%v", leases, err)
	}
}

func TestSweepRanksDrainBeforeWidenAndOldestWithinPeers(t *testing.T) {
	values := []sweepCandidate{{facts: Facts{PR: "9"}}, {facts: Facts{PR: "7"}, drain: true}, {facts: Facts{PR: "3"}}, {facts: Facts{PR: "2"}, drain: true}}
	rankCandidates(values)
	got := []string{values[0].facts.PR, values[1].facts.PR, values[2].facts.PR, values[3].facts.PR}
	want := []string{"2", "7", "3", "9"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want=%v", got, want)
		}
	}
}

func TestFailedDetachedLaunchReleasesCapacity(t *testing.T) {
	cfg, repo, facts := coordinationConfig(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := systemdRunCommand
	defer func() { systemdRunCommand = original }()
	systemdRunCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	if err := SpawnRun(t.Context(), cfg, repo, facts, "target-1", "pr-opened"); err == nil {
		t.Fatal("SpawnRun succeeded")
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	leases, err := store.ListLeases(t.Context())
	if err != nil || len(leases) != 0 {
		t.Fatalf("leases=%v err=%v", leases, err)
	}
	assertPresenceClosed(t, reaction, removals)
}

func TestReplacementEmptiesUnitBeforeAllocatingToken(t *testing.T) {
	cfg, repo, facts := coordinationConfig(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "old-unit", Workspace: t.TempDir()}, 2)
	if err != nil {
		t.Fatal(err)
	}
	originalSystemctl := systemctlCommand
	originalRun := systemdRunCommand
	defer func() { systemctlCommand = originalSystemctl; systemdRunCommand = originalRun }()
	var calls []string
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, args[1])
		current, found, err := store.Lease(context.Background(), lease.Key)
		if err != nil || !found || current.Token != lease.Token {
			t.Fatalf("token changed before %s: %#v %v", args[1], current, err)
		}
		if args[1] == "show" {
			return exec.CommandContext(ctx, "sh", "-c", "printf inactive")
		}
		return exec.CommandContext(ctx, "true")
	}
	systemdRunCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}
	snapshot := reconcile.ForgeSnapshot{Key: lease.Key, HeadSHA: "head-2", TargetSHA: "target-2", Open: true, AuthorInScope: true}
	if err := executeDecision(t.Context(), cfg, repo, facts, snapshot, reconcile.Decision{Kind: reconcile.Replace}, store, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "stop" || calls[1] != "show" {
		t.Fatalf("systemctl calls=%v, want stop then show", calls)
	}
	replacement, found, err := store.Lease(t.Context(), lease.Key)
	if err != nil || !found || replacement.Token <= lease.Token {
		t.Fatalf("replacement=%#v found=%v err=%v", replacement, found, err)
	}
	assertPresenceClosed(t, reaction, removals)
}

func TestIneligibleReapClosesPresenceAndLeaseTogether(t *testing.T) {
	cfg, repo, facts := coordinationConfig(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "old-unit", Workspace: t.TempDir()}, 2)
	if err != nil {
		t.Fatal(err)
	}
	originalSystemctl := systemctlCommand
	defer func() { systemctlCommand = originalSystemctl }()
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if args[1] == "show" {
			return exec.CommandContext(ctx, "sh", "-c", "printf inactive")
		}
		return exec.CommandContext(ctx, "true")
	}
	snapshot := reconcile.ForgeSnapshot{Key: lease.Key, Open: true, AuthorInScope: false}
	if err := executeDecision(t.Context(), cfg, repo, facts, snapshot, reconcile.Decision{Kind: reconcile.Replace}, store, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lease(t.Context(), lease.Key); err != nil || found {
		t.Fatalf("lease remains found=%v err=%v", found, err)
	}
	assertPresenceClosed(t, reaction, removals)
}

func TestFailedSuccessorLaunchClosesPresenceAndReplacementLease(t *testing.T) {
	cfg, repo, facts := coordinationConfig(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "old-unit", Workspace: t.TempDir()}, 2)
	if err != nil {
		t.Fatal(err)
	}
	originalSystemctl := systemctlCommand
	originalRun := systemdRunCommand
	defer func() { systemctlCommand = originalSystemctl; systemdRunCommand = originalRun }()
	systemctlCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if args[1] == "show" {
			return exec.CommandContext(ctx, "sh", "-c", "printf inactive")
		}
		return exec.CommandContext(ctx, "true")
	}
	systemdRunCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	snapshot := reconcile.ForgeSnapshot{Key: lease.Key, HeadSHA: "head-2", TargetSHA: "target-2", Open: true, AuthorInScope: true}
	if err := executeDecision(t.Context(), cfg, repo, facts, snapshot, reconcile.Decision{Kind: reconcile.Replace}, store, os.Stderr); err == nil {
		t.Fatal("replacement launch succeeded")
	}
	if _, found, err := store.Lease(t.Context(), lease.Key); err != nil || found {
		t.Fatalf("replacement lease remains found=%v err=%v", found, err)
	}
	assertPresenceClosed(t, reaction, removals)
	data, err := os.ReadFile(removals)
	if err != nil || strings.Count(string(data), "removed") != 2 {
		t.Fatalf("old and successor removals=%q err=%v", data, err)
	}
}

func TestRunWrapReleasesLeaseOnEveryExit(t *testing.T) {
	tests := []struct {
		name    string
		prepare string
		body    func(context.Context) (*exec.Cmd, error)
	}{
		{"success", "#!/bin/sh\nexit 0\n", func(ctx context.Context) (*exec.Cmd, error) { return exec.CommandContext(ctx, "true"), nil }},
		{"prepare failure", "#!/bin/sh\nexit 1\n", func(ctx context.Context) (*exec.Cmd, error) { return exec.CommandContext(ctx, "true"), nil }},
		{"body failure", "#!/bin/sh\nexit 0\n", func(ctx context.Context) (*exec.Cmd, error) { return exec.CommandContext(ctx, "false"), nil }},
		{"panic", "#!/bin/sh\nexit 0\n", func(context.Context) (*exec.Cmd, error) { panic("test panic") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, _, facts := coordinationConfig(t)
			reaction, removals := presenceProbe(t, cfg)
			if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
				t.Fatal(err)
			}
			adapt := cfg.Forges["local"].Adaptation
			writeScript(t, filepath.Join(adapt, "prepare-workspace"), test.prepare)
			store, err := ledger.Open(ledgerPath(cfg))
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(t.TempDir(), "workspace")
			lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "unit", Workspace: workspace}, 2)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			t.Setenv("MINOS_CONFIG", cfg.Root)
			t.Setenv("MINOS_FORGE", facts.Forge)
			t.Setenv("MINOS_OWNER", facts.Owner)
			t.Setenv("MINOS_REPO_NAME", facts.Repo)
			t.Setenv("MINOS_PR", facts.PR)
			t.Setenv("MINOS_HEAD_SHA", facts.HeadSHA)
			t.Setenv("MINOS_TARGET_SHA", "target-1")
			t.Setenv("MINOS_BASE_REF", facts.BaseRef)
			t.Setenv("MINOS_ATTEMPT_TOKEN", fmtInt(lease.Token))
			t.Setenv("MINOS_RUN_DIR", RunDir(cfg.Runs.Dir, facts, lease.Token))
			t.Setenv("MINOS_WORKSPACE", workspace)
			t.Setenv("MINOS_DIFF", filepath.Join(t.TempDir(), "diff"))
			t.Setenv("MINOS_UNIT", "unit")
			original := runBodyCommandForWrap
			runBodyCommandForWrap = test.body
			defer func() { runBodyCommandForWrap = original }()
			_ = RunWrapCommand(t.Context(), []string{"--config", cfg.Root})
			verify, err := ledger.Open(ledgerPath(cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer verify.Close()
			if _, found, err := verify.Lease(t.Context(), lease.Key); err != nil || found {
				t.Fatalf("lease remains found=%v err=%v", found, err)
			}
			assertPresenceClosed(t, reaction, removals)
		})
	}
}

func TestHeartbeatStopsAfterLeaseReplacement(t *testing.T) {
	now := time.Now()
	store, err := ledger.OpenWithClock(filepath.Join(t.TempDir(), "ledger.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := ledger.Key{Forge: "f", Owner: "o", Repo: "r", PR: "1"}
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: key, ObservedHead: "h", ObservedTarget: "t", Unit: "u", Workspace: "w"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go renewHeartbeat(ctx, store, key, lease.Token, 5*time.Millisecond, os.Stderr, done)
	if _, err := store.ReplaceLease(t.Context(), lease.Token, ledger.Lease{Key: key, ObservedHead: "h2", ObservedTarget: "t2", Unit: "u2", Workspace: "w2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("old-token heartbeat did not stop")
	}
	cancel()
}

func TestReceiverReconcileLaunchExitReleaseJourney(t *testing.T) {
	cfg, _, _ := coordinationConfig(t)
	adapt := cfg.Forges["local"].Adaptation
	writeScript(t, filepath.Join(adapt, "normalise-event"), "#!/bin/sh\nprintf 'OCCASION=pr-opened\\nOWNER=owner\\nREPO=subject\\nPR=7\\nHEAD_SHA=head-1\\nBASE_REF=main\\nAUTHOR=alice\\nDRAFT=false\\n'\n")
	writeScript(t, filepath.Join(adapt, "get-pr-facts"), "#!/bin/sh\nprintf 'OCCASION=reconcile\\nOWNER=owner\\nREPO=subject\\nPR=7\\nHEAD_SHA=head-1\\nBASE_REF=main\\nAUTHOR=alice\\nDRAFT=false\\n'\n")
	writeScript(t, filepath.Join(adapt, "get-statuses"), "#!/bin/sh\nprintf '[]\\n'\n")
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := "forge=\"local\"\nowner=\"owner\"\nrepo=\"subject\"\n[adaptation]\nbuild=\"true\"\ntest=\"true\"\nskill=\"skill\"\n[[eligibility]]\nauthors=[\"*\"]\n"
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	original := systemdRunCommand
	defer func() { systemdRunCommand = original }()
	var launches atomic.Int32
	systemdRunCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		launches.Add(1)
		return exec.CommandContext(ctx, "true")
	}
	body := []byte(`{"event":"opened"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(string(body)))
			request.Header.Set("X-Forgejo-Signature", signature)
			response := httptest.NewRecorder()
			errs <- handleHook(context.Background(), cfg, response, request)
			responses <- response
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(responses)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for response := range responses {
		if response.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if launches.Load() != 1 {
		t.Fatalf("launches=%d, want 1", launches.Load())
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	leases, err := store.ListLeases(t.Context())
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases=%v err=%v", leases, err)
	}
	if _, err := store.ReleaseLease(t.Context(), leases[0].Key, leases[0].Token); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if leases, err = store.ListLeases(t.Context()); err != nil || len(leases) != 0 {
		t.Fatalf("released leases=%v err=%v", leases, err)
	}
}

func TestMutationFenceRejectsSupersededToken(t *testing.T) {
	cfg, _, facts := coordinationConfig(t)
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "u", Workspace: "w"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceLease(t.Context(), first.Token, ledger.Lease{Key: first.Key, ObservedHead: "head-2", ObservedTarget: "target-2", Unit: "u2", Workspace: "w2"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_HEAD_SHA", facts.HeadSHA)
	t.Setenv("MINOS_TARGET_SHA", "target-1")
	t.Setenv("MINOS_ATTEMPT_TOKEN", fmtInt(first.Token))
	if err := guardAdaptationMutation(t.Context()); !errors.Is(err, ledger.ErrNotOwner) {
		t.Fatalf("guard error=%v, want ErrNotOwner", err)
	}
}

func fmtInt(value int64) string { return strconv.FormatInt(value, 10) }
