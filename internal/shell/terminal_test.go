package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalMarkerKeepsFirstReasonAndFullIdentity(t *testing.T) {
	runDir := t.TempDir()
	if err := writeTerminalMarker(runDir, RunReview, "abcdef1234567890", "controlled-failure"); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalMarker(runDir, RunReview, "abcdef1234567890", "body-exit-after-forge-write"); err != nil {
		t.Fatal(err)
	}

	marker, err := readTerminalMarker(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if marker.RunKind != RunReview || marker.HeadSHA != "abcdef1234567890" || marker.Disposition != "operational-error" || marker.Reason != "controlled-failure" {
		t.Fatalf("terminal marker = %#v", marker)
	}
	if marker.Timestamp.IsZero() {
		t.Fatal("terminal marker omitted its timestamp")
	}
	if matches, err := filepath.Glob(filepath.Join(runDir, ".terminal.env.*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary terminal files remain: %v err=%v", matches, err)
	}
}

func TestAdaptationMarksMutatingVerbBeforeItExecutes(t *testing.T) {
	runDir := t.TempDir()
	adaptationDir := t.TempDir()
	writeScript(t, filepath.Join(adaptationDir, "post-comment"), "#!/usr/bin/env sh\ntest -f \"$PUMP19_RUN_DIR/forge-writes-attempted.env\"\n")
	t.Setenv("PUMP19_RUN_DIR", runDir)
	t.Setenv("PUMP19_RUN_KIND", "review")
	t.Setenv("PUMP19_HEAD_SHA", "abcdef1234567890")

	if _, err := (Adaptation{Dir: adaptationDir}).Run(context.Background(), "post-comment", nil, nil); err != nil {
		t.Fatalf("mutating adaptation did not observe its pre-exec marker: %v", err)
	}
	values, err := readMetaFile(filepath.Join(runDir, forgeWritesAttemptedFile))
	if err != nil {
		t.Fatal(err)
	}
	if values["PUMP19_FORGE_OPERATION"] != "post-comment" {
		t.Fatalf("recorded operation = %q", values["PUMP19_FORGE_OPERATION"])
	}
}

func TestAdaptationReadDoesNotMarkForgeWrite(t *testing.T) {
	runDir := t.TempDir()
	adaptationDir := t.TempDir()
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	t.Setenv("PUMP19_RUN_DIR", runDir)

	if _, err := (Adaptation{Dir: adaptationDir}).Run(context.Background(), "get-statuses", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, forgeWritesAttemptedFile)); !os.IsNotExist(err) {
		t.Fatalf("read-only adaptation produced forge-write marker: %v", err)
	}
}

func TestMissionDrivenLabelMutationStillMarksForgeWrite(t *testing.T) {
	runDir := t.TempDir()
	adaptationDir := t.TempDir()
	writeScript(t, filepath.Join(adaptationDir, "add-label"), "#!/usr/bin/env sh\ntest -f \"$PUMP19_RUN_DIR/forge-writes-attempted.env\"\n")
	t.Setenv("PUMP19_RUN_DIR", runDir)

	if err := (Adaptation{Dir: adaptationDir}).AddLabel(t.Context(), "pump19", "subject", "42", LabelReady); err != nil {
		t.Fatalf("mission-driven label mutation did not observe its marker: %v", err)
	}
}

func TestRearmRetainsAuditCopyForExactIdentity(t *testing.T) {
	root := t.TempDir()
	runDir := RunDir(root, "local", "pump19", "subject", "42", "abcdef1234567890", RunReview) + ".retry-5"
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalMarker(runDir, RunReview, "abcdef1234567890", "retry-exhausted"); err != nil {
		t.Fatal(err)
	}

	count, err := rearmTerminalMarkers(root, Facts{Forge: "local", Owner: "pump19", Repo: "subject", PR: "42", HeadSHA: "abcdef1234567890"}, RunReview)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rearmed markers = %d, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(runDir, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Fatalf("active marker remains after re-arm: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(runDir, terminalMarkerFile+".rearmed-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("re-arm audit copies = %v err=%v", matches, err)
	}

	other := RunDir(root, "local", "pump19", "subject", "42", "abcdef1234567890", RunFix)
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalMarker(other, RunFix, "abcdef1234567890", "controlled-failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, terminalMarkerFile)); err != nil {
		t.Fatalf("re-arm touched a different run kind: %v", err)
	}
}

func TestTerminalReasonRejectsProse(t *testing.T) {
	err := writeTerminalMarker(t.TempDir(), RunReview, "abcdef1234567890", "backend failed: secret-shaped prose")
	if err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("error = %v, want constrained reason rejection", err)
	}
}

func TestStubCrashWritesOnlyInternalTerminalMarker(t *testing.T) {
	root := t.TempDir()
	adaptationDir := filepath.Join(root, "adaptation")
	if err := os.Mkdir(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "get-pr-facts"), "#!/usr/bin/env sh\nprintf 'OCCASION=pr-opened\\nOWNER=pump19\\nREPO=subject\\nPR=42\\nHEAD_SHA=%s\\nBASE_REF=main\\nAUTHOR=alice\\nDRAFT=false\\n' \"$PUMP19_HEAD_SHA\"\n")
	for _, operation := range []string{"add-label", "add-reaction", "assign-if-missing", "remove-reaction", "remove-label"} {
		writeScript(t, filepath.Join(adaptationDir, operation), "#!/usr/bin/env sh\nexit 0\n")
	}
	statusFile := filepath.Join(root, "status.args")
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+statusFile+"'\n")
	credential := filepath.Join(root, "token")
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(credential, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := strings.Replace(validServiceConfig, "/tmp/adapt", adaptationDir, 1)
	service = strings.Replace(service, "/tmp/token", credential, 1)
	service = strings.Replace(service, "/tmp/secret", secret, 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"PUMP19_CONFIG":    root,
		"PUMP19_RUN_DIR":   runDir,
		"PUMP19_RUN_KIND":  "review",
		"PUMP19_FORGE":     "local",
		"PUMP19_OWNER":     "pump19",
		"PUMP19_REPO_NAME": "subject",
		"PUMP19_PR":        "42",
		"PUMP19_HEAD_SHA":  "abcdef1234567890",
		"PUMP19_STUB_MODE": "crash",
	} {
		t.Setenv(key, value)
	}

	if err := StubRunCommand(t.Context(), nil); err == nil {
		t.Fatal("stub crash unexpectedly succeeded")
	}
	marker, err := readTerminalMarker(runDir)
	if err != nil || marker.Reason != "stub-crash" {
		t.Fatalf("stub terminal marker = %#v err=%v", marker, err)
	}
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("stub crash wrote PR status: %v", err)
	}
}
