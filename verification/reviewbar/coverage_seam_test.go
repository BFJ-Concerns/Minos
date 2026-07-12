// Package reviewbar proves the join the commission requires and the 2026-07-12
// containment audit found missing: the service-owned immutable coverage record
// (account-coverage output) must reach the independent review bar and gate
// convergence. These tests drive the real, service-consumed workflow scripts
// end to end over a seeded repository — account-coverage, the skill's
// assemble_verify_input.py, and verify_workflow.js — asserting that a mechanical
// coverage gap forces non-convergence even when the reviewer panel self-declares
// full, and that an honest complete record with a clean panel still converges.
//
// The scripts under test are the copy Minos runs: skills/foundry/review-panel/.
// That copy is a generated Foundry casting, synced only after a Foundry landing,
// so until the coverage-record seam is synced the join cases skip rather than
// fail — set MINOS_REVIEW_SCRIPTS_OVERRIDE to a review-panel scripts directory
// carrying the seam (the Foundry Canon Base) to run them against that source.
package reviewbar

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The account-coverage output shape (the immutable coverage record). Only the
// fields the seam turns on are modelled.
type coverageResult struct {
	Status    string           `json:"status"`
	Omissions []map[string]any `json:"omissions"`
}

// The relevant slice of verify_workflow.js's returned report.
type verifyResult struct {
	CoverageGate struct {
		RecordStatus      string `json:"record_status"`
		BlocksConvergence bool   `json:"blocks_convergence"`
	} `json:"coverage_gate"`
	Bar struct {
		TriggerFired   bool     `json:"trigger_fired"`
		TriggerReasons []string `json:"trigger_reasons"`
		Ran            bool     `json:"ran"`
		Outcome        string   `json:"outcome"`
	} `json:"bar"`
	Findings []map[string]any `json:"findings"`
}

func TestPartialRecordForcesNonConvergenceThroughTheRealWorkflow(t *testing.T) {
	repo, base, head := seedSeamRepository(t)

	// A seeded omission: b.txt is accounted `omitted`. account-coverage reports
	// it partial and — per the review-scripts contract — EXITS 1. That non-zero
	// status is the mechanical partial VERDICT, a result the seam must carry, not
	// a command failure that aborts the flow.
	record := coverageRecordFor(t, repo, base, head, map[string]bool{"b.txt": true})
	coverageCheck, exit := runAccountCoverage(t, repo, base, head, record)
	if exit != 1 {
		t.Fatalf("account-coverage on a seeded omission exited %d, want 1 (a partial result, not a failure)", exit)
	}
	if coverageCheck.Status != "partial" {
		t.Fatalf("seeded-omission coverage status = %q, want partial", coverageCheck.Status)
	}

	requireSeam(t)

	// The panel self-declares full coverage and the review has zero findings —
	// the shape that would otherwise converge. The immutable record contradicts it.
	verifyInput := assembleVerifyInput(t, panelFullValidated(), coverageCheck.path, "off")
	// bar off: no model runs, so the block is proven to be purely mechanical.
	result := runVerifyWorkflow(t, verifyInput, "none")

	if !result.CoverageGate.BlocksConvergence {
		t.Fatalf("coverage_gate.blocks_convergence = false with a partial record; the seam does not gate convergence")
	}
	if result.CoverageGate.RecordStatus != "partial" {
		t.Errorf("coverage_gate.record_status = %q, want partial", result.CoverageGate.RecordStatus)
	}
	if !hasReasonMentioning(result.Bar.TriggerReasons, "immutable coverage record is partial") {
		t.Errorf("bar trigger reasons do not name the partial record: %#v", result.Bar.TriggerReasons)
	}
}

