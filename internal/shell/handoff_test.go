package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAdmissionReadsScriptedContinuationWithPredecessorProgress(t *testing.T) {
	runDir := t.TempDir()
	path := filepath.Join(runDir, "handoff.json")
	if err := os.WriteFile(filepath.Join(runDir, "memory-pressure"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", `
import { continueRun } from "../../workflows/run-steps/continue-run.mjs";
const [runDir, handoff] = process.argv.slice(1);
const head = "a".repeat(40);
const run = {
  runDir, head,
  pullRequest: { owner: "acme", repository: "widgets", number: "7" },
  env: {
    MINOS_HANDOFF: handoff,
    MINOS_PREDECESSOR_PROGRESS: JSON.stringify({ stage: "review", head, latestReview: 6 }),
  },
  async forge(operation) {
    if (operation === "snapshot") return {
      head_sha: head, target_sha: "b".repeat(40), authenticated_user: "minos",
      reviews: [{ id: 7, user: "minos", commit_id: head }],
    };
    if (operation === "status") return { outcome: "applied" };
    throw new Error("unexpected forge operation: " + operation);
  },
};
if (!(await continueRun(run, "review"))) throw new Error("the run did not hand off");
`, runDir, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write scripted handoff: %v\n%s", err, output)
	}

	handoff, err := readRunHandoff(path, Facts{Owner: "acme", Repo: "widgets", PR: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if decision := compareContinuationProgress(handoff); decision != continuationProgressAdvanced {
		t.Fatalf("continuation progress = %q, want advanced", decision)
	}
}
