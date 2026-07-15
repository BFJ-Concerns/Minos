package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
	"bfj/minos/internal/reconcile"
)

func forgeCommandAttempt(t *testing.T) (ServiceConfig, Facts, ledger.Lease) {
	t.Helper()
	cfg, _, facts := coordinationConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.Root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := "forge=\"local\"\nowner=\"owner\"\nrepo=\"subject\"\n[adaptation]\nbuild=\"true\"\ntest=\"true\"\nskill=\"skill\"\n[finding-disposition]\nmode=\"publish-through-p3\"\n[[eligibility]]\nauthors=[\"*\"]\n"
	if err := os.WriteFile(filepath.Join(cfg.Root, "repos", "subject.toml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(t.Context(), ledger.Lease{Key: coordinationKey(facts), ObservedHead: facts.HeadSHA, ObservedTarget: "target-1", Unit: "unit", Workspace: t.TempDir()}, 2)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_CONFIG", cfg.Root)
	t.Setenv("MINOS_FORGE", facts.Forge)
	t.Setenv("MINOS_OWNER", facts.Owner)
	t.Setenv("MINOS_REPO_NAME", facts.Repo)
	t.Setenv("MINOS_PR", facts.PR)
	t.Setenv("MINOS_HEAD_SHA", facts.HeadSHA)
	t.Setenv("MINOS_TARGET_SHA", "target-1")
	t.Setenv("MINOS_BASE_REF", facts.BaseRef)
	t.Setenv("MINOS_ATTEMPT_TOKEN", strconv.FormatInt(lease.Token, 10))
	t.Setenv("MINOS_WORKSPACE", lease.Workspace)
	return cfg, facts, lease
}

func TestForgeCommandPublishesOnlyNamedProductState(t *testing.T) {
	cfg, _, _ := forgeCommandAttempt(t)
	captured := filepath.Join(t.TempDir(), "status")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	var out bytes.Buffer
	if err := ForgeCommand(t.Context(), []string{"status", "working"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil || !strings.Contains(string(data), "Minos pending Reviewing changes") {
		t.Fatalf("status arguments=%q err=%v", data, err)
	}
	if err := ForgeCommand(t.Context(), []string{"status", "invented"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("invented product state was accepted")
	}
}

func TestForgeCommandReviewAddsAuthenticatedProductRecord(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	captured := filepath.Join(t.TempDir(), "review")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-post-review"), "#!/bin/sh\ncat >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	body := filepath.Join(t.TempDir(), "body.md")
	comments := filepath.Join(t.TempDir(), "comments.json")
	if err := os.WriteFile(body, []byte("One material finding.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(comments, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgeCommand(t.Context(), []string{"review", "material", body, comments}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		State forge.ReviewVerdict `json:"state"`
		Body  string              `json:"body"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != forge.ReviewRequestChanges {
		t.Fatalf("review verdict=%q", payload.State)
	}
	record, ok := product.TrailingRecord(payload.Body)
	if !ok || record["head"] != facts.HeadSHA || record["target"] != "target-1" || record["governing"] == "" || record["block-kind"] != string(reconcile.FindingBlock) {
		t.Fatalf("review product record=%#v body=%q", record, payload.Body)
	}
}

func TestForgeCommandMergeReadsSnapshotAndAddsCleanupObligation(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	mergeCalls := filepath.Join(t.TempDir(), "merge-calls")
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "guarded-merge"), "#!/bin/sh\nprintf 'call\\n' >>"+strconv.Quote(mergeCalls)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "merged", Merged: true,
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "merged-target", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(cfg.Forges["local"].Adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	if err := ForgeCommand(t.Context(), []string{"merge", "squash"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(mergeCalls)
	if err != nil || strings.Count(string(calls), "call") != 1 {
		t.Fatalf("merge calls=%q err=%v", calls, err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cleanups, err := store.ListCleanup(t.Context())
	if err != nil || len(cleanups) != 1 || cleanups[0].MergedHead != facts.HeadSHA || cleanups[0].Branch != "change" {
		t.Fatalf("cleanup obligations=%#v err=%v", cleanups, err)
	}
}

func TestServiceAuthoredMergeAdvanceCarriesEveryFenceThroughTeardown(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	reaction, removals := presenceProbe(t, cfg)
	if err := os.WriteFile(reaction, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	adaptation := cfg.Forges["local"].Adaptation
	writeScript(t, filepath.Join(adaptation, "guarded-merge"), "#!/bin/sh\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	merged := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "merged", Merged: true,
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-2", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	statusArgs := filepath.Join(t.TempDir(), "status-args")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(statusArgs)+"\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")
	writeScript(t, filepath.Join(adaptation, "delete-branch"), "#!/bin/sh\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")

	if err := ForgeCommand(t.Context(), []string{"merge", "squash"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "advance", facts.HeadSHA, "target-1", facts.HeadSHA, "target-2"}); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	current, err := currentAttempt(t.Context(), cfg, facts, store, lease.Token)
	if err != nil || !current {
		t.Fatalf("advanced attempt current=%v err=%v", current, err)
	}
	owned, err := store.Owns(t.Context(), lease.Key, lease.Token)
	if err != nil || !owned {
		t.Fatalf("advanced token owns=%v err=%v", owned, err)
	}

	var failures []error
	if err := ForgeCommand(t.Context(), []string{"status", "merged"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		failures = append(failures, err)
	}
	if err := ForgeCommand(t.Context(), []string{"cleanup"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		failures = append(failures, err)
	}
	forgeConfig := cfg.Forges[facts.Forge]
	adapt, err := NewAdaptation(forgeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseRun(t.Context(), adapt, facts, store, lease.Token); err != nil {
		failures = append(failures, err)
	}
	if err := closeRunLease(t.Context(), cfg, facts, store, lease.Token); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		t.Errorf("post-merge teardown failures: %v", errors.Join(failures...))
	}
	if _, found, err := store.Lease(t.Context(), lease.Key); err != nil || found {
		t.Errorf("lease remains found=%v err=%v", found, err)
	}
	if cleanup, found, err := store.Cleanup(t.Context(), lease.Key); err != nil || found {
		t.Errorf("cleanup remains=%#v found=%v err=%v", cleanup, found, err)
	}
	store.Close()
	assertPresenceClosed(t, reaction, removals)
	arguments, err := os.ReadFile(statusArgs)
	if err != nil || !strings.Contains(string(arguments), "head-1 target-2 Minos Minos success Merged") {
		t.Errorf("terminal status arguments=%q err=%v", arguments, err)
	}
}

func TestUnrelatedTargetMovementRemainsStaleWithoutOwnerAdvance(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	moved := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-2", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(moved)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nif [ \"$5\" = target-2 ]; then\n  printf '{\"outcome\":\"applied\"}\\n'\nelse\n  printf '{\"outcome\":\"rejected\",\"reason\":\"stale target\"}\\n'\nfi\n")
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := currentAttempt(t.Context(), cfg, facts, store, lease.Token)
	if err != nil || current {
		t.Fatalf("unadvanced attempt current=%v err=%v", current, err)
	}
	if err := ForgeCommand(t.Context(), []string{"status", "merged"}, strings.NewReader(""), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "stale target") {
		t.Fatalf("unrelated movement status error=%v, want stale target rejection", err)
	}
	currentLease, found, err := store.Lease(t.Context(), lease.Key)
	if err != nil || !found || currentLease.ObservedTarget != "target-1" {
		t.Fatalf("unrelated movement changed lease=%#v found=%v err=%v", currentLease, found, err)
	}
}

func TestRunGuardBeginClearsPriorWaitAndPublishesWorking(t *testing.T) {
	cfg, facts, lease := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-1", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	captured := filepath.Join(t.TempDir(), "status")
	writeScript(t, filepath.Join(adaptation, "guarded-set-status"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >"+strconv.Quote(captured)+"\nprintf '{\"outcome\":\"applied\"}\\n'\n")
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.SetWait(t.Context(), ledger.Wait{Key: lease.Key, Fingerprint: "old"}, lease.Token); err != nil || !set {
		t.Fatalf("SetWait()=%v err=%v", set, err)
	}
	store.Close()

	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "begin"}); err != nil {
		t.Fatal(err)
	}
	store, err = ledger.Open(ledgerPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, found, err := store.Wait(t.Context(), lease.Key); err != nil || found {
		t.Fatalf("prior wait remains found=%v err=%v", found, err)
	}
	status, err := os.ReadFile(captured)
	if err != nil || !strings.Contains(string(status), "Minos pending Reviewing changes") {
		t.Fatalf("working status=%q err=%v", status, err)
	}
}

func TestRunGuardRevalidateChecksCurrentOwnershipAndLifecycleIndex(t *testing.T) {
	cfg, facts, _ := forgeCommandAttempt(t)
	adaptation := cfg.Forges["local"].Adaptation
	snapshot := forge.Snapshot{
		AuthenticatedUser: "Minos", Repository: "owner/subject", PullRequest: 7, State: "open",
		Author: "alice", HeadSHA: facts.HeadSHA, HeadBranch: "change", HeadRepository: "owner/subject",
		TargetSHA: "target-1", TargetBranch: "main", TargetRepository: "owner/subject", DefaultBranch: "main",
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptation, "snapshot"), "#!/bin/sh\nprintf '%s\\n' "+strconv.Quote(string(data))+"\n")
	runDir := filepath.Join(cfg.Runs.Dir, "attempt")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(runDir, "lifecycle-index.md")
	if err := os.WriteFile(index, []byte("# Current lifecycle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "resolved-lead.json"), []byte("[{\"resolved_model\":\"pinned\"}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINOS_RUN_DIR", runDir)
	t.Setenv("MINOS_GOVERNING_IDENTITY", governingIdentity(repo))
	t.Setenv("MINOS_DEPLOYMENT_PROFILE", deploymentIdentity(cfg, repo))
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "revalidate", index}); err != nil {
		t.Fatal(err)
	}
	if err := RunGuardCommand(t.Context(), []string{"--config", cfg.Root, "revalidate", filepath.Join(t.TempDir(), "outside")}); err == nil {
		t.Fatal("revalidation accepted an index outside the attempt directory")
	}
}
