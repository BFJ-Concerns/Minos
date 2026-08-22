package shell

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The timing telemetry is presentation-class residue for the operator:
// time-on-exit appends one event line per wrapped command execution,
// collect-timings assembles the deterministic per-run timing record from
// that log and the Ensemble run records already on disk, and archive-run
// delivers the record as a sidecar beside the archive tarball. None of it
// may change a wrapped command's outcome or fail a run.

type timingEvent struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	DurationMs *int64 `json:"duration_ms"`
	ExitStatus int    `json:"exit_status"`
}

type timingWorker struct {
	Record      string  `json:"record"`
	Label       *string `json:"label"`
	Engine      *string `json:"engine"`
	Model       *string `json:"model"`
	Status      *string `json:"status"`
	QueuedMs    *int64  `json:"queued_ms"`
	ExecutionMs *int64  `json:"execution_ms"`
	StartedAt   *string `json:"started_at"`
	EndedAt     *string `json:"ended_at"`
}

type timingPhase struct {
	Record      string  `json:"record"`
	Phase       *string `json:"phase"`
	Arms        int     `json:"arms"`
	StartedAt   *string `json:"started_at"`
	WallMs      *int64  `json:"wall_ms"`
	MedianArmMs *int64  `json:"median_arm_ms"`
	LongestArm  *struct {
		Label *string `json:"label"`
		Ms    *int64  `json:"ms"`
	} `json:"longest_arm"`
	Straggler bool `json:"straggler"`
}

type timingRecord struct {
	Kind        string `json:"kind"`
	GeneratedAt string `json:"generated_at"`
	PullRequest struct {
		Owner      string `json:"owner"`
		Repository string `json:"repository"`
		Number     string `json:"number"`
	} `json:"pull_request"`
	Commands []timingEvent  `json:"commands"`
	Workers  []timingWorker `json:"workers"`
	Phases   []timingPhase  `json:"phases"`
	Memory   *struct {
		Baseline         map[string]any   `json:"baseline"`
		Final            map[string]any   `json:"final"`
		PeakAnonPlusSwap *int64           `json:"peak_anon_plus_swap"`
		Delta            map[string]int64 `json:"delta"`
	} `json:"memory"`
}

func runTimeOnExit(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "time-on-exit"), args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func runCollectTimings(t *testing.T, env map[string]string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "collect-timings"), args...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd.CombinedOutput()
}

func readTimingEvents(t *testing.T, eventLog string) []timingEvent {
	t.Helper()
	content, err := os.ReadFile(eventLog)
	if err != nil {
		t.Fatalf("event log missing: %v", err)
	}
	var events []timingEvent
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		var event timingEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q does not parse: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestTimeOnExitAppendsOneEventPerExecutionAfterTheCommandExits(t *testing.T) {
	dir := t.TempDir()
	eventLog := filepath.Join(dir, "timings.ndjson")
	seen := filepath.Join(dir, "log-state-during-command")

	// The wrapped command records whether the log already exists while it is
	// still running: the event must appear only after the command has exited.
	output, err := runTimeOnExit(t, eventLog, "first-command", "sh", "-c",
		`if [ -e "$0" ]; then echo present >"$1"; else echo absent >"$1"; fi`, eventLog, seen)
	if err != nil {
		t.Fatalf("time-on-exit: %v\n%s", err, output)
	}
	during, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(during)); got != "absent" {
		t.Fatalf("event log state during the first command = %q, want absent", got)
	}

	// A second, failing execution appends rather than replacing, and the
	// wrapper preserves the command's exit status.
	output, err = runTimeOnExit(t, eventLog, "second-command", "sh", "-c", "exit 7")
	var exitError *exec.ExitError
	if err == nil {
		t.Fatalf("wrapper exited 0 for a failing command\n%s", output)
	} else if ok := errors.As(err, &exitError); !ok || exitError.ExitCode() != 7 {
		t.Fatalf("wrapper exit = %v, want the command's status 7\n%s", err, output)
	}

	events := readTimingEvents(t, eventLog)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 (append, never replace)", len(events))
	}
	first, second := events[0], events[1]
	if first.Kind != "minos-timing-event-v1" || second.Kind != "minos-timing-event-v1" {
		t.Fatalf("event kinds = %q, %q", first.Kind, second.Kind)
	}
	if first.Name != "first-command" || first.ExitStatus != 0 {
		t.Fatalf("first event = %+v, want first-command with exit 0", first)
	}
	if second.Name != "second-command" || second.ExitStatus != 7 {
		t.Fatalf("second event = %+v, want second-command with exit 7", second)
	}
	for _, event := range events {
		if event.StartedAt == "" || event.EndedAt == "" || event.StartedAt > event.EndedAt {
			t.Fatalf("event span %q..%q is not ordered", event.StartedAt, event.EndedAt)
		}
		if event.DurationMs != nil && *event.DurationMs < 0 {
			t.Fatalf("negative duration %d", *event.DurationMs)
		}
	}
}