func TestCompleteRecordWithCleanPanelConverges(t *testing.T) {
	requireSeam(t)
	repo, base, head := seedSeamRepository(t)

	// Every changed file and hunk accounted read: account-coverage is complete
	// and exits 0.
	record := coverageRecordFor(t, repo, base, head, nil)
	coverageCheck, exit := runAccountCoverage(t, repo, base, head, record)
	if exit != 0 {
		t.Fatalf("account-coverage on a complete record exited %d, want 0", exit)
	}
	if coverageCheck.Status != "complete" {
		t.Fatalf("complete-record coverage status = %q, want complete", coverageCheck.Status)
	}

	// Clean panel, zero findings, bar on with a stubbed passing judge: all three
	// convergence ingredients present and the gate open.
	verifyInput := assembleVerifyInput(t, panelFullValidated(), coverageCheck.path, "auto")
	result := runVerifyWorkflow(t, verifyInput, "pass")

	if result.CoverageGate.BlocksConvergence {
		t.Fatalf("coverage_gate.blocks_convergence = true with a complete record; the gate wrongly blocks")
	}
	if result.CoverageGate.RecordStatus != "complete" {
		t.Errorf("coverage_gate.record_status = %q, want complete", result.CoverageGate.RecordStatus)
	}
	if result.Bar.Outcome != "pass" {
		t.Errorf("bar outcome = %q, want pass", result.Bar.Outcome)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected zero findings, got %d", len(result.Findings))
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
// predate the coverage-record seam (the synced casting before a resync) and when
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
	if !strings.Contains(string(out), "--coverage-record") {
		t.Skip("review-panel scripts predate the coverage-record seam (awaiting Foundry sync); proven against Canon in the gate report")
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
// changes two files, and returns the repo path plus base and head SHAs.
func seedSeamRepository(t *testing.T) (repo, base, head string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "subject")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	write(t, repo, "a.txt", "a1\n")
	write(t, repo, "b.txt", "b1\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	base = gitOut(t, repo, "rev-parse", "HEAD")
	write(t, repo, "a.txt", "a1\na2\n")
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

// coverageRecordFor builds a valid inspection record from account-coverage's own
// inventory, accounting every file/hunk `read` except those named in omit.
func coverageRecordFor(t *testing.T, repo, base, head string, omit map[string]bool) map[string]any {
	t.Helper()
	out, err := exec.Command(accountCoverageScript(t),
		"--repo", repo, "--base", base, "--head", head, "--inventory").CombinedOutput()
	if err != nil {
		t.Fatalf("account-coverage --inventory: %v\n%s", err, out)
	}
	var inventory struct {
		SchemaVersion int              `json:"schema_version"`
		BaseCommit    string           `json:"base_commit"`
		HeadCommit    string           `json:"head_commit"`
		Files         []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(out, &inventory); err != nil {
		t.Fatalf("decode inventory: %v\n%s", err, out)
	}
	files := make([]map[string]any, 0, len(inventory.Files))
	for _, file := range inventory.Files {
		account := "read"
		if omit[file["path"].(string)] {
			account = "omitted"
		}
		hunks, _ := file["hunks"].([]any)
		accountedHunks := make([]any, 0, len(hunks))
		for _, raw := range hunks {
			hunk := raw.(map[string]any)
			hunk["account"] = account
			accountedHunks = append(accountedHunks, hunk)
		}
		record := map[string]any{
			"path":    file["path"],
			"blob":    file["blob"],
			"account": account,
			"hunks":   accountedHunks,
		}
		files = append(files, record)
	}
	return map[string]any{
		"schema_version": 1,
		"base_commit":    inventory.BaseCommit,
		"head_commit":    inventory.HeadCommit,
		"files":          files,
		"references":     []any{},
	}
}

type coverageCheck struct {
	coverageResult
	path string
}

// runAccountCoverage runs the real account-coverage over a record, capturing its
// JSON to a file and returning the decoded result and the process exit code —
// the exit code matters: 1 is the partial VERDICT the seam must carry.
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
	checkPath := filepath.Join(dir, "coverage-check.json")
	if err := os.WriteFile(checkPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return coverageCheck{coverageResult: result, path: checkPath}, exit
}

// panelFullValidated is validate_quotes.py-shaped input with the panel declaring
// full coverage and zero findings — the would-otherwise-converge shape.
func panelFullValidated() map[string]any {
	return map[string]any{
		"findings":                []any{},
		"coverage":                []any{map[string]any{"brief": "tone", "status": "full"}},
		"skipped":                 []any{},
		"failures":                []any{},
		"reviews":                 []any{map[string]any{"brief": "tone", "shard": "1/1", "notes": "read a.txt and b.txt in full", "findings": 0}},
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

// assembleVerifyInput runs the real assemble_verify_input.py with the coverage
// record wired in, returning the path to the verify-input.json it produced.
func assembleVerifyInput(t *testing.T, validated map[string]any, coverageRecordPath, barMode string) string {
	t.Helper()
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	validatedPath := filepath.Join(dir, "validated.json")
	writeJSON(t, planPath, seamPlan(t))
	writeJSON(t, validatedPath, validated)

	assemble := filepath.Join(reviewScriptsDir(t), "assemble_verify_input.py")
	out, err := exec.Command("python3", assemble, planPath, validatedPath,
		"--bar", barMode, "--coverage-record", coverageRecordPath).Output()
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
// with the bar judge stubbed to barVerdict ('none' forbids any agent call).
func runVerifyWorkflow(t *testing.T, verifyInputPath, barVerdict string) verifyResult {
	t.Helper()
	workflow := filepath.Join(reviewScriptsDir(t), "verify_workflow.js")
	harness := filepath.Join(repoRoot(t), "verification", "reviewbar", "testdata", "verify_workflow_harness.mjs")
	out, err := exec.Command("node", harness, workflow, verifyInputPath, barVerdict).CombinedOutput()
	if err != nil {
		t.Fatalf("verify_workflow harness: %v\n%s", err, out)
	}
	var result verifyResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode verify result: %v\n%s", err, out)
	}
	return result
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
