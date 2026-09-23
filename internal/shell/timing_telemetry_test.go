package shell

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	Attempt    int    `json:"attempt"`
}

type timingRefusal struct {
	Name   *string `json:"name"`
	Reason string  `json:"reason"`
}

type timingWorker struct {
	Record        string  `json:"record"`
	ID            *int    `json:"id"`
	Label         *string `json:"label"`
	Engine        *string `json:"engine"`
	Model         *string `json:"model"`
	Status        *string `json:"status"`
	QueuedMs      *int64  `json:"queued_ms"`
	ExecutionMs   *int64  `json:"execution_ms"`
	StartedAt     *string `json:"started_at"`
	EndedAt       *string `json:"ended_at"`
	SpanBreakdown *struct {
		Status      string  `json:"status"`
		Format      *string `json:"format"`
		Reason      string  `json:"reason"`
		BuildMs     *int64  `json:"build_ms"`
		TestMs      *int64  `json:"test_ms"`
		ReasoningMs *int64  `json:"reasoning_ms"`
	} `json:"span_breakdown"`
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
	Commands []timingEvent   `json:"commands"`
	Refusals []timingRefusal `json:"refusals"`
	Workers  []timingWorker  `json:"workers"`
	Phases   []timingPhase   `json:"phases"`
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

func runArchivedSpanAnalysis(t *testing.T, recordPath, runDirectory string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "analyse-archived-spans"), recordPath, runDirectory)
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