func TestTimeOnExitNeverFailsTheCommandForAnUnwritableLog(t *testing.T) {
	dir := t.TempDir()
	eventLog := filepath.Join(dir, "missing-directory", "timings.ndjson")
	marker := filepath.Join(dir, "command-ran")

	output, err := runTimeOnExit(t, eventLog, "unrecorded-command", "sh", "-c",
		`echo ran >"$0"`, marker)
	if err != nil {
		t.Fatalf("wrapper failed because telemetry failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("wrapped command did not run: %v", err)
	}
	if !strings.Contains(string(output), "could not append the timing event") {
		t.Fatalf("missing telemetry warning\n%s", output)
	}
}

func TestTimeOnExitRefusesMissingArguments(t *testing.T) {
	output, err := runTimeOnExit(t, "/tmp/event-log-only", "name-only")
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		t.Fatalf("usage exit = %v, want 2\n%s", err, output)
	}
	if !strings.Contains(string(output), "usage: time-on-exit") {
		t.Fatalf("usage output = %s", output)
	}
}

func TestCollectTimingsSummarisesPhasesAndFlagsTheStraggler(t *testing.T) {
	runDir := t.TempDir()
	verify := filepath.Join(runDir, "ensemble-records", "record-a", "agents")
	writeTimingPhaseAgentRecord(t, filepath.Join(verify, "0001", "agent.json"),
		"verifier-1", "Verify", 300000, "2026-08-17T00:00:00Z", "2026-08-17T00:05:00Z")
	writeTimingPhaseAgentRecord(t, filepath.Join(verify, "0002", "agent.json"),
		"verifier-2", "Verify", 320000, "2026-08-17T00:00:00Z", "2026-08-17T00:05:20Z")
	writeTimingPhaseAgentRecord(t, filepath.Join(verify, "0003", "agent.json"),
		"verifier-3", "Verify", 900000, "2026-08-17T00:00:10Z", "2026-08-17T00:15:10Z")
	writeTimingPhaseAgentRecord(t, filepath.Join(verify, "0004", "agent.json"),
		"reviewer-1", "Review", 240000, "2026-08-17T00:16:00Z", "2026-08-17T00:20:00Z")
	writeTimingPhaseAgentRecord(t, filepath.Join(verify, "0005", "agent.json"),
		"reviewer-2", "Review", 250000, "2026-08-17T00:16:00Z", "2026-08-17T00:20:10Z")

	recordPath := filepath.Join(runDir, "timing-record.json")
	output, err := runCollectTimings(t, map[string]string{
		"MINOS_OWNER": "example", "MINOS_REPO_NAME": "Relay", "MINOS_PR": "53",
	}, runDir, recordPath)
	if err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("timing record does not parse: %v\n%s", err, content)
	}

	if len(record.Phases) != 2 {
		t.Fatalf("phase count = %d, want 2\n%s", len(record.Phases), content)
	}
	held, balanced := record.Phases[0], record.Phases[1]
	if held.Phase == nil || *held.Phase != "Verify" || balanced.Phase == nil || *balanced.Phase != "Review" {
		t.Fatalf("phases are not sorted by start: %+v, %+v", held.Phase, balanced.Phase)
	}
	if held.Arms != 3 || balanced.Arms != 2 {
		t.Fatalf("arm counts = %d, %d, want 3 and 2", held.Arms, balanced.Arms)
	}
	if held.WallMs == nil || *held.WallMs != 910000 {
		t.Fatalf("held phase wall = %+v, want 910000 (earliest start to latest end)", held.WallMs)
	}
	if held.MedianArmMs == nil || *held.MedianArmMs != 320000 {
		t.Fatalf("held phase median arm = %+v, want 320000", held.MedianArmMs)
	}
	if held.LongestArm == nil || held.LongestArm.Label == nil || *held.LongestArm.Label != "verifier-3" ||
		held.LongestArm.Ms == nil || *held.LongestArm.Ms != 900000 {
		t.Fatalf("held phase longest arm = %+v, want verifier-3 at 900000", held.LongestArm)
	}
	if !held.Straggler {
		t.Fatalf("an arm at nearly three times the phase median was not flagged as straggling:\n%s", content)
	}
	if balanced.Straggler {
		t.Fatalf("a balanced phase was flagged as straggling:\n%s", content)
	}
}

