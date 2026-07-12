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
	env.setRunEnv(t, "aaaaaaaaaaaaaaaa", map[string]string{"MINOS_STANDIN_FIX_OUTCOME": "landed"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	landed := RunDir(env.runsDir, "local", "minos", "subject", "7", "aaaaaaaaaaaaaaaa", RunFix)
	assertContainsFile(t, filepath.Join(landed, "fix-summary.md"), "outcome=landed")
	assertContainsFile(t, filepath.Join(landed, "fix-summary.md"), "run=fix")
	assertPostedSummaryOmitsModelIdentity(t, filepath.Join(landed, "fix-summary.md"))
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "main")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/fix\nsuccess")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-added"), "Fixing")
	assertContainsFile(t, filepath.Join(env.stateDir, "labels-removed"), "Fixing")
	assertContainsFile(t, filepath.Join(env.stateDir, "review-comments"), "41:fixed in `landedsha`")
	if data, err := os.ReadFile(filepath.Join(env.stateDir, "review-comments")); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), "42:") {
		t.Fatalf("unfixed finding received a fix comment:\n%s", data)
	}

	// Acknowledgements are supplementary prose. Once the fix outcome is
	// recorded and presence released, an unavailable review-comment endpoint
	// must leave the successfully landed run complete.
	if err := os.Remove(filepath.Join(env.stateDir, "operations")); err != nil {
		t.Fatal(err)
	}
	env.setRunEnv(t, "ffffffffffffffff", map[string]string{
		"MINOS_STANDIN_FIX_OUTCOME": "landed",
		"MINOS_FIXTURE_ACK_FAILURE": "1",
	})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatalf("landed fix failed because its acknowledgement failed: %v", err)
	}
	operations, err := os.ReadFile(filepath.Join(env.stateDir, "operations"))
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := "add-reaction:eyes\ncommit-push\npost-comment\nset-status:minos/fix:success\nremove-reaction:eyes\nremove-label:Fixing\npost-review-comment\n"
	if string(operations) != wantOrder {
		t.Fatalf("landed publication order =\n%s\nwant:\n%s", operations, wantOrder)
	}
	failedAck := RunDir(env.runsDir, "local", "minos", "subject", "7", "ffffffffffffffff", RunFix)
	assertContainsFile(t, filepath.Join(failedAck, "run.log"), "per-finding acknowledgement failed for review 41")

	// Fruitless: a writable head, but the pass lands nothing — no push, and the
	// standing findings remain the verdict.
	_ = os.Remove(filepath.Join(env.stateDir, "commit-push.args"))
	env.setRunEnv(t, "bbbbbbbbbbbbbbbb", map[string]string{"MINOS_STANDIN_FIX_OUTCOME": "fruitless"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	fruitless := RunDir(env.runsDir, "local", "minos", "subject", "7", "bbbbbbbbbbbbbbbb", RunFix)
	assertContainsFile(t, filepath.Join(fruitless, "fix-summary.md"), "outcome=fruitless")
	if _, err := os.Stat(filepath.Join(env.stateDir, "commit-push.args")); !os.IsNotExist(err) {
		t.Fatal("fruitless fix unexpectedly pushed a commit")
	}
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/fix\nsuccess")

	// Unwritable head: a fork PR (head repo != base repo). No fix path; findings
	// stand. The stand-in reads this from the forge facts, not from a flag.
	env.setRunEnv(t, "cccccccccccccccc", map[string]string{"MINOS_STANDIN_FIX_OUTCOME": "landed", "MINOS_FIXTURE_FORK": "1"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	fork := RunDir(env.runsDir, "local", "minos", "subject", "7", "cccccccccccccccc", RunFix)
	assertContainsFile(t, filepath.Join(fork, "fix-summary.md"), "outcome=unwritable")
	if _, err := os.Stat(filepath.Join(env.stateDir, "commit-push.args")); !os.IsNotExist(err) {
		t.Fatal("unwritable head unexpectedly pushed a commit")
	}

	// Protected same-repo head: writable by the fork check, but the push is
	// refused. commit-push reports the unwritable token; the run records the
	// standing-findings unwritable outcome rather than a generic error.
	env.setRunEnv(t, "eeeeeeeeeeeeeeee", map[string]string{"MINOS_STANDIN_FIX_OUTCOME": "landed", "MINOS_FIXTURE_PROTECTED": "1"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err != nil {
		t.Fatal(err)
	}
	protected := RunDir(env.runsDir, "local", "minos", "subject", "7", "eeeeeeeeeeeeeeee", RunFix)
	assertContainsFile(t, filepath.Join(protected, "fix-summary.md"), "outcome=unwritable")
	// commit-push WAS attempted here (unlike the fork), and reported unwritable.
	assertContainsFile(t, filepath.Join(env.stateDir, "commit-push.args"), "main")
	assertContainsFile(t, filepath.Join(env.stateDir, "status.args"), "minos/fix\nsuccess")

	// Loud model mismatch: the served model is not the lead pin. It occurs before
	// the mission can attempt a forge write, so it is retryable and PR-invisible.
	postedBefore, _ := os.ReadFile(filepath.Join(env.stateDir, "comments"))
	env.setRunEnv(t, "dddddddddddddddd", map[string]string{"MINOS_STANDIN_LEAD_MODEL": "floating-alias-surprise"})
	if err := RunWrapCommand(t.Context(), []string{"--config", env.configRoot}); err == nil {
		t.Fatal("fix model mismatch unexpectedly completed")
	}
	mismatch := RunDir(env.runsDir, "local", "minos", "subject", "7", "dddddddddddddddd", RunFix)
	if _, err := os.Stat(filepath.Join(mismatch, "retry.env")); err != nil {
		t.Fatalf("fix mismatch did not remain retryable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mismatch, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Fatalf("fix mismatch latched before a forge write: %v", err)
	}
	if status, _ := os.ReadFile(filepath.Join(env.stateDir, "status.args")); strings.Contains(string(status), "minos/fix\nerror") {
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
		binary:      filepath.Join(root, "minos"),
		kind:        kind,
	}
	adaptationDir := filepath.Join(root, "adaptation")
	for _, dir := range []string{h.configRoot, adaptationDir, h.stateDir, h.runsDir, filepath.Join(h.installRoot, "run-body"), filepath.Join(h.installRoot, "missions")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", h.binary, "../../cmd/minos")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build minos fixture: %v\n%s", err, out)
	}

	copyRepoFile(t, filepath.Join("..", "..", "scripts", "run-body", "run-body"), filepath.Join(h.installRoot, "run-body", "run-body"), 0o755)
	copyRepoFile(t, filepath.Join("..", "..", "missions", missionName), filepath.Join(h.installRoot, "missions", missionName), 0o644)

	reviewScripts, _ := filepath.Abs(filepath.Join("..", "..", "scripts", "review"))
	standin, _ := filepath.Abs(filepath.Join("..", "..", "scripts", "e2e", standinName))
	fixSkill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "service", "fix", "SKILL.md"))
	flakySkill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "foundry", "root-cause", "SKILL.md"))
	pins := filepath.Join(root, "pins.toml")
	if err := os.WriteFile(pins, []byte(leadPinsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	runBodyEnv := "MINOS_ENGINE_LAUNCH_LEAD='" + standin + "'\n" +
		"MINOS_ENSEMBLE_LAUNCH='/bin/false'\n" +
		"MINOS_PINS='" + pins + "'\n" +
		"MINOS_REVIEW_SCRIPTS='" + reviewScripts + "'\n" +
		"MINOS_BIN='" + h.binary + "'\n" +
		"MINOS_FIX_SKILL='" + fixSkill + "'\n" +
		"MINOS_FLAKY_SKILL='" + flakySkill + "'\n" +
		"MINOS_FIX_AUTHOR_NAME='Minos'\n" +
		"MINOS_FIX_AUTHOR_EMAIL='minos@bfj.invalid'\n"
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
	service := "[service]\nbot-login = \"Minos\"\n\n[listener]\nbind = \":0\"\n\n[forges.local]\nadaptation = \"" + adaptationDir + "\"\napi-base = \"http://forge.invalid\"\nwebhook-secret-file = \"" + webhookSecret + "\"\ncredential-file = \"" + token + "\"\n\n[runs]\ndir = \"" + h.runsDir + "\"\nmax-concurrent = 2\n\n[sweep]\nliveness-threshold = \"1h\"\n"
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
	runDir := RunDir(h.runsDir, "local", "minos", "subject", "7", head, kind)
	values := map[string]string{
		"MINOS_RUN_DIR":      runDir,
		"MINOS_RUN_KIND":     h.kind,
		"MINOS_OCCASION":     "reconcile",
		"MINOS_FORGE":        "local",
		"MINOS_REPO":         "minos/subject",
		"MINOS_OWNER":        "minos",
		"MINOS_REPO_NAME":    "subject",
		"MINOS_PR":           "7",
		"MINOS_HEAD_SHA":     head,
		"MINOS_BASE_REF":     "main",
		"MINOS_WORKSPACE":    filepath.Join(h.root, "workspace-"+head),
		"MINOS_DIFF":         filepath.Join(runDir, "diff.patch"),
		"MINOS_ADAPTATION":   filepath.Join(h.root, "adaptation"),
		"MINOS_RUN_BODY":     filepath.Join(h.installRoot, "run-body", "run-body"),
		"MINOS_BRIEFS":       ".review",
		"MINOS_BUILD_CMD":    "true",
		"MINOS_TEST_CMD":     "true",
		"MINOS_AUTO_MERGE":   "false",
		"MINOS_CONFIG":       h.configRoot,
		"MINOS_UNIT":         "minos-test.service",
		"MINOS_FIXTURE_FORK": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	// Reset scenario knobs so a prior case does not leak into the next.
	for _, key := range []string{"MINOS_STANDIN_FIX_OUTCOME", "MINOS_STANDIN_FLAKY_OUTCOME", "MINOS_STANDIN_LEAD_MODEL", "MINOS_STANDIN_MAINTENANCE", "MINOS_FIXTURE_MERGEABLE", "MINOS_FIXTURE_BUILD", "MINOS_FIXTURE_TEST", "MINOS_FIXTURE_PROTECTED", "MINOS_FIXTURE_COMMIT_OUTCOME", "MINOS_FIXTURE_LABELS", "MINOS_FIXTURE_VERDICT", "MINOS_FIXTURE_HUMAN_APPROVE", "MINOS_FIXTURE_ACK_FAILURE", "MINOS_FIXTURE_BEHIND_BASE"} {
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
rm -rf "$MINOS_WORKSPACE"
mkdir -p "$MINOS_WORKSPACE"
printf 'head\n' >"$MINOS_WORKSPACE/file.txt"
: >"$MINOS_DIFF"
git -C "$MINOS_WORKSPACE" init -q
git -C "$MINOS_WORKSPACE" config user.name "Minos Test"
git -C "$MINOS_WORKSPACE" config user.email "minos@example.invalid"
git -C "$MINOS_WORKSPACE" add file.txt
git -C "$MINOS_WORKSPACE" commit -qm base
base=$(git -C "$MINOS_WORKSPACE" rev-parse HEAD)
git -C "$MINOS_WORKSPACE" update-ref refs/remotes/origin/main "$base"
if [ "${MINOS_FIXTURE_BEHIND_BASE:-}" = 1 ]; then
  printf 'feature\n' >>"$MINOS_WORKSPACE/file.txt"
  git -C "$MINOS_WORKSPACE" commit -qam feature
  feature=$(git -C "$MINOS_WORKSPACE" rev-parse HEAD)
  git -C "$MINOS_WORKSPACE" checkout -q --detach "$base"
  printf 'base advance\n' >"$MINOS_WORKSPACE/base.txt"
  git -C "$MINOS_WORKSPACE" add base.txt
  git -C "$MINOS_WORKSPACE" commit -qm "advance base"
  git -C "$MINOS_WORKSPACE" update-ref refs/remotes/origin/main HEAD
  git -C "$MINOS_WORKSPACE" checkout -q --detach "$feature"
fi
`)
	writeScript(t, filepath.Join(adaptationDir, "get-statuses"), "#!/usr/bin/env sh\nprintf '[]\\n'\n")
	// A fork fixture reports a head repo distinct from the base — an unwritable
	// head. MERGEABLE and the outcome labels are scenario knobs.
	writeScript(t, filepath.Join(adaptationDir, "get-pr-facts"), `#!/usr/bin/env sh
head_repo=minos/subject
if [ "${MINOS_FIXTURE_FORK:-}" = 1 ]; then head_repo=forker/subject; fi
printf 'OCCASION=reconcile\nOWNER=%s\nREPO=%s\nPR=%s\nHEAD_SHA=%s\nBASE_REF=main\n' "$1" "$2" "$3" "$MINOS_HEAD_SHA"
printf 'HEAD_BRANCH=main\nHEAD_REPO=%s\nBASE_REPO=minos/subject\n' "$head_repo"
printf 'MERGEABLE=%s\n' "${MINOS_FIXTURE_MERGEABLE:-true}"
printf 'LABELS=%s\n' "${MINOS_FIXTURE_LABELS:-Converged,Ready}"
`)
	writeScript(t, filepath.Join(adaptationDir, "list-review-comments"), `#!/usr/bin/env sh
printf '%s\n' '[{"id":91,"body":"First finding\n\nMinos: finding=F-FIXED head='"$MINOS_HEAD_SHA"' priority=P1 run=review","path":"main.go","new_position":12,"old_position":0,"review_id":41},{"id":92,"body":"Second finding\n\nMinos: finding=F-STANDS head='"$MINOS_HEAD_SHA"' priority=P1 run=review","path":"other.go","new_position":8,"old_position":0,"review_id":42}]'
`)
	// Finish eligibility reads the service's OWN verdict from the trailing marker
	// of its posted reviews. MINOS_FIXTURE_VERDICT sets that verdict (default
	// converged; `none` means the service posted no verdict for this head).
	// MINOS_FIXTURE_HUMAN_APPROVE=1 adds an unrelated human APPROVE with no
	// service marker — which must never satisfy eligibility.
	listReviews := "#!/usr/bin/env sh\n" +
		"reviews='[]'\n" +
		"if [ \"${MINOS_FIXTURE_HUMAN_APPROVE:-}\" = 1 ]; then\n" +
		"  reviews=$(printf '%s' \"$reviews\" | jq --arg h \"$MINOS_HEAD_SHA\" '. + [{state:\"APPROVED\",commit_id:$h,body:\"Looks good to me\"}]')\n" +
		"fi\n" +
		"verdict=\"${MINOS_FIXTURE_VERDICT:-converged}\"\n" +
		"bar=passed\n" +
		"if [ \"$verdict\" = bar-dissent ]; then bar=failed; fi\n" +
		"if [ \"$verdict\" != none ]; then\n" +
		"  marker=\"Minos: bar=$bar coverage=full head=$MINOS_HEAD_SHA run=review verdict=$verdict\"\n" +
		"  reviews=$(printf '%s' \"$reviews\" | jq --arg h \"$MINOS_HEAD_SHA\" --arg m \"$marker\" '. + [{state:\"COMMENT\",commit_id:$h,body:(\"service review\\n\\n\" + $m)}]')\n" +
		"fi\n" +
		"printf '%s\\n' \"$reviews\"\n"
	writeScript(t, filepath.Join(adaptationDir, "list-reviews"), listReviews)
	writeScript(t, filepath.Join(adaptationDir, "commit-push"), "#!/usr/bin/env sh\nprintf 'commit-push\\n' >>'"+operations+"'\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "commit-push.args")+"'\nif [ \"${MINOS_FIXTURE_BEHIND_BASE:-}\" = 1 ]; then git -C \"$MINOS_WORKSPACE\" commit -qm 'sync base'; git -C \"$MINOS_WORKSPACE\" show -s --format=%P HEAD >'"+filepath.Join(stateDir, "sync-parents")+"'; printf 'landed %s\\n' \"$(git -C \"$MINOS_WORKSPACE\" rev-parse HEAD)\"; elif [ \"${MINOS_FIXTURE_PROTECTED:-}\" = 1 ]; then printf 'unwritable\\n'; else printf '%s\\n' \"${MINOS_FIXTURE_COMMIT_OUTCOME:-landed landedsha}\"; fi\n")
	writeScript(t, filepath.Join(adaptationDir, "merge"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "merge.args")+"'\nprintf '{\"merged\":true}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-comment"), "#!/usr/bin/env sh\nprintf 'post-comment\\n' >>'"+operations+"'\ncat \"$4\" >>'"+filepath.Join(stateDir, "comments")+"'\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "post-review-comment"), "#!/usr/bin/env sh\nprintf 'post-review-comment\\n' >>'"+operations+"'\nprintf '%s:' \"$4\" >>'"+filepath.Join(stateDir, "review-comments")+"'\ncat \"$8\" >>'"+filepath.Join(stateDir, "review-comments")+"'\n[ \"${MINOS_FIXTURE_ACK_FAILURE:-}\" != 1 ] || exit 1\nprintf '{}\\n'\n")
	writeScript(t, filepath.Join(adaptationDir, "set-status"), "#!/usr/bin/env sh\nprintf 'set-status:%s:%s\\n' \"$4\" \"$5\" >>'"+operations+"'\nprintf '%s\\n' \"$@\" >'"+filepath.Join(stateDir, "status.args")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "add-label"), "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-added")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-label"), "#!/usr/bin/env sh\nprintf 'remove-label:%s\\n' \"$4\" >>'"+operations+"'\nprintf '%s\\n' \"$@\" >>'"+filepath.Join(stateDir, "labels-removed")+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "add-reaction"), "#!/usr/bin/env sh\n[ \"$MINOS_FORGE_TOKEN\" = test-token ]\nprintf 'add-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "remove-reaction"), "#!/usr/bin/env sh\n[ \"$MINOS_FORGE_TOKEN\" = test-token ]\nprintf 'remove-reaction:%s\\n' \"$4\" >>'"+operations+"'\n")
	writeScript(t, filepath.Join(adaptationDir, "assign-if-missing"), `#!/usr/bin/env sh
set -eu
[ "$MINOS_FORGE_TOKEN" = test-token ]
assignees='`+filepath.Join(stateDir, "assignees")+`'
if [ -f "$assignees" ] && grep -Fxiq "$4" "$assignees"; then exit 0; fi
printf '%s\n' "$4" >>"$assignees"
printf 'assign-if-missing:%s\n' "$4" >>'`+operations+`'
`)
}