func TestArchivedSpanAnalysisAttributesKnownTranscriptsAndRefusesUnknownFormats(t *testing.T) {
	runDir := t.TempDir()
	writeArchivedSpanAgent(t, runDir, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:01:00Z","method":"item/started","params":{"item":{"id":"build","type":"commandExecution","command":"go build ./..."}}}
{"recordedAt":"2026-08-26T00:03:00Z","method":"item/completed","params":{"item":{"id":"build","type":"commandExecution"}}}
{"recordedAt":"2026-08-26T00:04:00Z","method":"turn/completed"}
`)
	writeArchivedSpanAgent(t, runDir, "claude", "claude-session-jsonl", `
{"type":"custom-title","customTitle":"ensemble fixture"}
{"timestamp":"2026-08-26T01:00:00Z","type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"file-history-snapshot","snapshot":{}}
{"timestamp":"2026-08-26T01:02:00Z","type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}
{"timestamp":"2026-08-26T01:03:00Z","type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}
`)
	// A retry has its own archived transcript.  The projection must account for
	// both attempts before calling the worker's breakdown parsed.
	secondCodexTranscript := filepath.Join(runDir, "ensemble-records", "codex", "agents", "0001", "attempt-2.jsonl")
	if err := os.WriteFile(secondCodexTranscript, []byte(`{"recordedAt":"2026-08-26T00:04:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:05:00Z","method":"item/started","params":{"item":{"id":"build-2","type":"commandExecution","command":"cargo build"}}}
{"recordedAt":"2026-08-26T00:09:00Z","method":"item/completed","params":{"item":{"id":"build-2","type":"commandExecution"}}}
{"recordedAt":"2026-08-26T00:10:00Z","method":"turn/completed"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	secondCodexAgent := filepath.Join(runDir, "ensemble-records", "codex", "agents", "0001", "agent.json")
	if err := os.WriteFile(secondCodexAgent, []byte(`{"id":1,"engine":"codex","label":"codex","phase":"Analyse","status":"complete","execution_ms":600000,"attempts":[{"attempt":1},{"attempt":2}],"transcripts":[{"path":"agents/0001/transcript.jsonl","source":"codex-app-server-events"},{"path":"agents/0001/attempt-2.jsonl","source":"codex-app-server-events"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeArchivedSpanAgent(t, runDir, "unknown", "future-session-jsonl", `{"at":"2026-08-26T02:00:00Z"}`)

	recordPath := filepath.Join(runDir, "timing-record.json")
	if output, err := runCollectTimings(t, nil, runDir, recordPath); err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var baseline timingRecord
	if err := json.Unmarshal(before, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Workers) != 3 || baseline.Workers[0].SpanBreakdown != nil {
		t.Fatalf("the timing collector must expose only its existing arm wall data before archive analysis: %+v", baseline.Workers)
	}

	output, err := runArchivedSpanAnalysis(t, recordPath, runDir)
	if err != nil {
		t.Fatalf("analyse-archived-spans: %v\n%s", err, output)
	}
	var analysed timingRecord
	if err := json.Unmarshal(output, &analysed); err != nil {
		t.Fatalf("analysis does not produce a timing record: %v\n%s", err, output)
	}
	byRecord := map[string]timingWorker{}
	for _, worker := range analysed.Workers {
		byRecord[worker.Record] = worker
	}
	assertArchivedSpan(t, byRecord["ensemble-records/codex"].SpanBreakdown, "codex-app-server-events", 360000, 0, 240000)
	assertArchivedSpan(t, byRecord["ensemble-records/claude"].SpanBreakdown, "claude-session-jsonl", 0, 120000, 60000)
	unknown := byRecord["ensemble-records/unknown"].SpanBreakdown
	if unknown == nil || unknown.Status != "unparseable" || unknown.Format == nil || *unknown.Format != "future-session-jsonl" {
		t.Fatalf("unknown transcript was attributed instead of refused: %+v", unknown)
	}
}

func TestArchivedSpanAnalysisMatchesSiblingAgentsByWorkerIdentity(t *testing.T) {
	runDir := t.TempDir()
	recordRoot := filepath.Join(runDir, "ensemble-records", "mixed")
	writeArchivedSpanAgentAt(t, recordRoot, 1, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:01:00Z","method":"item/started","params":{"item":{"id":"build","type":"commandExecution","command":"go build ./..."}}}
{"recordedAt":"2026-08-26T00:03:00Z","method":"item/completed","params":{"item":{"id":"build","type":"commandExecution"}}}
{"recordedAt":"2026-08-26T00:04:00Z","method":"turn/completed"}
`)
	writeArchivedSpanAgentAt(t, recordRoot, 2, "claude", "claude-session-jsonl", `
{"timestamp":"2026-08-26T01:00:00Z","type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"timestamp":"2026-08-26T01:05:00Z","type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}
{"timestamp":"2026-08-26T01:07:00Z","type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}
`)
	writeArchivedSpanAgentAt(t, recordRoot, 3, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T02:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T02:08:00Z","method":"turn/completed"}
`)

	recordPath := filepath.Join(runDir, "timing-record.json")
	if output, err := runCollectTimings(t, nil, runDir, recordPath); err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	output, err := runArchivedSpanAnalysis(t, recordPath, runDir)
	if err != nil {
		t.Fatalf("analyse-archived-spans: %v\n%s", err, output)
	}
	var analysed timingRecord
	if err := json.Unmarshal(output, &analysed); err != nil {
		t.Fatalf("analysis does not produce a timing record: %v\n%s", err, output)
	}
	byID := map[int]timingWorker{}
	for _, worker := range analysed.Workers {
		if worker.ID == nil {
			t.Fatalf("worker has no identity: %+v", worker)
		}
		byID[*worker.ID] = worker
	}
	if len(byID) != 3 {
		t.Fatalf("workers by identity = %+v, want three sibling agents", byID)
	}
	assertArchivedSpan(t, byID[1].SpanBreakdown, "codex-app-server-events", 120000, 0, 120000)
	assertArchivedSpan(t, byID[2].SpanBreakdown, "claude-session-jsonl", 0, 300000, 120000)
	assertArchivedSpan(t, byID[3].SpanBreakdown, "codex-app-server-events", 0, 0, 480000)
}

func TestArchivedSpanAnalysisScopesToCollectedRecordRootsAndReportsUnresolvedRecords(t *testing.T) {
	runDir := t.TempDir()
	writeArchivedSpanAgent(t, runDir, "valid", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:01:00Z","method":"turn/completed"}
`)
	broken := filepath.Join(runDir, "ensemble-records", "broken", "agents", "0001", "agent.json")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("not JSON\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A cloned repository may itself contain an agent-shaped file.  It is not
	// an Ensemble record and must never be considered by this wind-down pass.
	writeArchivedSpanAgentAt(t, filepath.Join(runDir, "repository"), 1, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:01:00Z","method":"turn/completed"}
`)
	fallbackRecord := "home/.local/share/ensemble/runs/cwd/hash/fallback"
	writeArchivedSpanAgentAt(t, filepath.Join(runDir, filepath.FromSlash(fallbackRecord)), 1, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:02:00Z","method":"turn/completed"}
`)
	id := 1
	record := timingRecord{Workers: []timingWorker{
		{Record: "ensemble-records/valid", ID: &id},
		{Record: fallbackRecord, ID: &id},
		{Record: "ensemble-records/broken", ID: &id},
		{Record: "repository", ID: &id},
	}}
	recordPath := filepath.Join(runDir, "timing-record.json")
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := runArchivedSpanAnalysis(t, recordPath, runDir)
	if err != nil {
		t.Fatalf("analyse-archived-spans: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, &record); err != nil {
		t.Fatalf("analysis does not produce a timing record: %v\n%s", err, output)
	}
	assertArchivedSpan(t, record.Workers[0].SpanBreakdown, "codex-app-server-events", 0, 0, 60000)
	assertArchivedSpan(t, record.Workers[1].SpanBreakdown, "codex-app-server-events", 0, 0, 120000)
	if span := record.Workers[2].SpanBreakdown; span == nil || span.Status != "unparseable" || span.Reason != "unreadable or invalid agent record" {
		t.Fatalf("broken agent record was not reported as unparseable: %+v", span)
	}
	if span := record.Workers[3].SpanBreakdown; span == nil || span.Status != "unparseable" || span.Reason != "unresolved ensemble record" {
		t.Fatalf("repository artefact was scanned instead of refused: %+v", span)
	}
}

func TestListRecentTimingsReadsTheStaticArchivedTimingRecord(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(root, "archive-root")
	writeArchivedSpanAgent(t, archiveRoot, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-26T00:00:00Z","method":"turn/start"}
{"recordedAt":"2026-08-26T00:01:00Z","method":"item/started","params":{"item":{"id":"test","type":"commandExecution","command":"go test ./..."}}}
{"recordedAt":"2026-08-26T00:03:00Z","method":"item/completed","params":{"item":{"id":"test","type":"commandExecution"}}}
`)
	sidecar := filepath.Join(root, "sidecar.json")
	if output, err := runCollectTimings(t, nil, archiveRoot, sidecar); err != nil {
		t.Fatalf("collect-timings: %v\n%s", err, output)
	}
	analysed, err := runArchivedSpanAnalysis(t, sidecar, archiveRoot)
	if err != nil {
		t.Fatalf("analyse-archived-spans: %v\n%s", err, analysed)
	}
	if err := os.WriteFile(sidecar, analysed, 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	archiveName := "20260826T030000Z-fixture-minos-run-fixture-1.tar.zst"
	archivePath := filepath.Join(destination, archiveName)
	if output, err := exec.Command("tar", "-C", archiveRoot, "-cf", archivePath, ".").CombinedOutput(); err != nil {
		t.Fatalf("create archive fixture: %v\n%s", err, output)
	}
	if content, err := os.ReadFile(sidecar); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(strings.TrimSuffix(archivePath, ".tar.zst")+".timings.json", content, 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	installArchiveReceiverSSH(t, bin, destination)
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\nlast=\"\"\nfor argument in \"$@\"; do last=\"$argument\"; done\nif [ -n \"$last\" ] && [ -f \"$last\" ]; then exec cat \"$last\"; fi\nexec cat\n")
	identity := filepath.Join(root, "identity")
	knownHosts := filepath.Join(root, "known-hosts")
	for _, file := range []string{identity, knownHosts} {
		if err := os.WriteFile(file, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "archive.env")
	if err := os.WriteFile(config, []byte("MINOS_ARCHIVE_HOST=fixture\nMINOS_ARCHIVE_IDENTITY_FILE=\""+identity+"\"\nMINOS_ARCHIVE_KNOWN_HOSTS=\""+knownHosts+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sshLog := filepath.Join(root, "ssh.log")
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "list-recent-timings"), "20260826T000000Z", "1")
	cmd.Env = append(os.Environ(), "MINOS_ARCHIVE_CONFIG="+config, "SSH_LOG="+sshLog, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-recent-timings: %v\n%s", err, output)
	}
	_, encoded, found := strings.Cut(strings.TrimSpace(string(output)), "\t")
	if !found {
		t.Fatalf("listing = %q, want one framed record", output)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var record timingRecord
	if err := json.Unmarshal(decoded, &record); err != nil {
		t.Fatalf("enriched sidecar does not parse: %v\n%s", err, decoded)
	}
	assertArchivedSpan(t, record.Workers[0].SpanBreakdown, "codex-app-server-events", 0, 120000, 60000)
	log, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(log)) != "list 20260826T000000Z 1" {
		t.Fatalf("recent listing asked the archive host for more than the sidecar listing:\n%s", log)
	}
}

func writeArchivedSpanAgent(t *testing.T, runDir, record, source, transcript string) {
	t.Helper()
	writeArchivedSpanAgentAt(t, filepath.Join(runDir, "ensemble-records", record), 1, "codex", source, transcript)
}

func writeArchivedSpanAgentAt(t *testing.T, recordRoot string, id int, engine, source, transcript string) {
	t.Helper()
	agentDir := fmt.Sprintf("%04d", id)
	dir := filepath.Join(recordRoot, "agents", agentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := fmt.Sprintf(`{"id":%d,"engine":%q,"label":"fixture-%d","phase":"Analyse","status":"complete","execution_ms":240000,"attempts":[{"attempt":1,"started_at":"2026-08-26T00:00:00Z","ended_at":"2026-08-26T00:04:00Z"}],"transcripts":[{"path":%q,"source":%q}]}`,
		id, engine, id, filepath.ToSlash(filepath.Join("agents", agentDir, "transcript.jsonl")), source)
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(strings.TrimSpace(transcript)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertArchivedSpan(t *testing.T, span *struct {
	Status      string  `json:"status"`
	Format      *string `json:"format"`
	Reason      string  `json:"reason"`
	BuildMs     *int64  `json:"build_ms"`
	TestMs      *int64  `json:"test_ms"`
	ReasoningMs *int64  `json:"reasoning_ms"`
}, format string, build, test, reasoning int64) {
	t.Helper()
	if span == nil || span.Status != "parsed" || span.Format == nil || *span.Format != format || span.BuildMs == nil || *span.BuildMs != build || span.TestMs == nil || *span.TestMs != test || span.ReasoningMs == nil || *span.ReasoningMs != reasoning {
		t.Fatalf("span = %+v, want parsed %s with %d/%d/%d ms", span, format, build, test, reasoning)
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

// The pressure watch and the telemetry snapshot read this run's cgroup through
// one shared collector, so the anon-plus-swap figure the watch records as the
// run's peak is the same figure the snapshot reports. The fixture's memory.stat
// is laid out to punish a second, independent parse: the anon line is preceded
// by keys that a looser match would take instead, and swap lives in its own
// file. A copy that drifted would make these two numbers disagree.
func TestCgroupMemoryCollectorFeedsBothTheWatchAndTheSnapshot(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := filepath.Join(fixture.root, "cgroup")
	if err := os.MkdirAll(cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current":      "18000000000\n",
		"memory.max":          "22000000000\n",
		"memory.stat":         "anon_thp 3300000000\nfile 12687937536\nanon 5100000000\nswapcached 770000000\nworkingset_refault_anon 14067688\nworkingset_refault_file 47279103\n",
		"memory.swap.current": "2300000000\n",
		"memory.events":       "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
		"memory.peak":         "18000000000\n",
		"memory.pressure":     "some avg10=0.12 avg60=0.16 avg300=2.37 total=1064808220\nfull avg10=0.00 avg60=0.09 avg300=1.72 total=880404397\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(cgroup, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "memory-telemetry"), snapshotPath)
	cmd.Env = append(os.Environ(), "MINOS_CGROUP_DIR="+cgroup)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("memory-telemetry: %v\n%s", err, combined)
	}
	content, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Anon *float64 `json:"anon"`
		Swap *float64 `json:"swap"`
	}
	if err := json.Unmarshal(content, &snapshot); err != nil {
		t.Fatalf("snapshot does not parse: %v\n%s", err, content)
	}
	if snapshot.Anon == nil || snapshot.Swap == nil {
		t.Fatalf("snapshot omits the footprint counters: %s", content)
	}

	output, err := fixture.execute(map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	if err != nil {
		t.Fatalf("run-body: %v\n%s", err, output)
	}
	peak, err := os.ReadFile(filepath.Join(fixture.runDir, "memory-peak"))
	if err != nil {
		t.Fatalf("the pressure watch recorded no peak: %v\n%s", err, output)
	}
	want := fmt.Sprintf("%d", int64(*snapshot.Anon)+int64(*snapshot.Swap))
	if strings.TrimSpace(string(peak)) != want {
		t.Fatalf("watch peak = %s, snapshot anon+swap = %s — the two call paths parsed the cgroup differently",
			strings.TrimSpace(string(peak)), want)
	}
	fixture.assertProcessesStopped(t)
}

// The shared collector's tolerance reaches both callers: one cgroup missing
// anon disables the watch and leaves the field out of the snapshot.
func TestCgroupMemoryCollectorToleratesAMissingCounterOnBothPaths(t *testing.T) {
	fixture := newRunBodyFixture(t)
	cgroup := writeTestCgroup(t, fixture.root, 900_000_000, 1_000_000_000, 0, 900_000_000, 0)
	if err := os.WriteFile(filepath.Join(cgroup, "memory.stat"), []byte("inactive_file 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "run-body", "memory-telemetry"), snapshotPath)
	cmd.Env = append(os.Environ(), "MINOS_CGROUP_DIR="+cgroup)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("memory-telemetry: %v\n%s", err, combined)
	}
	content, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(content, &snapshot); err != nil {
		t.Fatalf("snapshot does not parse: %v\n%s", err, content)
	}
	if _, present := snapshot["anon"]; present {
		t.Fatalf("snapshot invented an anon figure from a cgroup without one: %s", content)
	}

	output, err := fixture.execute(map[string]string{
		"MINOS_CGROUP_DIR":          cgroup,
		"MINOS_TEST_PENDING_STATE":  "done",
		"MINOS_TEST_WAIT_POLLS":     "2",
		"MINOS_TEST_TERMINAL_STATE": "failed",
	})
	if err != nil {
		t.Fatalf("run-body: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "memory-pressure watch disabled: cgroup memory counters are unavailable or malformed") {
		t.Fatalf("run-body kept judging memory without an anon counter:\n%s", output)
	}
	fixture.assertProcessesStopped(t)
}

// Two faithful copies of a counter read agree on every cgroup until one of
// them drifts, so agreement alone cannot show that only one copy is left.
// What can is the absence of the second reader: this run's cgroup is resolved
// and its counters are parsed in one file of the run-body family, and every
// other script in it reaches those numbers through that file. The terminal
// death evidence is deliberately not caught here — it cats memory.events,
// memory.peak and memory.pressure verbatim through the loop variable, taking
// only the shared directory.
func TestOnlyTheSharedFragmentResolvesTheCgroupAndReadsItsCounters(t *testing.T) {
	signatures := []string{
		"/proc/self/cgroup",
		"$cgroup_dir/memory.stat",
		"$cgroup_dir/memory.swap.current",
		"$cgroup_dir/memory.pressure",
	}
	scripts := filepath.Join("..", "..", "scripts", "run-body")
	const fragment = "cgroup-memory.sh"

	shared, err := os.ReadFile(filepath.Join(scripts, fragment))
	if err != nil {
		// Reported rather than fatal, so the scan below still names whichever
		// scripts are reading the cgroup for themselves.
		t.Errorf("the shared cgroup fragment is missing: %v", err)
	} else {
		for _, signature := range signatures {
			if !strings.Contains(string(shared), signature) {
				t.Errorf("%s no longer carries %q, so the single-reader check below proves nothing", fragment, signature)
			}
		}
	}

	entries, err := os.ReadDir(scripts)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == fragment {
			continue
		}
		content, err := os.ReadFile(filepath.Join(scripts, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, signature := range signatures {
			if strings.Contains(string(content), signature) {
				t.Errorf("scripts/run-body/%s reads the cgroup itself (%q) — a second copy free to drift; source %s instead",
					entry.Name(), signature, fragment)
			}
		}
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
		"model": nil, "resolved_model": "gpt-6-sol", "status": "complete",
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
		`"phase":"Specialise","model":null,"resolved_model":"gpt-6-sol",` +
		`"status":"complete","queued_ms":12,"execution_ms":345,` +
		`"attempts":[{"attempt":1,"started_at":"` + startedAt + `","ended_at":"` + endedAt + `"}]}`
	if err := os.WriteFile(path, []byte(record+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectTimingsAssemblesCommandsAndWorkersSorted(t *testing.T) {
	runDir := t.TempDir()
	events := `{"kind":"minos-timing-event-v1","name":"review@2","started_at":"2026-08-17T00:20:00Z","ended_at":"2026-08-17T00:40:00Z","duration_ms":1200000,"exit_status":0}
{"kind":"minos-timing-event-v1","name":"review@10","started_at":"2026-08-17T00:30:00Z","ended_at":"2026-08-17T00:50:00Z","duration_ms":1200000,"exit_status":0}
this line is not JSON and must cost one event, never the record
{"kind":"minos-timing-event-v1","name":"build-round6b","started_at":"2026-08-17T00:10:00Z","ended_at":"2026-08-17T00:11:00Z","duration_ms":60000,"exit_status":0}
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
	if len(record.Commands) != 3 {
		t.Fatalf("command count = %d, want 3 (malformed and refused names each cost one event only)", len(record.Commands))
	}
	if record.Commands[0].Name != "build-command" || record.Commands[1].Name != "review" || record.Commands[1].Attempt != 2 || record.Commands[2].Name != "review" || record.Commands[2].Attempt != 10 {
		t.Fatalf("commands are not sorted by start: %+v", record.Commands)
	}
	if len(record.Refusals) != 1 || record.Refusals[0].Name == nil || *record.Refusals[0].Name != "build-round6b" || record.Refusals[0].Reason != "invalid step name" {
		t.Fatalf("refusals = %+v, want the named improvised step refusal", record.Refusals)
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
		if worker.Model == nil || *worker.Model != "gpt-6-sol" {
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
	if err := os.WriteFile(filepath.Join(runDir, "cgroup-death-evidence"), []byte("[memory.events]\noom_kill 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Add a deterministic agent result under the nested layout the timing and
	// archive scripts consume without talking to a model provider.
	records := map[string]string{}
	for _, fixture := range []struct {
		name      string
		startedAt string
		endedAt   string
	}{
		{name: "record-a", startedAt: "2026-08-17T00:21:00Z", endedAt: "2026-08-17T00:25:00Z"},
	} {
		archive := filepath.Join(runDir, "ensemble-records", fixture.name)
		writeTimingFixtureAgentRecord(t, filepath.Join(archive, "agents", "0001", "agent.json"), fixture.startedAt, fixture.endedAt)
		relative, err := filepath.Rel(runDir, archive)
		if err != nil {
			t.Fatal(err)
		}
		records[fixture.name] = filepath.ToSlash(relative)
	}
	writeArchivedSpanAgentAt(t, filepath.Join(runDir, records["record-a"]), 1, "codex", "codex-app-server-events", `
{"recordedAt":"2026-08-17T00:21:00Z","method":"turn/start"}
{"recordedAt":"2026-08-17T00:22:00Z","method":"item/started","params":{"item":{"id":"test","type":"commandExecution","command":"go test ./..."}}}
{"recordedAt":"2026-08-17T00:24:00Z","method":"item/completed","params":{"item":{"id":"test","type":"commandExecution"}}}
{"recordedAt":"2026-08-17T00:25:00Z","method":"turn/completed"}
`)

	bin := filepath.Join(root, "bin")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{bin, destination} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture zstd is a passthrough, so the whole transport runs inside
	// the temp directory while archive-run's own logic stays real.
	installArchiveReceiverSSH(t, bin, destination)
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
		t.Fatalf("sidecar carries %d commands, %d workers, want 1 and 1 genuine worker", len(record.Commands), len(record.Workers))
	}
	workerRecords := make(map[string]bool, len(record.Workers))
	for _, worker := range record.Workers {
		workerRecords[worker.Record] = true
	}
	for _, want := range []string{records["record-a"]} {
		if !workerRecords[want] {
			t.Fatalf("timing record omits genuine worker record %q: %#v", want, record.Workers)
		}
	}
	for _, worker := range record.Workers {
		if worker.Record == records["record-a"] {
			assertArchivedSpan(t, worker.SpanBreakdown, "codex-app-server-events", 0, 120000, 120000)
		}
	}
	archiveListing, err := exec.Command("tar", "-tf", tarballs[0]).CombinedOutput()
	if err != nil {
		t.Fatalf("list delivered tarball: %v\n%s", err, archiveListing)
	}
	for _, want := range []string{records["record-a"] + "/agents/0001/agent.json"} {
		if !strings.Contains(string(archiveListing), want) {
			t.Fatalf("delivered tarball omits %q:\n%s", want, archiveListing)
		}
	}
	if !strings.Contains(string(archiveListing), "cgroup-death-evidence") {
		t.Fatalf("delivered tarball lacks cgroup death evidence:\n%s", archiveListing)
	}
	if _, err := os.Stat(sidecar + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial sidecar left beside the delivered one: %v", err)
	}
}

func TestArchiveRunKeepsTheArchiveWhenSpanAnalysisFails(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "run-body")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"archive-run", "archive-transport.sh", "collect-timings", "memory-telemetry", "cgroup-memory.sh"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tools, name), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(t, filepath.Join(tools, "analyse-archived-spans"), "#!/usr/bin/env sh\nexit 1\n")

	bin := filepath.Join(root, "bin")
	destination := filepath.Join(root, "destination")
	for _, dir := range []string{bin, destination} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installArchiveReceiverSSH(t, bin, destination)
	writeScript(t, filepath.Join(bin, "zstd"), "#!/usr/bin/env sh\nexec cat\n")
	identity := filepath.Join(root, "identity")
	knownHosts := filepath.Join(root, "known-hosts")
	for _, path := range []string{identity, knownHosts} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "archive.env")
	content := strings.Join([]string{
		`MINOS_ARCHIVE_HOST="fixture"`,
		`MINOS_ARCHIVE_IDENTITY_FILE="` + identity + `"`,
		`MINOS_ARCHIVE_KNOWN_HOSTS="` + knownHosts + `"`,
	}, "\n") + "\n"
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(tools, "archive-run"), runDir, "analysis-failure")
	cmd.Env = append(os.Environ(), "MINOS_ARCHIVE_CONFIG="+config, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("archive-run after failed analysis: %v\n%s", err, output)
	}
	tarballs, err := filepath.Glob(filepath.Join(destination, "*.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tarballs) != 1 {
		t.Fatalf("tarball count after failed analysis = %d, want 1: %v", len(tarballs), tarballs)
	}
	sidecar := strings.TrimSuffix(tarballs[0], ".tar.zst") + ".timings.json"
	stored, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar after failed analysis: %v", err)
	}
	var record timingRecord
	if err := json.Unmarshal(stored, &record); err != nil {
		t.Fatalf("sidecar after failed analysis does not parse: %v\n%s", err, stored)
	}
	if len(record.Workers) != 0 {
		t.Fatalf("failed analysis changed the ordinary timing record: %+v", record.Workers)
	}
	if err := os.Remove(filepath.Join(tools, "analyse-archived-spans")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(filepath.Join(tools, "archive-run"), runDir, "analysis-missing")
	cmd.Env = append(os.Environ(), "MINOS_ARCHIVE_CONFIG="+config, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("archive-run with unavailable analysis: %v\n%s", err, output)
	}
	tarballs, err = filepath.Glob(filepath.Join(destination, "*.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tarballs) != 2 {
		t.Fatalf("tarball count after unavailable analysis = %d, want 2: %v", len(tarballs), tarballs)
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
	installArchiveReceiverSSH(t, bin, destination)
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
