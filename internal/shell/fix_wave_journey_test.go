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