func TestMemoryTelemetrySnapshotsHonestCountersFromTheCgroup(t *testing.T) {
	cgroup := t.TempDir()
	stat := "anon 2265636864\nfile 12687937536\nworkingset_refault_anon 14067688\nworkingset_refault_file 47279103\n"
	pressure := "some avg10=0.12 avg60=0.16 avg300=2.37 total=1064808220\n" +
		"full avg10=0.00 avg60=0.09 avg300=1.72 total=880404397\n"
	for name, content := range map[string]string{
		"memory.stat": stat, "memory.pressure": pressure, "memory.swap.current": "4800729088\n",
	} {
		if err := os.WriteFile(filepath.Join(cgroup, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(t.TempDir(), "snapshot.json")
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "memory-telemetry"), output)
	cmd.Env = append(os.Environ(), "MINOS_CGROUP_DIR="+cgroup)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("memory-telemetry: %v\n%s", err, combined)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(content, &snapshot); err != nil {
		t.Fatalf("snapshot does not parse: %v\n%s", err, content)
	}
	if snapshot["kind"] != "minos-memory-snapshot-v1" {
		t.Fatalf("snapshot kind = %v", snapshot["kind"])
	}
	for key, want := range map[string]float64{
		"anon": 2265636864, "swap": 4800729088,
		"stall_some_us": 1064808220, "stall_full_us": 880404397,
		"refault_anon": 14067688, "refault_file": 47279103,
	} {
		if got, ok := snapshot[key].(float64); !ok || got != want {
			t.Fatalf("snapshot %s = %v, want %v", key, snapshot[key], want)
		}
	}
	if _, present := snapshot["memory_current"]; present {
		t.Fatal("snapshot carries a page-cache-inflated counter")
	}
}

func TestCollectTimingsCarriesMemoryStallAndThrashDeltas(t *testing.T) {
	runDir := t.TempDir()
	baseline := `{"kind":"minos-memory-snapshot-v1","sampled_at":"2026-08-22T00:00:00Z",` +
		`"anon":2000000000,"swap":1000000000,"stall_some_us":100000,"stall_full_us":40000,` +
		`"refault_anon":5000,"refault_file":90000}`
	final := `{"kind":"minos-memory-snapshot-v1","sampled_at":"2026-08-22T04:00:00Z",` +
		`"anon":2100000000,"swap":6000000000,"stall_some_us":900100000,"stall_full_us":600040000,` +
		`"refault_anon":14005000,"refault_file":47090000}`
	if err := os.WriteFile(filepath.Join(runDir, "memory-baseline.json"), []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "memory-final.json"), []byte(final), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "memory-peak"), []byte("9400000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recordPath := filepath.Join(runDir, "timing-record.json")
	output, err := runCollectTimings(t, map[string]string{
		"MINOS_OWNER": "example", "MINOS_REPO_NAME": "Relay", "MINOS_PR": "53",
	}, runDir, recordPath)
	if err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("timing record does not parse: %v\n%s", err, content)
	}
	if record.Memory == nil || record.Memory.Delta == nil {
		t.Fatalf("memory deltas absent despite both snapshots:\n%s", content)
	}
	for key, want := range map[string]int64{
		"stall_some_us": 900000000, "stall_full_us": 600000000,
		"refault_anon": 14000000, "refault_file": 47000000,
	} {
		if record.Memory.Delta[key] != want {
			t.Fatalf("memory delta %s = %d, want %d", key, record.Memory.Delta[key], want)
		}
	}
	if record.Memory.Baseline == nil || record.Memory.Final == nil {
		t.Fatalf("memory snapshots not retained alongside the delta:\n%s", content)
	}
	if record.Memory.PeakAnonPlusSwap == nil || *record.Memory.PeakAnonPlusSwap != 9400000000 {
		t.Fatalf("supervisor's peak footprint not carried:\n%s", content)
	}
}

