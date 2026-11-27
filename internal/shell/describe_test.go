package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

const describedTip = "3333333333333333333333333333333333333333"

// describeFixture is a configuration root whose runs directory and run body
// are the test's own, with a describe body beside the run body.
type describeFixture struct {
	root, runsDir, runBody string
	cfg                    ServiceConfig
}

func newDescribeFixture(t *testing.T) describeFixture {
	t.Helper()
	root := t.TempDir()
	runsDir := filepath.Join(root, "runs")
	bodies := filepath.Join(root, "run-body")
	for _, dir := range []string{runsDir, bodies} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runBody := filepath.Join(bodies, "run-body")
	writeScript(t, runBody, "#!/bin/sh\nexit 0\n")
	writeScript(t, filepath.Join(bodies, describeBodyName), "#!/bin/sh\nexit 0\n")
	service := strings.Replace(testServiceConfig, `dir = "/tmp/runs"`, `dir = "`+runsDir+`"`, 1)
	if err := os.WriteFile(filepath.Join(root, "service.toml"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServiceConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	return describeFixture{root: root, runsDir: runsDir, runBody: runBody, cfg: cfg}
}

// enrol writes a repository file for repo with its description on or off
// and returns the repositories as the sweep loads them.
func (f describeFixture) enrol(t *testing.T, repo string, enabled bool) []RepoConfig {
	t.Helper()
	knob := "[description]\nenabled = false\n"
	if enabled {
		knob = "[description]\nenabled = true\n"
	}
	writeRepoConfig(t, f.root, repo+".toml", "forge = \"local\"\nowner = \"owner\"\nrepo = \""+repo+"\"\n"+knob+"[adaptation]\nrun-body = \""+f.runBody+"\"\n")
	repos, err := LoadRepoConfigs(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return repos
}

func repoNamed(repos []RepoConfig, name string) RepoConfig {
	return repos[slices.IndexFunc(repos, func(repo RepoConfig) bool { return repo.Repo == name })]
}

// describeSystemd stands in for systemctl and systemd-run: active names the
// live description units, and each systemd-run's arguments are recorded.
func describeSystemd(t *testing.T, active ...string) *[][]string {
	t.Helper()
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	var started [][]string
	commandCombinedOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "systemctl":
			var out strings.Builder
			for _, unit := range active {
				out.WriteString(unit + ".service loaded active running Minos\n")
			}
			return []byte(out.String()), nil
		case "systemd-run":
			started = append(started, args)
			return nil, nil
		}
		t.Fatalf("unexpected command %s", name)
		return nil, nil
	}
	return &started
}

func describeTip(t *testing.T, commit string) *int {
	t.Helper()
	original := defaultBranchTip
	t.Cleanup(func() { defaultBranchTip = original })
	asked := 0
	defaultBranchTip = func(context.Context, ServiceConfig, RepoConfig) (forge.BranchTip, error) {
		asked++
		return forge.BranchTip{DefaultBranch: "main", Commit: commit}, nil
	}
	return &asked
}

func setenvValue(args []string, key string) (string, bool) {
	for index, arg := range args {
		if arg == "--setenv" && index+1 < len(args) && strings.HasPrefix(args[index+1], key+"=") {
			return strings.TrimPrefix(args[index+1], key+"="), true
		}
	}
	return "", false
}

func TestSweepStartsADescriptionUnitOnlyWhenOneIsDue(t *testing.T) {
	fixture := newDescribeFixture(t)
	fixture.enrol(t, "off", false)
	repos := fixture.enrol(t, "widgets", true)
	widgets := repoNamed(repos, "widgets")
	started := describeSystemd(t)
	asked := describeTip(t, describedTip)

	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if *asked != 1 || len(*started) != 1 {
		t.Fatalf("asked the forge %d times and started %d units; want one of each, for the enabled repository alone", *asked, len(*started))
	}
	args := (*started)[0]
	unit := descriptionUnitName(widgets)
	if !slices.Contains(args, unit) || strings.HasPrefix(unit, "minos-run-") || !slices.Contains(args, "describe") {
		t.Fatalf("systemd-run %q; want unit %s outside the run prefix running minos describe", args, unit)
	}
	if commit, _ := setenvValue(args, "MINOS_DESCRIPTION_COMMIT"); commit != describedTip {
		t.Fatalf("the unit describes %q; want the default branch's tip", commit)
	}
	if record, _ := setenvValue(args, "MINOS_DESCRIPTION_RECORD"); record != repositoryDescriptionPath(fixture.cfg, widgets) {
		t.Fatalf("the unit keeps %q", record)
	}
	runDir, _ := setenvValue(args, "MINOS_RUN_DIR")
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() || filepath.Dir(runDir) != fixture.runsDir || !strings.HasPrefix(filepath.Base(runDir), unit+"-") {
		t.Fatalf("unit directory %q (%v); want one of its own under the runs directory", runDir, err)
	}

	// The description kept is of the tip: nothing is due.
	if err := keepRepositoryDescription(repositoryDescriptionPath(fixture.cfg, widgets), describedTip, "A widget library."); err != nil {
		t.Fatal(err)
	}
	*started = nil
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %d units for a description of the current tip", len(*started))
	}

	// The default branch moved: due again.
	describeTip(t, strings.Repeat("4", 40))
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 {
		t.Fatalf("started %d units after the default branch moved; want one", len(*started))
	}
}

