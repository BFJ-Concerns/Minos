package shell

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunBodyLaunchesAndStopsIsolatedResidentClaude(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, nil)

	homeDir := filepath.Join(fixture.runDir, "home")
	configDir := filepath.Join(homeDir, ".claude")
	codexConfigDir := filepath.Join(homeDir, ".codex")
	projectsDir := filepath.Join(homeDir, ".claude", "projects")
	stateDir := filepath.Join(fixture.runDir, "state")
	cacheDir := fixture.cacheDir("forgejo", "owner", "repository")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("CLAUDE_CONFIG_DIR mode = %o, want 700", info.Mode().Perm())
	}
	assertContainsFile(t, filepath.Join(configDir, "skills", "root-cause", "SKILL.md"), "casting: claude-code")
	assertContainsFile(t, filepath.Join(configDir, "skills", "playwright", "SKILL.md"), "casting: claude-code")
	assertContainsFile(t, filepath.Join(codexConfigDir, "skills", "root-cause", "SKILL.md"), "casting: codex")
	assertContainsFile(t, filepath.Join(codexConfigDir, "skills", "playwright", "SKILL.md"), "casting: codex")
	assertContainsFile(t, filepath.Join(configDir, ".claude.json"), `"hasCompletedOnboarding":true`)
	assertContainsFile(t, filepath.Join(configDir, ".claude.json"), `"bypassPermissionsModeAccepted":true`)
	assertRegularFile(t, filepath.Join(configDir, ".credentials.json"))
	assertRegularFile(t, filepath.Join(codexConfigDir, "auth.json"))
	if info, err := os.Stat(projectsDir); err != nil || !info.IsDir() {
		t.Fatalf("control-plane transcript directory = %s: %v", projectsDir, err)
	}
	if filepath.Dir(projectsDir) != configDir {
		t.Fatalf("control-plane transcript directory %q is not inside seeded Claude config %q", projectsDir, configDir)
	}
	stateInfo, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("XDG_STATE_HOME mode = %o, want 700", stateInfo.Mode().Perm())
	}
	for _, path := range []string{
		filepath.Join(homeDir, ".cargo", "bin"),
		filepath.Join(homeDir, ".local", "bin"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("provisioned tool directory %s mode = %o, want 700", path, info.Mode().Perm())
		}
	}
	assertContainsFile(t, filepath.Join(stateDir, "worker-state"), "worker wrote state")
	assertContainsFile(t, fixture.record+".worker-tool", filepath.Join(homeDir, ".local", "bin", "minos-worker-probe"))
	assertContainsFile(t, fixture.record+".worker-env", "XDG_STATE_HOME="+stateDir)
	for name, path := range map[string]string{
		"MINOS_SHARED_CACHE_DIR":   cacheDir,
		"SCCACHE_DIR":              filepath.Join(cacheDir, "rust", "sccache"),
		"GOCACHE":                  filepath.Join(cacheDir, "go", "build"),
		"GOMODCACHE":               filepath.Join(cacheDir, "go", "modules"),
		"npm_config_cache":         filepath.Join(cacheDir, "node", "npm"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(cacheDir, "playwright", "browsers"),
	} {
		assertContainsFile(t, fixture.record+".worker-env", name+"="+path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("shared cache directory %s mode = %s, want directory 700", path, info.Mode())
		}
	}
	tempDir := environmentValue(t, fixture.record+".worker-env", "TMPDIR")
	if filepath.Dir(tempDir) != filepath.Join(fixture.root, "tmp") {
		t.Fatalf("TMPDIR = %q, want child of disk-backed Minos temp root %q", tempDir, filepath.Join(fixture.root, "tmp"))
	}
	assertContainsFile(t, fixture.record+".worker-temp", "mode=700")
	assertContainsFile(t, fixture.record+".worker-temp", "worker wrote temp")
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("TMPDIR survived run-body exit: %v", err)
	}
	assertContainsFile(
		t,
		fixture.record+".worker-env",
		"PATH="+filepath.Join(homeDir, ".cargo", "bin")+":"+filepath.Join(homeDir, ".local", "bin")+":",
	)
	assertContainsFile(t, fixture.record+".setup", "setup invoked")
	assertContainsFile(t, fixture.record+".argv", "--bg")
	assertContainsFile(t, fixture.record+".argv", "--model")
	assertContainsFile(t, fixture.record+".argv", "claude-opus-5")
	assertContainsFile(t, fixture.record+".argv", "Follow the Minos lifecycle exactly.")
	assertContainsFile(t, fixture.record+".calls", "agents")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")

	for _, value := range []string{
		"HOME=" + homeDir,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"CODEX_HOME=" + codexConfigDir,
		"FORCE_COLOR=0",
		"PWD=" + fixture.runDir,
		"MINOS_RUN_DIR=" + fixture.runDir,
		"MINOS_CONFIG=" + fixture.configRoot,
		"MINOS_FORGE=forgejo",
		"MINOS_WORKSPACE=" + filepath.Join(fixture.runDir, "workspace"),
		"MINOS_ORIENTATION=" + filepath.Join(fixture.runDir, "orientation.json"),
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
		"GIT_AUTHOR_NAME=Minos",
		"GIT_AUTHOR_EMAIL=minos@example.invalid",
		"GIT_COMMITTER_NAME=Minos",
		"GIT_COMMITTER_EMAIL=minos@example.invalid",
		"MINOS_OWNER=owner",
		"MINOS_REPO_NAME=repository",
		"MINOS_PR=17",
		"MINOS_LIFECYCLE_INSTRUCTION=" + fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW=/opt/minos/workflows/adjudicated-review",
		"MINOS_BIN=/usr/local/bin/minos",
		"MINOS_HEAD_SHA=head-sha",
		"MINOS_TARGET_SHA=target-sha",
		"MINOS_BASE_REF=main",
		"MINOS_HEAD_BRANCH=feature",
		"MINOS_API_BASE=http://forge.test",
		"MINOS_CREDENTIAL_FILE=/etc/minos/forge.token",
		"MINOS_BUILD_CMD=make build",
		"MINOS_TEST_CMD=make test",
		"MINOS_AUTO_MERGE=true",
	} {
		assertContainsFile(t, fixture.record+".env", value)
	}
	assertContainsFile(t, fixture.record+".auth", "claude-auth-present")
	assertContainsFile(t, fixture.record+".auth", "codex-auth-present")
	assertContainsFile(t, fixture.record+".projects", projectsDir)
	assertFileOmitsEnvironmentNames(t, fixture.record+".env", []string{
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE",
	})
	if after := snapshotDirectory(t, fixture.ambientHome); !reflect.DeepEqual(after, fixture.ambientHomeBefore) {
		t.Fatalf("ambient home changed\nbefore: %#v\nafter:  %#v", fixture.ambientHomeBefore, after)
	}
	stdin, err := os.ReadFile(fixture.record + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if len(stdin) != 0 {
		t.Fatalf("Claude stdin = %q, want empty because instruction is the background prompt", stdin)
	}
	assertFileEmpty(t, fixture.failureLog)

	fixture.assertProcessesStopped(t)
}

