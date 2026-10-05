package shell

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The run contract carries no skill provisioning: a deployment sets no
// MINOS_SKILLS_DIR and the run body has no vendored-skills stage to fail at.
func TestRunBodyCarriesNoSkillProvisioningStage(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "run-body"))
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"MINOS_SKILLS_DIR", "vendored-skills"} {
		if strings.Contains(string(script), retired) {
			t.Fatalf("run body still carries %q; skill provisioning was retired from the run contract", retired)
		}
	}
}

func TestRunBodyLaunchesAndStopsIsolatedResidentClaude(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, nil)

	homeDir := filepath.Join(fixture.runDir, "home")
	configDir := filepath.Join(homeDir, ".claude")
	codexConfigDir := filepath.Join(homeDir, ".codex")
	projectsDir := filepath.Join(homeDir, ".claude", "projects")
	stateDir := filepath.Join(fixture.runDir, "state")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("CLAUDE_CONFIG_DIR mode = %o, want 700", info.Mode().Perm())
	}
	// The lead needs no skills and the workers run with theirs stripped, so
	// the run body installs none into either home.
	for _, skills := range []string{filepath.Join(configDir, "skills"), filepath.Join(codexConfigDir, "skills")} {
		if _, err := os.Stat(skills); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("run body created %s (err=%v); no skills belong in a run-private home", skills, err)
		}
	}
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
	assertContainsFile(t, filepath.Join(stateDir, "worker-state"), "worker wrote state")
	assertContainsFile(t, fixture.record+".worker-tool", filepath.Join(homeDir, ".local", "bin", "minos-worker-probe"))
	assertContainsFile(t, fixture.record+".worker-env", "XDG_STATE_HOME="+stateDir)
	tempDir := environmentValue(t, fixture.record+".worker-env", "TMPDIR")
	if filepath.Dir(tempDir) != filepath.Join(fixture.root, "tmp") {
		t.Fatalf("TMPDIR = %q, want child of disk-backed Minos temp root %q", tempDir, filepath.Join(fixture.root, "tmp"))
	}
	assertContainsFile(t, fixture.record+".worker-temp", "mode=700")
	assertContainsFile(t, fixture.record+".worker-temp", "worker wrote temp")
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("TMPDIR survived run-body exit: %v", err)
	}
	assertContainsFile(t, fixture.record+".setup", "setup invoked")
	assertContainsFile(t, fixture.record+".argv", "--bg")
	assertContainsFile(t, fixture.record+".argv", "--model")
	assertContainsFile(t, fixture.record+".argv", "claude-opus-5-5")
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
		"MINOS_GIT_AUTHOR_NAME",
		"MINOS_GIT_AUTHOR_EMAIL",
		"GIT_AUTHOR_NAME",
		"GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME",
		"GIT_COMMITTER_EMAIL",
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

