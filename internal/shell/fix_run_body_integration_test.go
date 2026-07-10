package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixRunBodyRecordsEachOutcomeThroughRunWrap drives the fix kind end to end
// through the real RunWrapCommand spawn path with a deterministic engine
// stand-in: dispatch, the launch indirection, the head guard, the writability
// read, the landing adaptation, and the outcome record. It proves the seam is
// correct and honest for all three fix outcomes plus the loud model mismatch,
// at zero model cost. The fix's judgement quality is the skill's, exercised
// only once the general debugging skill has synced.
func TestFixRunBodyRecordsEachOutcomeThroughRunWrap(t *testing.T) {
	env := setupRunBodyHarness(t, "fix", "fix-engine-standin", "fix.md")

	// Landed: a writable head, the stand-in edits and lands through commit-push.
	env.setRunEnv(t, "aaaaaaaaaaaaaaaa", map[string]string{"PUMP19_STANDIN_FIX_OUTCOME": "landed"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	landed := RunDir(env.runsDir, "local", "pump19", "subject", "7", "aaaaaaaaaaaaaaaa", RunFix)
	assertContainsFile(t, filepath.Join(landed, "fix-summary.md"), "outcome=landed")
	assertContainsFile(t, filepath.Join(landed, "fix-summary.md"), "run=fix")
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "main")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/fix\nsuccess")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-added"), "Fixing")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-removed"), "Fixing")

	// Fruitless: a writable head, but the pass lands nothing — no push, and the
	// standing findings remain the verdict.
	_ = os.Remove(filepath.Join(env.stateDir, "commit-push.args"))
	env.setRunEnv(t, "bbbbbbbbbbbbbbbb", map[string]string{"PUMP19_STANDIN_FIX_OUTCOME": "fruitless"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	fruitless := RunDir(env.runsDir, "local", "pump19", "subject", "7", "bbbbbbbbbbbbbbbb", RunFix)
	assertContainsFile(t, filepath.Join(fruitless, "fix-summary.md"), "outcome=fruitless")
	if _, err := os.Stat(filepath.Join(env.stateDir, "commit-push.args")); !os.IsNotExist(err) {
		t.Fatal("fruitless fix unexpectedly pushed a commit")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/fix\nsuccess")

	// Unwritable head: a fork PR (head repo != base repo). No fix path; findings
	// stand. The stand-in reads this from the forge facts, not from a flag.
	env.setRunEnv(t, "cccccccccccccccc", map[string]string{"PUMP19_STANDIN_FIX_OUTCOME": "landed", "PUMP19_FIXTURE_FORK": "1"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	fork := RunDir(env.runsDir, "local", "pump19", "subject", "7", "cccccccccccccccc", RunFix)
	assertContainsFile(t, filepath.Join(fork, "fix-summary.md"), "outcome=unwritable")
	if _, err := os.Stat(filepath.Join(env.stateDir, "commit-push.args")); !os.IsNotExist(err) {
		t.Fatal("unwritable head unexpectedly pushed a commit")
	}

	// Protected same-repo head: writable by the fork check, but the push is
	// refused. commit-push reports the unwritable token; the run records the
	// standing-findings unwritable outcome rather than a generic error.
	env.setRunEnv(t, "eeeeeeeeeeeeeeee", map[string]string{"PUMP19_STANDIN_FIX_OUTCOME": "landed", "PUMP19_FIXTURE_PROTECTED": "1"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	protected := RunDir(env.runsDir, "local", "pump19", "subject", "7", "eeeeeeeeeeeeeeee", RunFix)
	assertContainsFile(t, filepath.Join(protected, "fix-summary.md"), "outcome=unwritable")
	// commit-push WAS attempted here (unlike the fork), and reported unwritable.
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "main")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "pump19/fix\nsuccess")

	// Loud model mismatch: the served model is not the lead pin. It occurs before
	// the mission can attempt a forge write, so it is retryable and PR-invisible.
	postedBefore, _ := os.ReadFile(filepath.Join(env.stateDir, "comments"))
	env.setRunEnv(t, "dddddddddddddddd", map[string]string{"PUMP19_STANDIN_LEAD_MODEL": "floating-alias-surprise"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err == nil {
		t.Fatal("fix model mismatch unexpectedly completed")
	}
	mismatch := RunDir(env.runsDir, "local", "pump19", "subject", "7", "dddddddddddddddd", RunFix)
	if _, err := os.Stat(filepath.Join(mismatch, "retry.env")); err != nil {
		t.Fatalf("fix mismatch did not remain retryable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mismatch, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Fatalf("fix mismatch latched before a forge write: %v", err)
	}
	if status, _ := os.ReadFile(filepath.Join(env.stateDir, "status.args")); strings.Contains(string(status), "pump19/fix\nerror") {
		t.Fatalf("fix mismatch wrote PR error status:\n%s", status)
	}
	postedAfter, _ := os.ReadFile(filepath.Join(env.stateDir, "comments"))
	if string(postedAfter) != string(postedBefore) {
		t.Fatal("fix model mismatch posted a summary comment")
	}
}

// runBodyHarness holds the shared fixture wiring for a fix or finish run driven
// through the real wrapper. The two kinds differ only in their engine stand-in,
// mission, and per-run environment.
type runBodyHarness struct {
	root        string
	configRoot  string
	installRoot string
	stateDir    string
	runsDir     string
	binary      string
	kind        string
}

func setupRunBodyHarness(t *testing.T, kind, standinName, missionName string) *runBodyHarness {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	h := &runBodyHarness{
		root:        root,
		configRoot:  filepath.Join(root, "config"),
		installRoot: filepath.Join(root, "install"),
		stateDir:    filepath.Join(root, "state"),
		runsDir:     filepath.Join(root, "runs"),
		binary:      filepath.Join(root, "pump19"),
		kind:        kind,
	}
	adaptationDir := filepath.Join(root, "adaptation")
	for _, dir := range []string{h.configRoot, adaptationDir, h.stateDir, h.runsDir, filepath.Join(h.installRoot, "run-body"), filepath.Join(h.installRoot, "missions")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", h.binary, "../../cmd/pump19")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pump19 fixture: %v\n%s", err, out)
	}

	copyRepoFile(t, filepath.Join("..", "..", "scripts", "run-body", "run-body"), filepath.Join(h.installRoot, "run-body", "run-body"), 0o755)
	copyRepoFile(t, filepath.Join("..", "..", "missions", missionName), filepath.Join(h.installRoot, "missions", missionName), 0o644)

	reviewScripts, _ := filepath.Abs(filepath.Join("..", "..", "scripts", "review"))
	standin, _ := filepath.Abs(filepath.Join("..", "..", "scripts", "e2e", standinName))
	fixSkill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "service", "fix", "SKILL.md"))
	flakySkill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "root-cause", "SKILL.md"))
	pins := filepath.Join(root, "pins.toml")
	if err := os.WriteFile(pins, []byte(pinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := "PUMP19_ENGINE_LAUNCH_LEAD='" + standin + "'\n" +
		"PUMP19_ENSEMBLE_LAUNCH='/bin/false'\n" +
		"PUMP19_PINS='" + pins + "'\n" +
		"PUMP19_REVIEW_SCRIPTS='" + reviewScripts + "'\n" +
		"PUMP19_BIN='" + h.binary + "'\n" +
		"PUMP19_FIX_SKILL='" + fixSkill + "'\n" +
		"PUMP19_FLAKY_SKILL='" + flakySkill + "'\n" +
		"PUMP19_FIX_AUTHOR_NAME='Pump-19'\n" +
		"PUMP19_FIX_AUTHOR_EMAIL='pump19@bfj.invalid'\n"
	if err := os.WriteFile(filepath.Join(h.configRoot, "run-body.env"), []byte(runBodyEnv), 0o644); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(root, "token")
	webhookSecret := filepath.Join(root, "webhook-secret")
	if err := os.WriteFile(token, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webhookSecret, []byte("test-webhook-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := "[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + token + "\"\n\n[runs]\ndir = \"" + h.runsDir + "\"\n\n[sweep]\nliveness-threshold = \"1h\"\n"
	if err := os.WriteFile(filepath.Join(h.configRoot, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRunBodyAdaptationFixture(t, adaptationDir, h.stateDir)
	return h
}

func (h *runBodyHarness) setRunEnv(t *testing.T, head string, extra map[string]string) {
	t.Helper()
	kind, err := ParseRunKind(h.kind)
	if err != nil {
		t.Fatal(err)
	}
	runDir := RunDir(h.runsDir, "local", "pump19", "subject", "7", head, kind)
	values := map[string]string{
		"PUMP19_RUN_DIR":      runDir,
		"PUMP19_RUN_KIND":     h.kind,
		"PUMP19_OCCASION":     "reconcile",
		"PUMP19_FORGE":        "local",
		"PUMP19_REPO":         "pump19/subject",
		"PUMP19_OWNER":        "pump19",
		"PUMP19_REPO_NAME":    "subject",
		"PUMP19_PR":           "7",
		"PUMP19_HEAD_SHA":     head,
		"PUMP19_BASE_REF":     "main",
		"PUMP19_WORKSPACE":    filepath.Join(h.root, "workspace-"+head),
		"PUMP19_DIFF":         filepath.Join(runDir, "diff.patch"),
		"PUMP19_ADAPTATION":   filepath.Join(h.root, "adaptation"),
		"PUMP19_RUN_BODY":     filepath.Join(h.installRoot, "run-body", "run-body"),
		"PUMP19_BRIEFS":       ".review",
		"PUMP19_BUILD_CMD":    "true",
		"PUMP19_TEST_CMD":     "true",
		"PUMP19_AUTO_MERGE":   "false",
		"PUMP19_CONFIG":       h.configRoot,
		"PUMP19_UNIT":         "pump19-test.service",
		"PUMP19_FIXTURE_FORK": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	// Reset scenario knobs so a prior case does not leak into the next.
	for _, key := range []string{"PUMP19_STANDIN_FIX_OUTCOME", "PUMP19_STANDIN_FLAKY_OUTCOME", "PUMP19_STANDIN_LEAD_MODEL", "PUMP19_STANDIN_MAINTENANCE", "PUMP19_FIXTURE_MERGEABLE", "PUMP19_FIXTURE_BUILD", "PUMP19_FIXTURE_TEST", "PUMP19_FIXTURE_PROTECTED", "PUMP19_FIXTURE_COMMIT_OUTCOME", "PUMP19_FIXTURE_LABELS", "PUMP19_FIXTURE_VERDICT", "PUMP19_FIXTURE_HUMAN_APPROVE"} {
		t.Setenv(key, "")
	}
	for key, value := range extra {
		t.Setenv(key, value)
	}
}

func copyRepoFile(t *testing.T, source, target string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, mode); err != nil {
		t.Fatal(err)
	}
}

// writeRunBodyAdaptationFixture supplies fixture forge adaptations for the fix
// and finish seam tests: deterministic, forge-free stand-ins that record their
// arguments so the test can observe every forge write the run made. The real
// curl/git adaptations are proven separately (commit-push by its own focused
// test, the curl wrappers by make e2e).
func writeRunBodyAdaptationFixture(t *testing.T, adaptationDir, stateDir string) {
	t.Helper()
	operations := filepath.Join(stateDir, "operations")
	writeScript(t, filepath.Join(adaptationDir, "prepare-workspace"), `#!/usr/bin/env sh
set -eu
rm -rf "$PUMP19_WORKSPACE"
mkdir -p "$PUMP19_WORKSPACE"
printf 'head\n' >"$PUMP19_WORKSPACE/file.txt"
: >"$PUMP19_DIFF"
`)
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	// A fork fixture reports a head repo distinct from the base — an unwritable
	// head. MERGEABLE and the outcome labels are scenario knobs.
	writeScript(t, filepath.Join(adaptationDir, "get-pr-facts"), `#!/usr/bin/env sh
head_repo=pump19/subject
if [ "${PUMP19_FIXTURE_FORK:-}" = 1 ]; then head_repo=forker/subject; fi
printf 'OCCASION=reconcile\nOWNER=%s\nREPO=%s\nPR=%s\nHEAD_SHA=%s\nBASE_REF=main\n' "$1" "$2" "$3" "$PUMP19_HEAD_SHA"
printf 'HEAD_BRANCH=main\nHEAD_REPO=%s\nBASE_REPO=pump19/subject\n' "$head_repo"
printf 'MERGEABLE=%s\n' "${PUMP19_FIXTURE_MERGEABLE:-true}"
printf 'LABELS=%s\n' "${PUMP19_FIXTURE_LABELS:-Converged,Ready}"
`)
	writeScript(t, filepath.Join(adaptationDir, "list-review-comments"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	// Finish eligibility reads the service's OWN verdict from the trailing marker
	// of its posted reviews. PUMP19_FIXTURE_VERDICT sets that verdict (default
	// converged; `none` means the service posted no verdict for this head).
	// PUMP19_FIXTURE_HUMAN_APPROVE=1 adds an unrelated human APPROVE with no
	// service marker — which must never satisfy eligibility.
	listReviews := "#!/usr/bin/env sh\n" +
		"reviews='[]'\n" +
		"if [ \"${PUMP19_FIXTURE_HUMAN_APPROVE:-}\" = 1 ]; then\n" +
		"  reviews=$(printf '%s' \"$reviews\" | jq --arg h \"$PUMP19_HEAD_SHA\" '. + [{state:\"APPROVED\",commit_id:$h,body:\"Looks good to me\"}]')\n" +
		"fi\n" +
		"verdict=\"${PUMP19_FIXTURE_VERDICT:-converged}\"\n" +
		"if [ \"$verdict\" != none ]; then\n" +
		"  marker=\"Pump-19: bar=passed coverage=full head=$PUMP19_HEAD_SHA run=review verdict=$verdict\"\n" +
		"  reviews=$(printf '%s' \"$reviews\" | jq --arg h \"$PUMP19_HEAD_SHA\" --arg m \"$marker\" '. + [{state:\"COMMENT\",commit_id:$h,body:(\"service review\\n\\n\" + $m)}]')\n" +
		"fi\n" +
		"printf '%s\\n' \"$reviews\"\n"
	writeScript(t, filepath.Join(adaptationDir, "list-reviews"), listReviews)
	writeScript(t, filepath.Join(adaptationDir, "commit-push"), "#!/usr/bin/env sh\nprintf 'commit-push\\n' >>'"+operations+"'\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "commit-push.args")+"'\nif [ \"${PUMP19_FIXTURE_PROTECTED:-}\" = 1 ]; then printf 'unwritable\\n'; else printf '%s\\n' \"${PUMP19_FIXTURE_COMMIT_OUTCOME:-landed landedsha}\"; fi\n")
	writeScript(t, filepath.Join(adaptationDir, "merge"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "merge.args")+"'\nprintf '{\"merged\":true}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-comment"), "#!/usr/bin/env sh\nprintf 'post-comment\\n' >>'"+operations+"'\ncat \"$4\" >>'"+filepath.Join(stateDir, "comments")+"'\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf 'set-status:%s:%s\\n' \"$4\" \"$5\" >>'"+operations+"'\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "status.args")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "add-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-added")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf 'remove-label:%s\\n' \"$4\" >>'"+operations+"'\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-removed")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "add-reaction"), "#!/usr/bin/env sh\n[ \"$PUMP19_FORGE_TOKEN\" = test-token ]\nprintf 'add-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\n[ \"$PUMP19_FORGE_TOKEN\" = test-token ]\nprintf 'remove-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "assign-if-missing"), `#!/usr/bin/env sh
set -eu
[ "$PUMP19_FORGE_TOKEN" = test-token ]
assignees='`+filepath.Join(stateDir, "assignees")+`'
if [ -f "$assignees" ] && grep -Fxiq "$4" "$assignees"; then exit 0; fi
printf '%s\n' "$4" >>"$assignees"
printf 'assign-if-missing:%s\n' "$4" >>'`+operations+`'
`)
}