func TestSweepStartsNoDescriptionWhileOneIsLiveOrAFailureIsRecent(t *testing.T) {
	fixture := newDescribeFixture(t)
	repos := fixture.enrol(t, "widgets", true)
	widgets := repoNamed(repos, "widgets")
	describeTip(t, describedTip)

	started := describeSystemd(t, descriptionUnitName(widgets))
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 {
		t.Fatal("a second description unit was started while the first is live")
	}

	started = describeSystemd(t)
	if err := recordDescriptionFailure(fixture.cfg, widgets, describedTip, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 {
		t.Fatal("a description that just failed at this commit was tried again at once")
	}
	if err := recordDescriptionFailure(fixture.cfg, widgets, describedTip, time.Now().Add(-descriptionRetryDelay-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 {
		t.Fatalf("started %d units once the retry delay had passed; want one", len(*started))
	}

	describeTip(t, "")
	*started = nil
	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 {
		t.Fatal("a description was started for a default branch with no commit")
	}
}

func TestSweepRemovesTheDescriptionsOfRepositoriesSwitchedOffOrGone(t *testing.T) {
	fixture := newDescribeFixture(t)
	fixture.enrol(t, "off", false)
	repos := fixture.enrol(t, "widgets", true)
	describeSystemd(t)
	describeTip(t, describedTip)
	gone := RepoConfig{Forge: "local", Owner: "owner", Repo: "gone"}
	for _, repo := range []RepoConfig{repoNamed(repos, "widgets"), repoNamed(repos, "off"), gone} {
		if err := keepRepositoryDescription(repositoryDescriptionPath(fixture.cfg, repo), describedTip, "A description."); err != nil {
			t.Fatal(err)
		}
		if err := recordDescriptionFailure(fixture.cfg, repo, describedTip, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []RepoConfig{repoNamed(repos, "off"), gone} {
		for _, path := range []string{repositoryDescriptionPath(fixture.cfg, repo), descriptionFailurePath(fixture.cfg, repo)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("%s survived its repository's description being off or its enrolment ending (%v)", filepath.Base(path), err)
			}
		}
	}
	if kept, err := readRepositoryDescription(repositoryDescriptionPath(fixture.cfg, repoNamed(repos, "widgets"))); err != nil || kept == nil {
		t.Fatalf("the enabled repository's description was removed: %+v, %v", kept, err)
	}
}

func TestSweepRemovesTheDirectoryOfADeadDescriptionUnitAlone(t *testing.T) {
	fixture := newDescribeFixture(t)
	repos := fixture.enrol(t, "widgets", true)
	widgets := repoNamed(repos, "widgets")
	if err := keepRepositoryDescription(repositoryDescriptionPath(fixture.cfg, widgets), describedTip, "A description."); err != nil {
		t.Fatal(err)
	}
	describeTip(t, describedTip)
	live := filepath.Join(fixture.runsDir, descriptionUnitName(widgets)+"-live")
	dead := filepath.Join(fixture.runsDir, descriptionUnitPrefix+"local-owner-other-dead")
	run := filepath.Join(fixture.runsDir, "minos-run-local-owner-widgets-pr1-abc")
	for _, dir := range []string{live, dead, run} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	describeSystemd(t, descriptionUnitName(widgets))

	if err := reconcileRepositoryDescriptions(t.Context(), fixture.cfg, repos); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("the dead description unit's directory survived (%v)", err)
	}
	for _, dir := range []string{live, run} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was removed: %v", filepath.Base(dir), err)
		}
	}
}

// describeCommandEnvironment sets what a description unit is started with
// and returns its directory.
func describeCommandEnvironment(t *testing.T, fixture describeFixture, repo RepoConfig, body string) string {
	t.Helper()
	runDir, err := os.MkdirTemp(fixture.runsDir, descriptionUnitName(repo)+"-")
	if err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.Join(t.TempDir(), describeBodyName)
	writeScript(t, bodyPath, body)
	for key, value := range map[string]string{
		"MINOS_RUN_DIR": runDir, "MINOS_DESCRIBE_BODY": bodyPath, "MINOS_DESCRIPTION_COMMIT": describedTip,
		"MINOS_FORGE": repo.Forge, "MINOS_OWNER": repo.Owner, "MINOS_REPO_NAME": repo.Repo,
	} {
		t.Setenv(key, value)
	}
	return runDir
}

func TestDescribeCommandKeepsTheStagesAnswerAsTheRepositorysRecord(t *testing.T) {
	fixture := newDescribeFixture(t)
	widgets := repoNamed(fixture.enrol(t, "widgets", true), "widgets")
	if err := recordDescriptionFailure(fixture.cfg, widgets, describedTip, time.Now()); err != nil {
		t.Fatal(err)
	}
	runDir := describeCommandEnvironment(t, fixture, widgets,
		"#!/bin/sh\nprintf '%s\\n' '{\"stage\":\"repository-description\",\"answer\":{\"description\":\"A widget library.\"}}' >\"$MINOS_RUN_DIR/"+descriptionResultName+"\"\n")

	if err := DescribeCommand(t.Context(), []string{"--config", fixture.root}); err != nil {
		t.Fatal(err)
	}
	kept, err := readRepositoryDescription(repositoryDescriptionPath(fixture.cfg, widgets))
	if err != nil || kept == nil || kept.Commit != describedTip || kept.Description != "A widget library." {
		t.Fatalf("kept %+v (%v); want the stage's description under the described commit", kept, err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("the unit's directory survived (%v)", err)
	}
	if _, err := os.Stat(descriptionFailurePath(fixture.cfg, widgets)); !os.IsNotExist(err) {
		t.Fatalf("the earlier failure survived a kept description (%v)", err)
	}
}

func TestDescribeCommandThatGetsNoDescriptionKeepsTheEarlierOneAndRecordsTheFailure(t *testing.T) {
	fixture := newDescribeFixture(t)
	widgets := repoNamed(fixture.enrol(t, "widgets", true), "widgets")
	earlier := strings.Repeat("2", 40)
	if err := keepRepositoryDescription(repositoryDescriptionPath(fixture.cfg, widgets), earlier, "The earlier description."); err != nil {
		t.Fatal(err)
	}
	runDir := describeCommandEnvironment(t, fixture, widgets,
		"#!/bin/sh\necho 'run-judgement: the answer was too long' >\"$MINOS_RUN_DIR/repository-description.log\"\nexit 1\n")

	if err := DescribeCommand(t.Context(), []string{"--config", fixture.root}); err == nil {
		t.Fatal("a failed description reported success")
	}
	kept, err := readRepositoryDescription(repositoryDescriptionPath(fixture.cfg, widgets))
	if err != nil || kept == nil || kept.Commit != earlier {
		t.Fatalf("kept %+v (%v); want the earlier description untouched", kept, err)
	}
	if !descriptionRetryHeld(fixture.cfg, widgets, describedTip, time.Now()) {
		t.Fatal("the failure was not recorded, so the next pass would try this commit again at once")
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("the unit's directory survived (%v)", err)
	}
}

func TestDescribeCommandKeepsNothingForARepositorySwitchedOffMeanwhile(t *testing.T) {
	fixture := newDescribeFixture(t)
	widgets := repoNamed(fixture.enrol(t, "widgets", false), "widgets")
	describeCommandEnvironment(t, fixture, widgets,
		"#!/bin/sh\nprintf '%s\\n' '{\"stage\":\"repository-description\",\"answer\":{\"description\":\"A widget library.\"}}' >\"$MINOS_RUN_DIR/"+descriptionResultName+"\"\n")

	if err := DescribeCommand(t.Context(), []string{"--config", fixture.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repositoryDescriptionPath(fixture.cfg, widgets)); !os.IsNotExist(err) {
		t.Fatalf("a description was kept for a repository with its description off (%v)", err)
	}
}

// The describe body clones the repository at the commit it was given, runs
// the stage from inside that clone on the engine environment run-body.env
// provisions, and leaves the stage's answer where `minos describe` reads
// it. The stage's runner is a stand-in that reports what it was run with.
func TestDescribeBodyRunsTheStageInACloneOfTheDescribedCommit(t *testing.T) {
	repository, commit := createGitRepository(t, "README.md", "A widget library.\n")
	stub := newSetupForge(t, commit, repository, "")

	workflows := t.TempDir()
	writeTestFile(t, filepath.Join(workflows, "judgement.js"), "// stand-in\n")
	writeTestFile(t, filepath.Join(workflows, "repository-description-input.mjs"),
		`import { writeFileSync } from "node:fs";
writeFileSync(process.env.MINOS_RUN_DIR + "/repository-description-args.json", JSON.stringify({ commit: process.env.MINOS_DESCRIPTION_COMMIT }) + "\n");
`)
	seen := filepath.Join(t.TempDir(), "seen")
	writeScript(t, filepath.Join(workflows, "run-judgement"), `#!/bin/sh
{
  printf 'cwd=%s\n' "$(pwd -P)"
  printf 'head=%s\n' "$(git rev-parse HEAD)"
  printf 'home=%s\n' "$HOME"
  printf 'engines=%s\n' "$MINOS_PROVISIONED_ENGINES"
  printf 'args=%s\n' "$3"
} >'`+seen+`'
echo '{"stage":"repository-description","answer":{"description":"A widget library."}}'
`)
	config := t.TempDir()
	seed := filepath.Join(config, "claude-seed")
	writeTestFile(t, filepath.Join(seed, "settings.json"), "{}\n")
	setupWorkspace, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "setup-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(config, "run-body.env"), strings.Join([]string{
		"MINOS_BIN=" + stub.Bin,
		"MINOS_CLAUDE_CONFIG_SEED=" + seed,
		"MINOS_REVIEW_WORKFLOW=" + filepath.Join(workflows, "adjudicated-review"),
		"MINOS_SETUP_WORKSPACE=" + setupWorkspace,
		"MINOS_LEAD_ENGINE=codex",
	}, "\n")+"\n")
	runDir := filepath.Join(t.TempDir(), "runs", "minos-describe-local-owner-repository-x")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}

	body, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", describeBodyName))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(body)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(),
		"MINOS_CONFIG="+config, "MINOS_RUN_DIR="+runDir, "MINOS_FORGE=local",
		"MINOS_OWNER=owner", "MINOS_REPO_NAME=repository",
		"MINOS_DESCRIPTION_COMMIT="+commit,
		"MINOS_DESCRIPTION_RECORD="+filepath.Join(runDir, "absent.json"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("describe-body failed: %v\n%s", err, output)
	}

	description, err := describedRepository(runDir, nil)
	if err != nil || description != "A widget library." {
		t.Fatalf("answer %q (%v)", description, err)
	}
	workspace, err := filepath.EvalSymlinks(filepath.Join(runDir, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cwd=" + workspace, "head=" + commit, "home=" + filepath.Join(runDir, "home"), "engines=claude",
		"args=@" + filepath.Join(runDir, "repository-description-args.json"),
	} {
		if !strings.Contains(string(raw), want+"\n") {
			t.Errorf("the stage ran without %q; it saw:\n%s", want, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, "home", ".claude", "settings.json")); err != nil {
		t.Errorf("the Claude seed was not copied into the unit's home: %v", err)
	}
	if push := gitOutput(t, workspace, "remote", "get-url", "--push", "origin"); push != "no-push://minos-description-clone" {
		t.Errorf("the clone's push URL is %q", push)
	}
}
