// Package reviewbar proves the join the commission requires and the 2026-07-12
// containment audits (opening and closing) fixed in stages: the service's two
// coverage artefacts must both reach the independent review bar, unconflated.
// These tests drive the real, service-consumed workflow scripts end to end over
// a seeded repository — account-coverage, the skill's assemble_verify_input.py,
// and verify_workflow.js — asserting that:
//
//   - the mechanical accounting RESULT gates convergence: a seeded gap forces
//     non-convergence even when the reviewer panel self-declares full; and
//   - the lead-owned inspection RECORD reaches the bar as depth evidence: on a
//     converging review the bar's material demonstrably contains the record's
//     own entries — the changed files and the named definition it recorded —
//     not merely a non-empty field, and the record rides through even when the
//     result is partial and the bar does not run.
//
// The scripts under test are the copy Minos runs: skills/foundry/review-panel/.
// That copy is a generated Foundry casting, synced only after a Foundry landing,
// so until the seam is synced the join cases skip rather than fail — set
// MINOS_REVIEW_SCRIPTS_OVERRIDE to a review-panel scripts directory carrying the
// seam (the Foundry Canon Base) to run them against that source.
package reviewbar

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The account-coverage output shape (the accounting result). Only the fields the
// seam turns on are modelled.
type coverageResult struct {
	Status    string           `json:"status"`
	Omissions []map[string]any `json:"omissions"`
}

// The relevant slice of verify_workflow.js's returned report.
type verifyResult struct {
	CoverageGate struct {
		ResultStatus      string `json:"result_status"`
		BlocksConvergence bool   `json:"blocks_convergence"`
	} `json:"coverage_gate"`
	Bar struct {
		TriggerFired   bool     `json:"trigger_fired"`
		TriggerReasons []string `json:"trigger_reasons"`
		Ran            bool     `json:"ran"`
		Outcome        string   `json:"outcome"`
	} `json:"bar"`
	Findings         []map[string]any `json:"findings"`
	InspectionRecord json.RawMessage  `json:"inspection_record"`
}

// The harness prints the workflow return plus the exact prompt the bar received.
type harnessOutput struct {
	Result    verifyResult `json:"result"`
	BarPrompt string       `json:"bar_prompt"`
}

// referenceName is the named definition the seeded inspection record cites; the
// converging case asserts it appears verbatim in the bar's material.
const referenceName = "compute_total"

func TestPartialResultForcesNonConvergenceAndRecordRides(t *testing.T) {
	repo, base, head := seedSeamRepository(t)

	// A seeded omission: b.txt is accounted `omitted`. account-coverage reports
	// it partial and — per the review-scripts contract — EXITS 1. That non-zero
	// status is the mechanical partial VERDICT, a result the seam must carry, not
	// a command failure that aborts the flow.
	record := inspectionRecordFor(t, repo, base, head, map[string]bool{"b.txt": true})
	check, exit := runAccountCoverage(t, repo, base, head, record)
	if exit != 1 {
		t.Fatalf("account-coverage on a seeded omission exited %d, want 1 (a partial result, not a failure)", exit)
	}
	if check.Status != "partial" {
		t.Fatalf("seeded-omission coverage status = %q, want partial", check.Status)
	}

	requireSeam(t)

	// The panel self-declares full coverage and the review has zero findings —
	// the shape that would otherwise converge. The accounting result contradicts it.
	verifyInput := assembleVerifyInput(t, panelFullValidated(), check.resultPath, check.recordPath, "off")
	// bar off: no model runs, so the block is proven to be purely mechanical.
	out := runVerifyWorkflow(t, verifyInput, "none")
	result := out.Result

	if !result.CoverageGate.BlocksConvergence {
		t.Fatalf("coverage_gate.blocks_convergence = false with a partial result; the seam does not gate convergence")
	}
	if result.CoverageGate.ResultStatus != "partial" {
		t.Errorf("coverage_gate.result_status = %q, want partial", result.CoverageGate.ResultStatus)
	}
	if !hasReasonMentioning(result.Bar.TriggerReasons, "coverage accounting result is partial") {
		t.Errorf("bar trigger reasons do not name the partial result: %#v", result.Bar.TriggerReasons)
	}
	// The inspection record rides through even when the result is partial and the
	// bar did not run — unconflated from the result that gates.
	assertRecordCarriesReference(t, "workflow return", string(result.InspectionRecord))
}

