package shell

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestArchiveRunSummaryBindsFailureToTheInvocation(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger")
	started := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	stamp := func(offset time.Duration) string {
		return started.Add(offset).Format("2006-01-02T15:04:05.000Z")
	}
	writeTestFile(t, ledger, fmt.Sprintf(
		"timestamp=%s pull_request=owner/repo#5 head=abc stage=old cause=old failure\n"+
			"timestamp=%s pull_request=owner/repo#5 head=abc stage=review cause=workflow returned no verdict\n"+
			"timestamp=%s pull_request=owner/repo#5 head=def stage=other cause=another head\n",
		stamp(-time.Second), stamp(time.Millisecond), stamp(time.Second)))
	t.Setenv("MINOS_RUN_STARTED_AT", strconv.FormatInt(started.Unix(), 10))
	t.Setenv("MINOS_FAILURE_LOG", ledger)
	t.Setenv("MINOS_OWNER", "owner")
	t.Setenv("MINOS_REPO_NAME", "repo")
	t.Setenv("MINOS_PR", "5")
	t.Setenv("MINOS_HEAD_SHA", "abc")
	writeTestFile(t, filepath.Join(root, "lead-complete"), "non-clean\n")
	writeTestFile(t, filepath.Join(root, "forge-status"), "incomplete\n")
	summary := runArchiveSummary(t, root, false)
	if summary["outcome"] != "incomplete" || summary["failure_reason"] != "stage=review cause=workflow returned no verdict" {
		t.Fatalf("unexpected outcome or failure reason: %#v", summary)
	}
	if elapsed, _ := summary["duration_seconds"].(float64); elapsed < 180 || elapsed > 240 {
		t.Fatalf("unexpected elapsed seconds: %#v", summary)
	}
}

func TestArchiveRunSummaryKeepsSkipEvidence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MINOS_RUN_STARTED_AT", "")
	writeTestFile(t, filepath.Join(root, "lead-complete"), "clean\n")
	writeTestFile(t, filepath.Join(root, "report.md"), "Only the target changed.\n")
	summary := runArchiveSummary(t, root, false)
	if summary["outcome"] != "clean" || summary["review_skipped"] != true || summary["report_excerpt"] != "Only the target changed." {
		t.Fatalf("legacy lead skip evidence: %#v", summary)
	}
	writeTestFile(t, filepath.Join(root, "judgements", "push-inspection.json"), `{"answer":{"outcome":"no-review","reason":"Generated content only."}}`)
	writeTestFile(t, filepath.Join(root, "review-args.json"), "{}")
	summary = runArchiveSummary(t, root, false)
	if summary["review_skipped"] != true || summary["skip_reason"] != "Generated content only." {
		t.Fatalf("scripted skip evidence: %#v", summary)
	}
}

func TestArchiveRunSummaryReportsSalvageWithoutInventingADuration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MINOS_RUN_STARTED_AT", "")
	writeTestFile(t, filepath.Join(root, "failure.log"), "workspace unavailable\n")
	summary := runArchiveSummary(t, root, true)
	if summary["outcome"] != "failed" || summary["failure_reason"] != "workspace unavailable" || summary["duration_seconds"] != nil {
		t.Fatalf("salvage summary: %#v", summary)
	}
}

func runArchiveSummary(t *testing.T, root string, salvaged bool) map[string]any {
	t.Helper()
	command := filepath.Join("..", "..", "scripts", "run-body", "archive-run-summary")
	result, err := exec.CommandContext(t.Context(), command, root, strconv.FormatBool(salvaged)).CombinedOutput()
	if err != nil {
		t.Fatalf("summarise archive: %v\n%s", err, result)
	}
	var summary map[string]any
	if err := json.Unmarshal(result, &summary); err != nil {
		t.Fatalf("decode summary: %v, output %s", err, strings.TrimSpace(string(result)))
	}
	return summary
}
