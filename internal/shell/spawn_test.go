package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCountLiveRunUnitsFiltersByNameAndActiveState(t *testing.T) {
	output := strings.Join([]string{
		"minos-run-one.service loaded active running one",
		"minos-run-two.service loaded activating start two",
		"minos-run-old.service loaded inactive dead old",
		"minos-run-failed.service loaded failed failed failed",
		"unrelated.service loaded active running unrelated",
	}, "\n")

	got, err := countLiveRunUnits([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("live run count = %d, want 2", got)
	}
}

func TestMalformedSystemdLedgerFailsClosed(t *testing.T) {
	if _, err := countLiveRunUnits([]byte("unexpected-output\n")); err == nil {
		t.Fatal("malformed systemd output was treated as an empty ledger")
	}
}

func TestConcurrentAdmissionAtCapSpawnsExactlyOne(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	spawned := filepath.Join(root, "spawned")
	installAdmissionCommands(t, root,
		"if [ -f \"$MINOS_TEST_ACTIVE\" ]; then printf 'minos-run-live.service loaded active running live\\n'; fi\n",
		"sleep 0.2\n: >\"$MINOS_TEST_ACTIVE\"\nprintf 'spawned\\n' >>\"$MINOS_TEST_SPAWNED\"\n")
	t.Setenv("MINOS_TEST_ACTIVE", active)
	t.Setenv("MINOS_TEST_SPAWNED", spawned)

	cfg, repo, facts := admissionFixture(root, 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- SpawnRun(t.Context(), cfg, repo, facts, RunReview, "pr-opened")
		}()
	}
	ready.Wait()
	close(start)

	var spawnedResults, deferredResults int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			spawnedResults++
		case errors.Is(err, ErrRunCapacity):
			deferredResults++
		default:
			t.Fatalf("admission error = %v", err)
		}
	}
	if spawnedResults != 1 || deferredResults != 1 {
		t.Fatalf("spawned=%d deferred=%d, want one each", spawnedResults, deferredResults)
	}
	data, err := os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "spawned\n") != 1 {
		t.Fatalf("systemd-run calls = %q, want one", data)
	}
}

func TestSystemdQueryFailureSpawnsNothing(t *testing.T) {
	root := t.TempDir()
	spawned := filepath.Join(root, "spawned")
	installAdmissionCommands(t, root, "exit 23\n", ": >\"$MINOS_TEST_SPAWNED\"\n")
	t.Setenv("MINOS_TEST_SPAWNED", spawned)
	cfg, repo, facts := admissionFixture(root, 2)

	err := SpawnRun(t.Context(), cfg, repo, facts, RunReview, "pr-opened")
	if !errors.Is(err, ErrRunLedger) {
		t.Fatalf("spawn error = %v, want run-ledger failure", err)
	}
	if _, err := os.Stat(spawned); !os.IsNotExist(err) {
		t.Fatalf("systemd-run was called after ledger failure: %v", err)
	}
}

func TestSystemdRunFailureReleasesAdmissionLock(t *testing.T) {
	root := t.TempDir()
	failOnce := filepath.Join(root, "fail-once")
	spawned := filepath.Join(root, "spawned")
	if err := os.WriteFile(failOnce, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	installAdmissionCommands(t, root, "exit 0\n", `
if [ -f "$MINOS_TEST_FAIL_ONCE" ]; then
  rm "$MINOS_TEST_FAIL_ONCE"
  exit 19
fi
printf '%s\n' "$@" >"$MINOS_TEST_SPAWNED"
`)
	t.Setenv("MINOS_TEST_FAIL_ONCE", failOnce)
	t.Setenv("MINOS_TEST_SPAWNED", spawned)
	cfg, repo, facts := admissionFixture(root, 2)

	if err := SpawnRun(t.Context(), cfg, repo, facts, RunReview, "pr-opened"); err == nil {
		t.Fatal("first systemd-run unexpectedly succeeded")
	}
	if err := SpawnRun(t.Context(), cfg, repo, facts, RunReview, "pr-opened"); err != nil {
		t.Fatalf("second spawn did not acquire the released lock: %v", err)
	}
	data, err := os.ReadFile(spawned)
	if err != nil {
		t.Fatal(err)
	}
	for _, property := range []string{"--property=ExitType=cgroup", "--property=KillMode=control-group"} {
		if !strings.Contains(string(data), property) {
			t.Fatalf("systemd-run arguments lack %s:\n%s", property, data)
		}
	}
}

func TestUserSystemdCountsSurvivingRunCgroupUntilItEmpties(t *testing.T) {
	if _, err := liveRunUnitCount(t.Context()); err != nil {
		t.Skipf("user systemd manager unavailable: %v", err)
	}
	baseline, err := liveRunUnitCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	unit := fmt.Sprintf("minos-run-cgroup-test-%d", os.Getpid())
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	cleanup := func() {
		_ = exec.Command("systemctl", "--user", "stop", unit+".service").Run()
	}
	t.Cleanup(cleanup)

	command := exec.Command("systemd-run", "--user", "--collect", "--unit", unit,
		"--property=ExitType=cgroup", "--property=KillMode=control-group",
		"/bin/sh", "-c", `setsid /bin/sleep 30 </dev/null >/dev/null 2>&1 & echo $! >"$1"; wait`, "minos-test", childPIDPath)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start cgroup probe: %v: %s", err, out)
	}
	waitForTestCondition(t, "run unit to become active", func() bool {
		count, err := liveRunUnitCount(t.Context())
		return err == nil && count == baseline+1
	})
	waitForTestCondition(t, "run child to start", func() bool {
		childData, err := os.ReadFile(childPIDPath)
		return err == nil && len(strings.TrimSpace(string(childData))) > 0
	})

	mainPIDOutput, err := exec.Command("systemctl", "--user", "show", unit+".service", "--property=MainPID", "--value").Output()
	if err != nil {
		t.Fatal(err)
	}
	mainPID, err := strconv.Atoi(strings.TrimSpace(string(mainPIDOutput)))
	if err != nil || mainPID <= 0 {
		t.Fatalf("main PID = %q: %v", mainPIDOutput, err)
	}
	if err := syscall.Kill(mainPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForTestCondition(t, "wrapper death with child still counted", func() bool {
		childData, readErr := os.ReadFile(childPIDPath)
		if readErr != nil {
			return false
		}
		childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(childData)))
		if parseErr != nil || syscall.Kill(childPID, 0) != nil {
			return false
		}
		count, countErr := liveRunUnitCount(t.Context())
		return countErr == nil && count == baseline+1
	})

	cleanup()
	waitForTestCondition(t, "emptied run cgroup to free its slot", func() bool {
		count, err := liveRunUnitCount(t.Context())
		return err == nil && count == baseline
	})
}

func waitForTestCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", description)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func admissionFixture(root string, limit int) (ServiceConfig, RepoConfig, Facts) {
	cfg := ServiceConfig{Root: root, Forges: map[string]ForgeConfig{"local": {}}}
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.MaxConcurrent = limit
	repo := RepoConfig{Forge: "local", Owner: "minos", Repo: "subject"}
	facts := Facts{Forge: "local", Owner: "minos", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}
	return cfg, repo, facts
}

func installAdmissionCommands(t *testing.T, root, systemctlBody, systemdRunBody string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(bin, "systemctl"), "#!/usr/bin/env sh\nset -eu\n"+systemctlBody)
	writeScript(t, filepath.Join(bin, "systemd-run"), "#!/usr/bin/env sh\nset -eu\n"+systemdRunBody)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}
