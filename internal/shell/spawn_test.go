package shell

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestSpawnRunReportsSuppressedForActiveUnit(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var commands []string
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		commands = append(commands, name)
		return []byte("minos-other-repo-pr9.service loaded active running Minos lead\n"), nil
	}

	cfg := ServiceConfig{}
	cfg.Runs.Dir = t.TempDir()
	outcome, err := SpawnRun(t.Context(), cfg, RepoConfig{}, Facts{Owner: "owner", Repo: "repo", PR: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != SpawnSuppressed {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnSuppressed)
	}
	if !slices.Equal(commands, []string{"systemctl"}) {
		t.Fatalf("commands = %v, want only systemctl", commands)
	}
}

func TestSpawnRunExportsRunContractAndHardTimeout(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var systemdArgs []string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			return nil, nil
		case "systemd-run":
			systemdArgs = append([]string(nil), args...)
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{
		"forgejo": {APIBase: "http://forge.local", CredentialFile: "/etc/minos/forge.token"},
	}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"
	repo.Adaptation.Build = "make build"
	repo.Adaptation.Test = "make test"
	repo.Policy.AutoMerge = true
	facts := Facts{
		Forge: "forgejo", Owner: "owner", Repo: "repo", PR: "7",
		HeadSHA: "head", BaseSHA: "target", BaseRef: "main", HeadRef: "feature",
	}

	outcome, err := SpawnRun(t.Context(), cfg, repo, facts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != SpawnStarted {
		t.Fatalf("outcome = %q, want %q", outcome, SpawnStarted)
	}
	assertArgument(t, systemdArgs, "--property=ExitType=cgroup")
	assertArgument(t, systemdArgs, "--property=KillMode=control-group")
	assertArgument(t, systemdArgs, "--property=RuntimeMaxSec=12h")
	for _, value := range []string{
		"MINOS_CONFIG=/etc/minos",
		"MINOS_FORGE=forgejo",
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repo",
		"MINOS_PR=7",
		"MINOS_HEAD_SHA=head",
		"MINOS_TARGET_SHA=target",
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_API_BASE=http://forge.local",
		"MINOS_CREDENTIAL_FILE=/etc/minos/forge.token",
		"MINOS_BUILD_CMD=make build",
		"MINOS_TEST_CMD=make test",
		"MINOS_RUN_BODY=/opt/minos/run-body/run-body",
		"MINOS_AUTO_MERGE=true",
	} {
		assertArgument(t, systemdArgs, "--setenv")
		assertArgument(t, systemdArgs, value)
	}
	if !slices.ContainsFunc(systemdArgs, func(arg string) bool {
		return strings.HasPrefix(arg, "MINOS_RUN_DIR=")
	}) {
		t.Fatalf("systemd-run arguments omit MINOS_RUN_DIR: %v", systemdArgs)
	}
	if !slices.ContainsFunc(systemdArgs, func(arg string) bool {
		return strings.HasPrefix(arg, "MINOS_WORKSPACE=")
	}) {
		t.Fatalf("systemd-run arguments omit MINOS_WORKSPACE: %v", systemdArgs)
	}
	if !slices.ContainsFunc(systemdArgs, func(arg string) bool {
		return strings.HasPrefix(arg, "MINOS_ORIENTATION=")
	}) {
		t.Fatalf("systemd-run arguments omit MINOS_ORIENTATION: %v", systemdArgs)
	}
}

func TestSpawnRunSerialisesConcurrentAdmissionAgainstSystemdFacts(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })

	var mu sync.Mutex
	active := false
	starts := 0
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch name {
		case "systemctl":
			if active {
				return []byte("minos-owner-repo-pr1.service loaded active running Minos lead\n"), nil
			}
			return nil, nil
		case "systemd-run":
			active = true
			starts++
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return nil, nil
		}
	}

	cfg := ServiceConfig{Root: "/etc/minos"}
	cfg.Runs.Dir = t.TempDir()
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	repo := RepoConfig{}
	repo.Adaptation.RunBody = "/opt/minos/run-body/run-body"

	results := make(chan SpawnOutcome, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for _, pr := range []string{"1", "2"} {
		group.Add(1)
		go func() {
			defer group.Done()
			outcome, err := SpawnRun(t.Context(), cfg, repo, Facts{Forge: "forgejo", Owner: "owner", Repo: "repo", PR: pr})
			results <- outcome
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var outcomes []SpawnOutcome
	for outcome := range results {
		outcomes = append(outcomes, outcome)
	}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []SpawnOutcome{SpawnStarted, SpawnSuppressed}) {
		t.Fatalf("outcomes = %v, want one start and one suppression", outcomes)
	}
	if starts != 1 {
		t.Fatalf("systemd starts = %d, want one", starts)
	}
}

func assertArgument(t *testing.T, arguments []string, want string) {
	t.Helper()
	if !slices.Contains(arguments, want) {
		t.Fatalf("arguments omit %q: %v", want, arguments)
	}
}
