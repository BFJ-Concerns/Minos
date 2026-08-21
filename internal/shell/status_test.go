package shell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The status projection is what an operator surface reads to say which pull
// requests are live and how far each has got. These tests pin the two things
// that make it trustworthy: nothing reaches it without the configured token,
// and a run's reported stage follows the lifecycle's own args/flag/result
// convention rather than a guess.

const statusRunUnit = "minos-run-example-Relay-pr96"

func TestStatusRouteIsAbsentUntilATokenIsConfigured(t *testing.T) {
	cfg := statusTestConfig(t)
	cfg.Listener.StatusTokenFile = ""

	response := httptest.NewRecorder()
	receiverRoutes(context.Background(), cfg).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestStatusRefusesRequestsWithoutTheConfiguredToken(t *testing.T) {
	cfg := statusTestConfig(t)
	stubStatusCommands(t)

	for _, test := range []struct {
		name       string
		authorises func(*http.Request)
	}{
		{name: "no header", authorises: func(*http.Request) {}},
		{name: "wrong token", authorises: func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }},
		{name: "not a bearer", authorises: func(r *http.Request) { r.Header.Set("Authorization", "token status-token") }},
		{name: "bare token", authorises: func(r *http.Request) { r.Header.Set("Authorization", "status-token") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/status", nil)
			test.authorises(request)
			response := httptest.NewRecorder()
			if err := handleStatus(context.Background(), cfg, response, request); err != nil {
				t.Fatalf("handleStatus() error = %v", err)
			}
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusUnauthorized, response.Body.String())
			}
		})
	}
}

