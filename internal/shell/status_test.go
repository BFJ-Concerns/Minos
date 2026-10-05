package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestStatusReportsLiveRunIdentityDispatchStageAndTimings(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	// A finished review, then a review-brief workflow still running.
	writeStatusFile(t, filepath.Join(runDir, "review-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "review-result.done"), "0\n")
	writeStatusFile(t, filepath.Join(runDir, "review-result.json"), `{"status":"complete"}`)
	writeStatusFile(t, filepath.Join(runDir, "review-brief-args.json"), "{}")
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
	if run.Head != "58c2a8dce6310320b4868d954b22fcbdc81bca57" {
		t.Fatalf("head = %q, want the head recorded in the run orientation", run.Head)
	}
	if run.StartedAt != "2026-08-21T20:01:44Z" {
		t.Fatalf("started_at = %q, want the unit's active-enter time", run.StartedAt)
	}
	if run.Stage != "review-brief" {
		t.Fatalf("stage = %q, want review-brief", run.Stage)
	}
	if want := `{"kind":"minos-timing-record-v1","workers":[]}`; string(run.Timings) != want {
		t.Fatalf("timings = %s, want %s", run.Timings, want)
	}
	states := map[string]string{}
	for _, stage := range run.Stages {
		states[stage.Name] = stage.State
	}
	if states["review"] != stagePassed || states["review-brief"] != stageRunning {
		t.Fatalf("stage states = %v, want review passed and review-brief running", states)
	}
	if len(document.Repos) != 1 || document.Repos[0].Repo != "Relay" {
		t.Fatalf("repos = %v, want the configured repository", document.Repos)
	}
}

func TestStatusProjectsRecordedSweepDeferralsVerbatim(t *testing.T) {
	cfg := statusTestConfig(t)
	stubStatusCommands(t)
	want := sweepDeferralDocument{
		Kind:        sweepDeferralDocumentKind,
		CompletedAt: "2026-08-30T14:05:06Z",
		Deferrals: []sweepDeferral{
			{Forge: "forgejo", Owner: "example", Repo: "Relay", PR: "96", Reason: "open dependencies: example/prerequisite#7"},
			{Forge: "forgejo", Owner: "example", Repo: "Relay", PR: "97", Reason: `work-in-progress branch "structural/rework"`},
			{Forge: "forgejo", Owner: "example", Repo: "Relay", PR: "98", Reason: "completed-marker"},
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	writeStatusFile(t, sweepDeferralsPath(cfg), string(encoded))

	document := readStatusDocument(t, cfg)
	if document.Sweep == nil {
		t.Fatal("sweep deferrals were omitted")
	}
	if document.Sweep.CompletedAt != want.CompletedAt {
		t.Fatalf("completed_at = %q, want recorded stamp %q", document.Sweep.CompletedAt, want.CompletedAt)
	}
	if string(document.GeneratedAt) == document.Sweep.CompletedAt {
		t.Fatalf("generated_at = %q impersonates the sweep completion stamp", document.GeneratedAt)
	}
	if len(document.Sweep.Deferrals) != len(want.Deferrals) {
		t.Fatalf("deferrals = %#v, want %#v", document.Sweep.Deferrals, want.Deferrals)
	}
	for index, deferral := range want.Deferrals {
		if got := document.Sweep.Deferrals[index]; !reflect.DeepEqual(got, deferral) {
			t.Fatalf("deferral %d = %#v, want %#v", index, got, deferral)
		}
	}
}

func TestStatusToleratesMissingOrUnreadableSweepDeferrals(t *testing.T) {
	cfg := statusTestConfig(t)
	stubStatusCommands(t)
	if document := readStatusDocument(t, cfg); document.Sweep != nil {
		t.Fatalf("legacy tree sweep = %#v, want absent", document.Sweep)
	}
	writeStatusFile(t, sweepDeferralsPath(cfg), "not json")
	if document := readStatusDocument(t, cfg); document.Sweep != nil {
		t.Fatalf("unreadable sweep document = %#v, want absent", document.Sweep)
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

func TestStatusSplitsAttemptSuffixesBeforeClassifyingResidue(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	writeStatusFile(t, filepath.Join(runDir, "review@2-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "review@2-result.json"), `{"verdict":"complete"}`)
	writeStatusFile(t, filepath.Join(runDir, "review@10-args.json"), "{}")
	writeStatusFile(t, filepath.Join(runDir, "review@10-result.json"), `{"verdict":"complete"}`)
	writeStatusFile(t, filepath.Join(runDir, "gate-repair-r6d-args.json"), "{}")
	stubStatusCommands(t)

	run := readStatusDocument(t, cfg).Runs[0]
	if len(run.Stages) != 2 {
		t.Fatalf("stages = %+v, want only the valid dispatch attempts", run.Stages)
	}
	for _, stage := range run.Stages {
		if stage.Name == "review" && stage.Attempt == 2 && stage.State == stagePassed {
			continue
		}
		if stage.Name == "review" && stage.Attempt == 10 && stage.State == stagePassed {
			continue
		}
		t.Fatalf("stage = %+v, want valid base name and attempt 2 or 10 only", stage)
	}
}

func TestStatusReportsAJustStartedRunAsStarting(t *testing.T) {
	cfg := statusTestConfig(t)
	// The unit is live but has written nothing yet: no run directory, so no
	// orientation and no stages. This is how every run begins.
	stubStatusCommands(t)

	document := readStatusDocument(t, cfg)

	if len(document.Runs) != 1 {
		t.Fatalf("runs = %d, want the live unit reported", len(document.Runs))
	}
	run := document.Runs[0]
	if run.Error != "" {
		t.Fatalf("run error = %q, want a starting run reported without one", run.Error)
	}
	if run.Stage != stageStarting {
		t.Fatalf("stage = %q, want %q", run.Stage, stageStarting)
	}
	// Identity is recoverable from the unit name alone, which is what lets the
	// run name its pull request before it has oriented itself.
	if run.Owner != "example" || run.Repo != "Relay" || run.PR != "96" || run.Forge != "forgejo" {
		t.Fatalf("identity = %s/%s#%s on %q, want example/Relay#96 on forgejo", run.Owner, run.Repo, run.PR, run.Forge)
	}
}

func TestStatusReportsAnUnreadableRunWithoutFailingTheRequest(t *testing.T) {
	cfg := statusTestConfig(t)
	// Two directories for one unit: an earlier attempt's residue outlived its
	// unit, and which one the run owns cannot be told. That is a real fault,
	// unlike a directory that has not appeared yet.
	writeStatusRunDirectory(t, cfg)
	if err := os.MkdirAll(filepath.Join(cfg.Runs.Dir, statusRunUnit+"-99"), 0o700); err != nil {
		t.Fatal(err)
	}
	stubStatusCommands(t)

	document := readStatusDocument(t, cfg)

	if len(document.Runs) != 1 {
		t.Fatalf("runs = %d, want the live unit reported", len(document.Runs))
	}
	if document.Runs[0].Error == "" {
		t.Fatal("run error = empty, want the ambiguous run directory reported")
	}
}

func TestStatusReportsNullTimingsWhenTheRecordCannotBeAssembled(t *testing.T) {
	cfg := statusTestConfig(t)
	runDir := writeStatusRunDirectory(t, cfg)
	writeStatusFile(t, filepath.Join(runDir, "review-args.json"), "{}")
	failing := filepath.Join(t.TempDir(), "collect-timings")
	writeScript(t, failing, "#!/bin/sh\necho 'could not assemble the timing record' >&2\nexit 1\n")
	cfg.Runs.TimingsCommand = failing
	stubStatusCommands(t)

	run := readStatusDocument(t, cfg).Runs[0]

	if string(run.Timings) != "null" {
		t.Fatalf("timings = %s, want null", run.Timings)
	}
	if run.Stage != "review" {
		t.Fatalf("stage = %q, want review", run.Stage)
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

// The recent-runs route reports what a finished run did, from the timing
// sidecars archive-run leaves beside each archive. These pin the parsing that
// turns those sidecars back into runs, and that one unreadable sidecar costs
// only itself.

func TestRecentRunsReportsArchivedRecords(t *testing.T) {
	cfg := statusTestConfig(t)
	cfg.Runs.RecentTimingsCommand = archiveListingScript(t, map[string]string{
		"20260821T213617Z-Example-Corp-Gizmo-pr106-minos-run-Example-Corp-Gizmo-pr106-3124491702.timings.json": `{"kind":"minos-timing-record-v1","pull_request":{"owner":"Example-Corp","repository":"Gizmo","number":"106"},"workers":[{"label":"setup"}]}`,
		"20260821T200140Z-example-Relay-pr96-minos-run-example-Relay-pr96-527798279.timings.json":              `{"kind":"minos-timing-record-v1","pull_request":{"owner":"example","repository":"Relay","number":"96"},"workers":[]}`,
	})

	document := readRecentRuns(t, cfg, "")

	if document.Kind != recentRunsKind {
		t.Fatalf("kind = %q, want %q", document.Kind, recentRunsKind)
	}
	if document.WindowHours != defaultRecentWindowHours {
		t.Fatalf("window = %d, want %d", document.WindowHours, defaultRecentWindowHours)
	}
	if len(document.Runs) != 2 {
		t.Fatalf("runs = %d, want 2; %+v", len(document.Runs), document.Runs)
	}
	run := document.Runs[0]
	if run.Owner != "Example-Corp" || run.Repo != "Gizmo" || run.PR != "106" {
		t.Fatalf("identity = %s/%s#%s, want Example-Corp/Gizmo#106", run.Owner, run.Repo, run.PR)
	}
	if run.ArchivedAt != "2026-08-21T21:36:17Z" {
		t.Fatalf("archived_at = %q, want the archive's own timestamp", run.ArchivedAt)
	}
	if run.Unit != "minos-run-Example-Corp-Gizmo-pr106" {
		t.Fatalf("unit = %q, want the run's unit without its directory suffix", run.Unit)
	}
	if !json.Valid(run.Timings) {
		t.Fatalf("timings = %s, want the archived record", run.Timings)
	}
}

func TestRecentRunsSkipsAnUnreadableSidecar(t *testing.T) {
	cfg := statusTestConfig(t)
	cfg.Runs.RecentTimingsCommand = archiveListingScript(t, map[string]string{
		"20260821T213617Z-Example-Corp-Gizmo-pr106-minos-run-Example-Corp-Gizmo-pr106-3124491702.timings.json": `{"pull_request":{"owner":"Example-Corp","repository":"Gizmo","number":"106"}}`,
		"20260821T200140Z-example-Relay-pr96-minos-run-example-Relay-pr96-527798279.timings.json":              `{"truncated":`,
	})

	document := readRecentRuns(t, cfg, "")

	if len(document.Runs) != 1 || document.Runs[0].Repo != "Gizmo" {
		t.Fatalf("runs = %+v, want only the readable sidecar", document.Runs)
	}
}

func TestRecentRunsIsEmptyWithoutAnArchiveCommand(t *testing.T) {
	cfg := statusTestConfig(t)

	document := readRecentRuns(t, cfg, "")

	if len(document.Runs) != 0 {
		t.Fatalf("runs = %+v, want none when no archive command is configured", document.Runs)
	}
}

func TestRecentRunsHoldsTheWindowAndLimitWithinBounds(t *testing.T) {
	for _, test := range []struct {
		name       string
		query      string
		wantWindow int
		wantLimit  int
	}{
		{name: "as asked", query: "hours=6&limit=5", wantWindow: 6, wantLimit: 5},
		{name: "unasked", query: "", wantWindow: defaultRecentWindowHours, wantLimit: defaultRecentRecords},
		{name: "nonsense", query: "hours=soon&limit=lots", wantWindow: defaultRecentWindowHours, wantLimit: defaultRecentRecords},
		{name: "zero", query: "hours=0&limit=0", wantWindow: defaultRecentWindowHours, wantLimit: defaultRecentRecords},
		{name: "beyond the ceiling", query: "hours=100000&limit=100000", wantWindow: maximumRecentWindowHours, wantLimit: maximumRecentRecords},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := readRecentRuns(t, statusTestConfig(t), test.query)
			if document.WindowHours != test.wantWindow || document.Limit != test.wantLimit {
				t.Fatalf("window/limit = %d/%d, want %d/%d",
					document.WindowHours, document.Limit, test.wantWindow, test.wantLimit)
			}
		})
	}
}

// archiveListingScript stands in for the archive host, emitting the same
// filename-and-base64 framing the real listing produces.
func archiveListingScript(t *testing.T, sidecars map[string]string) string {
	t.Helper()
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	names := make([]string, 0, len(sidecars))
	for name := range sidecars {
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		body.WriteString("printf '%s\\t%s\\n' '" + name + "' '" +
			base64.StdEncoding.EncodeToString([]byte(sidecars[name])) + "'\n")
	}
	path := filepath.Join(t.TempDir(), "list-recent-timings")
	writeScript(t, path, body.String())
	return path
}

func readRecentRuns(t *testing.T, cfg ServiceConfig, query string) recentRunsDocument {
	t.Helper()
	return readCachedRecentRuns(t, cfg, newRecentRunsCache(0), query)
}
