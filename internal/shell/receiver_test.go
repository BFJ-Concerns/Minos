package shell

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleHookRejectsOversizedBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(strings.Repeat("x", int(maxWebhookBodyBytes+1))))
	response := httptest.NewRecorder()
	cfg := ServiceConfig{Forges: map[string]ForgeConfig{"local": {}}}

	err := handleHook(t.Context(), cfg, response, request)
	if err == nil {
		t.Fatal("oversized request unexpectedly succeeded")
	}
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(response.Body.String(), "too large") {
		t.Fatalf("body = %q, want explicit size rejection", response.Body.String())
	}
}

func TestHandleHookMakesInvalidRepoConfigLoud(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig+"\nquiet-typo = true\n")
	response := httptest.NewRecorder()
	err := handleHook(t.Context(), receiverTestConfig(t, root), response, signedHookRequest(t))
	if err == nil || !strings.Contains(err.Error(), "quiet-typo") {
		t.Fatalf("error = %v, want invalid key named", err)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(response.Body.String(), "configuration unavailable") {
		t.Fatalf("body = %q, want loud configuration failure", response.Body.String())
	}
}

func TestHandleHookKeepsGenuineNotOptedInAccepted(t *testing.T) {
	root := t.TempDir()
	response := httptest.NewRecorder()
	err := handleHook(t.Context(), receiverTestConfig(t, root), response, signedHookRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusAccepted || response.Body.String() != "not opted in\n" {
		t.Fatalf("response = %d %q, want 202 not opted in", response.Code, response.Body.String())
	}
}

func TestHandleHookDefersAtCapacity(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig)
	spawned := filepath.Join(root, "spawned")
	installAdmissionCommands(t, root,
		"printf 'pump19-run-one.service loaded active running one\\npump19-run-two.service loaded active running two\\n'\n",
		"exit 99\n")
	response := httptest.NewRecorder()
	cfg := receiverTestConfig(t, root)

	err := handleHook(t.Context(), cfg, response, signedHookRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusAccepted || response.Body.String() != "deferred: capacity\n" {
		t.Fatalf("response = %d %q, want 202 capacity deferral", response.Code, response.Body.String())
	}

	bin := filepath.Join(root, "bin")
	writeScript(t, filepath.Join(bin, "systemctl"), "#!/usr/bin/env sh\nexit 0\n")
	writeScript(t, filepath.Join(bin, "systemd-run"), "#!/usr/bin/env sh\n: >'"+spawned+"'\n")
	adaptationDir := filepath.Join(root, "sweep-adaptation")
	if err := os.MkdirAll(adaptationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	repos, err := LoadRepoConfigs(root)
	if err != nil {
		t.Fatal(err)
	}
	facts := Facts{
		Forge: "local", Owner: "pump19", Repo: "subject", PR: "1",
		HeadSHA: "2ff55f9248630929f5dbef0f99d713743d5f0a33", BaseRef: "main",
		Author: "pump19", Draft: false,
	}
	logFile, err := os.Create(filepath.Join(root, "sweep.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sweepPR(t.Context(), cfg, repos[0], Adaptation{Dir: adaptationDir}, facts, logFile); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spawned); err != nil {
		t.Fatalf("later sweep did not recover the capacity-deferred delivery: %v", err)
	}
}

func TestHandleHookReturnsUnavailableWhenRunLedgerFails(t *testing.T) {
	root := writeRepoConfig(t, validRepoConfig)
	installAdmissionCommands(t, root, "exit 31\n", "exit 99\n")
	response := httptest.NewRecorder()

	err := handleHook(t.Context(), receiverTestConfig(t, root), response, signedHookRequest(t))
	if !errors.Is(err, ErrRunLedger) {
		t.Fatalf("error = %v, want run ledger failure", err)
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func receiverTestConfig(t *testing.T, root string) ServiceConfig {
	t.Helper()
	secretFile := filepath.Join(t.TempDir(), "webhook-secret")
	if err := os.WriteFile(secretFile, []byte("test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ServiceConfig{
		Root: root,
		Forges: map[string]ForgeConfig{
			"local": {
				Adaptation:        filepath.Join("..", "..", "scripts", "adaptations", "forgejo"),
				WebhookSecretFile: secretFile,
			},
		},
	}
	cfg.Runs.Dir = filepath.Join(t.TempDir(), "runs")
	cfg.Runs.MaxConcurrent = 2
	return cfg
}

func signedHookRequest(t *testing.T) *http.Request {
	t.Helper()
	fixture := readFixture(t, "001-pull_request-opened.json")
	request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(fixture.Body))
	request.Header.Set("X-Forgejo-Event", "pull_request")
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write([]byte(fixture.Body))
	request.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	return request
}

func TestResolveReceiverFactsBindsReadyActorToLabelApplication(t *testing.T) {
	adaptation := fixtureTimelineAdaptation(t, "timeline-ready-added.json")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "mallory", Labels: []string{LabelReady}}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-added:Ready" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "bob" {
		t.Fatalf("actor = %q, want label actor bob", got.Actor)
	}
}

func TestResolveReceiverFactsSupportsGenericLabelRemovalVocabulary(t *testing.T) {
	adaptation := fixtureTimelineAdaptation(t, "timeline-ready-removed.json")
	facts := Facts{Occasion: "label-updated", Owner: "pump19", Repo: "subject", PR: "1", Actor: "mallory"}
	got, err := resolveReceiverFacts(t.Context(), adaptation, facts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Occasion != "label-removed:Ready" {
		t.Fatalf("occasion = %q", got.Occasion)
	}
	if got.Actor != "bob" {
		t.Fatalf("actor = %q, want event actor bob", got.Actor)
	}
}

func fixtureTimelineAdaptation(t *testing.T, fixture string) Adaptation {
	t.Helper()
	fakeBin := t.TempDir()
	curl := filepath.Join(fakeBin, "curl")
	data, err := os.ReadFile(filepath.Join("testdata", "forgejo14", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(curl, []byte("#!/usr/bin/env sh\ncat <<'JSON'\n"+string(data)+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	return Adaptation{
		Dir:        filepath.Join("..", "..", "scripts", "adaptations", "forgejo"),
		APIBase:    "http://forge.invalid",
		Credential: "token",
	}
}