func TestStatusReportsLiveRunIdentityStageAndTimings(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	// A finished dispatch, then one still running: the run is in gate repair.
	writeStatusFile(t, filepath.Join(runDir, "setup-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "setup-result.done"), "0\n")
	writeStatusFile(t, filepath.Join(runDir, "setup-result.json"), `{"verdict":"ready"}`)
	// A foreground dispatch publishes its result but no completion flag; the
	// result alone settles it as passed.
	writeStatusFile(t, filepath.Join(runDir, "fix-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "fix-result.json"), `{"applied":1}`)
	writeStatusFile(t, filepath.Join(runDir, "test-command-output.log"), "suite output\n")
	writeStatusFile(t, filepath.Join(runDir, "timings.ndjson"),
		`{"kind":"minos-timing-event-v1","name":"test-command",`+
			`"started_at":"2026-08-21T20:02:00Z","ended_at":"2026-08-21T20:03:00Z",`+
			`"duration_ms":60000,"exit_status":1}`+"\n")
	writeStatusFile(t, filepath.Join(runDir, "gate-repair-args.json"), "{}")
	cfg.Runs.TimingsCommand = statusTimingsScript(t, `{"kind":"minos-timing-record-v1","workers":[]}`)
	stubStatusCommands(t)

	document := readStatusDocument(t, cfg)

	if document.Kind != statusDocumentKind {
		t.Fatalf("kind = %q, want %q", document.Kind, statusDocumentKind)
	}
	if len(document.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(document.Runs))
	}
	run := document.Runs[0]
	if run.Error != "" {
		t.Fatalf("run error = %q, want none", run.Error)
	}
	if run.Owner != "example" || run.Repo != "Relay" || run.PR != "96" || run.Forge != "forgejo" {
		t.Fatalf("identity = %s/%s#%s on %q, want example/Relay#96 on forgejo", run.Owner, run.Repo, run.PR, run.Forge)
	}
	if run.Head != "6fb27999e7bc27d3778d7128593aba2ea833c281" {
		t.Fatalf("head = %q, want the current head the run moved to", run.Head)
	}
	if run.StartedAt != "2026-08-21T20:01:44Z" {
		t.Fatalf("started_at = %q, want the unit's active-enter time", run.StartedAt)
	}
	if run.Stage != "gate-repair" {
		t.Fatalf("stage = %q, want gate-repair", run.Stage)
	}
	if want := `{"kind":"minos-timing-record-v1","workers":[]}`; string(run.Timings) != want {
		t.Fatalf("timings = %s, want %s", run.Timings, want)
	}
	states := map[string]string{}
	for _, stage := range run.Stages {
		states[stage.Name] = stage.State
	}
	if states["setup"] != stagePassed || states["test"] != stageFailed || states["gate-repair"] != stageRunning {
		t.Fatalf("stage states = %v, want setup passed, test failed, gate-repair running", states)
	}
	if states["fix"] != stagePassed {
		t.Fatalf("stage states = %v, want an unflagged dispatch with a result counted as passed", states)
	}
	if len(document.Repos) != 1 || document.Repos[0].Repo != "Relay" {
		t.Fatalf("repos = %v, want the configured repository", document.Repos)
	}
}

func TestStatusReportsAFailedDispatchAsFailed(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	// The flag is written whatever the outcome; only a clean exit publishes
	// the result beside it, so a flag alone is a dispatch that failed.
	writeStatusFile(t, filepath.Join(runDir, "review-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "review-result.done"), "1\n")
	writeStatusFile(t, filepath.Join(runDir, "review-result.json.partial"), "{")
	stubStatusCommands(t)

	run := readStatusDocument(t, cfg).Runs[0]

	if run.Stage != "review" {
		t.Fatalf("stage = %q, want review", run.Stage)
	}
	if len(run.Stages) != 1 || run.Stages[0].State != stageFailed {
		t.Fatalf("stages = %+v, want a single failed review stage", run.Stages)
	}
}

func TestStatusReportsAnUnreadableRunWithoutFailingTheRequest(t *testing.T) {
	cfg := statusTestConfig(t)
	// No run directory at all: the unit is live but its residue is gone.
	stubStatusCommands(t)

	document := readStatusDocument(t, cfg)

	if len(document.Runs) != 1 {
		t.Fatalf("runs = %d, want the live unit reported", len(document.Runs))
	}
	if document.Runs[0].Error == "" {
		t.Fatal("run error = empty, want the missing run directory reported")
	}
	if document.Runs[0].Stage != stageUnknown {
		t.Fatalf("stage = %q, want %q", document.Runs[0].Stage, stageUnknown)
	}
}

func TestStatusReportsNullTimingsWhenTheRecordCannotBeAssembled(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	writeStatusFile(t, filepath.Join(runDir, "setup-args.json"), "{}")
	failing := filepath.Join(t.TempDir(), "collect-timings")
	writeScript(t, failing, "#!/bin/sh\necho 'could not assemble the timing record' >&2\nexit 1\n")
	cfg.Runs.TimingsCommand = failing
	stubStatusCommands(t)

	run := readStatusDocument(t, cfg).Runs[0]

	if string(run.Timings) != "null" {
		t.Fatalf("timings = %s, want null", run.Timings)
	}
	if run.Stage != "setup" {
		t.Fatalf("stage = %q, want setup", run.Stage)
	}
}

func statusTestConfig(t *testing.T) ServiceConfig {
	t.Helper()
	root := t.TempDir()
	tokenPath := filepath.Join(root, "status.token")
	writeStatusFile(t, tokenPath, "status-token\n")
	if err := os.Mkdir(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := `forge = "forgejo"
owner = "example"
repo = "Relay"
[adaptation]
run-body = "/opt/minos/run-body/run-body"
`
	writeStatusFile(t, filepath.Join(root, "repos", "relay.toml"), repoConfig)

	cfg := ServiceConfig{Root: root}
	cfg.Listener.StatusTokenFile = tokenPath
	cfg.Runs.Dir = t.TempDir()
	cfg.Runs.MaxConcurrent = 2
	cfg.Forges = map[string]ForgeConfig{"forgejo": {}}
	return cfg
}

func writeStatusRunDirectory(t *testing.T, cfg ServiceConfig) string {
	t.Helper()
	runDir := filepath.Join(cfg.Runs.Dir, statusRunUnit+"-3706721003")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orientation := `{"head":"58c2a8dce6310320b4868d954b22fcbdc81bca57",
"source":{"owner":"example","repo":"Relay","pr":"96"}}`
	writeStatusFile(t, filepath.Join(runDir, "orientation.json"), orientation)
	reconciliation := `{"outcome":"reconciled","publish":true,` +
		`"merge":"6fb27999e7bc27d3778d7128593aba2ea833c281"}`
	writeStatusFile(t, filepath.Join(runDir, "reconciliation.json"), reconciliation)
	return runDir
}

// statusTimingsScript stands in for collect-timings: it publishes the given
// record through the temporary file and rename the real script uses, so the
// endpoint's read-back path is exercised as deployed.
func statusTimingsScript(t *testing.T, record string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collect-timings")
	writeScript(t, path, "#!/bin/sh\nprintf '%s' '"+record+"' >\"$2.tmp\"\nmv \"$2.tmp\" \"$2\"\n")
	return path
}

// stubStatusCommands answers the two systemd queries the projection makes and
// lets the timing command run for real, so the wrapper's argument order is
// tested rather than assumed.
func stubStatusCommands(t *testing.T) {
	t.Helper()
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" {
			return original(ctx, name, args...)
		}
		for _, arg := range args {
			if arg == "list-units" {
				return []byte(statusRunUnit + ".service loaded active running run\n"), nil
			}
		}
		return []byte("@1787342504\n"), nil
	}
}

func readStatusDocument(t *testing.T, cfg ServiceConfig) statusDocument {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set("Authorization", "Bearer status-token")
	response := httptest.NewRecorder()
	if err := handleStatus(context.Background(), cfg, response, request); err != nil {
		t.Fatalf("handleStatus() error = %v", err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusOK, response.Body.String())
	}
	var document statusDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode status document: %v; body = %q", err, response.Body.String())
	}
	if generated, err := time.Parse(time.RFC3339, document.GeneratedAt); err != nil || generated.IsZero() {
		t.Fatalf("generated_at = %q, want an RFC 3339 instant", document.GeneratedAt)
	}
	return document
}

func writeStatusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