func TestCompleteResultConvergesAndInspectionRecordReachesTheBar(t *testing.T) {
	requireSeam(t)
	repo, base, head := seedSeamRepository(t)

	// Every changed file and hunk accounted read, plus a valid named definition
	// reference: account-coverage is complete and exits 0.
	record := inspectionRecordFor(t, repo, base, head, nil)
	check, exit := runAccountCoverage(t, repo, base, head, record)
	if exit != 0 {
		t.Fatalf("account-coverage on a complete record exited %d, want 0: omissions may indicate an invalid seeded reference", exit)
	}
	if check.Status != "complete" {
		t.Fatalf("complete-record coverage status = %q, want complete", check.Status)
	}

	// Clean panel, zero findings, bar on with a stubbed passing judge that
	// captures its prompt: all three convergence ingredients present, the gate
	// open, and the record's own evidence in front of the judge.
	verifyInput := assembleVerifyInput(t, panelFullValidated(), check.resultPath, check.recordPath, "auto")
	out := runVerifyWorkflow(t, verifyInput, "pass")
	result := out.Result

	if result.CoverageGate.BlocksConvergence {
		t.Fatalf("coverage_gate.blocks_convergence = true with a complete result; the gate wrongly blocks")
	}
	if result.CoverageGate.ResultStatus != "complete" {
		t.Errorf("coverage_gate.result_status = %q, want complete", result.CoverageGate.ResultStatus)
	}
	if result.Bar.Outcome != "pass" {
		t.Errorf("bar outcome = %q, want pass", result.Bar.Outcome)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected zero findings, got %d", len(result.Findings))
	}

	// The audit's specific point: on a complete result the bar must see the
	// inspection evidence, not just empty omissions and counts. Assert the
	// record's own entries are in the material the bar actually received — the
	// per-file path AND the named definition, not merely a non-empty field.
	if !strings.Contains(out.BarPrompt, "inspection_record") {
		t.Fatalf("bar material does not carry inspection_record")
	}
	if !strings.Contains(out.BarPrompt, referenceName) {
		t.Errorf("bar material does not contain the named reference %q; it cannot judge recorded depth", referenceName)
	}
	if !strings.Contains(out.BarPrompt, "a.py") {
		t.Errorf("bar material does not contain the inspected file path a.py")
	}
	// And it rides in the return too.
	assertRecordCarriesReference(t, "workflow return", string(result.InspectionRecord))
}

func TestCompleteResultWithoutInspectionRecordIsBlocked(t *testing.T) {
	requireSeam(t)
	repo, base, head := seedSeamRepository(t)

	// A genuinely complete accounting result — but the lead forwards no inspection
	// record. Depth cannot be judged, so this must not converge: the gate blocks
	// deterministically, with the bar off (no LLM). Without this, a complete
	// result alone could carry a convergence-bearing pass past the depth judgement.
	record := inspectionRecordFor(t, repo, base, head, nil)
	check, exit := runAccountCoverage(t, repo, base, head, record)
	if exit != 0 || check.Status != "complete" {
		t.Fatalf("expected a complete result (exit 0), got status=%q exit=%d", check.Status, exit)
	}

	// Omit the inspection record (empty path), bar off — the block must be purely
	// mechanical.
	verifyInput := assembleVerifyInput(t, panelFullValidated(), check.resultPath, "", "off")
	out := runVerifyWorkflow(t, verifyInput, "none")
	result := out.Result

	if !result.CoverageGate.BlocksConvergence {
		t.Fatalf("coverage_gate.blocks_convergence = false for a complete result with no inspection record; depth would go unjudged")
	}
	if result.CoverageGate.ResultStatus != "complete" {
		t.Errorf("coverage_gate.result_status = %q, want complete", result.CoverageGate.ResultStatus)
	}
	if !hasReasonMentioning(result.Bar.TriggerReasons, "inspection record is missing") {
		t.Errorf("bar trigger reasons do not name the missing inspection record: %#v", result.Bar.TriggerReasons)
	}
}

// --- helpers -------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func reviewScriptsDir(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("MINOS_REVIEW_SCRIPTS_OVERRIDE"); override != "" {
		return override
	}
	return filepath.Join(repoRoot(t), "skills", "foundry", "review-panel", "scripts")
}

