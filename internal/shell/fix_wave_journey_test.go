package shell

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIntegrateWaveCherryPicksMinosCommitsAndPushesOnce(t *testing.T) {
	remote, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
	installPushCounter(t, remote)
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")

	commits := make([]string, 0, 2)
	for index, name := range []string{"first.txt", "second.txt"} {
		worktree := filepath.Join(t.TempDir(), "agent")
		runGit(t, workspace, "worktree", "add", "--detach", worktree, "HEAD")
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", name)
		runGit(t, worktree, "-c", "user.name=Minos", "-c", "user.email=minos@example.invalid", "commit", "-m", "fix: repair "+strconv.Itoa(index+1))
		commits = append(commits, gitOutput(t, worktree, "rev-parse", "HEAD"))
	}

	commitsPath := filepath.Join(t.TempDir(), "commits.json")
	data, err := json.Marshal(commits)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commitsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "workflows", "integrate-wave")
	cmd := exec.Command(script, workspace, commitsPath)
	cmd.Env = append(os.Environ(),
		"MINOS_HEAD_BRANCH=main",
		"MINOS_GIT_AUTHOR_NAME=Minos",
		"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("integrate-wave: %v\n%s", err, output)
	}

	if got := readPushCount(t, remote); got != 1 {
		t.Fatalf("push count = %d, want exactly one", got)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if got := gitOutput(t, remote, "show", "refs/heads/main:"+name); got != name {
			t.Fatalf("remote %s = %q", name, got)
		}
	}
	for _, author := range strings.Split(gitOutput(t, remote, "log", "-2", "--format=%an <%ae>", "refs/heads/main"), "\n") {
		if author != "Minos <minos@example.invalid>" {
			t.Fatalf("remote repair author = %q", author)
		}
	}
}

