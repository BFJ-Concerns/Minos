package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBodyLaunchesOneReviewSessionFromRunDirectory(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	configDir := filepath.Join(root, "config")
	installRoot := filepath.Join(root, "install")
	copyRunBodyFixture(t, installRoot)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "launch")
	capture := filepath.Join(root, "pump19-capture")
	writeScript(t, launcher, `#!/usr/bin/env sh
set -eu
printf 'cwd=%s\n' "$PWD"
printf 'scripts=%s\n' "$PUMP19_REVIEW_SCRIPTS"
printf 'mission='; cat
`)
	writeScript(t, capture, `#!/usr/bin/env sh
set -eu
[ "$1" = capture-claude ]
shift
while [ "$#" -gt 0 ]; do
  case "$1" in
    --pins) shift 2 ;;
    --output) output=$2; shift 2 ;;
    --resolved) resolved=$2; shift 2 ;;
    *) launcher=$1; shift; break ;;
  esac
done
"$launcher" >"$output"
printf '[{"id":"lead","resolved_model":"fixture"}]\n' >"$resolved"
`)
	envFile := "PUMP19_ENGINE_LAUNCH_LEAD='" + launcher + "'\n" +
		"PUMP19_ENSEMBLE_LAUNCH='/opt/pump19/bin/ensemble'\n" +
		"PUMP19_PINS='/opt/pump19/pins.toml'\n" +
		"PUMP19_REVIEW_SCRIPTS='/opt/pump19/review'\n" +
		"PUMP19_BIN='" + capture + "'\n"
	if err := os.WriteFile(filepath.Join(configDir, "run-body.env"), []byte(envFile), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join(installRoot, "run-body", "run-body"))
	cmd.Env = append(os.Environ(),
		"PUMP19_CONFIG="+configDir,
		"PUMP19_RUN_DIR="+runDir,
		"PUMP19_RUN_KIND=review",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run body failed: %v\n%s", err, out)
	}
	lead, err := os.ReadFile(filepath.Join(runDir, "sessions", "lead.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(lead)
	if !strings.Contains(text, "cwd="+runDir) || !strings.Contains(text, "scripts=/opt/pump19/review") || !strings.Contains(text, "mission=review mission fixture") {
		t.Fatalf("launcher did not receive the run contract:\n%s", text)
	}
}

func TestRunBodyRejectsUnknownKindAndUnconfiguredSkillRuns(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	installRoot := filepath.Join(root, "install")
	copyRunBodyFixture(t, installRoot)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The run-specific skill paths are deliberately absent: a fix or flaky run
	// without its skill wired must fail loudly. An unknown kind is rejected.
	if err := os.WriteFile(filepath.Join(configDir, "run-body.env"), []byte("PUMP19_ENGINE_LAUNCH_LEAD=x\nPUMP19_ENSEMBLE_LAUNCH=x\nPUMP19_PINS=x\nPUMP19_REVIEW_SCRIPTS=x\nPUMP19_BIN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fix", "flaky", "surprise"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command(filepath.Join(installRoot, "run-body", "run-body"))
			cmd.Env = append(os.Environ(), "PUMP19_CONFIG="+configDir, "PUMP19_RUN_DIR="+filepath.Join(root, kind), "PUMP19_RUN_KIND="+kind)
			if err := cmd.Run(); err == nil {
				t.Fatalf("kind %s unexpectedly succeeded", kind)
			}
		})
	}
}

func TestRunWrapEarlyBodyFailureIsRetryableAndInvisibleOnEveryRunKind(t *testing.T) {
	for _, kind := range []string{"fix", "finish", "flaky"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			adaptationDir := filepath.Join(root, "adaptation")
			installRoot := filepath.Join(root, "install")
			for _, dir := range []string{configDir, adaptationDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			copyRunBodyFixture(t, installRoot)
			credential := filepath.Join(root, "token")
			webhookSecret := filepath.Join(root, "webhook-secret")
			if err := os.WriteFile(credential, []byte("token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(webhookSecret, []byte("secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			writeScript(t, filepath.Join(adaptationDir, "prepare-workspace"), "#!/usr/bin/env sh\nmkdir -p \"$PUMP19_WORKSPACE\"\n: >\"$PUMP19_DIFF\"\n")
			service := "[service]\nbot-login = \"Minos\"\n\n[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + credential + "\"\n\n[runs]\ndir = \"" + filepath.Join(root, "runs") + "\"\nmax-concurrent = 2\n\n[sweep]\nliveness-threshold = \"1h\"\n"
			if err := os.WriteFile(filepath.Join(configDir, "service.toml"), []byte(service), 0o644); err != nil {
				t.Fatal(err)
			}
			// The kind dispatches, but the engine launch fails before the mission can
			// attempt a forge write. This common backend-start failure is retryable.
			if err := os.WriteFile(filepath.Join(configDir, "run-body.env"), []byte("PUMP19_ENGINE_LAUNCH_LEAD=x\nPUMP19_ENSEMBLE_LAUNCH=x\nPUMP19_PINS=x\nPUMP19_REVIEW_SCRIPTS=x\nPUMP19_BIN=x\nPUMP19_FIX_SKILL=x\nPUMP19_FLAKY_SKILL=x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runDir := filepath.Join(root, "runs", kind)
			t.Setenv("PUMP19_RUN_DIR", runDir)
			t.Setenv("PUMP19_WORKSPACE", filepath.Join(root, "workspace"))
			t.Setenv("PUMP19_DIFF", filepath.Join(runDir, "diff.patch"))
			t.Setenv("PUMP19_RUN_KIND", kind)
			t.Setenv("PUMP19_FORGE", "local")
			t.Setenv("PUMP19_OWNER", "pump19")
			t.Setenv("PUMP19_REPO_NAME", "subject")
			t.Setenv("PUMP19_PR", "42")
			t.Setenv("PUMP19_HEAD_SHA", "abcdef123456")
			t.Setenv("PUMP19_CONFIG", configDir)
			t.Setenv("PUMP19_RUN_BODY", filepath.Join(installRoot, "run-body", "run-body"))
			if err := RunWrapCommand(t.Context(), []string{"--config", configDir}); err == nil {
				t.Fatalf("%s body unexpectedly completed", kind)
			}
			assertContainsFile(t, filepath.Join(runDir, "run.log"), "pump19 run-wrap error")
			assertContainsFile(t, filepath.Join(runDir, "retry.env"), "PUMP19_FAILURE_PHASE=run-body-exit")
			if _, err := os.Stat(filepath.Join(runDir, terminalMarkerFile)); !os.IsNotExist(err) {
				t.Fatalf("%s early body failure latched instead of retrying: %v", kind, err)
			}
		})
	}
}

func copyRunBodyFixture(t *testing.T, installRoot string) {
	t.Helper()
	runBodyDir := filepath.Join(installRoot, "run-body")
	missionDir := filepath.Join(installRoot, "missions")
	if err := os.MkdirAll(runBodyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(missionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "run-body"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runBodyDir, "run-body"), source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, mission := range []string{"review", "fix", "finish", "flaky"} {
		if err := os.WriteFile(filepath.Join(missionDir, mission+".md"), []byte(mission+" mission fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