// requireSeam skips the join cases when the review-panel scripts under test
// predate the two-artefact seam (the synced casting before a resync) and when
// node is unavailable to run the workflow.
func requireSeam(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping verify-workflow join")
	}
	assemble := filepath.Join(reviewScriptsDir(t), "assemble_verify_input.py")
	out, err := exec.Command("python3", assemble, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("probe assemble_verify_input.py: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--inspection-record") {
		t.Skip("review-panel scripts predate the two-artefact coverage seam (awaiting Foundry sync); proven against Canon in the gate report")
	}
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=seam", "GIT_AUTHOR_EMAIL=seam@example.invalid",
		"GIT_COMMITTER_NAME=seam", "GIT_COMMITTER_EMAIL=seam@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedSeamRepository builds a deterministic two-commit repository whose head
// changes two files. a.py carries a named definition (compute_total) an
// unchanged line the inspection record can cite as a definition reference; b.txt
// is the file the partial case omits. Returns the repo path plus base/head SHAs.
func seedSeamRepository(t *testing.T) (repo, base, head string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "subject")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	write(t, repo, "a.py", "def compute_total():\n    return 0\n")
	write(t, repo, "b.txt", "b1\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	base = gitOut(t, repo, "rev-parse", "HEAD")
	// Change the body of a.py (line 2) — its definition line stays put — and b.txt.
	write(t, repo, "a.py", "def compute_total():\n    return 1\n")
	write(t, repo, "b.txt", "b1\nb2\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "head")
	head = gitOut(t, repo, "rev-parse", "HEAD")
	return repo, base, head
}

func write(t *testing.T, repo, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func accountCoverageScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "scripts", "review", "account-coverage")
}

// inspectionRecordFor builds a valid inspection record from account-coverage's
// own inventory: every file/hunk accounted `read` except those named in omit,
// plus a valid `definition` reference to a.py's compute_total (an unchanged line
// account-coverage will validate against the head blob). This is the lead-owned
// record — the same artefact fed to account-coverage and forwarded to the bar.
func inspectionRecordFor(t *testing.T, repo, base, head string, omit map[string]bool) map[string]any {
	t.Helper()
	out, err := exec.Command(accountCoverageScript(t),
		"--repo", repo, "--base", base, "--head", head, "--inventory").CombinedOutput()
	if err != nil {
		t.Fatalf("account-coverage --inventory: %v\n%s", err, out)
	}
	var inventory struct {
		BaseCommit string           `json:"base_commit"`
		HeadCommit string           `json:"head_commit"`
		Files      []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(out, &inventory); err != nil {
		t.Fatalf("decode inventory: %v\n%s", err, out)
	}
	files := make([]map[string]any, 0, len(inventory.Files))
	var aBlob string
	for _, file := range inventory.Files {
		path := file["path"].(string)
		if path == "a.py" {
			aBlob = file["blob"].(string)
		}
		account := "read"
		if omit[path] {
			account = "omitted"
		}
		hunks, _ := file["hunks"].([]any)
		accountedHunks := make([]any, 0, len(hunks))
		for _, raw := range hunks {
			hunk := raw.(map[string]any)
			hunk["account"] = account
			accountedHunks = append(accountedHunks, hunk)
		}
		files = append(files, map[string]any{
			"path": path, "blob": file["blob"], "account": account, "hunks": accountedHunks,
		})
	}
	if aBlob == "" {
		t.Fatal("a.py missing from coverage inventory")
	}
	return map[string]any{
		"schema_version": 1,
		"base_commit":    inventory.BaseCommit,
		"head_commit":    inventory.HeadCommit,
		"files":          files,
		// A real named definition on an unchanged line — the depth evidence the
		// bar must be able to weigh. account-coverage validates it against the blob.
		"references": []any{map[string]any{
			"kind": "definition", "path": "a.py", "commit": head, "blob": aBlob,
			"line": 1, "name": referenceName, "line_text": "def compute_total():",
		}},
	}
}

type coverageCheck struct {
	coverageResult
	resultPath string // the account-coverage output (coverage-check.json)
	recordPath string // the lead-owned inspection record fed to it
}

// runAccountCoverage runs the real account-coverage over an inspection record,
// capturing its JSON to a file, and returns both file paths and the process exit
// code — the exit code matters: 1 is the partial VERDICT the seam must carry.
func runAccountCoverage(t *testing.T, repo, base, head string, record map[string]any) (coverageCheck, int) {
	t.Helper()
	dir := t.TempDir()
	recordPath := filepath.Join(dir, "inspection.json")
	writeJSON(t, recordPath, record)

	cmd := exec.Command(accountCoverageScript(t),
		"--repo", repo, "--base", base, "--head", head, "--record", recordPath)
	out, _ := cmd.CombinedOutput()
	exit := cmd.ProcessState.ExitCode()
	if exit == 2 {
		t.Fatalf("account-coverage could not check the record (exit 2): %s", out)
	}
	var result coverageResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode coverage result: %v\n%s", err, out)
	}
	resultPath := filepath.Join(dir, "coverage-check.json")
	if err := os.WriteFile(resultPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return coverageCheck{coverageResult: result, resultPath: resultPath, recordPath: recordPath}, exit
}

// panelFullValidated is validate_quotes.py-shaped input with the panel declaring
// full coverage and zero findings — the would-otherwise-converge shape.
func panelFullValidated() map[string]any {
	return map[string]any{
		"findings":                []any{},
		"coverage":                []any{map[string]any{"brief": "tone", "status": "full"}},
		"skipped":                 []any{},
		"failures":                []any{},
		"reviews":                 []any{map[string]any{"brief": "tone", "shard": "1/1", "notes": "read a.py and b.txt in full", "findings": 0}},
		"mode":                    "diff",
		"pr":                      nil,
		"suppressed_by_validator": []any{},
		"quote_validation":        map[string]any{"checked": 0, "passed": 0, "suppressed": 0, "lines_corrected": 0},
	}
}

func seamPlan(t *testing.T) map[string]any {
	t.Helper()
	prompts := filepath.Join(filepath.Dir(reviewScriptsDir(t)), "prompts")
	return map[string]any{
		"repo_root":           "/subject",
		"mode":                "diff",
		"base_ref":            "origin/main",
		"template_path":       filepath.Join(prompts, "reviewer-method.md"),
		"checker_method_path": filepath.Join(prompts, "finding-check.md"),
		"bar_method_path":     filepath.Join(prompts, "bar-check.md"),
		"briefs":              []any{map[string]any{"name": "tone", "path": ".review/tone.md", "title": "Tone", "scope": nil}},
	}
}

// assembleVerifyInput runs the real assemble_verify_input.py, wiring in whichever
// coverage artefacts are given (an empty path omits that flag), and returns the
// verify-input.json path.
func assembleVerifyInput(t *testing.T, validated map[string]any, coverageResultPath, inspectionRecordPath, barMode string) string {
	t.Helper()
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	validatedPath := filepath.Join(dir, "validated.json")
	writeJSON(t, planPath, seamPlan(t))
	writeJSON(t, validatedPath, validated)

	assemble := filepath.Join(reviewScriptsDir(t), "assemble_verify_input.py")
	cmdArgs := []string{assemble, planPath, validatedPath, "--bar", barMode}
	if coverageResultPath != "" {
		cmdArgs = append(cmdArgs, "--coverage-result", coverageResultPath)
	}
	if inspectionRecordPath != "" {
		cmdArgs = append(cmdArgs, "--inspection-record", inspectionRecordPath)
	}
	out, err := exec.Command("python3", cmdArgs...).Output()
	if err != nil {
		t.Fatalf("assemble_verify_input.py: %v", err)
	}
	inputPath := filepath.Join(dir, "verify-input.json")
	if err := os.WriteFile(inputPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return inputPath
}

// runVerifyWorkflow runs the real verify_workflow.js through the node harness,
// with the bar judge stubbed to barVerdict ('none' forbids any agent call), and
// returns both the workflow report and the captured bar prompt.
func runVerifyWorkflow(t *testing.T, verifyInputPath, barVerdict string) harnessOutput {
	t.Helper()
	workflow := filepath.Join(reviewScriptsDir(t), "verify_workflow.js")
	harness := filepath.Join(repoRoot(t), "verification", "reviewbar", "testdata", "verify_workflow_harness.mjs")
	out, err := exec.Command("node", harness, workflow, verifyInputPath, barVerdict).CombinedOutput()
	if err != nil {
		t.Fatalf("verify_workflow harness: %v\n%s", err, out)
	}
	var parsed harnessOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("decode harness output: %v\n%s", err, out)
	}
	return parsed
}

// assertRecordCarriesReference proves the forwarded inspection record is the real
// evidence, not an empty stand-in: its named definition must be present.
func assertRecordCarriesReference(t *testing.T, where, recordJSON string) {
	t.Helper()
	if recordJSON == "" || recordJSON == "null" {
		t.Fatalf("%s: inspection_record is absent; it must ride the seam", where)
	}
	if !strings.Contains(recordJSON, referenceName) {
		t.Errorf("%s: inspection_record does not carry the named reference %q: %s", where, referenceName, recordJSON)
	}
}

func hasReasonMentioning(reasons []string, substr string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, substr) {
			return true
		}
	}
	return false
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
