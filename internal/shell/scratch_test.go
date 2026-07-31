package shell

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweepRunResidueRemovesDeadReadOnlyTreeAndKeepsLiveUnit(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatalf("command = %s, want systemctl", name)
		}
		return []byte("minos-run-owner-live-pr2.service loaded active running Live run\n"), nil
	}

	cfg := scratchTestConfig(t)
	dead := filepath.Join(cfg.Runs.Dir, "minos-run-owner-dead-pr1-dead")
	live := filepath.Join(cfg.Runs.Dir, "minos-run-owner-live-pr2-live")
	t.Cleanup(func() { _ = removeRunDir(live) })
	for _, runDir := range []string{dead, live} {
		if err := os.MkdirAll(filepath.Join(runDir, "cache", "readonly"), 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(runDir, "cache", "readonly", "artifact")
		if err := os.WriteFile(file, []byte("cache\n"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(file), 0o500); err != nil {
			t.Fatal(err)
		}
	}

	_ = sweepRunResidue(t.Context(), cfg, nil)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead run still exists: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live run was removed: %v", err)
	}
}

func TestSweepRunResidueProtectsOnlyDirectoryNamedByValidHandoff(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	facts := Facts{Owner: "owner", Repo: "repository", PR: "7", HeadSHA: "head"}
	preserved := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	residue := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-different")
	for _, runDir := range []string{preserved, residue} {
		if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestHandoff(t, cfg, facts, preserved, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	_ = sweepRunResidue(t.Context(), cfg, map[string]Facts{UnitName(facts): facts})
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("valid handoff directory was removed: %v", err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("different residue directory was protected: %v", err)
	}
}

func TestSweepRunResidueDoesNotProtectInvalidHandoff(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	facts := Facts{Owner: "owner", Repo: "repository", PR: "8", HeadSHA: "current"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-rejected")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, runDir, "stale-head", 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))

	_ = sweepRunResidue(t.Context(), cfg, map[string]Facts{UnitName(facts): facts})
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("invalid handoff protected residue: %v", err)
	}
}

func TestSweepRunResiduePreservesEvidenceWhenDurableSalvageFails(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	cfg := scratchTestConfig(t)
	cfg.Runs.FailuresRepo = filepath.Join(t.TempDir(), "missing-Minos-Annexe")
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr10-dead")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "failure.log"), []byte("only evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sweepRunResidue(t.Context(), cfg, nil); err == nil {
		t.Fatal("sweep succeeded despite unavailable durable salvage target")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run evidence was deleted after salvage failed: %v", err)
	}
}

func TestAppendFailureDigestWritesFailuresMarkdown(t *testing.T) {
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr9-dead")
	files := map[string]string{
		"failure.log": "stage=review cause=worker died\n",
		"home/.claude/projects/repository/session.jsonl": "{\"type\":\"assistant\",\"text\":\"last turn\"}\n",
		"ensemble-records/review/run.json":               "{\"status\":\"incomplete\"}\n",
	}
	for name, contents := range files {
		path := filepath.Join(runDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if appended, err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo); err != nil {
		t.Fatal(err)
	} else if !appended {
		t.Fatal("failure evidence was not appended")
	}
	digest, err := os.ReadFile(filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Failures", "## ", filepath.Base(runDir), "### failure.log", "worker died",
		"### home/.claude/projects/repository/session.jsonl", "last turn",
		"### ensemble-records/review/run.json", "incomplete",
	} {
		if !strings.Contains(string(digest), want) {
			t.Fatalf("FAILURES.md omits %q:\n%s", want, digest)
		}
	}
}

func TestArchiveRunRejectsFailedPipelineWithoutPublishingFinalName(t *testing.T) {
	archive, err := filepath.Abs(filepath.Join("..", "..", "scripts", "run-body", "archive-run"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		zstdScript string
		sshScript  string
	}{
		{
			name:       "zstd fails",
			zstdScript: "#!/usr/bin/env sh\ncat >/dev/null\nexit 3\n",
			sshScript: `#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
exec sh -c "$last"
`,
		},
		{
			name:       "transfer fails",
			zstdScript: "#!/usr/bin/env sh\ncat\n",
			sshScript:  "#!/usr/bin/env sh\ncat >/dev/null\nexit 7\n",
		},
		{
			name:       "compressor produces empty output",
			zstdScript: "#!/usr/bin/env sh\ncat >/dev/null\nexit 0\n",
			sshScript: `#!/usr/bin/env sh
last=""
for argument in "$@"; do last="$argument"; done
exec sh -c "$last"
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			runDir := filepath.Join(root, "run")
			destination := filepath.Join(root, "destination")
			for _, path := range []string{bin, runDir, destination} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeScript(t, filepath.Join(bin, "zstd"), test.zstdScript)
			writeScript(t, filepath.Join(bin, "ssh"), test.sshScript)
			identity := filepath.Join(root, "identity")
			knownHosts := filepath.Join(root, "known_hosts")
			for _, path := range []string{identity, knownHosts} {
				if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(runDir, "run-report.md"), []byte("evidence\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(root, "archive.env")
			contents := strings.Join([]string{
				`MINOS_ARCHIVE_HOST="fixture"`,
				`MINOS_ARCHIVE_DESTINATION="` + destination + `"`,
				`MINOS_ARCHIVE_IDENTITY_FILE="` + identity + `"`,
				`MINOS_ARCHIVE_KNOWN_HOSTS="` + knownHosts + `"`,
			}, "\n") + "\n"
			if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(archive, runDir, "fixture")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MINOS_ARCHIVE_CONFIG="+config)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("archive succeeded despite pipeline failure\n%s", output)
			}
			if got := strings.Count(strings.TrimSpace(string(output)), "\n") + 1; got != 1 {
				t.Fatalf("archive emitted %d diagnostic lines, want one\n%s", got, output)
			}
			matches, err := filepath.Glob(filepath.Join(destination, "*.tar.zst"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("failed archive was promoted: %v", matches)
			}
		})
	}
}

func TestFailureDigestUsesExplicitBoundedEvidenceSources(t *testing.T) {
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr11-dead")
	unrelated := map[string]string{
		"workspace/docs/report.md":                    "reviewed repository report",
		"repository-Annexe/maestro/units/a/report.md": "reviewed annexe report",
		"cache/report.md":                             "cache report",
		"state/report.md":                             "state report",
	}
	for name, contents := range unrelated {
		writeTestFile(t, filepath.Join(runDir, name), strings.Repeat(contents, 100))
	}
	writeTestFile(t, filepath.Join(runDir, "run-report.md"), "run-local report\n")
	for index := 0; index < 40; index++ {
		writeTestFile(t,
			filepath.Join(runDir, "home", ".claude", "projects", "repo", fmt.Sprintf("session-%02d.jsonl", index)),
			strings.Repeat(fmt.Sprintf("transcript-%02d ", index), 6000),
		)
	}
	writeTestFile(t, filepath.Join(runDir, "ensemble-records", "review", "run.json"), "ENSEMBLE-VERDICT: incomplete\n")
	failuresPath := filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md")
	before, err := os.Stat(failuresPath)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo)
	if err != nil || !appended {
		t.Fatalf("appendFailureDigest() = %t, %v", appended, err)
	}
	after, err := os.ReadFile(failuresPath)
	if err != nil {
		t.Fatal(err)
	}
	if delta := len(after) - int(before.Size()); delta > salvageDigestBytes {
		t.Fatalf("digest bytes = %d, want at most %d", delta, salvageDigestBytes)
	}
	text := string(after)
	for _, forbidden := range []string{"reviewed repository report", "reviewed annexe report", "cache report", "state report"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("digest contains unrelated evidence %q", forbidden)
		}
	}
	for _, want := range []string{"run-local report", "transcript-00", "ENSEMBLE-VERDICT", "Digest truncated at 256 KiB"} {
		if !strings.Contains(text, want) {
			t.Fatalf("digest omits %q", want)
		}
	}
}

func TestPublishFailureDigestTimesOutAndRetainsLocalCommit(t *testing.T) {
	originalTimeout := failurePublishTimeout
	t.Cleanup(func() { failurePublishTimeout = originalTimeout })
	failurePublishTimeout = 50 * time.Millisecond

	root := t.TempDir()
	checkout := filepath.Join(root, "Minos-Annexe")
	runScratchGit(t, root, "init", "-b", "main", checkout)
	configureTestGit(t, checkout)
	writeTestFile(t, filepath.Join(checkout, "FAILURES.md"), "# Failures\n")
	runScratchGit(t, checkout, "add", "FAILURES.md")
	runScratchGit(t, checkout, "commit", "-m", "seed failures")
	file, err := os.OpenFile(filepath.Join(checkout, "FAILURES.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n## salvaged run\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(bin, "git"), fmt.Sprintf(`#!/usr/bin/env sh
if [ "$3" = "fetch" ]; then
  sleep 2
  exit 1
fi
exec %q "$@"
`, realGit))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	credential := filepath.Join(root, "forgejo.token")
	writeTestFile(t, credential, "fixture-token\n")
	var cfg ServiceConfig
	cfg.Runs.FailuresRepo = checkout
	cfg.Runs.FailuresCredentialFile = credential

	started := time.Now()
	if err := publishFailureDigest(t.Context(), cfg); err == nil {
		t.Fatal("publish succeeded despite hung fetch")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("publish stopped after %s, want within one second", elapsed)
	}
	message := strings.TrimSpace(runScratchGitOutput(t, checkout, "log", "-1", "--pretty=%s"))
	if message != "docs: record salvaged Minos run failure" {
		t.Fatalf("local salvage commit = %q, want retained commit", message)
	}
}

func TestFailureDigestNotesUnreadableEvidenceAndContinues(t *testing.T) {
	original := walkEvidenceRoot
	t.Cleanup(func() { walkEvidenceRoot = original })
	walkEvidenceRoot = func(root string, fn fs.WalkDirFunc) error {
		if strings.Contains(root, filepath.Join(".claude", "projects")) {
			return fn(filepath.Join(root, "unreadable"), nil, fs.ErrPermission)
		}
		return filepath.WalkDir(root, fn)
	}
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr12-dead")
	if err := os.MkdirAll(filepath.Join(runDir, "home", ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(runDir, "ensemble-records", "run.json"), "ensemble evidence\n")
	appended, err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo)
	if err != nil || !appended {
		t.Fatalf("appendFailureDigest() = %t, %v", appended, err)
	}
	digest, err := os.ReadFile(filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Evidence skipped", "permission denied", "ensemble evidence"} {
		if !strings.Contains(string(digest), want) {
			t.Fatalf("digest omits %q:\n%s", want, digest)
		}
	}
}

func TestSweepRunResidueRechecksLivenessBeforeDeletion(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	calls := 0
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []byte("minos-run-owner-repository-pr13.service loaded active running Run\n"), nil
	}
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr13-new")
	writeTestFile(t, filepath.Join(runDir, "run-report.md"), "evidence\n")
	_ = sweepRunResidue(t.Context(), cfg, nil)
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run that became live was removed: %v", err)
	}
}

func TestSweepRunResiduePreservesOwnedMidSpawnDirectory(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr14-starting")
	writeTestFile(t, filepath.Join(runDir, "run-report.md"), "evidence\n")
	writeTestFile(t, filepath.Join(runDir, runOwnerMarker), "")
	_ = sweepRunResidue(t.Context(), cfg, nil)
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("owned mid-spawn directory was removed: %v", err)
	}
}

func TestSweepRunResiduePreservesStructurallyValidHandoffWithoutCurrentFacts(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cfg := scratchTestConfig(t)
	facts := Facts{Owner: "owner", Repo: "repository", PR: "15", HeadSHA: "head"}
	runDir := filepath.Join(cfg.Runs.Dir, UnitName(facts)+"-preserved")
	if err := os.MkdirAll(filepath.Join(runDir, "workspace", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestHandoff(t, cfg, facts, runDir, facts.HeadSHA, 0, []byte(`{"round":0,"confirmedUnfixed":[]}`))
	if err := sweepRunResidue(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("structurally valid handoff was not fail-safe without current facts: %v", err)
	}
}

func TestSweepRunResidueRequiresProvisionedArchiveCommand(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cfg := scratchTestConfig(t)
	cfg.Runs.ArchiveCommand = ""
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr16-dead")
	writeTestFile(t, filepath.Join(runDir, "run-report.md"), "evidence\n")
	if err := sweepRunResidue(t.Context(), cfg, nil); err == nil {
		t.Fatal("sweep accepted an unprovisioned archive command")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run was removed without provisioned archiving: %v", err)
	}
}

func TestSweepRunResidueDoesNotAppendEmptyDigest(t *testing.T) {
	original := commandCombinedOutput
	t.Cleanup(func() { commandCombinedOutput = original })
	commandCombinedOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cfg := scratchTestConfig(t)
	runDir := filepath.Join(cfg.Runs.Dir, "minos-run-owner-repository-pr17-empty")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	failuresPath := filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md")
	before, err := os.ReadFile(failuresPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = sweepRunResidue(t.Context(), cfg, nil)
	after, err := os.ReadFile(failuresPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("empty digest was appended:\n%s", after)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("empty husk was not removed: %v", err)
	}
}

func TestArchiveRunEvidenceHonoursCallerDeadline(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "archive")
	writeScript(t, helper, "#!/usr/bin/env sh\nsleep 5\n")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := archiveRunEvidence(ctx, helper, root, "fixture"); err == nil {
		t.Fatal("archive helper ignored caller deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("archive helper stopped after %s, want within one second", elapsed)
	}
}

func TestPublishFailureDigestRebasesOntoUpdatedOrigin(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	checkout := filepath.Join(root, "Minos-Annexe")
	operator := filepath.Join(root, "operator")
	verification := filepath.Join(root, "verification")
	runScratchGit(t, root, "init", "--bare", remote)
	runScratchGit(t, root, "init", "-b", "main", seed)
	configureTestGit(t, seed)
	writeTestFile(t, filepath.Join(seed, "FAILURES.md"), "# Failures\n")
	runScratchGit(t, seed, "add", "FAILURES.md")
	runScratchGit(t, seed, "commit", "-m", "seed failures")
	runScratchGit(t, seed, "remote", "add", "origin", remote)
	runScratchGit(t, seed, "push", "-u", "origin", "main")
	runScratchGit(t, root, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runScratchGit(t, root, "clone", remote, checkout)
	configureTestGit(t, checkout)
	runScratchGit(t, root, "clone", remote, operator)
	configureTestGit(t, operator)
	writeTestFile(t, filepath.Join(operator, "operator.md"), "operator change\n")
	runScratchGit(t, operator, "add", "operator.md")
	runScratchGit(t, operator, "commit", "-m", "operator change")
	runScratchGit(t, operator, "push", "origin", "main")

	file, err := os.OpenFile(filepath.Join(checkout, "FAILURES.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n## salvaged run\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "forgejo.token")
	writeTestFile(t, credential, "fixture-token\n")
	var cfg ServiceConfig
	cfg.Runs.FailuresRepo = checkout
	cfg.Runs.FailuresCredentialFile = credential
	if err := publishFailureDigest(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	runScratchGit(t, root, "clone", remote, verification)
	for path, want := range map[string]string{
		"FAILURES.md": "salvaged run",
		"operator.md": "operator change",
	} {
		contents, err := os.ReadFile(filepath.Join(verification, path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), want) {
			t.Fatalf("%s omits %q: %s", path, want, contents)
		}
	}
}

func runScratchGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	_ = runScratchGitOutput(t, directory, arguments...)
}

func runScratchGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	cmd := exec.Command("git", arguments...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func configureTestGit(t *testing.T, directory string) {
	t.Helper()
	runScratchGit(t, directory, "config", "user.name", "Minos Test")
	runScratchGit(t, directory, "config", "user.email", "minos-test@example.invalid")
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func scratchTestConfig(t *testing.T) ServiceConfig {
	t.Helper()
	root := t.TempDir()
	var cfg ServiceConfig
	cfg.Runs.Dir = filepath.Join(root, "runs")
	cfg.Runs.FailuresRepo = filepath.Join(root, "Minos-Annexe")
	cfg.Runs.ArchiveCommand = "/bin/true"
	if err := os.MkdirAll(cfg.Runs.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Runs.FailuresRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Runs.FailuresRepo, "FAILURES.md"), []byte("# Failures\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}
