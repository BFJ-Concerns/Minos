package shell

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverAcknowledgesAuthenticatedUnmappedEvent(t *testing.T) {
	cfg := receiverTestConfig(t)
	response, err := sendAuthenticatedHook(t, cfg, "push", `{"repository":{"owner":{"login":"owner"},"name":"subject"}}`)
	if err != nil {
		t.Fatalf("handleHook() error = %v", err)
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusAccepted, response.Body.String())
	}
	if response.Body.String() != "ignored\n" {
		t.Fatalf("body = %q, want unmapped acknowledgement", response.Body.String())
	}
}

func TestReceiverRejectsMalformedAuthenticatedBody(t *testing.T) {
	cfg := receiverTestConfig(t)
	response, err := sendAuthenticatedHook(t, cfg, "push", `{`)
	if err == nil {
		t.Fatal("handleHook() error = nil, want normalisation failure")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusBadRequest, response.Body.String())
	}
}

func TestReceiverRejectsMappedEventWithoutPullRequestIdentity(t *testing.T) {
	cfg := receiverTestConfig(t)
	response, err := sendAuthenticatedHook(t, cfg, "pull_request", `{"action":"opened"}`)
	if err == nil {
		t.Fatal("handleHook() error = nil, want incomplete mapped facts to fail")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusBadRequest, response.Body.String())
	}
}

func receiverTestConfig(t *testing.T) ServiceConfig {
	t.Helper()
	root := t.TempDir()
	secretPath := filepath.Join(root, "webhook-secret")
	tokenPath := filepath.Join(root, "forge-token")
	if err := os.WriteFile(secretPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adaptation, err := filepath.Abs(filepath.Join("..", "..", "scripts", "adaptations", "forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	return ServiceConfig{
		Root: root,
		Forges: map[string]ForgeConfig{
			"local": {
				Adaptation:        adaptation,
				APIBase:           "http://forge.invalid",
				WebhookSecretFile: secretPath,
				CredentialFile:    tokenPath,
			},
		},
	}
}

func sendAuthenticatedHook(t *testing.T, cfg ServiceConfig, event, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(body))
	request := httptest.NewRequest(http.MethodPost, "/hooks/local", strings.NewReader(body))
	request.Header.Set("X-Forgejo-Event", event)
	request.Header.Set("X-Forgejo-Signature", hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	return response, handleHook(context.Background(), cfg, response, request)
}