func TestRunBodySelectsGuideForRunClassAndRejectsMissingMaintenanceGuide(t *testing.T) {
	t.Run("maintenance uses the maintenance guide", func(t *testing.T) {
		fixture := newRunBodyFixture(t)
		maintenanceGuide := filepath.Join(fixture.root, "maintenance.md")
		if err := os.WriteFile(maintenanceGuide, []byte("Follow the maintenance playbook exactly.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fixture.appendConfig(t, map[string]string{"MINOS_MAINTENANCE_INSTRUCTION": maintenanceGuide})

		fixture.run(t, map[string]string{"MINOS_RUN_CLASS": "maintenance"})
		assertContainsFile(t, fixture.record+".argv", "Follow the maintenance playbook exactly.")
	})

	t.Run("missing maintenance guide fails before launch", func(t *testing.T) {
		fixture := newRunBodyFixture(t)
		fixture.appendConfig(t, map[string]string{"MINOS_MAINTENANCE_INSTRUCTION": ""})

		output, err := fixture.execute(map[string]string{"MINOS_RUN_CLASS": "maintenance"})
		if err == nil {
			t.Fatalf("run-body accepted a maintenance run without a maintenance guide\n%s", output)
		}
		assertFailureLine(t, fixture.failureLog, "stage=configuration", "cause=MINOS_MAINTENANCE_INSTRUCTION is required")
	})
}

func TestRunBodyExportsSocketSafeTempDirectoryForLongRunName(t *testing.T) {
	fixture := newRunBodyFixtureWithShortRoot(t).withRun(
		"minos-run-BFJ-Concerns-Gizmo-pr85-123456789",
		"socket-safe-temp-record",
	)
	fixture.run(t, map[string]string{"MINOS_TEST_COMPLETION_MARKER": "clean"})

	tempDir := environmentValue(t, fixture.record+".worker-env", "TMPDIR")
	relativeTempDir, err := filepath.Rel(fixture.root, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	const deployedStorageRoot = "/var/lib/minos"
	runBodyContribution := 1 + len(relativeTempDir)
	deployedTempDirLength := len(deployedStorageRoot) + runBodyContribution
	if got := deployedTempDirLength + 1 + 50; got > 108 {
		t.Fatalf("deployed TMPDIR plus separator and 50-byte socket filename uses %d bytes, want at most 108 (storage-root-relative TMPDIR=%q)", got, relativeTempDir)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("TMPDIR survived run-body exit: %v", err)
	}
}

func TestRunBodyUsesPublishedSetupMergeAsCurrentHead(t *testing.T) {
	fixture := newRunBodyFixture(t)
	writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
mkdir -p "$MINOS_WORKSPACE"
install -m 700 "$MINOS_TEST_WORKER_PROBE_SOURCE" "$HOME/.local/bin/minos-worker-probe"
printf '%s\n' '{"grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf '%s\n' '{"outcome":"reconciled","publish":true,"merge":"setup-merge-sha"}' >"$MINOS_RUN_DIR/reconciliation.json"
`)

	fixture.run(t, nil)
	assertContainsFile(t, fixture.record+".worker-env", "MINOS_HEAD_SHA=setup-merge-sha")
}

func TestRunBodyUsesTheHeadSetupActuallyCheckedOut(t *testing.T) {
	fixture := newRunBodyFixture(t)
	writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
mkdir -p "$MINOS_WORKSPACE"
install -m 700 "$MINOS_TEST_WORKER_PROBE_SOURCE" "$HOME/.local/bin/minos-worker-probe"
printf '%s\n' '{"head":"own-moved-head","grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf '%s\n' '{"outcome":"unchanged","publish":false}' >"$MINOS_RUN_DIR/reconciliation.json"
`)

	fixture.run(t, nil)
	assertContainsFile(t, fixture.record+".worker-env", "MINOS_HEAD_SHA=own-moved-head")
}

func TestRunBodyKeepsAdmittedHeadForLocalOnlySetup(t *testing.T) {
	for _, geometry := range []string{"fork", "agit"} {
		t.Run(geometry, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
mkdir -p "$MINOS_WORKSPACE"
install -m 700 "$MINOS_TEST_WORKER_PROBE_SOURCE" "$HOME/.local/bin/minos-worker-probe"
printf '%s\n' '{"grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf '%s\n' '{"outcome":"reconciled","publish":false,"merge":"local-merge-sha"}' >"$MINOS_RUN_DIR/reconciliation.json"
`)

			fixture.run(t, nil)
			assertContainsFile(t, fixture.record+".worker-env", "MINOS_HEAD_SHA=head-sha")
		})
	}
}

func TestRunBodySharesPersistentCachesOnlyWithinARepository(t *testing.T) {
	fixture := newRunBodyFixture(t)
	first := fixture.withRun("first", "first-record")
	second := fixture.withRun("second", "second-record")

	first.run(t, map[string]string{"MINOS_TEST_COMPLETION_MARKER": "clean"})
	cacheDir := environmentValue(t, first.record+".worker-env", "MINOS_SHARED_CACHE_DIR")
	wantCacheDir := first.cacheDir("forgejo", "owner", "repository")
	if cacheDir != wantCacheDir {
		t.Fatalf("persistent cache = %q, want repository-scoped path %q", cacheDir, wantCacheDir)
	}
	if strings.HasPrefix(cacheDir, filepath.Clean(filepath.Join(fixture.root, "runs"))+string(os.PathSeparator)) {
		t.Fatalf("persistent cache %q is inside runs directory %q", cacheDir, filepath.Join(fixture.root, "runs"))
	}
	assertContainsFile(t, first.record+".setup", "setup invoked")
	if err := os.WriteFile(filepath.Join(cacheDir, "warm-marker"), []byte("first run cache\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	second.run(t, map[string]string{"MINOS_TEST_COMPLETION_MARKER": "clean"})
	assertContainsFile(t, second.record+".worker-env", "MINOS_SHARED_CACHE_DIR="+cacheDir)
	assertContainsFile(t, second.record+".setup", "setup invoked")
	assertContainsFile(t, filepath.Join(cacheDir, "warm-marker"), "first run cache")

	otherRepository := fixture.withRun("other-repository", "other-record")
	otherRepository.run(t, map[string]string{
		"MINOS_REPO_NAME":              "another-repository",
		"MINOS_TEST_COMPLETION_MARKER": "clean",
	})
	otherCacheDir := environmentValue(t, otherRepository.record+".worker-env", "MINOS_SHARED_CACHE_DIR")
	assertContainsFile(t, otherRepository.record+".worker-env", "MINOS_SHARED_CACHE_DIR="+otherCacheDir)
	if otherCacheDir == cacheDir {
		t.Fatalf("repositories resolved to the same persistent cache %q", cacheDir)
	}
	if _, err := os.Stat(filepath.Join(otherCacheDir, "warm-marker")); !os.IsNotExist(err) {
		t.Fatalf("other repository inherited cache marker: %v", err)
	}
}

func TestRunBodyRejectsCachePathTraversal(t *testing.T) {
	fixture := newRunBodyFixture(t)
	escapeDir := filepath.Join(fixture.root, "cache", "escape")
	out, err := fixture.execute(map[string]string{
		"MINOS_OWNER": "../escape",
	})
	if err == nil {
		t.Fatalf("run-body accepted a traversing cache identity\n%s", out)
	}
	if !strings.Contains(string(out), "cache path components must not equal dot or dot-dot, or contain a slash") {
		t.Fatalf("run-body output did not explain rejected cache identity\n%s", out)
	}
	if _, err := os.Stat(escapeDir); !os.IsNotExist(err) {
		t.Fatalf("traversing cache identity created %s: %v", escapeDir, err)
	}
	assertContainsFile(t, fixture.failureLog, "stage=runtime-home cause=forge, owner and repository cache path components must not equal dot or dot-dot, or contain a slash")
}

func TestRunBodyRejectsExactDotCachePathComponents(t *testing.T) {
	for _, variable := range []string{"MINOS_FORGE", "MINOS_OWNER", "MINOS_REPO_NAME"} {
		for _, component := range []string{".", ".."} {
			t.Run(variable+"="+component, func(t *testing.T) {
				fixture := newRunBodyFixture(t)
				out, err := fixture.execute(map[string]string{variable: component})
				if err == nil {
					t.Fatalf("run-body accepted %s=%q\n%s", variable, component, out)
				}
				if !strings.Contains(string(out), "cache path components must not equal dot or dot-dot, or contain a slash") {
					t.Fatalf("run-body output did not explain rejected cache identity\n%s", out)
				}
				if _, err := os.Stat(fixture.record + ".worker-env"); !os.IsNotExist(err) {
					t.Fatalf("rejected cache identity launched worker: %v", err)
				}
				assertContainsFile(t, fixture.failureLog, "stage=runtime-home cause=forge, owner and repository cache path components must not equal dot or dot-dot, or contain a slash")
			})
		}
	}
}

func TestRunBodyAllowsDotNamedRepository(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, map[string]string{"MINOS_REPO_NAME": ".github"})

	cacheDir := fixture.cacheDir("forgejo", "owner", ".github")
	assertContainsFile(t, fixture.record+".worker-env", "MINOS_SHARED_CACHE_DIR="+cacheDir)
	assertFileEmpty(t, fixture.failureLog)
}

func TestRunBodyRecordsMissingCacheIdentity(t *testing.T) {
	for _, variable := range []string{"MINOS_FORGE", "MINOS_OWNER", "MINOS_REPO_NAME"} {
		t.Run(variable, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			out, err := fixture.execute(map[string]string{variable: ""})
			if err == nil {
				t.Fatalf("run-body accepted missing %s\n%s", variable, out)
			}
			assertContainsFile(t, fixture.failureLog, "stage=runtime-home cause="+variable+" is required for the shared cache path")
		})
	}
}

func TestRunBodyCreatesPersistentCacheSafelyForConcurrentRuns(t *testing.T) {
	fixture := newRunBodyFixture(t)
	first := fixture.withRun("concurrent-first", "concurrent-first-record")
	second := fixture.withRun("concurrent-second", "concurrent-second-record")
	cacheDir := first.cacheDir("forgejo", "owner", "repository")
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatal(err)
	}

	type result struct {
		name string
		out  []byte
		err  error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for name, current := range map[string]runBodyFixture{"first": first, "second": second} {
		go func(name string, current runBodyFixture) {
			ready.Done()
			<-start
			out, err := current.execute(map[string]string{"MINOS_TEST_COMPLETION_MARKER": "clean"})
			results <- result{name: name, out: out, err: err}
		}(name, current)
	}
	ready.Wait()
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent %s run failed: %v\n%s", result.name, result.err, result.out)
		}
	}

	for _, path := range []string{
		filepath.Join(cacheDir, "rust", "sccache"),
		filepath.Join(cacheDir, "go", "build"),
		filepath.Join(cacheDir, "go", "modules"),
		filepath.Join(cacheDir, "node", "npm"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("concurrent cache directory %s mode = %s, want directory 700", path, info.Mode())
		}
	}
	assertContainsFile(t, first.record+".setup", "setup invoked")
	assertContainsFile(t, second.record+".setup", "setup invoked")
	firstTemp := environmentValue(t, first.record+".worker-env", "TMPDIR")
	secondTemp := environmentValue(t, second.record+".worker-env", "TMPDIR")
	if firstTemp == secondTemp {
		t.Fatalf("concurrent runs shared TMPDIR %q", firstTemp)
	}
	for _, tempDir := range []string{firstTemp, secondTemp} {
		if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
			t.Fatalf("concurrent run TMPDIR survived exit: %s: %v", tempDir, err)
		}
	}
}

func TestRunBodyUsesConfiguredGatewayCredentials(t *testing.T) {
	fixture := newRunBodyFixture(t)
	credentialFile := filepath.Join(fixture.root, "gateway.token")
	if err := os.WriteFile(credentialFile, []byte("gateway-token-from-configured-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.appendConfig(t, map[string]string{
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            credentialFile,
		"ANTHROPIC_BASE_URL":                         "http://configured-gateway.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "configured-haiku-model",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "196000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "180000",
	})

	fixture.run(t, nil)

	assertEnvironmentValues(t, fixture.record+".env", map[string]string{
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            credentialFile,
		"ANTHROPIC_AUTH_TOKEN":                       "gateway-token-from-configured-file",
		"ANTHROPIC_BASE_URL":                         "http://configured-gateway.test:8317",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "configured-haiku-model",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "196000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "180000",
	})
	fixture.assertProcessesStopped(t)
}

func TestRunBodyLeavesSharedSccacheServerRunningForConcurrentRuns(t *testing.T) {
	fixture := newRunBodyFixture(t)
	sccacheSource := filepath.Join(fixture.root, "sccache")
	writeScript(t, sccacheSource, `#!/usr/bin/env sh
set -eu
case "${1:-}" in
  --start-server)
    sleep 300 </dev/null >/dev/null 2>&1 &
    printf '%s\n' "$!" >"$MINOS_TEST_RECORD.sccache-server"
    ;;
  --stop-server)
    kill "$(cat "$MINOS_TEST_RECORD.sccache-server")"
    ;;
esac
`)
	serverEnv := environmentWithOverrides(map[string]string{"MINOS_TEST_RECORD": fixture.record})
	start := exec.Command(sccacheSource, "--start-server")
	start.Env = serverEnv
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start shared sccache server: %v\n%s", err, out)
	}
	serverPID, err := os.ReadFile(fixture.record + ".sccache-server")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(serverPID)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })

	fixture.run(t, map[string]string{"MINOS_TEST_SCCACHE_SOURCE": sccacheSource})

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("run-body stopped the shared sccache server used by concurrent runs: %v", err)
	}
}

func TestRunBodyRejectsInvalidGatewayConfigurationBeforeLaunchingClaude(t *testing.T) {
	tests := []struct {
		name             string
		createCredential bool
		credential       string
		includeBaseURL   bool
		wantError        string
	}{
		{
			name:           "missing credential file",
			includeBaseURL: true,
			wantError:      "is missing or unreadable",
		},
		{
			name:             "empty credential file",
			createCredential: true,
			includeBaseURL:   true,
			wantError:        "is empty",
		},
		{
			name:             "missing base URL",
			createCredential: true,
			credential:       "configured-token\n",
			wantError:        "ANTHROPIC_BASE_URL is required in gateway mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			credentialFile := filepath.Join(fixture.root, "gateway.token")
			if test.createCredential {
				if err := os.WriteFile(credentialFile, []byte(test.credential), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			config := map[string]string{
				"MINOS_ANTHROPIC_CREDENTIAL_FILE": credentialFile,
			}
			if test.includeBaseURL {
				config["ANTHROPIC_BASE_URL"] = "http://configured-gateway.test:8317"
			}
			fixture.appendConfig(t, config)

			output, err := fixture.execute(nil)
			if err == nil {
				t.Fatalf("run-body succeeded with invalid gateway configuration\n%s", output)
			}
			if !strings.Contains(string(output), test.wantError) {
				t.Fatalf("run-body error does not contain %q\n%s", test.wantError, output)
			}
			if _, err := os.Stat(fixture.record + ".argv"); !os.IsNotExist(err) {
				t.Fatalf("Claude launch record exists after gateway configuration failure: %v", err)
			}
			assertFailureLine(t, fixture.failureLog, "stage=configuration", "cause=", test.wantError)
		})
	}
}

func TestRunBodyReportsPrelaunchFailures(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, runBodyFixture) map[string]string
		wantStage string
		wantCause string
	}{
		{
			name: "missing bootstrap variable with known failure log",
			configure: func(_ *testing.T, fixture runBodyFixture) map[string]string {
				return map[string]string{
					"MINOS_CONFIG":      "",
					"MINOS_FAILURE_LOG": fixture.failureLog,
				}
			},
			wantStage: "configuration",
			wantCause: "MINOS_CONFIG is required",
		},
		{
			name: "run-body configuration source",
			configure: func(_ *testing.T, fixture runBodyFixture) map[string]string {
				return map[string]string{
					"MINOS_CONFIG":      filepath.Join(fixture.root, "missing-config"),
					"MINOS_FAILURE_LOG": fixture.failureLog,
				}
			},
			wantStage: "configuration",
			wantCause: "could not load",
		},
		{
			name: "missing required variable",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{"MINOS_CODEX_CONFIG_SEED": ""})
				return nil
			},
			wantStage: "configuration",
			wantCause: "MINOS_CODEX_CONFIG_SEED is required",
		},
		{
			name: "Claude seed copy",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{
					"MINOS_CLAUDE_CONFIG_SEED": filepath.Join(fixture.root, "missing-claude-seed"),
				})
				return nil
			},
			wantStage: "claude-seed",
			wantCause: "could not copy Claude configuration seed",
		},
		{
			name: "Codex seed copy",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{
					"MINOS_CODEX_CONFIG_SEED": filepath.Join(fixture.root, "missing-codex-seed"),
				})
				return nil
			},
			wantStage: "codex-seed",
			wantCause: "could not copy Codex configuration seed",
		},
		{
			name: "runtime state directory",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				if err := os.MkdirAll(fixture.runDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.runDir, "state"), []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return nil
			},
			wantStage: "runtime-home",
			wantCause: "could not create isolated runtime home, state, cache and tool directories",
		},
		{
			name: "vendored skills copy",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{
					"MINOS_SKILLS_DIR": filepath.Join(fixture.root, "missing-skills"),
				})
				return nil
			},
			wantStage: "vendored-skills",
			wantCause: "could not copy the vendored Claude Code skills",
		},
		{
			name: "workspace setup",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				failingSetup := filepath.Join(fixture.root, "failing-setup")
				writeScript(t, failingSetup, "#!/usr/bin/env sh\nexit 19\n")
				fixture.appendConfig(t, map[string]string{"MINOS_SETUP_WORKSPACE": failingSetup})
				return nil
			},
			wantStage: "workspace-setup",
			wantCause: "workspace setup failed",
		},
		{
			name: "lifecycle instruction read",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{
					"MINOS_LIFECYCLE_INSTRUCTION": filepath.Join(fixture.root, "missing-lifecycle.md"),
				})
				return nil
			},
			wantStage: "lifecycle-instruction",
			wantCause: "could not read lifecycle instruction",
		},
		{
			name: "Claude launch",
			configure: func(_ *testing.T, _ runBodyFixture) map[string]string {
				return map[string]string{"MINOS_TEST_CLAUDE_LAUNCH_FAIL": "1"}
			},
			wantStage: "lead-launch",
			wantCause: "Claude lead launch failed",
		},
		{
			name: "missing Claude session ID",
			configure: func(_ *testing.T, _ runBodyFixture) map[string]string {
				return map[string]string{"MINOS_TEST_NO_SESSION_ID": "1"}
			},
			wantStage: "lead-launch",
			wantCause: "Claude did not report a background session ID",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			output, err := fixture.execute(test.configure(t, fixture))
			if err == nil {
				t.Fatalf("run-body succeeded despite pre-launch failure\n%s", output)
			}
			assertFailureLine(t, fixture.failureLog, "stage="+test.wantStage, "cause="+test.wantCause)
		})
	}
}

func TestRunBodyFailsLoudlyWhenFailureLogIsUnwritable(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.appendConfig(t, map[string]string{"MINOS_FAILURE_LOG": fixture.root})

	output, err := fixture.execute(nil)
	if err == nil {
		t.Fatalf("run-body succeeded with an unwritable failure log\n%s", output)
	}
	if !strings.Contains(string(output), "MINOS_FAILURE_LOG ("+fixture.root+") is not writable") {
		t.Fatalf("run-body did not report the unwritable failure log\n%s", output)
	}
}

func TestRunBodyKeepsWaitingLeadsAlive(t *testing.T) {
	for _, state := range []string{"done", "blocked"} {
		t.Run(state, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			if err := os.WriteFile(
				fixture.failureLog,
				[]byte("timestamp=fixture stage=review cause=recorded-before-terminal-work\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			fixture.run(t, map[string]string{
				"MINOS_TEST_PENDING_STATE":   state,
				"MINOS_TEST_WAIT_POLLS":      "3",
				"MINOS_LEAD_SILENCE_TIMEOUT": "5",
				"MINOS_CLAUDE_POLL_SECONDS":  "0",
			})
			assertContainsFile(t, fixture.record+".terminal", `"status":null`)
			assertContainsFile(t, fixture.record+".waiting-states", state)
			assertContainsFile(t, fixture.record+".survived", "3")
			assertContainsFile(t, fixture.failureLog, "recorded-before-terminal-work")
			assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyRecordsBlockedLeadBeforeFirstRunActivity(t *testing.T) {
	fixture := newRunBodyFixture(t)
	output, err := fixture.execute(map[string]string{
		"MINOS_TEST_NO_WORKER_PROBE": "1",
		"MINOS_TEST_TERMINAL_STATE":  "blocked",
	})
	if err == nil {
		t.Fatalf("run-body succeeded for a lead blocked before first activity\n%s", output)
	}

	assertFailureLine(
		t,
		fixture.failureLog,
		"stage=lead-supervision",
		"cause=Claude lead entered blocked state before producing run activity",
	)
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyStopsLeadAfterCompletionMarker(t *testing.T) {
	for _, outcome := range []string{"clean", "non-clean", "continuation"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			fixture.run(t, map[string]string{
				"MINOS_TEST_TERMINAL_STATE":    "done",
				"MINOS_TEST_COMPLETION_MARKER": outcome,
				"MINOS_LEAD_SILENCE_TIMEOUT":   "1",
				"MINOS_CLAUDE_POLL_SECONDS":    "0",
			})

			assertContainsFile(t, filepath.Join(fixture.runDir, "lead-complete"), outcome)
			assertContainsFile(t, fixture.record+".terminal", `"state":"done"`)
			attemptsData, err := os.ReadFile(fixture.record + ".attempts")
			if err != nil {
				t.Fatal(err)
			}
			if attempts := strings.TrimSpace(string(attemptsData)); attempts != "1" {
				t.Fatalf("Claude agent queries = %s, want exactly 1", attempts)
			}
			assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
			assertFileEmpty(t, fixture.failureLog)
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodySignalsSustainedMemoryPressureAtLeadReadSurface(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 0, 900_000_000, 0)
	fixture.run(t, map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "memory approaching the run ceiling")
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "anonymous memory plus swap")
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "finish this stage and hand off")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyDoesNotSignalReclaimablePageCache(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 1_072_668_082, 1_073_741_824, 1_060_000_000, 12_000_000, 0)
	fixture.run(t, map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	if _, err := os.Stat(filepath.Join(fixture.runDir, "memory-pressure")); !os.IsNotExist(err) {
		t.Fatalf("reclaimable page cache produced a pressure signal or stat failed: %v", err)
	}
	fixture.assertProcessesStopped(t)
}

func TestRunBodyDoesNotSignalGrossUsageAtCeilingWithLowAnonymousMemory(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 1_000_000_000, 1_000_000_000, 80_000_000, 85_000_000, 0)
	fixture.run(t, map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	if _, err := os.Stat(filepath.Join(fixture.runDir, "memory-pressure")); !os.IsNotExist(err) {
		t.Fatalf("gross usage with low anonymous memory produced a pressure signal or stat failed: %v", err)
	}
	fixture.assertProcessesStopped(t)
}

func TestRunBodySignalsWhenAnonymousMemoryPlusSwapCrossesThreshold(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 100_000_000, 700_000_000, 160_000_000)
	fixture.run(t, map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "anonymous memory plus swap")
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "finish this stage and hand off")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyDisablesMemoryPressureWatchForInvalidUnreclaimableCounters(t *testing.T) {
	tests := map[string]func(t *testing.T, cgroup string){
		"missing anon": func(t *testing.T, cgroup string) {
			if err := os.WriteFile(filepath.Join(cgroup, "memory.stat"), []byte("inactive_file 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"malformed anon": func(t *testing.T, cgroup string) {
			if err := os.WriteFile(filepath.Join(cgroup, "memory.stat"), []byte("anon unknown\ninactive_file 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing swap": func(t *testing.T, cgroup string) {
			if err := os.Remove(filepath.Join(cgroup, "memory.swap.current")); err != nil {
				t.Fatal(err)
			}
		},
		"malformed swap": func(t *testing.T, cgroup string) {
			if err := os.WriteFile(filepath.Join(cgroup, "memory.swap.current"), []byte("unknown\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, invalidate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 0, 900_000_000, 0)
			invalidate(t, cgroup)
			output, err := fixture.execute(map[string]string{
				"MINOS_CGROUP_DIR":          cgroup,
				"MINOS_TEST_PENDING_STATE":  "done",
				"MINOS_TEST_WAIT_POLLS":     "2",
				"MINOS_TEST_TERMINAL_STATE": "failed",
			})
			if err != nil {
				t.Fatalf("run-body failed after disabling the pressure watch: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "memory-pressure watch disabled: cgroup memory counters are unavailable or malformed") {
				t.Fatalf("run-body output did not report a disabled pressure watch:\n%s", output)
			}
			if _, err := os.Stat(filepath.Join(fixture.runDir, "memory-pressure")); !os.IsNotExist(err) {
				t.Fatalf("invalid unreclaimable counter produced a pressure signal or stat failed: %v", err)
			}
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyPressureSignalDoesNotCountAsLeadActivity(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 0, 900_000_000, 0)
	output, err := fixture.execute(map[string]string{
		"MINOS_CGROUP_DIR":           cgroup,
		"MINOS_TEST_NO_WORKER_PROBE": "1",
		"MINOS_TEST_PENDING_STATE":   "done",
		"MINOS_TEST_WAIT_POLLS":      "1",
		"MINOS_TEST_TERMINAL_STATE":  "blocked",
	})
	if err == nil {
		t.Fatalf("run-body accepted a blocked lead after its own pressure write\n%s", output)
	}
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-pressure"), "finish this stage and hand off")
	// The supervisor's running peak lands beside the signal, and neither of
	// its own residue files may reset the lead-silence detector — this run
	// wrote both, and the backstop still fired above.
	assertContainsFile(t, filepath.Join(fixture.runDir, "memory-peak"), "900000000")
	assertFailureLine(t, fixture.failureLog, "stage=lead-supervision", "blocked state before producing run activity")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyIgnoresUnrecognisedCompletionMarker(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, map[string]string{
		"MINOS_TEST_COMPLETION_MARKER": "unclean",
		"MINOS_TEST_PENDING_STATE":     "done",
		"MINOS_TEST_WAIT_POLLS":        "3",
		"MINOS_LEAD_SILENCE_TIMEOUT":   "5",
		"MINOS_CLAUDE_POLL_SECONDS":    "0",
	})

	assertContainsFile(t, filepath.Join(fixture.runDir, "lead-complete"), "unclean")
	assertContainsFile(t, fixture.record+".survived", "3")
	assertContainsFile(t, fixture.record+".terminal", `"state":"failed"`)
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyStopsFailedAndStoppedLeads(t *testing.T) {
	for _, state := range []string{"failed", "stopped"} {
		t.Run(state, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			fixture.run(t, map[string]string{"MINOS_TEST_TERMINAL_STATE": state})
			assertContainsFile(t, fixture.record+".terminal", `"state":"`+state+`"`)
			assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyStopsSilentLeadAtConfiguredTimeout(t *testing.T) {
	fixture := newRunBodyFixture(t)
	output, err := fixture.execute(map[string]string{
		"MINOS_TEST_TERMINAL_STATE":  "done",
		"MINOS_LEAD_SILENCE_TIMEOUT": "0",
		"MINOS_CLAUDE_POLL_SECONDS":  "0",
	})
	if err == nil {
		t.Fatalf("run-body succeeded after the silence backstop stopped the lead\n%s", output)
	}

	assertContainsFile(t, fixture.record+".terminal", `"state":"done"`)
	assertContainsFile(t, fixture.record+".attempts", "1")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	assertFailureLine(
		t,
		fixture.failureLog,
		"stage=lead-supervision",
		"cause=Claude lead produced no run activity for 0 seconds (last state: done)",
	)
	fixture.assertProcessesStopped(t)
}

func TestRunBodyRejectsInvalidSilenceTimeout(t *testing.T) {
	fixture := newRunBodyFixture(t)
	output, err := fixture.execute(map[string]string{"MINOS_LEAD_SILENCE_TIMEOUT": "one-hour"})
	if err == nil {
		t.Fatalf("run-body accepted an invalid silence timeout\n%s", output)
	}
	if !strings.Contains(string(output), "MINOS_LEAD_SILENCE_TIMEOUT must be whole seconds") {
		t.Fatalf("run-body did not report the invalid silence timeout\n%s", output)
	}
	assertFailureLine(
		t,
		fixture.failureLog,
		"stage=configuration",
		"cause=MINOS_LEAD_SILENCE_TIMEOUT must be whole seconds",
	)
}

// A digit string too large for shell integer arithmetic used to pass
// validation and then make the backstop comparison error on every poll, which
// removed the backstop entirely rather than lengthening it — a fat-fingered
// value in run-body.env would leave a hung run to the systemd limit with no
// other signal.
func TestRunBodyRejectsOutOfRangeSilenceTimeout(t *testing.T) {
	for _, value := range []string{"99999999999999999999", "604801"} {
		t.Run(value, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			output, err := fixture.execute(map[string]string{"MINOS_LEAD_SILENCE_TIMEOUT": value})
			if err == nil {
				t.Fatalf("run-body accepted an out-of-range silence timeout\n%s", output)
			}
			if !strings.Contains(string(output), "MINOS_LEAD_SILENCE_TIMEOUT") {
				t.Fatalf("run-body did not report the out-of-range silence timeout\n%s", output)
			}
			assertFailureLine(t, fixture.failureLog, "stage=configuration")
		})
	}
}

func TestRunBodyKeepsWorkingLeadAliveWithoutStateTransition(t *testing.T) {
	fixture := newRunBodyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Interleaved A/B runs under 12 parallel builds made this interval
	// load-bearing: the 1s calibration failed 2/8, 5/10, and 2/10 runs at the
	// write-count assertion, while the otherwise identical 2s calibration
	// failed 0/8 and 0/10. Keep the measured margin rather than shortening it.
	output, err := fixture.executeContext(ctx, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":   "done",
		"MINOS_TEST_POLL_WORK_WRITES": "12",
		"MINOS_LEAD_SILENCE_TIMEOUT":  "2",
		"MINOS_CLAUDE_POLL_SECONDS":   "0.25",
	})
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("run-body did not finish within 10s\n%s", output)
	}
	if err == nil {
		t.Fatalf("run-body succeeded after the silence backstop stopped the lead\n%s", output)
	}

	workData, err := os.ReadFile(filepath.Join(fixture.runDir, "workspace", "lead-work.log"))
	if err != nil {
		t.Fatal(err)
	}
	if writes := strings.Count(string(workData), "\n"); writes != 12 {
		t.Fatalf("lead work writes = %d, want all 12 poll-observed activity events", writes)
	}
	assertContainsFile(t, fixture.record+".terminal", `"state":"done"`)
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	assertFailureLine(t, fixture.failureLog, "stage=lead-supervision")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyDoesNotTreatSupervisorPollingAsLeadActivity(t *testing.T) {
	fixture := newRunBodyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	output, err := fixture.executeContext(ctx, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":   "done",
		"MINOS_TEST_POLL_HOME_WRITES": "1",
		"MINOS_LEAD_SILENCE_TIMEOUT":  "1",
		"MINOS_CLAUDE_POLL_SECONDS":   "0.25",
	})
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("run-body did not finish within 4s\n%s", output)
	}
	if err == nil {
		t.Fatalf("run-body succeeded after the silence backstop stopped the lead\n%s", output)
	}

	assertContainsFile(t, filepath.Join(fixture.runDir, "home", ".claude", "daemon.status.json"), "poll")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	assertFailureLine(t, fixture.failureLog, "stage=lead-supervision")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyRetriesTransientAgentQuery(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, map[string]string{"MINOS_TEST_FAIL_AGENTS_ONCE": "1"})

	calls, err := os.ReadFile(fixture.record + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "agents failed\nagents\nstop abcdef12\n" {
		t.Fatalf("Claude calls = %q, want one failed query followed by recovery and stop", calls)
	}
	assertContainsFile(t, fixture.record+".attempts", "2")
	fixture.assertProcessesStopped(t)
}

func TestVendoredEnsembleBundleRunsWithoutRepositoryDependencies(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	bundle, err := filepath.Abs(filepath.Join("..", "..", "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cmd := exec.Command("node", bundle, "workflows")
	cmd.Dir = root
	cmd.Env = environmentWithOverrides(map[string]string{
		"HOME":              filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "home", ".claude"),
		"CODEX_HOME":        filepath.Join(root, "home", ".codex"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "home", ".config"),
		"XDG_DATA_HOME":     filepath.Join(root, "home", ".local", "share"),
	})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vendored Ensemble bundle is not self-contained: %v\n%s", err, output)
	}
}

func TestVendoredEnsembleResolvesConfiguredConcurrencyFromRunEnvironment(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	bundle, err := filepath.Abs(filepath.Join("..", "..", "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	workflow := filepath.Join(root, "workflow.mjs")
	if err := os.WriteFile(workflow, []byte("export default {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(root, "records")
	cmd := exec.Command("node", bundle, workflow)
	cmd.Dir = root
	cmd.Env = environmentWithOverrides(map[string]string{
		"HOME":                        filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":             filepath.Join(root, "config"),
		"ENSEMBLE_CONCURRENCY_CLAUDE": "10",
		"ENSEMBLE_CONCURRENCY_CODEX":  "6",
		"ENSEMBLE_RUN_RECORD":         "on",
		"ENSEMBLE_RUN_RECORD_DIR":     recordDir,
	})
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("vendored Ensemble accepted invalid probe workflow\n%s", output)
	}
	manifests, err := filepath.Glob(filepath.Join(recordDir, "runs", "*", "*", "*", "manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("run manifests = %v, error = %v; want one manifest", manifests, err)
	}
	data, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Concurrency struct {
			Engines map[string]struct {
				Layer string `json:"layer"`
				Value int    `json:"value"`
			} `json:"engines"`
		} `json:"concurrency"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for engine, want := range map[string]int{"claude": 10, "codex": 6} {
		got, ok := manifest.Concurrency.Engines[engine]
		if !ok || got.Value != want || got.Layer != "env" {
			t.Fatalf("%s concurrency = %+v, present = %t; want env value %d", engine, got, ok, want)
		}
	}
}

func TestInstallReviewRuntimeVerifiesLauncherAndInstallsSiblingArtefacts(t *testing.T) {
	sourceRoot := t.TempDir()
	for _, directory := range []string{"scripts", "runtime", "workflows"} {
		if err := os.MkdirAll(filepath.Join(sourceRoot, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFixtureFile(t, filepath.Join("..", "..", "scripts", "install-review-runtime"), filepath.Join(sourceRoot, "scripts", "install-review-runtime"), 0o755)
	for _, name := range []string{"ensemble.mjs", "ensemble.mjs.sha256", "ensemble.source-version"} {
		copyFixtureFile(t, filepath.Join("..", "..", "runtime", name), filepath.Join(sourceRoot, "runtime", name), 0o644)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "adjudicated-review"), []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "run-record-adjudicator.mjs"), []byte("export function adjudicate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "workflows", "setup-briefs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "setup.js"), []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "setup-briefs", "setup-agent.md"), []byte("# Setup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", "setup_test.mjs"), []byte("must not install\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "opt", "minos")
	cmd := exec.Command(filepath.Join(sourceRoot, "scripts", "install-review-runtime"), destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install review runtime: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(destination, "runtime", "ensemble.mjs"),
		filepath.Join(destination, "runtime", "ensemble.mjs.sha256"),
		filepath.Join(destination, "runtime", "ensemble.source-version"),
		filepath.Join(destination, "workflows", "adjudicated-review"),
		filepath.Join(destination, "workflows", "run-record-adjudicator.mjs"),
		filepath.Join(destination, "workflows", "setup.js"),
		filepath.Join(destination, "workflows", "setup-briefs", "setup-agent.md"),
	} {
		assertRegularFile(t, path)
	}
	if _, err := os.Stat(filepath.Join(destination, "workflows", "setup_test.mjs")); !os.IsNotExist(err) {
		t.Fatalf("workflow test was installed or stat failed unexpectedly: %v", err)
	}
	info, err := os.Stat(filepath.Join(destination, "workflows", "adjudicated-review"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed adjudication wrapper mode = %o, want 755", info.Mode().Perm())
	}
	installed, err := os.ReadFile(filepath.Join(destination, "runtime", "ensemble.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(installed)); got != "b3633ac75f5722cdb1383b8ba1bbd5939427327cf594665aa9565dc12daadab6" {
		t.Fatalf("installed Ensemble digest = %s", got)
	}
}

func TestProcessStatusIsZombie(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "zombie", status: "Name:\tsleep\nState:\tZ (zombie)\n", want: true},
		{name: "running", status: "Name:\tsleep\nState:\tR (running)\n", want: false},
		{name: "missing state", status: "Name:\tsleep\n", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := processStatusIsZombie([]byte(test.status)); got != test.want {
				t.Fatalf("processStatusIsZombie() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRunBodyArchiveFailureIsNonFatalAndReportedOnce(t *testing.T) {
	fixture := newRunBodyFixture(t)
	archive, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "archive-run"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(fixture.root, "archive-bin")
	destination := filepath.Join(fixture.root, "archive-destination")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\ncat >/dev/null\nexit 3\n")
	writeScript(t, filepath.Join(bin, "ssh"), `#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
exec sh -c "$last"
`)
	identity := filepath.Join(fixture.root, "archive-identity")
	knownHosts := filepath.Join(fixture.root, "archive-known-hosts")
	for _, path := range []string{identity, knownHosts} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archiveConfig := filepath.Join(fixture.root, "archive.env")
	config := strings.Join([]string{
		`MINOS_ARCHIVE_HOST="fixture"`,
		`MINOS_ARCHIVE_DESTINATION="` + destination + `"`,
		`MINOS_ARCHIVE_IDENTITY_FILE="` + identity + `"`,
		`MINOS_ARCHIVE_KNOWN_HOSTS="` + knownHosts + `"`,
	}, "\n") + "\n"
	if err := os.WriteFile(archiveConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.appendConfig(t, map[string]string{
		"MINOS_ARCHIVE_RUN":    archive,
		"MINOS_ARCHIVE_CONFIG": archiveConfig,
	})
	out, err := fixture.execute(map[string]string{
		"MINOS_TEST_COMPLETION_MARKER": "clean",
		"PATH":                         bin + ":" + os.Getenv("PATH"),
	})
	if err != nil {
		t.Fatalf("run-body failed because archiving failed: %v\n%s", err, out)
	}
	if got := strings.Count(string(out), "minos: could not archive run evidence"); got != 1 {
		t.Fatalf("archive diagnostics = %d, want one\n%s", got, out)
	}
	if matches, err := filepath.Glob(filepath.Join(destination, "*.tar.zst")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("failed archive was promoted: %v", matches)
	}
}

type runBodyFixture struct {
	root              string
	configRoot        string
	runDir            string
	record            string
	failureLog        string
	instructionPath   string
	claudeStub        string
	setupStub         string
	ambientHome       string
	ambientHomeBefore map[string]string
}

func newRunBodyFixture(t *testing.T) runBodyFixture {
	t.Helper()
	return newRunBodyFixtureAtRoot(t, t.TempDir())
}

func newRunBodyFixtureWithShortRoot(t *testing.T) runBodyFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "minos.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return newRunBodyFixtureAtRoot(t, root)
}

func newRunBodyFixtureAtRoot(t *testing.T, root string) runBodyFixture {
	t.Helper()
	fixture := runBodyFixture{
		root: root, configRoot: filepath.Join(root, "config"),
		runDir: filepath.Join(root, "runs", "run"), record: filepath.Join(root, "claude-record"),
		failureLog:      filepath.Join(root, "failures.log"),
		instructionPath: filepath.Join(root, "lifecycle.md"),
		claudeStub:      filepath.Join(root, "claude"),
		setupStub:       filepath.Join(root, "setup-workspace"),
		ambientHome:     filepath.Join(root, "ambient-home"),
	}
	for _, path := range []string{
		filepath.Join(fixture.ambientHome, ".claude", "operator-state"),
		filepath.Join(fixture.ambientHome, ".codex", "operator-state"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("must remain byte-unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.ambientHomeBefore = snapshotDirectory(t, fixture.ambientHome)
	claudeSeed := filepath.Join(root, "claude-seed")
	codexSeed := filepath.Join(root, "codex-seed")
	if err := os.MkdirAll(claudeSeed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexSeed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeSeed, ".credentials.json"), []byte("fixture Claude subscription state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexSeed, "auth.json"), []byte("fixture Codex ChatGPT state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(root, "skills")
	for _, casting := range []string{"claude-code", "codex"} {
		for _, skill := range []string{"root-cause", "playwright"} {
			skillSource := filepath.Join(skillsDir, casting, skill)
			if err := os.MkdirAll(filepath.Join(skillSource, "references"), 0o755); err != nil {
				t.Fatal(err)
			}
			content := fmt.Sprintf("# %s\ncasting: %s\n", skill, casting)
			if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(skillSource, "references", "proof.md"), []byte("proof\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(fixture.instructionPath, []byte("Follow the Minos lifecycle exactly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerProbeSource := filepath.Join(fixture.root, "minos-worker-probe")
	writeScript(t, workerProbeSource, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
env | sort >"$record.worker-env"
printf 'worker wrote state\n' >"$XDG_STATE_HOME/worker-state"
command -v minos-worker-probe >"$record.worker-tool"
printf 'mode=%s\n' "$(stat -c '%a' "$TMPDIR")" >"$record.worker-temp"
printf 'worker wrote temp\n' >"$TMPDIR/worker-temp"
cat "$TMPDIR/worker-temp" >>"$record.worker-temp"
`)
	writeScript(t, fixture.claudeStub, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
case "$1" in
  --bg)
    if [ "${MINOS_TEST_CLAUDE_LAUNCH_FAIL:-}" = "1" ]; then
      printf 'launch failed\n' >&2
      exit 23
    fi
    if [ "${MINOS_TEST_NO_SESSION_ID:-}" = "1" ]; then
      printf 'Claude accepted the launch without returning an id\n'
      exit 0
    fi
    : >"$record.argv"
    for argument in "$@"; do
      printf '%s\n' "$argument" >>"$record.argv"
    done
    env | sort >"$record.env"
    test -f "$CLAUDE_CONFIG_DIR/.credentials.json"
    test -f "$CODEX_HOME/auth.json"
    printf '%s\n%s\n' 'claude-auth-present' 'codex-auth-present' >"$record.auth"
    printf '%s\n' "$HOME/.claude/projects" >"$record.projects"
    cat >"$record.stdin"
    sleep 300 </dev/null >/dev/null 2>&1 &
    lead_pid=$!
    printf '%s\n' "$lead_pid" >"$record.pids"
    if [ "${MINOS_TEST_NO_WORKER_PROBE:-}" != "1" ]; then
      (
        minos-worker-probe
        exec sleep 300
      ) </dev/null >/dev/null 2>&1 &
      task_pid=$!
      printf '%s\n' "$task_pid" >>"$record.pids"
    fi
    if [ -n "${MINOS_TEST_COMPLETION_MARKER:-}" ]; then
      printf '%s\n' "$MINOS_TEST_COMPLETION_MARKER" >"$MINOS_RUN_DIR/lead-complete"
    fi
    printf 'Agent backgrounded: abcdef12\n'
    ;;
  agents)
    attempts=0
    if [ -f "$record.attempts" ]; then
      IFS= read -r attempts <"$record.attempts"
    fi
    attempts=$((attempts + 1))
    printf '%s\n' "$attempts" >"$record.attempts"
    if [ "${MINOS_TEST_FAIL_AGENTS_ONCE:-}" = "1" ] && [ "$attempts" -eq 1 ]; then
      printf 'agents failed\n' >>"$record.calls"
      exit 1
    fi
    printf 'agents\n' >>"$record.calls"
    if [ "$attempts" -le "${MINOS_TEST_POLL_WORK_WRITES:-0}" ]; then
      printf 'work %s\n' "$attempts" >>"$MINOS_WORKSPACE/lead-work.log"
    fi
    if [ "${MINOS_TEST_POLL_HOME_WRITES:-}" = "1" ]; then
      printf 'poll %s\n' "$attempts" >"$CLAUDE_CONFIG_DIR/daemon.status.json"
    fi
    while IFS= read -r pid; do
      kill -0 "$pid"
    done <"$record.pids"
    printf '%s\n' "$attempts" >>"$record.survived"
    terminal_state="${MINOS_TEST_TERMINAL_STATE:-failed}"
    wait_polls="${MINOS_TEST_WAIT_POLLS:-0}"
    if [ "$attempts" -le "$wait_polls" ]; then
      terminal_state="${MINOS_TEST_PENDING_STATE:?}"
      printf '%s\n' "$terminal_state" >>"$record.waiting-states"
    fi
    printf '[{"id":"abcdef12","sessionId":"session-one","status":null,"state":"%s"}]\n' "$terminal_state" |
      tee "$record.terminal"
    ;;
  stop)
    printf 'stop %s\n' "$2" >>"$record.calls"
    while IFS= read -r pid; do
      kill "$pid" 2>/dev/null || true
    done <"$record.pids"
    ;;
  *)
    printf 'unexpected command: %s\n' "$*" >&2
    exit 1
    ;;
esac
`)
	writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
mkdir -p "$MINOS_WORKSPACE"
install -m 700 "$MINOS_TEST_WORKER_PROBE_SOURCE" "$HOME/.local/bin/minos-worker-probe"
if [ -n "${MINOS_TEST_SCCACHE_SOURCE:-}" ]; then
  install -m 700 "$MINOS_TEST_SCCACHE_SOURCE" "$HOME/.local/bin/sccache"
fi
printf '%s\n' '{"grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf '%s\n' '{"outcome":"unnecessary","publish":false,"merge":null}' >"$MINOS_RUN_DIR/reconciliation.json"
printf 'setup invoked\n' >"${MINOS_TEST_RECORD}.setup"
`)
	t.Cleanup(func() { killRecordedProcesses(fixture.record + ".pids") })

	if err := os.MkdirAll(fixture.configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := map[string]string{
		"MINOS_CLAUDE":                   fixture.claudeStub,
		"MINOS_LEAD_MODEL":               "claude-opus-5",
		"MINOS_GIT_AUTHOR_NAME":          "Minos",
		"MINOS_GIT_AUTHOR_EMAIL":         "minos@example.invalid",
		"MINOS_CLAUDE_CONFIG_SEED":       claudeSeed,
		"MINOS_CODEX_CONFIG_SEED":        codexSeed,
		"MINOS_LIFECYCLE_INSTRUCTION":    fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW":          "/opt/minos/workflows/adjudicated-review",
		"MINOS_ROOT_CAUSE_SKILL":         filepath.Join(skillsDir, "codex", "root-cause"),
		"MINOS_SKILLS_DIR":               skillsDir,
		"MINOS_SETUP_WORKSPACE":          fixture.setupStub,
		"MINOS_BIN":                      "/usr/local/bin/minos",
		"MINOS_FAILURE_LOG":              fixture.failureLog,
		"MINOS_TEST_WORKER_PROBE_SOURCE": workerProbeSource,
	}
	var config strings.Builder
	for name, value := range runBodyEnv {
		fmt.Fprintf(&config, "%s=%s\n", name, strconv.Quote(value))
	}
	if err := os.WriteFile(filepath.Join(fixture.configRoot, "run-body.env"), []byte(config.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f runBodyFixture) withRun(name, record string) runBodyFixture {
	f.runDir = filepath.Join(f.root, "runs", name)
	f.record = filepath.Join(f.root, record)
	return f
}

func (f runBodyFixture) cacheDir(forge, owner, repository string) string {
	return filepath.Join(f.root, "cache", forge, owner, repository)
}

func (f runBodyFixture) appendConfig(t *testing.T, values map[string]string) {
	t.Helper()
	configPath := filepath.Join(f.configRoot, "run-body.env")
	config, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		if _, err := fmt.Fprintf(config, "%s=%s\n", name, strconv.Quote(value)); err != nil {
			_ = config.Close()
			t.Fatal(err)
		}
	}
	if err := config.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f runBodyFixture) run(t *testing.T, extraEnv map[string]string) {
	t.Helper()
	out, err := f.execute(extraEnv)
	if err != nil {
		t.Fatalf("run-body failed: %v\n%s", err, out)
	}
}

func (f runBodyFixture) runWithin(t *testing.T, timeout time.Duration, extraEnv map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := f.executeContext(ctx, extraEnv)
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("run-body did not finish within %s\n%s", timeout, out)
	}
	if err != nil {
		t.Fatalf("run-body failed: %v\n%s", err, out)
	}
}

func (f runBodyFixture) execute(extraEnv map[string]string) ([]byte, error) {
	return f.executeContext(context.Background(), extraEnv)
}

func (f runBodyFixture) executeContext(ctx context.Context, extraEnv map[string]string) ([]byte, error) {
	runEnv := map[string]string{
		"MINOS_RUN_DIR":                 f.runDir,
		"MINOS_CONFIG":                  f.configRoot,
		"MINOS_FORGE":                   "forgejo",
		"MINOS_WORKSPACE":               filepath.Join(f.runDir, "workspace"),
		"MINOS_ORIENTATION":             filepath.Join(f.runDir, "orientation.json"),
		"MINOS_OWNER":                   "owner",
		"MINOS_REPO_NAME":               "repository",
		"MINOS_PR":                      "17",
		"MINOS_HEAD_SHA":                "head-sha",
		"MINOS_TARGET_SHA":              "target-sha",
		"MINOS_BASE_REF":                "main",
		"MINOS_HEAD_BRANCH":             "feature",
		"MINOS_API_BASE":                "http://forge.test",
		"MINOS_CREDENTIAL_FILE":         "/etc/minos/forge.token",
		"MINOS_BUILD_CMD":               "make build",
		"MINOS_TEST_CMD":                "make test",
		"MINOS_AUTO_MERGE":              "true",
		"MINOS_RUN_BODY":                "/opt/minos/run-body/run-body",
		"MINOS_TEST_RECORD":             f.record,
		"MINOS_CLAUDE_POLL_SECONDS":     "0",
		"HOME":                          f.ambientHome,
		"CLAUDE_CONFIG_DIR":             filepath.Join(f.ambientHome, ".claude"),
		"CODEX_HOME":                    filepath.Join(f.ambientHome, ".codex"),
		"ANTHROPIC_AUTH_TOKEN":          "obsolete-proxy-token-must-not-reach-lead",
		"ANTHROPIC_BASE_URL":            "http://obsolete-proxy.invalid",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "obsolete-haiku-alias",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":             "265000",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW":            "265000",
		"MINOS_ANTHROPIC_CREDENTIAL_FILE":            filepath.Join(f.ambientHome, "obsolete.token"),
	}
	for name, value := range extraEnv {
		runEnv[name] = value
	}
	script := filepath.Join("..", "..", "scripts", "run-body", "run-body")
	cmd := exec.CommandContext(ctx, script)
	cmd.Env = environmentWithOverrides(runEnv)
	return cmd.CombinedOutput()
}

func assertRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
}

func assertFileEmpty(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("%s = %q, want empty", path, data)
	}
}

func writeTestCgroup(t *testing.T, root string, current, maximum, inactiveFile, anon, swap int64) string {
	t.Helper()
	directory := filepath.Join(root, "cgroup")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current":      fmt.Sprintf("%d\n", current),
		"memory.max":          fmt.Sprintf("%d\n", maximum),
		"memory.stat":         fmt.Sprintf("anon %d\ninactive_file %d\n", anon, inactiveFile),
		"memory.swap.current": fmt.Sprintf("%d\n", swap),
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func assertFailureLine(t *testing.T, path string, fragments ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	if line == "" || strings.Contains(line, "\n") {
		t.Fatalf("%s = %q, want exactly one failure line", path, data)
	}
	for _, fragment := range append([]string{
		"timestamp=",
		"pull_request=owner/repository#17",
		"head=head-sha",
	}, fragments...) {
		if !strings.Contains(line, fragment) {
			t.Errorf("%s = %q, want fragment %q", path, line, fragment)
		}
	}
}

func copyFixtureFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileOmitsEnvironmentNames(t *testing.T, path string, names []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for _, name := range names {
		prefix := name + "="
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				t.Fatalf("%s contains retired environment name %s", path, name)
			}
		}
	}
}

func assertEnvironmentValues(t *testing.T, path string, want map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found {
			got[name] = value
		}
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}

func environmentValue(t *testing.T, path, wantedName string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found && name == wantedName {
			return value
		}
	}
	t.Fatalf("%s does not contain %s", path, wantedName)
	return ""
}

func snapshotDirectory(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s:%o", info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += fmt.Sprintf(":%x", sha256.Sum256(data))
		}
		snapshot[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f runBodyFixture) assertProcessesStopped(t *testing.T) {
	t.Helper()
	pids := recordedPIDs(t, f.record+".pids")
	deadline := time.Now().Add(2 * time.Second)
	for processesExist(pids) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processesExist(pids) {
		t.Fatalf("resident process tree still exists after stop: %v", pids)
	}
}

func recordedPIDs(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func processesExist(pids []int) bool {
	for _, pid := range pids {
		if processExists(pid) {
			return true
		}
	}
	return false
}

func processExists(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return true
	}
	return !processStatusIsZombie(status)
}

func processStatusIsZombie(status []byte) bool {
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "State:" {
			return fields[1] == "Z"
		}
	}
	return false
}

func killRecordedProcesses(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
