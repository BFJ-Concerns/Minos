package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArchivedMonitoringDistinguishesSalvageFromTerminalRetry(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := statusTestConfig(t)
	cfg.Runs.FailureLog = filepath.Join(cfg.Root, "failures.log")
	var err error
	cfg.Runs.ArchiveCommand, err = filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "archive-run"))
	if err != nil {
		t.Fatal(err)
	}
	bin, destination := t.TempDir(), t.TempDir()
	installArchiveReceiverSSH(t, bin, destination)
	writeScript(t, filepath.Join(bin, "zstd"), "#!/bin/sh\nexec cat\n")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("MINOS_ARCHIVE_CONFIG", "")
	writeTestFile(t, filepath.Join(cfg.Root, "identity"), "fixture\n")
	writeTestFile(t, filepath.Join(cfg.Root, "known-hosts"), "fixture\n")
	writeTestFile(t, filepath.Join(cfg.Root, "archive.env"), "MINOS_ARCHIVE_HOST=fixture\n"+
		"MINOS_ARCHIVE_IDENTITY_FILE="+filepath.Join(cfg.Root, "identity")+"\n"+
		"MINOS_ARCHIVE_KNOWN_HOSTS="+filepath.Join(cfg.Root, "known-hosts")+"\n")

	// If recovery samples its own cgroup, these counters would replace
	// the terminal retry's figures and show up on the recovered run too.
	cgroup := filepath.Join(cfg.Root, "sweep-cgroup")
	t.Setenv("MINOS_CGROUP_DIR", cgroup)
	writeTestFile(t, filepath.Join(cgroup, "memory.stat"), "anon 1000\nworkingset_refault_anon 90\n")
	writeTestFile(t, filepath.Join(cgroup, "memory.pressure"), "some total=1000\n")
	baseline := `{"kind":"minos-memory-snapshot-v1","stall_some_us":10,"refault_anon":2}`
	final := `{"kind":"minos-memory-snapshot-v1","stall_some_us":25,"refault_anon":7}`

	crashed := filepath.Join(cfg.Runs.Dir, statusRunUnit+"-crashed")
	writeTestFile(t, filepath.Join(crashed, "report.md"), "Workspace from an earlier attempt")
	writeTestFile(t, filepath.Join(crashed, "judgements", "push-inspection.json"), `{"answer":{"outcome":"no-review","reason":"This was the predecessor."}}`)
	writeTestFile(t, filepath.Join(crashed, "memory-baseline.json"), baseline)
	writeTestFile(t, filepath.Join(crashed, "memory-final.json"), final)
	writeTestFile(t, filepath.Join(crashed, "timing-record.json"), `{"run_summary":{"outcome":"continuation","duration_seconds":999}}`)
	retrying := filepath.Join(cfg.Runs.Dir, "minos-run-forgejo-example-Relay-pr97-retrying")
	writeTestFile(t, filepath.Join(retrying, archivePendingFile), "example-Relay-pr97\n")
	writeTestFile(t, filepath.Join(retrying, "orientation.json"), `{"source":{"owner":"example","repo":"Relay","pr":"97"}}`)
	writeTestFile(t, filepath.Join(retrying, "memory-baseline.json"), baseline)
	writeTestFile(t, filepath.Join(retrying, "memory-final.json"), final)
	writeTestFile(t, filepath.Join(retrying, "timing-record.json"), `{"run_summary":{"outcome":"clean","duration_seconds":42}}`)
	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}

	sidecars, err := filepath.Glob(filepath.Join(destination, "*.timings.json"))
	if err != nil || len(sidecars) != 2 {
		t.Fatalf("delivered sidecars = %v, %v", sidecars, err)
	}
	listing := make(map[string]string)
	for _, path := range sidecars {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		listing[filepath.Base(path)] = string(body)
	}
	cfg.Runs.RecentTimingsCommand = archiveListingScript(t, listing)
	runs, err := archivedRuns(t.Context(), cfg, 24, 60)
	if err != nil || len(runs) != 2 {
		t.Fatalf("recent runs = %#v, %v", runs, err)
	}
	wantOutcome := map[string]string{"96": "failed", "97": "clean"}
	for _, run := range runs {
		if run.Owner != "example" || run.Repo != "Relay" {
			t.Fatalf("archive lost pull request identity: %+v", run)
		}
		var record struct {
			Summary struct {
				Outcome  string
				Duration *int   `json:"duration_seconds"`
				Salvaged bool   `json:"salvaged"`
				Skipped  bool   `json:"review_skipped"`
				Skip     string `json:"skip_reason"`
				Report   string `json:"report_excerpt"`
			} `json:"run_summary"`
			Memory struct {
				Final map[string]any
				Delta map[string]int
			}
		}
		if err := json.Unmarshal(run.Timings, &record); err != nil {
			t.Fatal(err)
		}
		summary := record.Summary
		if want := wantOutcome[run.PR]; want == "" || summary.Outcome != want {
			t.Fatalf("pull %s summary = %+v, want outcome %q", run.PR, summary, want)
		}
		delete(wantOutcome, run.PR)
		if run.PR == "96" && summary.Duration != nil || run.PR == "97" && (summary.Duration == nil || *summary.Duration != 42) {
			t.Fatalf("pull %s has the wrong invocation duration: %s", run.PR, run.Timings)
		}
		if run.PR == "96" {
			if !summary.Salvaged || summary.Skipped || summary.Skip != "" || summary.Report != "" ||
				record.Memory.Final != nil || record.Memory.Delta != nil {
				t.Fatalf("recovered run carries unowned evidence: %s", run.Timings)
			}
		} else if record.Memory.Final["stall_some_us"] != float64(25) ||
			record.Memory.Delta["stall_some_us"] != 15 || record.Memory.Delta["refault_anon"] != 5 {
			t.Fatalf("terminal retry lost the live run's memory sample: %s", run.Timings)
		}
	}
}

func TestArchivedMonitoringUsesTheUnitWhenLegacyIdentityIsUnknown(t *testing.T) {
	var cfg ServiceConfig
	cfg.Runs.RecentTimingsCommand = archiveListingScript(t, map[string]string{
		"20261127T052000Z-minos-run-forgejo-example-Relay-pr96-recovered.timings.json": `{"pull_request":{"owner":"unknown","repository":"unknown","number":"unknown"}}`,
	})
	runs, err := archivedRuns(t.Context(), cfg, 24, 60)
	if err != nil || len(runs) != 1 {
		t.Fatalf("recent runs = %#v, %v", runs, err)
	}
	run := runs[0]
	if run.Unit != statusRunUnit || run.Owner != "" || run.Repo != "" || run.PR != "" {
		t.Fatalf("legacy run should use unit identity: %+v", run)
	}
}
