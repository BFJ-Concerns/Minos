package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bfj/minos/internal/product"
)

func projectionJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("projection document unavailable: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func projectionRow(t *testing.T, doc map[string]any, field string) map[string]any {
	t.Helper()
	rows, _ := doc[field].([]any)
	if len(rows) != 1 {
		t.Fatalf("%s rows = %#v, want one recorded decision", field, doc[field])
	}
	row, _ := rows[0].(map[string]any)
	return row
}

func TestProjectionSweepFacts(t *testing.T) {
	for _, scenario := range []string{"cap", "unit", "multiple reasons", "three reasons", "partial", "clean", "attention", "review marker", "readied"} {
		t.Run(scenario, func(t *testing.T) {
			state := newForgejoFixtureState(t)
			cfg := writeSweepFixtureConfig(t, state)
			var active string
			switch scenario {
			case "cap":
				active = "minos-run-other-project-pr2.service"
				content, err := os.ReadFile(filepath.Join(cfg.Root, "service.toml"))
				if err != nil {
					t.Fatal(err)
				}
				content = []byte(string(content) + "\nmax-concurrent = 1\n")
				writeTestFile(t, filepath.Join(cfg.Root, "service.toml"), string(content))
			case "unit":
				active = "minos-run-minos-e2e-owner-subject-pr1.service"
			case "multiple reasons", "three reasons":
				setSweepFixtureWorkInProgressPrefix(t, cfg, "structural/")
				state.changePullRequest(func(pr map[string]any) { pr["head"].(map[string]any)["ref"] = "structural/work" })
				state.setDependencies([]map[string]any{{"number": 7, "state": "open", "repository": map[string]any{"full_name": "owner/prerequisite"}}})
				if scenario == "three reasons" {
					state.setStatuses([]map[string]any{{"id": 7, "context": "Minos", "status": "failure", "description": product.Attention().Description(), "creator": map[string]any{"login": "Minos"}}})
				}
			case "partial":
				writeTestFile(t, filepath.Join(cfg.Root, "repos", "aaa-vanished.toml"), "forge = \"forgejo\"\nowner = \"minos-e2e-owner\"\nrepo = \"vanished\"\n[adaptation]\nrun-body = \"/opt/minos/run-body/run-body\"\n")
			case "readied":
				writeTestFile(t, sweepDeferralsPath(cfg), `{"kind":"minos-sweep-deferrals-v1","deferrals":[{"pr":"1","reason":"unobserved old deferral"}]}`)
			case "review marker":
				state.setReviews([]map[string]any{{"id": 41, "state": "APPROVED", "commit_id": state.headSHA(), "user": map[string]any{"login": "Minos"}}})
			case "clean", "attention":
				terminal := product.Clean()
				if scenario == "attention" {
					terminal = product.Attention()
				}
				state.setStatuses([]map[string]any{{"id": 7, "context": "Minos", "status": terminal.ForgeState(), "description": terminal.Description(), "creator": map[string]any{"login": "Minos"}}})
			}
			original := commandCombinedOutput
			t.Cleanup(func() { commandCombinedOutput = original })
			commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" && strings.Contains(strings.Join(args, " "), "list-units") && active != "" {
					return []byte(active + " loaded active running run\n"), nil
				}
				return nil, nil
			}
			if err := SweepCommand(t.Context(), []string{"-config", cfg.Root}); err != nil {
				t.Fatal(err)
			}
			doc := projectionJSON(t, sweepDeferralsPath(cfg))
			switch scenario {
			case "cap", "unit":
				row := projectionRow(t, doc, "suppressed")
				if row["blocking_unit"] != active {
					t.Fatalf("blocking unit = %#v, want %s", row, active)
				}
				if scenario == "cap" && !strings.Contains(fmt.Sprint(row["detail"]), "slots") {
					t.Fatalf("cap cause missing: %#v", row)
				}
			case "multiple reasons", "three reasons":
				row := projectionRow(t, doc, "deferrals")
				reasons, _ := row["reasons"].([]any)
				want := []any{`work-in-progress branch "structural/work"`, "open dependencies: owner/prerequisite#7"}
				if scenario == "three reasons" {
					want = []any{want[0], "completed-marker", want[1]}
				}
				if !reflect.DeepEqual(reasons, want) || row["reason"] != want[0] {
					t.Fatalf("all deferral reasons = %#v, selected = %#v, want %#v with WIP precedence", row["reasons"], row["reason"], want)
				}
			case "partial":
				skipped, _ := doc["skipped_repositories"].(map[string]any)
				if doc["partial"] != true || skipped["minos-e2e-owner/vanished"] == nil {
					t.Fatalf("partial pass coverage missing: %#v", doc)
				}
			case "clean", "attention", "review marker":
				row := projectionRow(t, doc, "terminal")
				want := scenario
				if scenario == "review marker" {
					want = "clean"
				}
				if row["outcome"] != want {
					t.Fatalf("terminal outcome = %#v, want %s", row, scenario)
				}
			case "readied":
				row := projectionRow(t, doc, "readied")
				if row["cause"] != "current forge head is eligible" || row["cleared_deferral"] != nil {
					t.Fatalf("readiness must name current cause without invented history: %#v", row)
				}
			}
		})
	}
}

