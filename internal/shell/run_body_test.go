package shell

import (
	"context"
	"crypto/sha256"
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

func TestRunBodyLaunchesAndStopsIsolatedResidentClaude(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.run(t, nil)

	homeDir := filepath.Join(fixture.runDir, "home")
	configDir := filepath.Join(homeDir, ".claude")
	codexConfigDir := filepath.Join(homeDir, ".codex")
	projectsDir := filepath.Join(homeDir, ".claude", "projects")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("CLAUDE_CONFIG_DIR mode = %o, want 700", info.Mode().Perm())
	}
	assertContainsFile(t, filepath.Join(configDir, "skills", "root-cause", "SKILL.md"), "# Root Cause")
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
			name: "root-cause skill copy",
			configure: func(t *testing.T, fixture runBodyFixture) map[string]string {
				fixture.appendConfig(t, map[string]string{
					"MINOS_ROOT_CAUSE_SKILL": filepath.Join(fixture.root, "missing-root-cause"),
				})
				return nil
			},
			wantStage: "root-cause-skill",
			wantCause: "could not copy the vendored root-cause skill",
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

func TestRunBodyStopsLeadAfterCompletionMarker(t *testing.T) {
	for _, outcome := range []string{"clean", "non-clean"} {
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
			fixture.assertProcessesStopped(t)
		})
	}
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
	fixture.run(t, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":  "done",
		"MINOS_LEAD_SILENCE_TIMEOUT": "0",
		"MINOS_CLAUDE_POLL_SECONDS":  "0",
	})

	assertContainsFile(t, fixture.record+".terminal", `"state":"done"`)
	assertContainsFile(t, fixture.record+".attempts", "1")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
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
	fixture.runWithin(t, 6*time.Second, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":  "done",
		"MINOS_TEST_WORK_WRITES":     "20",
		"MINOS_TEST_WORK_INTERVAL":   "0.1",
		"MINOS_LEAD_SILENCE_TIMEOUT": "1",
		"MINOS_CLAUDE_POLL_SECONDS":  "0.25",
	})

	workData, err := os.ReadFile(filepath.Join(fixture.runDir, "workspace", "lead-work.log"))
	if err != nil {
		t.Fatal(err)
	}
	if writes := strings.Count(string(workData), "\n"); writes != 20 {
		t.Fatalf("lead work writes = %d, want 20 before the silence deadline stops the lead", writes)
	}
	assertContainsFile(t, fixture.record+".terminal", `"state":"done"`)
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
	fixture.assertProcessesStopped(t)
}

func TestRunBodyDoesNotTreatSupervisorPollingAsLeadActivity(t *testing.T) {
	fixture := newRunBodyFixture(t)
	fixture.runWithin(t, 4*time.Second, map[string]string{
		"MINOS_TEST_TERMINAL_STATE":   "done",
		"MINOS_TEST_POLL_HOME_WRITES": "1",
		"MINOS_LEAD_SILENCE_TIMEOUT":  "1",
		"MINOS_CLAUDE_POLL_SECONDS":   "0.25",
	})

	assertContainsFile(t, filepath.Join(fixture.runDir, "home", ".claude", "daemon.status.json"), "poll")
	assertContainsFile(t, fixture.record+".calls", "stop abcdef12")
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
	if got := fmt.Sprintf("%x", sha256.Sum256(installed)); got != "16815cd1c2a512d2361fcde94fd10df3212cadc5f706221400d37b8367e3305c" {
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
	root := t.TempDir()
	fixture := runBodyFixture{
		root: root, configRoot: filepath.Join(root, "config"),
		runDir: filepath.Join(root, "run"), record: filepath.Join(root, "claude-record"),
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
	skillSource := filepath.Join(root, "root-cause")
	if err := os.MkdirAll(filepath.Join(skillSource, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("# Root Cause\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "references", "proof.md"), []byte("proof\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.instructionPath, []byte("Follow the Minos lifecycle exactly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
    sleep 300 </dev/null >/dev/null 2>&1 &
    task_pid=$!
    printf '%s\n%s\n' "$lead_pid" "$task_pid" >"$record.pids"
    if [ "${MINOS_TEST_WORK_WRITES:-0}" -gt 0 ]; then
      (
        write=1
        while [ "$write" -le "$MINOS_TEST_WORK_WRITES" ]; do
          sleep "${MINOS_TEST_WORK_INTERVAL:-0.25}"
          printf 'work %s\n' "$write" >>"$MINOS_WORKSPACE/lead-work.log"
          write=$((write + 1))
        done
      ) </dev/null >/dev/null 2>&1 &
      printf '%s\n' "$!" >>"$record.pids"
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
printf '%s\n' '{"grounding":"repository","reason":"annexe-not-found"}' >"$MINOS_ORIENTATION"
printf 'setup invoked\n' >"${MINOS_TEST_RECORD}.setup"
`)
	t.Cleanup(func() { killRecordedProcesses(fixture.record + ".pids") })

	if err := os.MkdirAll(fixture.configRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := map[string]string{
		"MINOS_CLAUDE":                fixture.claudeStub,
		"MINOS_LEAD_MODEL":            "claude-opus-5",
		"MINOS_GIT_AUTHOR_NAME":       "Minos",
		"MINOS_GIT_AUTHOR_EMAIL":      "minos@example.invalid",
		"MINOS_CLAUDE_CONFIG_SEED":    claudeSeed,
		"MINOS_CODEX_CONFIG_SEED":     codexSeed,
		"MINOS_LIFECYCLE_INSTRUCTION": fixture.instructionPath,
		"MINOS_REVIEW_WORKFLOW":       "/opt/minos/workflows/adjudicated-review",
		"MINOS_ROOT_CAUSE_SKILL":      skillSource,
		"MINOS_SETUP_WORKSPACE":       fixture.setupStub,
		"MINOS_BIN":                   "/usr/local/bin/minos",
		"MINOS_FAILURE_LOG":           fixture.failureLog,
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