func writeTimingPhaseAgentRecord(t *testing.T, path, label, phase string, executionMs int64, startedAt, endedAt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"id": 1, "engine": "codex", "label": label, "phase": phase,
		"model": nil, "resolved_model": "gpt-5.6-terra", "status": "complete",
		"queued_ms": 12, "execution_ms": executionMs,
		"attempts": []map[string]any{{"attempt": 1, "started_at": startedAt, "ended_at": endedAt}},
	}
	content, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTimingFixtureAgentRecord(t *testing.T, path, startedAt, endedAt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"id":1,"engine":"codex","label":"specialist-1-correctness-gpt",` +
		`"phase":"Specialise","model":null,"resolved_model":"gpt-5.6-terra",` +
		`"status":"complete","queued_ms":12,"execution_ms":345,` +
		`"attempts":[{"attempt":1,"started_at":"` + startedAt + `","ended_at":"` + endedAt + `"}]}`
	if err := os.WriteFile(path, []byte(record+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectTimingsAssemblesCommandsAndWorkersSorted(t *testing.T) {
	runDir := t.TempDir()
	events := `{"kind":"minos-timing-event-v1","name":"review","started_at":"2026-08-17T00:20:00Z","ended_at":"2026-08-17T00:40:00Z","duration_ms":1200000,"exit_status":0}