func TestProjectionStatusFields(t *testing.T) {
	cfg := statusTestConfig(t)
	stubStatusCommands(t)
	sweep := `{"kind":"minos-sweep-deferrals-v1","completed_at":"2001-02-03T04:05:06Z","partial":true,"skipped_repositories":{"owner/broken":"unavailable"},"deferrals":[{"forge":"","owner":"","repo":"","pr":"1","reason":"wip","reasons":["wip","dependency"]}],"suppressed":[{"forge":"","owner":"","repo":"","pr":"2","blocking_unit":"unit","detail":"cap"}],"terminal":[{"forge":"","owner":"","repo":"","pr":"3","outcome":"attention"}],"readied":[{"forge":"","owner":"","repo":"","pr":"4","cause":"current forge head is eligible"}]}`
	heartbeat := `{"kind":"minos-receiver-heartbeat-v1","recorded_at":"2001-02-03T04:05:07Z","activity":"delivery","forge":"local"}`
	writeStatusFile(t, sweepDeferralsPath(cfg), sweep)
	writeStatusFile(t, filepath.Join(cfg.Runs.Dir, ".receiver-heartbeat.json"), heartbeat)
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set("Authorization", "Bearer status-token")
	response := httptest.NewRecorder()
	receiverRoutes(t.Context(), cfg).ServeHTTP(response, request)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"sweep": sweep, "receiver": heartbeat} {
		var expected, actual any
		_ = json.Unmarshal([]byte(want), &expected)
		_ = json.Unmarshal(got[field], &actual)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s projection = %s, want recorded fields %s", field, got[field], want)
		}
	}
	// A malformed heartbeat must cost only the heartbeat, not the sweep.
	writeStatusFile(t, filepath.Join(cfg.Runs.Dir, ".receiver-heartbeat.json"), "{")
	response = httptest.NewRecorder()
	receiverRoutes(t.Context(), cfg).ServeHTTP(response, request)
	var tolerant map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &tolerant); err != nil {
		t.Fatal(err)
	}
	if tolerant["sweep"] == nil || tolerant["receiver"] != nil {
		t.Fatalf("unreadable heartbeat must cost only itself: %s", response.Body.Bytes())
	}
}

func TestProjectionReceiverDelivery(t *testing.T) {
	cfg := receiverTestConfig(t)
	cfg.Runs.Dir = t.TempDir()
	response, err := sendAuthenticatedHook(t, cfg, "push", `{"repository":{"owner":{"login":"owner"},"name":"subject"}}`)
	if err != nil || response.Code != http.StatusAccepted {
		t.Fatalf("delivery = %d, %v", response.Code, err)
	}
	doc := projectionJSON(t, filepath.Join(cfg.Runs.Dir, ".receiver-heartbeat.json"))
	if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(doc["recorded_at"])); err != nil {
		t.Fatalf("invalid delivery stamp: %v", err)
	}
	if doc["activity"] != "delivery" || doc["forge"] != "local" || doc["recorded_at"] == nil {
		t.Fatalf("receiver delivery not recorded: %#v", doc)
	}
}