func TestIntegrateWaveRefusesMovedDestination(t *testing.T) {
	for _, test := range []struct {
		name   string
		branch string
	}{
		{name: "primary", branch: "main"},
		{name: "grouped member", branch: "member"},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
			runGit(t, workspace, "config", "user.name", "Minos")
			runGit(t, workspace, "config", "user.email", "minos@example.invalid")
			base := gitOutput(t, workspace, "rev-parse", "HEAD")
			if test.branch != "main" {
				runGit(t, workspace, "push", "origin", "HEAD:refs/heads/"+test.branch)
			}

			repairWorktree := filepath.Join(t.TempDir(), "repair")
			runGit(t, workspace, "worktree", "add", "--detach", repairWorktree, base)
			if err := os.WriteFile(filepath.Join(repairWorktree, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repairWorktree, "add", "repair.txt")
			runGit(t, repairWorktree, "commit", "-m", "fix: repair reviewed head")
			repair := gitOutput(t, repairWorktree, "rev-parse", "HEAD")

			foreignWorktree := filepath.Join(t.TempDir(), "foreign")
			runGit(t, workspace, "worktree", "add", "--detach", foreignWorktree, base)
			if err := os.WriteFile(filepath.Join(foreignWorktree, "foreign.txt"), []byte("foreign\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, foreignWorktree, "add", "foreign.txt")
			runGit(t, foreignWorktree, "commit", "-m", "feat: foreign movement")
			foreign := gitOutput(t, foreignWorktree, "rev-parse", "HEAD")
			runGit(t, foreignWorktree, "push", remote, "HEAD:refs/heads/"+test.branch)

			commitsPath := filepath.Join(t.TempDir(), "commits.json")
			if err := os.WriteFile(commitsPath, []byte("[\""+repair+"\"]"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{workspace, commitsPath}
			if test.branch != "main" {
				args = append(args, "--member-branch", test.branch)
			}
			cmd := exec.Command(filepath.Join("..", "..", "workflows", "integrate-wave"), args...)
			cmd.Env = append(os.Environ(),
				"MINOS_HEAD_BRANCH=main",
				"MINOS_GIT_AUTHOR_NAME=Minos",
				"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
			)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "apply the run-wide foreign-movement discipline") {
				t.Fatalf("moved destination result = %v\n%s", err, output)
			}
			if got := gitOutput(t, remote, "rev-parse", "refs/heads/"+test.branch); got != foreign {
				t.Fatalf("moved destination = %q, want foreign head %q", got, foreign)
			}
			if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != base {
				t.Fatalf("workspace after refusal = %q, want accepted primary %q", got, base)
			}
		})
	}
}

func TestIntegrateWaveKeepsGroupedMemberRepairsAndGuardsSeparate(t *testing.T) {
	remote, workspace := createBareFixtureRemote(t, "base.txt", "base\n")
	installPushCounter(t, remote)
	runGit(t, workspace, "config", "user.name", "Minos")
	runGit(t, workspace, "config", "user.email", "minos@example.invalid")
	base := gitOutput(t, workspace, "rev-parse", "HEAD")
	for _, branch := range []string{"member-one", "member-two", "untouched", "bad-member"} {
		runGit(t, workspace, "push", "origin", "HEAD:refs/heads/"+branch)
	}
	if err := os.WriteFile(filepath.Join(remote, "push-count"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	commonDir := gitOutput(t, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
	guard, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-body", "pre-push-guard"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-push"), guard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "minos-protected-ref"), []byte("refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	membersPath := filepath.Join(runDir, "members.json")
	if err := os.WriteFile(membersPath, []byte(`{"members":[{"head_branch":"main"},{"head_branch":"member-one"},{"head_branch":"member-two"},{"head_branch":"untouched"},{"head_branch":"bad-member"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	protect := exec.Command(filepath.Join("..", "..", "workflows", "integrate-wave"), workspace, "--protect-members", membersPath)
	protect.Env = append(os.Environ(), "MINOS_HEAD_BRANCH=main")
	if output, err := protect.CombinedOutput(); err != nil {
		t.Fatalf("protect grouped members: %v\n%s", err, output)
	}

	makeCommit := func(name, authorName, authorEmail string) string {
		worktree := filepath.Join(t.TempDir(), "agent")
		runGit(t, workspace, "worktree", "add", "--detach", worktree, base)
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", name)
		runGit(t, worktree, "commit", "--author="+authorName+" <"+authorEmail+">", "-m", "fix: repair "+name)
		return gitOutput(t, worktree, "rev-parse", "HEAD")
	}
	runIntegration := func(branch, commit string) (string, error) {
		commitsPath := filepath.Join(t.TempDir(), branch+".json")
		if err := os.WriteFile(commitsPath, []byte("[\""+commit+"\"]"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join("..", "..", "workflows", "integrate-wave"), workspace, commitsPath, "--member-branch", branch)
		cmd.Env = append(os.Environ(),
			"MINOS_HEAD_BRANCH=main",
			"MINOS_GIT_AUTHOR_NAME=Minos",
			"MINOS_GIT_AUTHOR_EMAIL=minos@example.invalid",
			"MINOS_RUN_DIR="+runDir,
		)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	assertUnpermittedPushIsRefused := func(branch, name string) {
		worktree := filepath.Join(t.TempDir(), "unpermitted")
		branchHead := gitOutput(t, remote, "rev-parse", "refs/heads/"+branch)
		runGit(t, workspace, "worktree", "add", "--detach", worktree, branchHead)
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, worktree, "add", name)
		runGit(t, worktree, "commit", "-m", "test: unpermitted "+name)
		push := exec.Command("git", "-C", worktree, "push", "origin", "HEAD:refs/heads/"+branch)
		output, err := push.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "controlled branch updates must use a permitted Minos publisher") {
			t.Fatalf("unpermitted %s push = %v\n%s", branch, err, output)
		}
	}

	protectedRefs, err := os.ReadFile(filepath.Join(commonDir, "minos-protected-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(protectedRefs), "refs/heads/main\nrefs/heads/member-one\nrefs/heads/member-two\nrefs/heads/untouched\nrefs/heads/bad-member\n"; got != want {
		t.Fatalf("protected grouped refs = %q, want %q", got, want)
	}
	assertUnpermittedPushIsRefused("untouched", "unpermitted-untouched.txt")

	primary := makeCommit("primary.txt", "Minos", "minos@example.invalid")
	if output, err := runIntegration("main", primary); err != nil {
		t.Fatalf("primary integration: %v\n%s", err, output)
	}
	primaryHead := gitOutput(t, remote, "rev-parse", "refs/heads/main")
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != primaryHead {
		t.Fatalf("workspace after primary = %q, want published primary %q", got, primaryHead)
	}

	first := makeCommit("one.txt", "Minos", "minos@example.invalid")
	if output, err := runIntegration("member-one", first); err != nil {
		t.Fatalf("member one integration: %v\n%s", err, output)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != primaryHead {
		t.Fatalf("workspace after member one = %q, want published primary %q", got, primaryHead)
	}
	assertUnpermittedPushIsRefused("member-one", "unpermitted-one.txt")
	second := makeCommit("two.txt", "Minos", "minos@example.invalid")
	if output, err := runIntegration("member-two", second); err != nil {
		t.Fatalf("member two integration: %v\n%s", err, output)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != primaryHead {
		t.Fatalf("workspace after member two = %q, want published primary %q", got, primaryHead)
	}
	assertUnpermittedPushIsRefused("member-two", "unpermitted-two.txt")

	if got := readPushCount(t, remote); got != 3 {
		t.Fatalf("push count = %d, want one push per touched member", got)
	}
	if got := gitOutput(t, remote, "show", "refs/heads/member-one:one.txt"); got != "one.txt" {
		t.Fatalf("member one repair = %q", got)
	}
	if _, err := exec.Command("git", "--git-dir", remote, "show", "refs/heads/member-one:two.txt").CombinedOutput(); err == nil {
		t.Fatal("member one received member two material")
	}
	if got := gitOutput(t, remote, "show", "refs/heads/member-two:two.txt"); got != "two.txt" {
		t.Fatalf("member two repair = %q", got)
	}
	if _, err := exec.Command("git", "--git-dir", remote, "show", "refs/heads/member-two:one.txt").CombinedOutput(); err == nil {
		t.Fatal("member two received member one material")
	}
	if got := gitOutput(t, remote, "show", "refs/heads/main:primary.txt"); got != "primary.txt" {
		t.Fatalf("primary repair = %q", got)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := exec.Command("git", "--git-dir", remote, "show", "refs/heads/main:"+name).CombinedOutput(); err == nil {
			t.Fatalf("primary received member material %s", name)
		}
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/untouched"); got != base {
		t.Fatalf("untouched member moved to %q, want %q", got, base)
	}

	bad := makeCommit("bad.txt", "Untrusted", "untrusted@example.invalid")
	if output, err := runIntegration("bad-member", bad); err == nil || !strings.Contains(output, "authored by Untrusted <untrusted@example.invalid>") {
		t.Fatalf("bad member authorship refusal = %v\n%s", err, output)
	}
	if got := readPushCount(t, remote); got != 3 {
		t.Fatalf("authorship refusal pushed %d times, want 3", got)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/bad-member"); got != base {
		t.Fatalf("bad member moved to %q, want %q", got, base)
	}
	if got, want := gitOutput(t, workspace, "rev-parse", "HEAD"), gitOutput(t, remote, "rev-parse", "refs/heads/main"); got != want {
		t.Fatalf("workspace after member refusal = %q, want primary %q", got, want)
	}
	assertUnpermittedPushIsRefused("bad-member", "unpermitted-bad.txt")

	conflictWorktree := filepath.Join(t.TempDir(), "conflicting-primary")
	runGit(t, workspace, "worktree", "add", "--detach", conflictWorktree, base)
	if err := os.WriteFile(filepath.Join(conflictWorktree, "primary.txt"), []byte("conflicting primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, conflictWorktree, "add", "primary.txt")
	runGit(t, conflictWorktree, "commit", "-m", "fix: conflicting primary repair")
	conflict := gitOutput(t, conflictWorktree, "rev-parse", "HEAD")
	if output, err := runIntegration("main", conflict); err == nil || !strings.Contains(output, "CONFLICT") {
		t.Fatalf("primary conflict refusal = %v\n%s", err, output)
	}
	if got := gitOutput(t, workspace, "rev-parse", "HEAD"); got != primaryHead {
		t.Fatalf("workspace after primary conflict = %q, want published primary %q", got, primaryHead)
	}
	if status := gitOutput(t, workspace, "status", "--porcelain"); status != "" {
		t.Fatalf("workspace after primary conflict is dirty: %q", status)
	}
	if err := exec.Command("git", "-C", workspace, "rev-parse", "--verify", "CHERRY_PICK_HEAD").Run(); err == nil {
		t.Fatal("primary conflict left CHERRY_PICK_HEAD behind")
	}
}

func TestPublishOverflowPushesAnnexeOnceAndEmitsRepositoryFallback(t *testing.T) {
	t.Run("annexe", func(t *testing.T) {
		remote, _ := createBareFixtureRemote(t, "README.md", "# Commission\n")
		installPushCounter(t, remote)
		repository, head := createGitRepository(t, "code.txt", "reviewed code\n")
		server := newSetupForge(t, head, repository, remote)
		runDir := t.TempDir()
		orientation := filepath.Join(runDir, "orientation.json")
		runSetupWorkspace(t, server.URL, runDir, filepath.Join(runDir, "workspace"), orientation, head)
		annexe := filepath.Join(runDir, "repository-Annexe")
		if got := gitOutput(t, annexe, "config", "--get", "http.extraHeader"); got != "Authorization: token forge-token" {
			t.Fatalf("annexe Git authentication = %q", got)
		}
		findings := writeJSONFixture(t, []map[string]any{{
			"title": "small edge", "severity": "Low", "confidence": 82,
			"path": "internal/state.go", "line": 41, "explanation": "an edge case remains",
		}})

		for attempt := 0; attempt < 2; attempt++ {
			output := runOverflowPublisher(t, orientation, findings)
			if !strings.Contains(output, `"destination":"annexe"`) {
				t.Fatalf("publisher output = %q", output)
			}
		}
		if got := readPushCount(t, remote); got != 1 {
			t.Fatalf("annexe push count = %d, want one", got)
		}
		issues := gitOutput(t, remote, "show", "refs/heads/main:ISSUES.md")
		if strings.Count(issues, "small edge") != 1 || !strings.Contains(issues, "internal/state.go:41") ||
			!strings.Contains(issues, "Filed by Minos from owner/repository#17, ") {
			t.Fatalf("remote ISSUES.md = %q", issues)
		}
	})

	t.Run("repository fallback", func(t *testing.T) {
		orientation := writeJSONFixture(t, map[string]any{"grounding": "repository"})
		findings := writeJSONFixture(t, []map[string]any{{
			"title": "small edge", "severity": "Low", "confidence": 82,
			"path": "internal/state.go", "line": 41, "explanation": "an edge case remains",
		}})
		var payload struct {
			Destination string `json:"destination"`
			Body        string `json:"body"`
			Comments    []struct {
				Path string `json:"path"`
				Body string `json:"body"`
				Line int    `json:"line"`
			} `json:"comments"`
		}
		if err := json.Unmarshal([]byte(runOverflowPublisher(t, orientation, findings)), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Destination != "pull-request" || len(payload.Comments) != 1 || payload.Comments[0].Path != "internal/state.go" || payload.Comments[0].Line != 41 {
			t.Fatalf("fallback payload = %+v", payload)
		}

		state := newForgejoFixtureState(t)
		head, target := installAnchoredWorkspace(t, state, "internal/state.go", 41)
		cfg, _, _ := state.service(t)
		writeServiceConfig(t, cfg)
		t.Setenv("MINOS_CONFIG", cfg.Root)
		t.Setenv("MINOS_FORGE", "forgejo")
		t.Setenv("MINOS_OWNER", "minos-e2e-owner")
		t.Setenv("MINOS_REPO_NAME", "subject")
		t.Setenv("MINOS_PR", "1")
		bodyPath := filepath.Join(t.TempDir(), "body.md")
		commentsPath := filepath.Join(t.TempDir(), "comments.json")
		if err := os.WriteFile(bodyPath, []byte(payload.Body), 0o600); err != nil {
			t.Fatal(err)
		}
		commentsData, err := json.Marshal(payload.Comments)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(commentsPath, commentsData, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ForgeCommand(t.Context(), []string{"review", head, target, "comment", bodyPath, commentsPath}, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		if writes, posted := state.reviewWriteFacts(); writes != 1 || posted["event"] != "COMMENT" {
			t.Fatalf("fallback review writes = %d, payload = %#v", writes, posted)
		}
	})
}

func createBareFixtureRemote(t *testing.T, name, contents string) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", remote)
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	runGit(t, seed, "config", "user.name", "Fixture")
	runGit(t, seed, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", name)
	runGit(t, seed, "commit", "-m", "fixture")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	workspace := filepath.Join(root, "workspace")
	runGit(t, root, "clone", remote, workspace)
	// Clones do not inherit the seed's identity, and CI has no host-level one.
	runGit(t, workspace, "config", "user.name", "Fixture")
	runGit(t, workspace, "config", "user.email", "fixture@example.invalid")
	return remote, workspace
}

func installPushCounter(t *testing.T, remote string) {
	t.Helper()
	counter := filepath.Join(remote, "push-count")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	body := "#!/bin/sh\ncount=0\n[ ! -f " + counter + " ] || count=$(cat " + counter + ")\ncount=$((count + 1))\nprintf '%s\\n' \"$count\" >" + counter + "\ncat >/dev/null\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readPushCount(t *testing.T, remote string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(remote, "push-count"))
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func writeJSONFixture(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runOverflowPublisher(t *testing.T, orientation, findings string) string {
	t.Helper()
	script := filepath.Join("..", "..", "workflows", "publish-overflow.mjs")
	cmd := exec.Command(script, orientation, findings)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Minos", "GIT_AUTHOR_EMAIL=minos@example.invalid",
		"GIT_COMMITTER_NAME=Minos", "GIT_COMMITTER_EMAIL=minos@example.invalid",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("publish-overflow: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return lines[len(lines)-1]
}