func TestRunBodyExportsSocketSafeTempDirectoryForLongRunName(t *testing.T) {
	fixture := newRunBodyFixtureWithShortRoot(t).withRun(
		"minos-run-Example-Corp-Gizmo-pr85-123456789",
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

func TestRunBodyExportsTheProvisionedEnginesFromTheSeedsItNames(t *testing.T) {
	both := newRunBodyFixture(t)
	both.run(t, nil)
	assertContainsFile(t, both.record+".worker-env", "MINOS_PROVISIONED_ENGINES=claude codex")
	assertContainsFile(t, both.record+".worker-env", "MINOS_LEAD_ENGINE=claude")

	// A deployment that seeds no Codex auth is a single-engine deployment:
	// the run proceeds with Claude alone and says so to the workflows.
	claudeOnly := newRunBodyFixture(t)
	claudeOnly.appendConfig(t, map[string]string{"MINOS_CODEX_CONFIG_SEED": ""})
	claudeOnly.run(t, nil)
	assertContainsFile(t, claudeOnly.record+".worker-env", "MINOS_PROVISIONED_ENGINES=claude")
	if _, err := os.Stat(filepath.Join(claudeOnly.runDir, "home", ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("an unprovisioned engine's home still received seed state: %v", err)
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
				fixture.appendConfig(t, map[string]string{"MINOS_CLAUDE_CONFIG_SEED": ""})
				return nil
			},
			wantStage: "configuration",
			wantCause: "MINOS_CLAUDE_CONFIG_SEED is required: the lead runs on claude",
		},
		{
			name: "lead engine this build cannot run",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{"MINOS_LEAD_ENGINE": "unsupported"})
				return nil
			},
			wantStage: "configuration",
			wantCause: "MINOS_LEAD_ENGINE must be claude or codex; the unsupported lead is not available in this build",
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
			wantCause: "could not create isolated runtime home and state directories",
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
			wantCause: "claude lead launch failed",
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

func TestRunBodyRecordsRealSetupTargetRefusal(t *testing.T) {
	server, head, target, environment := refusedTargetForge(t)
	fixture := realSetupRunBodyFixture(t, server.URL, head, target, environment)
	output, err := fixture.execute(environment)
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("run-body exit = %v, want refused fetch exit 1; output = %s", err, output)
	}
	for _, want := range []string{"stage=workspace-setup", "head=" + head, "cause=target fetch refused: the forge would not serve target sha " + target, "likely because the base branch moved", "the next sweep re-derives"} {
		assertContainsFile(t, fixture.failureLog, want)
	}
	if _, err := os.Stat(fixture.record + ".argv"); !os.IsNotExist(err) {
		t.Fatalf("lead launch exists or stat failed after setup refusal: %v", err)
	}
	if _, err := os.Stat(fixture.record + ".terminal"); !os.IsNotExist(err) {
		t.Fatalf("terminal lead record exists or stat failed after setup refusal: %v", err)
	}
}

func TestRunBodyRecordsProtocolV2TargetRefusal(t *testing.T) {
	server, head, target, environment := missingTargetForge(t)
	fixture := realSetupRunBodyFixture(t, server.URL, head, target, environment)
	output, err := fixture.execute(environment)
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 128 {
		t.Fatalf("run-body exit = %v, want protocol v2 refusal exit 128; output = %s", err, output)
	}
	if !strings.Contains(string(output), "upload-pack: not our ref "+target) {
		t.Fatalf("real upload-pack refusal missing from stderr: %s", output)
	}
	for _, want := range []string{"stage=workspace-setup", "head=" + head, "cause=target fetch refused: the forge would not serve target sha " + target, "likely because the base branch moved", "the next sweep re-derives"} {
		assertContainsFile(t, fixture.failureLog, want)
	}
	for _, suffix := range []string{".argv", ".terminal"} {
		if _, err := os.Stat(fixture.record + suffix); !os.IsNotExist(err) {
			t.Fatalf("lead record %s exists or stat failed after protocol v2 refusal: %v", suffix, err)
		}
	}
}

func TestRunBodyStreamsSetupStderrBeforeSetupExits(t *testing.T) {
	fixture := newRunBodyFixture(t)
	release := filepath.Join(fixture.root, "release-setup")
	const progress = "MINOS_SETUP_PROGRESS_BEFORE_EXIT"
	writeScript(t, fixture.setupStub, `#!/usr/bin/env sh
set -eu
printf '%s\n' 'MINOS_SETUP_PROGRESS_BEFORE_EXIT' >&2
while [ ! -f "$MINOS_TEST_RELEASE_SETUP" ]; do sleep 0.01; done
exit 19
`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := fixture.command(ctx, map[string]string{"MINOS_TEST_RELEASE_SETUP": release})
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = os.WriteFile(release, nil, 0o600)
			_ = cmd.Wait()
		}
	}()
	observed := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if scanner.Text() == progress {
				observed <- struct{}{}
			}
		}
	}()
	select {
	case <-observed:
		// Setup cannot exit until this test releases it, so receipt proves
		// forwarding during setup rather than replay after its exit.
	case <-time.After(2 * time.Second):
		t.Fatal("setup stderr was not forwarded while setup was waiting for release")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 19 {
		t.Fatalf("run-body exit = %v, want setup exit 19", err)
	}
	assertContainsFile(t, filepath.Join(fixture.runDir, "setup.stderr"), progress)
	assertFailureLine(t, fixture.failureLog, "stage=workspace-setup", "cause=workspace setup failed")
}

