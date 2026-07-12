package preflight

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForgeProbeChecksIdentityAndPermissionsWithoutPrintingToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("do-not-log-this"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "token do-not-log-this" {
			http.Error(writer, "unauthorised", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/api/v1/user":
			fmt.Fprint(writer, `{"login":"minos"}`)
		case "/api/v1/repos/owner/repo":
			fmt.Fprint(writer, `{"permissions":{"pull":true,"push":true}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	probe := ForgeProbe{APIBase: server.URL + "/api/v1", CredentialFile: tokenPath, ExpectedLogin: "minos", PermissionRepository: "owner/repo", RequiredPermissions: []string{"pull", "push"}}
	result := probe.Run(context.Background())
	if !result.Passed {
		t.Fatalf("healthy forge failed: %#v", result)
	}
	probe.ExpectedLogin = "wrong"
	result = probe.Run(context.Background())
	if result.Passed || result.Diagnostic == "" {
		t.Fatalf("identity mismatch not reported: %#v", result)
	}
	if strings.Contains(result.Diagnostic, "do-not-log-this") {
		t.Fatalf("token leaked in diagnostic: %s", result.Diagnostic)
	}
}

func TestHealthyEnvironmentPassesEveryProbe(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/user":
			fmt.Fprint(writer, `{"login":"minos"}`)
		case "/repos/owner/repo":
			fmt.Fprint(writer, `{"permissions":{"push":true}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	success := runnerFunc(func(context.Context, string, ...string) ([]byte, error) { return []byte("ok"), nil })
	probes := []Probe{
		ForgeProbe{APIBase: server.URL, CredentialFile: tokenPath, ExpectedLogin: "minos", PermissionRepository: "owner/repo", RequiredPermissions: []string{"push"}},
		EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Args: []string{"--model", "{model}"}, Models: []string{"pinned"}}}, Runner: success},
		RegistryProbe{Enabled: true, Command: "registry", Runner: success},
		AlertProbe{Directory: t.TempDir()},
		ToolchainProbe{Binaries: []string{"sh"}},
	}
	report := Run(context.Background(), probes)
	if !report.Passed || len(report.Results) != 5 {
		t.Fatalf("healthy environment failed: %#v", report)
	}
	for _, result := range report.Results {
		if !result.Passed || result.Diagnostic == "" {
			t.Fatalf("unhealthy result in healthy report: %#v", result)
		}
	}
}

func TestEveryNonNetworkPrerequisiteFailsLoudly(t *testing.T) {
	failing := runnerFunc(func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("unavailable") })
	results := []Result{
		EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Args: []string{"--model", "{model}"}, Models: []string{"pinned"}}}, Runner: failing}.Run(context.Background()),
		RegistryProbe{Enabled: true, Command: "registry", Runner: failing}.Run(context.Background()),
		AlertProbe{Directory: filepath.Join("/dev/null", "incidents")}.Run(context.Background()),
		ToolchainProbe{Binaries: []string{"minos-definitely-missing-binary"}}.Run(context.Background()),
	}
	for _, result := range results {
		if result.Passed || result.Diagnostic == "" {
			t.Fatalf("broken prerequisite did not fail loudly: %#v", result)
		}
	}
}

func TestEngineProbeNamesStaleOAuthAndRecovery(t *testing.T) {
	runner := runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"error":"HTTP 403: organization has disabled Claude subscription access","total_cost_usd":0}`), fmt.Errorf("exit status 1")
	})
	result := EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Models: []string{"pinned"}}}, Runner: runner}.Run(context.Background())
	if result.Passed || len(result.Checks) != 1 || !strings.Contains(result.Checks[0].Diagnostic, "stale-oauth") || !strings.Contains(result.Checks[0].Diagnostic, "re-authenticate") {
		t.Fatalf("stale OAuth was not distinguished: %#v", result)
	}
}

func TestEngineProbeDoesNotGuessStaleOAuthWithoutObservedCost(t *testing.T) {
	runner := runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("HTTP 403: organization has disabled Claude subscription access"), fmt.Errorf("exit status 1")
	})
	result := EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Models: []string{"pinned"}}}, Runner: runner}.Run(context.Background())
	if len(result.Checks) != 1 || !strings.Contains(result.Checks[0].Diagnostic, "unknown") || strings.Contains(result.Checks[0].Diagnostic, "stale-oauth") {
		t.Fatalf("unobserved discriminators produced confident stale OAuth: %#v", result)
	}
}

func TestEngineProbeDoesNotGuessStaleOAuthWhenFailureIsNotImmediate(t *testing.T) {
	runner := runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"error":"organization has disabled Claude subscription access","total_cost_usd":0}`), fmt.Errorf("exit status 1")
	})
	times := []time.Time{
		time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 10, 0, 6, 0, time.UTC),
	}
	index := 0
	now := func() time.Time {
		value := times[index]
		index++
		return value
	}
	result := EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Models: []string{"pinned"}}}, Runner: runner, Now: now}.Run(context.Background())
	if len(result.Checks) != 1 || !strings.Contains(result.Checks[0].Diagnostic, "unknown") {
		t.Fatalf("non-immediate failure produced confident stale OAuth: %#v", result)
	}
}

func TestObservedCostReadsNestedJSONEvidence(t *testing.T) {
	cost, observed := observedCostUSD([]byte("noise\n{\"result\":{\"total_cost_usd\":0}}\n"))
	if !observed || cost != 0 {
		t.Fatalf("cost evidence not observed: cost=%v observed=%v", cost, observed)
	}
}

func TestEngineProbeReportsEveryPinnedModelAfterFailure(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "broken") {
			return []byte("usage limit reached"), fmt.Errorf("exit status 1")
		}
		return []byte("ok"), nil
	})
	result := EngineProbe{Engines: []EngineConfig{{Name: "claude", Command: "claude", Args: []string{"--model", "{model}"}, Models: []string{"broken", "healthy"}}}, Runner: runner}.Run(context.Background())
	if result.Passed || len(result.Checks) != 2 || result.Checks[0].Passed || !result.Checks[1].Passed {
		t.Fatalf("per-model diagnostics incomplete: %#v", result)
	}
}