this line is not JSON and must cost one event, never the record
{"kind":"minos-timing-event-v1","name":"build-command","started_at":"2026-08-17T00:02:00Z","ended_at":"2026-08-17T00:03:00Z","duration_ms":60000,"exit_status":0}
`
	if err := os.WriteFile(filepath.Join(runDir, "timings.ndjson"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTimingFixtureAgentRecord(t,
		filepath.Join(runDir, "ensemble-records", "record-a", "agents", "0001", "agent.json"),
		"2026-08-17T00:21:00Z", "2026-08-17T00:25:00Z")
	writeTimingFixtureAgentRecord(t,
		filepath.Join(runDir, "home", ".local", "share", "ensemble", "runs", "cwd", "hash", "run-b", "agents", "0002", "agent.json"),
		"2026-08-17T00:05:00Z", "2026-08-17T00:06:00Z")

	recordPath := filepath.Join(runDir, "timing-record.json")
	output, err := runCollectTimings(t, map[string]string{
		"MINOS_OWNER": "example", "MINOS_REPO_NAME": "Relay", "MINOS_PR": "53",
	}, runDir, recordPath)
	if err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}

	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("timing record does not parse: %v\n%s", err, content)
	}
	if record.Kind != "minos-timing-record-v1" {
		t.Fatalf("record kind = %q", record.Kind)
	}
	if record.PullRequest.Owner != "example" || record.PullRequest.Repository != "Relay" || record.PullRequest.Number != "53" {
		t.Fatalf("pull request identity = %+v", record.PullRequest)
	}
	if len(record.Commands) != 2 {
		t.Fatalf("command count = %d, want 2 (the malformed line costs one event only)", len(record.Commands))
	}
	if record.Commands[0].Name != "build-command" || record.Commands[1].Name != "review" {
		t.Fatalf("commands are not sorted by start: %q, %q", record.Commands[0].Name, record.Commands[1].Name)
	}
	if len(record.Workers) != 2 {
		t.Fatalf("worker count = %d, want 2 (both record roots harvested)", len(record.Workers))
	}
	early, late := record.Workers[0], record.Workers[1]
	if early.StartedAt == nil || *early.StartedAt != "2026-08-17T00:05:00Z" {
		t.Fatalf("workers are not sorted by start: first = %+v", early)
	}
	if !strings.HasPrefix(early.Record, "home/") {
		t.Fatalf("directly launched record path = %q, want the run-private home root", early.Record)
	}
	if late.Record != "ensemble-records/record-a" {
		t.Fatalf("retained record path = %q", late.Record)
	}
	for _, worker := range record.Workers {
		if worker.Engine == nil || *worker.Engine != "codex" {
			t.Fatalf("worker engine = %+v", worker.Engine)
		}
		if worker.Model == nil || *worker.Model != "gpt-5.6-terra" {
			t.Fatalf("worker model = %+v", worker.Model)
		}
		if worker.ExecutionMs == nil || *worker.ExecutionMs != 345 {
			t.Fatalf("worker execution_ms = %+v", worker.ExecutionMs)
		}
	}
}

func TestCollectTimingsHarvestsASingleRecordRootWhenTheOtherIsAbsent(t *testing.T) {
	runDir := t.TempDir()
	writeTimingFixtureAgentRecord(t,
		filepath.Join(runDir, "ensemble-records", "record-a", "agents", "0001", "agent.json"),
		"2026-08-17T00:21:00Z", "2026-08-17T00:25:00Z")

	recordPath := filepath.Join(runDir, "timing-record.json")
	output, err := runCollectTimings(t, nil, runDir, recordPath)
	if err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("timing record does not parse: %v\n%s", err, content)
	}
	if len(record.Workers) != 1 {
		t.Fatalf("worker count = %d, want 1 (a missing sibling root must not erase the present one)", len(record.Workers))
	}
}

func TestCollectTimingsWritesAnEmptyRecordForABareRunDirectory(t *testing.T) {
	runDir := t.TempDir()
	recordPath := filepath.Join(runDir, "timing-record.json")
	output, err := runCollectTimings(t, nil, runDir, recordPath)
	if err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("timing record does not parse: %v\n%s", err, content)
	}
	if len(record.Commands) != 0 || len(record.Workers) != 0 {
		t.Fatalf("bare run directory produced %d commands, %d workers", len(record.Commands), len(record.Workers))
	}
	if record.PullRequest.Owner != "unknown" {
		t.Fatalf("owner without environment = %q, want unknown", record.PullRequest.Owner)
	}
	if _, err := os.Stat(recordPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left beside the record: %v", err)
	}
}

func TestCollectTimingsRefusesMissingArguments(t *testing.T) {
	output, err := runCollectTimings(t, nil, "/tmp/run-dir-only")
	var exitError *exec.ExitError
	if err == nil || !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		t.Fatalf("usage exit = %v, want 2\n%s", err, output)
	}
	if !strings.Contains(string(output), "usage: collect-timings") {
		t.Fatalf("usage output = %s", output)
	}
}

func TestArchiveRunDeliversTimingSidecarBesideTheTarball(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"kind":"minos-timing-event-v1","name":"build-command","started_at":"2026-08-17T00:02:00Z","ended_at":"2026-08-17T00:03:00Z","duration_ms":60000,"exit_status":0}` + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "timings.ndjson"), []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTimingFixtureAgentRecord(t,
		filepath.Join(runDir, "ensemble-records", "record-a", "agents", "0001", "agent.json"),
		"2026-08-17T00:21:00Z", "2026-08-17T00:25:00Z")

	bin := filepath.Join(root, "bin")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{bin, destination} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture ssh executes the remote command locally and the fixture
	// zstd is a passthrough, so the whole transport runs inside the temp
	// directory while archive-run's own logic stays real.
	writeScript(t, filepath.Join(bin, "ssh"), `#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
exec sh -c "$last"
`)
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\nexec cat\n")

	identity := filepath.Join(root, "identity")
	knownHosts := filepath.Join(root, "known-hosts")
	for _, path := range []string{identity, knownHosts} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archiveConfig := filepath.Join(root, "archive.env")
	config := strings.Join([]string{
		`MINOS_ARCHIVE_HOST="fixture"`,
		`MINOS_ARCHIVE_DESTINATION="` + destination + `"`,
		`MINOS_ARCHIVE_IDENTITY_FILE="` + identity + `"`,
		`MINOS_ARCHIVE_KNOWN_HOSTS="` + knownHosts + `"`,
	}, "\n") + "\n"
	if err := os.WriteFile(archiveConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	archive, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "archive-run"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(archive, runDir, "example-Relay-pr53")
	cmd.Env = append(os.Environ(),
		"MINOS_ARCHIVE_CONFIG="+archiveConfig,
		"PATH="+bin+":"+os.Getenv("PATH"),
		"MINOS_OWNER=example", "MINOS_REPO_NAME=Relay", "MINOS_PR=53",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("archive-run: %v\n%s", err, output)
	}

	tarballs, err := filepath.Glob(filepath.Join(destination, "*.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tarballs) != 1 {
		t.Fatalf("tarball count = %d, want 1: %v", len(tarballs), tarballs)
	}
	if name := filepath.Base(tarballs[0]); !strings.HasSuffix(name, "-example-Relay-pr53-run.tar.zst") {
		t.Fatalf("ordinary tarball name = %q, want timestamp plus unchanged label and run name", name)
	}
	sidecar := strings.TrimSuffix(tarballs[0], ".tar.zst") + ".timings.json"
	content, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("timing sidecar missing beside the tarball: %v", err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("sidecar does not parse: %v\n%s", err, content)
	}
	if record.Kind != "minos-timing-record-v1" {
		t.Fatalf("sidecar kind = %q", record.Kind)
	}
	if len(record.Commands) != 1 || len(record.Workers) != 1 {
		t.Fatalf("sidecar carries %d commands, %d workers, want 1 and 1", len(record.Commands), len(record.Workers))
	}
	if _, err := os.Stat(sidecar + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial sidecar left beside the delivered one: %v", err)
	}
}

func TestArchiveRunSanitisesHostileRunNameBeforeRemoteCommands(t *testing.T) {
	root := t.TempDir()
	runName := "run'; touch \"$INJECTION_MARKER\"; printf '\nend"
	runDir := filepath.Join(root, runName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "timings.ndjson"), []byte(`{"kind":"minos-timing-event-v1","name":"hostile-name","started_at":"2026-08-17T00:02:00Z","ended_at":"2026-08-17T00:03:00Z","duration_ms":60000,"exit_status":0}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{bin, destination} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(t, filepath.Join(bin, "ssh"), `#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
exec sh -c "$last"
`)
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\nexec cat\n")

	identity := filepath.Join(root, "identity")
	knownHosts := filepath.Join(root, "known-hosts")
	for _, path := range []string{identity, knownHosts} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archiveConfig := filepath.Join(root, "archive.env")
	config := strings.Join([]string{
		`MINOS_ARCHIVE_HOST="fixture"`,
		`MINOS_ARCHIVE_DESTINATION="` + destination + `"`,
		`MINOS_ARCHIVE_IDENTITY_FILE="` + identity + `"`,
		`MINOS_ARCHIVE_KNOWN_HOSTS="` + knownHosts + `"`,
	}, "\n") + "\n"
	if err := os.WriteFile(archiveConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	archive, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "archive-run"))
	if err != nil {
		t.Fatal(err)
	}
	injectionMarker := filepath.Join(root, "injected")
	cmd := exec.Command(archive, runDir, "example-Relay-pr53")
	cmd.Env = append(os.Environ(),
		"MINOS_ARCHIVE_CONFIG="+archiveConfig,
		"PATH="+bin+":"+os.Getenv("PATH"),
		"INJECTION_MARKER="+injectionMarker,
		"MINOS_OWNER=example", "MINOS_REPO_NAME=Relay", "MINOS_PR=53",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("archive-run: %v\n%s", err, output)
	}
	if _, err := os.Stat(injectionMarker); !os.IsNotExist(err) {
		t.Fatalf("hostile run name executed a remote-shell command: %v", err)
	}

	tarballs, err := filepath.Glob(filepath.Join(destination, "*.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tarballs) != 1 {
		t.Fatalf("tarball count = %d, want 1: %v", len(tarballs), tarballs)
	}
	wantSuffix := "-example-Relay-pr53-run---touch---INJECTION_MARKER---printf---end.tar.zst"
	if name := filepath.Base(tarballs[0]); !strings.HasSuffix(name, wantSuffix) {
		t.Fatalf("hostile tarball name = %q, want suffix %q", name, wantSuffix)
	}
	sidecar := strings.TrimSuffix(tarballs[0], ".tar.zst") + ".timings.json"
	content, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("timing sidecar missing beside hostile-name tarball: %v", err)
	}
	var record timingRecord
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("sidecar does not parse: %v\n%s", err, content)
	}
	if len(record.Commands) != 1 || record.Commands[0].Name != "hostile-name" {
		t.Fatalf("sidecar commands = %#v, want archived hostile-name event", record.Commands)
	}
	archiveListing, err := exec.Command("tar", "-tf", tarballs[0]).CombinedOutput()
	if err != nil {
		t.Fatalf("list delivered tarball: %v\n%s", err, archiveListing)
	}
	if !strings.Contains(string(archiveListing), "timing-record.json") {
		t.Fatalf("delivered tarball lacks timing record:\n%s", archiveListing)
	}
}
