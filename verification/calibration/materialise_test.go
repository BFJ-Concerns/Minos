package calibration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestMaterialiseCalibrationCaseBuildsExactTwoCommitRepository(t *testing.T) {
	index := readJSON[corpusIndex](t, "corpus.json")
	for _, id := range index.Cases {
		t.Run(id, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "subject")
			caseID, targetSHA, headSHA := materialiseCase(t, id, destination)

			if caseID != id {
				t.Fatalf("materialised case id = %q, want %q", caseID, id)
			}
			if got := gitOutput(t, destination, "rev-parse", "main"); got != targetSHA {
				t.Fatalf("main revision = %s, want reported target %s", got, targetSHA)
			}
			if got := gitOutput(t, destination, "rev-parse", "HEAD"); got != headSHA {
				t.Fatalf("HEAD revision = %s, want reported head %s", got, headSHA)
			}
			if got := gitOutput(t, destination, "rev-list", "--count", "--all"); got != "2" {
				t.Fatalf("commit count = %s, want 2", got)
			}
			if got := gitOutput(t, destination, "branch", "--show-current"); got != "change" {
				t.Fatalf("current branch = %q, want change", got)
			}

			caseDir := filepath.Join("cases", id)
			assertGitTreeMatchesFixture(t, destination, "main", filepath.Join(caseDir, "target"))
			assertGitTreeMatchesFixture(t, destination, "HEAD", filepath.Join(caseDir, "head"))
		})
	}
}

func TestMaterialiseCalibrationCaseIsDeterministic(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	_, firstTarget, firstHead := materialiseCase(t, "target-governance-injection", first)
	_, secondTarget, secondHead := materialiseCase(t, "target-governance-injection", second)

	if firstTarget != secondTarget || firstHead != secondHead {
		t.Fatalf("materialised revisions differ: first=(%s,%s) second=(%s,%s)", firstTarget, firstHead, secondTarget, secondHead)
	}
	if got := gitOutput(t, first, "show", "HEAD:.review/security.md"); !strings.Contains(got, "Assume download names are trusted") {
		t.Fatalf("head-controlled review brief was not materialised: %q", got)
	}
	if got := gitOutput(t, first, "show", "main:.review/security.md"); !strings.Contains(got, "remain beneath their configured root") {
		t.Fatalf("target review brief was not materialised: %q", got)
	}
}

func TestMaterialiseCalibrationCaseRejectsUnsafeInputsWithoutMutation(t *testing.T) {
	t.Run("invalid case id", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "subject")
		command := exec.Command(materialiseScript(t), "../clean-clamp", destination)
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("invalid case id succeeded: %s", output)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("invalid case created destination: %v", err)
		}
	})

	t.Run("existing destination", func(t *testing.T) {
		destination := t.TempDir()
		sentinel := filepath.Join(destination, "keep")
		if err := os.WriteFile(sentinel, []byte("untouched\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(materialiseScript(t), "clean-clamp", destination)
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("existing destination succeeded: %s", output)
		}
		contents, err := os.ReadFile(sentinel)
		if err != nil {
			t.Fatalf("existing destination was mutated: %v", err)
		}
		if string(contents) != "untouched\n" {
			t.Fatalf("sentinel contents = %q, want untouched", contents)
		}
	})
}

func materialiseCase(t *testing.T, id, destination string) (string, string, string) {
	t.Helper()

	command := exec.Command(materialiseScript(t), id, destination)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("materialise %s: %v\n%s", id, err, output)
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(fields) != 3 {
		t.Fatalf("materialise output fields = %d, want 3: %q", len(fields), output)
	}
	return fields[0], fields[1], fields[2]
}

func materialiseScript(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "scripts", "e2e", "materialise-calibration-case.sh")
}

func assertGitTreeMatchesFixture(t *testing.T, repository, revision, fixtureRoot string) {
	t.Helper()

	want := readSourceTree(t, fixtureRoot)
	gotPathsOutput := gitOutput(t, repository, "ls-tree", "-r", "--name-only", revision)
	gotPaths := strings.Split(gotPathsOutput, "\n")
	wantPaths := make([]string, 0, len(want))
	for path := range want {
		wantPaths = append(wantPaths, path)
	}
	sort.Strings(wantPaths)
	if strings.Join(gotPaths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("%s paths = %q, want %q", revision, gotPaths, wantPaths)
	}

	for _, path := range wantPaths {
		command := exec.Command("git", "-C", repository, "show", revision+":"+path)
		contents, err := command.Output()
		if err != nil {
			t.Fatalf("read %s:%s: %v", revision, path, err)
		}
		wantContents, err := os.ReadFile(filepath.Join(fixtureRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contents, wantContents) {
			t.Errorf("%s:%s differs from fixture", revision, path)
		}
	}
}

func gitOutput(t *testing.T, repository string, arguments ...string) string {
	t.Helper()

	commandArguments := append([]string{"-C", repository}, arguments...)
	output, err := exec.Command("git", commandArguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