func TestRunBodyKeepsGenericCauseForTargetTransportFailure(t *testing.T) {
	server, head, target, environment := refusedTargetForge(t)
	// A missing server repository fails the actual Git transport, without
	// refusing a particular unadvertised object.
	writeScript(t, environment["GIT_SSH_COMMAND"], "#!/usr/bin/env sh\nexec git upload-pack '"+filepath.Join(t.TempDir(), "missing-repository")+"'\n")
	fixture := realSetupRunBodyFixture(t, server.URL, head, target, environment)
	output, err := fixture.execute(environment)
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 128 {
		t.Fatalf("run-body exit = %v, want transport failure exit 128; output = %s", err, output)
	}
	assertContainsFile(t, fixture.failureLog, "stage=workspace-setup cause=workspace setup failed status_write=skipped-prerequisites\n")
	if strings.Contains(string(output), "target fetch refused:") {
		t.Fatalf("unrelated transport failure reported as target refusal: %s", output)
	}
}

func realSetupRunBodyFixture(t *testing.T, apiBase string, head, target string, environment map[string]string) runBodyFixture {
	t.Helper()
	fixture := newRunBodyFixture(t)
	setup, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "setup-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.appendConfig(t, map[string]string{"MINOS_SETUP_WORKSPACE": setup})
	credential := filepath.Join(fixture.root, "forge.token")
	if err := os.WriteFile(credential, []byte("forge-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"MINOS_API_BASE": apiBase, "MINOS_CREDENTIAL_FILE": credential,
		"MINOS_HEAD_SHA": head, "MINOS_TARGET_SHA": target, "MINOS_HEAD_BRANCH": "feature",
		"MINOS_GUIDANCE_SOURCES": "[]",
	} {
		environment[key] = value
	}
	return fixture
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
				"MINOS_LEAD_POLL_SECONDS":    "0",
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
		"cause=claude lead entered blocked state before producing run activity",
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
				"MINOS_LEAD_POLL_SECONDS":      "0",
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
		"MINOS_LEAD_POLL_SECONDS":      "0",
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
			cgroup := writeTestCgroup(t, fixture.root, 850_000_000, 1_000_000_000, 0, 850_000_000, 0)
			if err := os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("low 0\nhigh 3\nmax 17\noom 2\noom_kill 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cgroup, "memory.peak"), []byte("987654321\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cgroup, "memory.pressure"), []byte("some avg10=0.01 avg60=0.02 avg300=0.03 total=456\nfull avg10=0.00 avg60=0.01 avg300=0.02 total=123\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			output, err := fixture.execute(map[string]string{
				"MINOS_CGROUP_DIR":          cgroup,
				"MINOS_TEST_TERMINAL_STATE": state,
			})
			if err != nil {
				t.Fatalf("run-body failed after recording terminal lead evidence: %v\n%s", err, output)
			}
			assertContainsFile(t, fixture.record+".terminal", `"state":"`+state+`"`)
			assertContainsFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"), "[memory.events]")
			assertContainsFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"), "oom_kill 1")
			assertContainsFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"), "[memory.peak]\n987654321")
			assertContainsFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"), "[memory.pressure]")
			assertContainsFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"), "some avg10=0.01")
			assertFileEmpty(t, fixture.failureLog)
			assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyCapturesDeathEvidenceBeforeCleanupAndRecordsUnavailableCounters(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 850_000_000, 1_000_000_000, 0, 850_000_000, 0)
	if err := os.WriteFile(filepath.Join(cgroup, "memory.events"), []byte("oom_kill 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cgroup, "memory.pressure")); err != nil {
		t.Fatal(err)
	}
	output, err := fixture.execute(map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_TERMINAL_STATE": "failed",
		"MINOS_TEST_CGROUP_ON_STOP": filepath.Join(cgroup, "memory.events"),
	})
	if err != nil {
		t.Fatalf("run-body failed after recording terminal lead evidence: %v\n%s", err, output)
	}
	evidence := filepath.Join(fixture.runDir, "cgroup-death-evidence")
	assertContainsFile(t, evidence, "oom_kill 1")
	assertContainsFile(t, evidence, "[memory.pressure]\nunavailable")
	assertContainsFile(t, filepath.Join(cgroup, "memory.events"), "oom_kill 0")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyStopsSilentLeadAtConfiguredTimeout(t *testing.T) {
	fixture := newRunBodyFixture(t)
	output, err := fixture.execute(map[string]string{
		"MINOS_TEST_TERMINAL_STATE":  "done",
		"MINOS_LEAD_SILENCE_TIMEOUT": "0",
		"MINOS_LEAD_POLL_SECONDS":    "0",
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
		"cause=claude lead produced no run activity for 0 seconds (last state: unknown)",
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

// Silence timeouts must fit shell integer arithmetic and the seven-day bound,
// so every poll can enforce the silence backstop.
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
	// The two-second silence margin allows work writes to remain observable
	// under parallel build load without triggering the backstop prematurely.
	output, err := fixture.executeContext(ctx, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":   "done",
		"MINOS_TEST_POLL_WORK_WRITES": "12",
		"MINOS_LEAD_SILENCE_TIMEOUT":  "2",
		"MINOS_LEAD_POLL_SECONDS":     "0.25",
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
		"MINOS_LEAD_POLL_SECONDS":     "0.25",
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
	sourceRoot := installerSourceRoot(t)
	for _, name := range []string{
		"adjudicated-review",
		"brief-dispositions.mjs",
		"file-triage.mjs",
		"finding-presentation.mjs",
		"isolate-from-live-run.mjs",
		"filing-destination.mjs",
		"out-of-scope-observations.mjs",
		"compose-review-publication.mjs",
		"review-brief-inputs.mjs",
		"review-briefs.js",
		"review-inputs.mjs",
		"review-scope-inputs.mjs",
		"review-scope.js",
		"review.js",
		"run-record-adjudicator.mjs",
		"verdict-classification.mjs",
	} {
		mode := os.FileMode(0o644)
		contents := []byte("export const workflow = {};\n")
		if name == "adjudicated-review" {
			mode = 0o755
			contents = []byte("#!/usr/bin/env node\n")
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, "workflows", name), contents, mode); err != nil {
			t.Fatal(err)
		}
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
		filepath.Join(destination, "workflows", "brief-dispositions.mjs"),
		filepath.Join(destination, "workflows", "file-triage.mjs"),
		filepath.Join(destination, "workflows", "finding-presentation.mjs"),
		filepath.Join(destination, "workflows", "isolate-from-live-run.mjs"),
		filepath.Join(destination, "workflows", "filing-destination.mjs"),
		filepath.Join(destination, "workflows", "out-of-scope-observations.mjs"),
		filepath.Join(destination, "workflows", "compose-review-publication.mjs"),
		filepath.Join(destination, "workflows", "review-brief-inputs.mjs"),
		filepath.Join(destination, "workflows", "review-briefs.js"),
		filepath.Join(destination, "workflows", "review-inputs.mjs"),
		filepath.Join(destination, "workflows", "review-scope-inputs.mjs"),
		filepath.Join(destination, "workflows", "review-scope.js"),
		filepath.Join(destination, "workflows", "review.js"),
		filepath.Join(destination, "workflows", "run-record-adjudicator.mjs"),
		filepath.Join(destination, "workflows", "verdict-classification.mjs"),
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
	if got := fmt.Sprintf("%x", sha256.Sum256(installed)); got != "d7e0676ce98ca82033dc3d91fd84c8c04632a30fa5eb1409b2768613e2d06356" {
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
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\ncat >/dev/null\nprintf 'partial compressed archive\\n'\nexit 3\n")
	installArchiveReceiverSSH(t, bin, destination)
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
	trace             bool
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
	if err := os.WriteFile(fixture.instructionPath, []byte("Follow the Minos lifecycle exactly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerProbeSource := filepath.Join(fixture.root, "minos-worker-probe")
	writeScript(t, workerProbeSource, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
env | sort >"$record.worker-env"
printf 'worker wrote state\n' >"$XDG_STATE_HOME/worker-state"
printf '%s\n' "$0" >"$record.worker-tool"
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
    case " $MINOS_PROVISIONED_ENGINES " in *" codex "*) test -f "$CODEX_HOME/auth.json" ;; esac
    printf '%s\n%s\n' 'claude-auth-present' 'codex-auth-present' >"$record.auth"
    printf '%s\n' "$HOME/.claude/projects" >"$record.projects"
    cat >"$record.stdin"
    sleep 300 </dev/null >/dev/null 2>&1 &
    lead_pid=$!
    printf '%s\n' "$lead_pid" >"$record.pids"
    if [ "${MINOS_TEST_NO_WORKER_PROBE:-}" != "1" ]; then
      (
        "$HOME/.local/bin/minos-worker-probe"
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
    if [ -n "${MINOS_TEST_CGROUP_ON_STOP:-}" ]; then
      printf 'low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n' >"$MINOS_TEST_CGROUP_ON_STOP"
    fi
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
mkdir -p "$HOME/.local/bin"
install -m 700 "$MINOS_TEST_WORKER_PROBE_SOURCE" "$HOME/.local/bin/minos-worker-probe"
printf '%s\n' '{"guidance":[],"misconfigurations":[]}' >"$MINOS_ORIENTATION"
printf 'setup invoked\n' >"${MINOS_TEST_RECORD}.setup"
`)
	t.Cleanup(func() { killRecordedProcesses(fixture.record + ".pids") })

	if err := os.MkdirAll(fixture.configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := map[string]string{
		"MINOS_CLAUDE":                   fixture.claudeStub,
		"MINOS_LEAD_MODEL":               "claude-opus-5-5",
		"MINOS_CLAUDE_CONFIG_SEED":       claudeSeed,
		"MINOS_CODEX_CONFIG_SEED":        codexSeed,
		"MINOS_LIFECYCLE_INSTRUCTION":    fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW":          "/opt/minos/workflows/adjudicated-review",
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
	return f.command(ctx, extraEnv).CombinedOutput()
}

func (f runBodyFixture) command(ctx context.Context, extraEnv map[string]string) *exec.Cmd {
	runEnv := map[string]string{
		"MINOS_PRESSURE_THRESHOLD_PERCENT": "85",
		"MINOS_RUN_DIR":                    f.runDir,
		"MINOS_CONFIG":                     f.configRoot,
		"MINOS_FORGE":                      "forgejo",
		"MINOS_WORKSPACE":                  filepath.Join(f.runDir, "workspace"),
		"MINOS_ORIENTATION":                filepath.Join(f.runDir, "orientation.json"),
		"MINOS_OWNER":                      "owner",
		"MINOS_REPO_NAME":                  "repository",
		"MINOS_PR":                         "17",
		"MINOS_HEAD_SHA":                   "head-sha",
		"MINOS_TARGET_SHA":                 "target-sha",
		"MINOS_BASE_REF":                   "main",
		"MINOS_HEAD_BRANCH":                "feature",
		"MINOS_API_BASE":                   "http://forge.test",
		"MINOS_CREDENTIAL_FILE":            "/etc/minos/forge.token",
		"MINOS_RUN_BODY":                   "/opt/minos/run-body/run-body",
		"MINOS_TEST_RECORD":                f.record,
		"MINOS_LEAD_POLL_SECONDS":          "0",
		"HOME":                             f.ambientHome,
		"CLAUDE_CONFIG_DIR":                filepath.Join(f.ambientHome, ".claude"),
		"CODEX_HOME":                       filepath.Join(f.ambientHome, ".codex"),
		"ANTHROPIC_AUTH_TOKEN":             "obsolete-proxy-token-must-not-reach-lead",
		"ANTHROPIC_BASE_URL":               "http://obsolete-proxy.invalid",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":    "obsolete-haiku-alias",
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
	if f.trace {
		cmd = exec.CommandContext(ctx, "sh", "-x", script)
	}
	cmd.Env = environmentWithOverrides(runEnv)
	return cmd
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
		"memory.events":       "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
		"memory.peak":         fmt.Sprintf("%d\n", current),
		"memory.pressure":     "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
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

// The stub supplies CLI behaviour; assertions read the real run body's
// arguments, captured output, state mapping and process cleanup.
func newCodexRunBodyFixture(t *testing.T) runBodyFixture {
	t.Helper()
	fixture := newRunBodyFixture(t)
	codexStub := filepath.Join(fixture.root, "codex")
	writeScript(t, codexStub, `#!/usr/bin/env sh
set -eu
record="${MINOS_TEST_RECORD:?}"
printf '%s\n' "$@" >"$record.argv"
env | sort >"$record.worker-env"
printf '%s\n' "$$" >"$record.pids"
printf '%s\n' '{"type":"thread.started","thread_id":"stub-thread"}'
printf 'Codex diagnostic\n' >&2
if [ "${MINOS_TEST_CODEX_HANG:-}" = 1 ]; then
  exec sleep 300
fi
if [ -n "${MINOS_TEST_COMPLETION_MARKER:-}" ]; then
  printf '%s\n' "$MINOS_TEST_COMPLETION_MARKER" >"$MINOS_RUN_DIR/lead-complete"
fi
exit "${MINOS_TEST_CODEX_EXIT:-0}"
`)
	fixture.appendConfig(t, map[string]string{
		"MINOS_LEAD_ENGINE": "codex", "MINOS_CODEX": codexStub,
		"MINOS_LEAD_MODEL": "", "MINOS_CLAUDE": "", "MINOS_CLAUDE_CONFIG_SEED": "",
	})
	return fixture
}

func TestRunBodyCodexLaunch(t *testing.T) {
	for _, model := range []string{"", "configured-codex-model"} {
		name := model
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCodexRunBodyFixture(t)
			if model != "" {
				fixture.appendConfig(t, map[string]string{"MINOS_LEAD_MODEL": model})
			}
			fixture.runWithin(t, 10*time.Second, map[string]string{"MINOS_TEST_COMPLETION_MARKER": "clean"})
			wantModel := model
			if wantModel == "" {
				wantModel = "gpt-6-sol"
			}
			argv, err := os.ReadFile(fixture.record + ".argv")
			if err != nil {
				t.Fatal(err)
			}
			instruction, err := os.ReadFile(fixture.instructionPath)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"exec", "--model", wantModel, "--dangerously-bypass-approvals-and-sandbox", "--cd", fixture.runDir, "--json", strings.TrimRight(string(instruction), "\n")}
			if got := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("Codex argv = %q, want %q", got, want)
			}
			assertContainsFile(t, fixture.record+".worker-env", "CODEX_HOME="+filepath.Join(fixture.runDir, "home", ".codex"))
			assertContainsFile(t, fixture.record+".worker-env", "MINOS_PROVISIONED_ENGINES=codex\n")
			assertContainsFile(t, filepath.Join(fixture.runDir, "codex-lead.jsonl"), `{"type":"thread.started","thread_id":"stub-thread"}`)
			assertContainsFile(t, filepath.Join(fixture.runDir, "codex-lead.stderr"), "Codex diagnostic")
			assertRegularFile(t, filepath.Join(fixture.runDir, "home", ".codex", "auth.json"))
			if _, err := os.Stat(filepath.Join(fixture.runDir, "home", ".claude", ".credentials.json")); !os.IsNotExist(err) {
				t.Fatalf("Claude seed unexpectedly copied: %v", err)
			}
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyCodexTerminalState(t *testing.T) {
	for _, test := range []struct{ code, state string }{{"0", "stopped"}, {"23", "failed"}} {
		t.Run(test.state, func(t *testing.T) {
			fixture := newCodexRunBodyFixture(t)
			fixture.trace = true
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := fixture.executeContext(ctx, map[string]string{"MINOS_TEST_CODEX_EXIT": test.code})
			if err != nil {
				t.Fatalf("run-body failed handling terminal Codex: %v\n%s", err, output)
			}
			// Shell tracing observes the supervisor's actual state assignment, not
			// a state invented by the CLI fixture. Terminal state alone retains the
			// existing supervisor exit contract; it is not lifecycle completion.
			if !strings.Contains(string(output), "agent_state="+test.state) {
				t.Fatalf("supervisor did not observe %s\n%s", test.state, output)
			}
			assertContainsFile(t, filepath.Join(fixture.runDir, "codex-lead.exit"), test.code+"\n")
			assertRegularFile(t, filepath.Join(fixture.runDir, "cgroup-death-evidence"))
			if _, err := os.Stat(filepath.Join(fixture.runDir, "lead-complete")); !os.IsNotExist(err) {
				t.Fatalf("terminal CLI exit manufactured completion: %v", err)
			}
			fixture.assertProcessesStopped(t)
		})
	}
}

func TestRunBodyCodexSilence(t *testing.T) {
	fixture := newCodexRunBodyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := fixture.executeContext(ctx, map[string]string{"MINOS_TEST_CODEX_HANG": "1", "MINOS_LEAD_SILENCE_TIMEOUT": "1", "MINOS_LEAD_POLL_SECONDS": "0.05"})
	if ctx.Err() != nil {
		t.Fatalf("supervisor failed to stop silent Codex: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("silent Codex run succeeded\n%s", output)
	}
	assertFailureLine(t, fixture.failureLog, "stage=lead-supervision", "last state: running")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyCodexRequiresExecutableAndSeed(t *testing.T) {
	for _, test := range []struct{ variable, cause string }{{"MINOS_CODEX", "MINOS_CODEX is required"}, {"MINOS_CODEX_CONFIG_SEED", "MINOS_CODEX_CONFIG_SEED is required: the lead runs on codex"}} {
		t.Run(test.variable, func(t *testing.T) {
			fixture := newCodexRunBodyFixture(t)
			fixture.appendConfig(t, map[string]string{test.variable: ""})
			output, err := fixture.execute(nil)
			if err == nil {
				t.Fatalf("run-body accepted missing %s\n%s", test.variable, output)
			}
			assertFailureLine(t, fixture.failureLog, "stage=configuration", test.cause)
			if _, err := os.Stat(fixture.record + ".argv"); !os.IsNotExist(err) {
				t.Fatalf("Codex launched despite missing configuration: %v", err)
			}
		})
	}
}

func TestRunBodyHonoursConfiguredPressureThreshold(t *testing.T) {
	for _, test := range []struct {
		name, threshold string
		anon            int64
		signal          bool
	}{
		{"lower threshold signals", "60", 700_000_000, true},
		{"higher threshold waits", "95", 900_000_000, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 0, test.anon, 0)
			fixture.run(t, map[string]string{
				"MINOS_PRESSURE_THRESHOLD_PERCENT": test.threshold,
				"MINOS_CGROUP_DIR":                 cgroup,
				"MINOS_TEST_PENDING_STATE":         "done",
				"MINOS_TEST_WAIT_POLLS":            "2",
				"MINOS_TEST_TERMINAL_STATE":        "failed",
			})
			signal := filepath.Join(fixture.runDir, "memory-pressure")
			if test.signal {
				assertContainsFile(t, signal, "anonymous memory plus swap")
			} else if _, err := os.Stat(signal); !os.IsNotExist(err) {
				t.Fatalf("footprint below configured threshold produced a pressure signal: %v", err)
			}
			fixture.assertProcessesStopped(t)
		})
	}
}

// The recorder observes the real exit trap, not a fixture-generated status.
func TestRunBodySetupDeathStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		stage      string
		writerExit int
		missing    string
		postlaunch bool
	}{
		{name: "configuration", stage: "configuration"},
		{name: "setup", stage: "workspace-setup"},
		{name: "writer failure", stage: "workspace-setup", writerExit: 47},
		{name: "missing service configuration", stage: "workspace-setup", missing: "service.toml"},
		{name: "missing coordinates", stage: "workspace-setup", missing: "MINOS_TARGET_SHA"},
		{name: "missing bootstrap configuration", stage: "configuration", missing: "MINOS_CONFIG"},
		{name: "postlaunch failure", postlaunch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunBodyFixture(t)
			calls := filepath.Join(fixture.root, "status-calls")
			writer := filepath.Join(fixture.root, "minos-recorder")
			writeScript(t, writer, fmt.Sprintf("#!/usr/bin/env sh\nfor argument do printf '%%s\\n' \"$argument\"; done >>%s\nexit %d\n", strconv.Quote(calls), test.writerExit))
			fixture.appendConfig(t, map[string]string{"MINOS_BIN": writer})
			// Only readability is relevant to this script-level test; parsing and
			// the actual guarded write are exercised by the fixture-forge journey.
			serviceConfig := filepath.Join(fixture.configRoot, "service.toml")
			if err := os.WriteFile(serviceConfig, []byte("# readable service configuration\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			extra := map[string]string{"MINOS_BIN": writer, "MINOS_FAILURE_LOG": fixture.failureLog}
			wantExit := 19
			wantCause := "workspace setup failed"
			if test.postlaunch {
				extra["MINOS_TEST_TERMINAL_STATE"] = "blocked"
				extra["MINOS_TEST_NO_WORKER_PROBE"] = "1"
				wantExit = 1
				wantCause = "blocked state before producing run activity"
			} else if test.stage == "configuration" {
				fixture.appendConfig(t, map[string]string{"MINOS_CLAUDE_CONFIG_SEED": ""})
				wantExit = 1
				wantCause = "MINOS_CLAUDE_CONFIG_SEED is required"
			} else {
				setup := filepath.Join(fixture.root, "failing-setup")
				writeScript(t, setup, "#!/usr/bin/env sh\nexit 19\n")
				fixture.appendConfig(t, map[string]string{"MINOS_SETUP_WORKSPACE": setup})
			}
			if test.missing == "service.toml" {
				if err := os.Remove(serviceConfig); err != nil {
					t.Fatal(err)
				}
			} else if test.missing != "" {
				extra[test.missing] = ""
				if test.missing == "MINOS_CONFIG" {
					wantCause = "MINOS_CONFIG is required"
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := fixture.executeContext(ctx, extra)
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != wantExit {
				t.Fatalf("exit = %v, want %d\n%s", err, wantExit, output)
			}
			assertFailureLine(t, fixture.failureLog, "cause=", wantCause)
			recorded, readErr := os.ReadFile(calls)
			if test.postlaunch || test.missing != "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("unexpected status call: %q (%v)", recorded, readErr)
				}
				if !test.postlaunch {
					assertFailureLine(t, fixture.failureLog, "status_write=skipped-prerequisites")
				}
				return
			}
			if readErr != nil {
				t.Fatalf("setup death recorded no status call: %v", readErr)
			}
			want := "forge\nstatus\nhead-sha\ntarget-sha\nincomplete\n--setup-failure\n" + test.stage + "\n"
			if string(recorded) != want {
				t.Fatalf("status calls = %q, want %q", recorded, want)
			}
			assertFailureLine(t, fixture.failureLog, "stage="+test.stage)
		})
	}
}
