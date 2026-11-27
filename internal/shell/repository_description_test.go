package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryDescriptionIsKeptWholeWithItsCommit(t *testing.T) {
	var cfg ServiceConfig
	cfg.Runs.Dir = t.TempDir()
	path := repositoryDescriptionPath(cfg, RepoConfig{Forge: "forgejo", Owner: "owner", Repo: "repo"})

	kept, err := readRepositoryDescription(path)
	if err != nil || kept != nil {
		t.Fatalf("before any description is kept: got %+v, %v; want none", kept, err)
	}
	if !repositoryDescriptionDue(kept, "1111") {
		t.Fatal("a repository with no description kept needs one written")
	}

	if err := keepRepositoryDescription(path, "1111", "A parsing library."); err != nil {
		t.Fatal(err)
	}
	if err := keepRepositoryDescription(path, "2222", "A parsing library and its command."); err != nil {
		t.Fatal(err)
	}
	kept, err = readRepositoryDescription(path)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Commit != "2222" || kept.Description != "A parsing library and its command." || kept.WrittenAt == "" {
		t.Fatalf("kept %+v; want the later description under its own commit", kept)
	}
	if repositoryDescriptionDue(kept, "2222") {
		t.Fatal("a description made from the default branch's commit needs no regeneration")
	}
	if !repositoryDescriptionDue(kept, "3333") {
		t.Fatal("a default branch that has moved needs its description written again")
	}
}

func TestRepositoryDescriptionsOfDifferentRepositoriesNeverShareARecord(t *testing.T) {
	var cfg ServiceConfig
	cfg.Runs.Dir = t.TempDir()
	paths := map[string]string{}
	for _, repo := range []RepoConfig{
		{Forge: "forgejo", Owner: "a-b", Repo: "c"},
		{Forge: "forgejo", Owner: "a", Repo: "b-c"},
		{Forge: "forgejo-a", Owner: "b", Repo: "c"},
		{Forge: "github", Owner: "a-b", Repo: "c"},
	} {
		path := repositoryDescriptionPath(cfg, repo)
		if filepath.Dir(path) != filepath.Join(cfg.Runs.Dir, ".descriptions") {
			t.Fatalf("%s is kept outside the descriptions directory", path)
		}
		if other, taken := paths[path]; taken {
			t.Fatalf("%s/%s/%s shares a record with %s", repo.Forge, repo.Owner, repo.Repo, other)
		}
		paths[path] = repo.Forge + "/" + repo.Owner + "/" + repo.Repo
	}
}

func TestRepositoryDescriptionThatIsNotAWholeRecordIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":       "A parsing library.\n",
		"another kind":   `{"kind":"minos-run-handoff-v1","commit":"1111","description":"A parsing library."}`,
		"no commit":      `{"kind":"minos-repository-description-v1","description":"A parsing library."}`,
		"no description": `{"kind":"minos-repository-description-v1","commit":"1111","description":" "}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "record.json")
			writeTestFile(t, path, content)
			if kept, err := readRepositoryDescription(path); err == nil {
				t.Fatalf("read %+v; want the record refused", kept)
			}
		})
	}
	if err := keepRepositoryDescription(filepath.Join(t.TempDir(), "record.json"), "1111", " \n"); err == nil {
		t.Fatal("a blank description was kept")
	}
}

// The service writes the record and a run's input builders read it, in two
// languages that each name its kind and fields.
func TestRunReadsTheRepositoryDescriptionTheServiceKeeps(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	commit := strings.Repeat("1", 40)
	path := filepath.Join(t.TempDir(), "record.json")
	if err := keepRepositoryDescription(path, commit, "A parsing library."); err != nil {
		t.Fatal(err)
	}
	reader, err := filepath.Abs(filepath.Join("..", "..", "workflows", "repository-description.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	script := `const { descriptionFromRecord } = await import(process.argv[1]);
process.stdout.write(JSON.stringify(descriptionFromRecord(process.argv[2])));`
	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "--eval", script, reader, path)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"commit":"` + commit + `","description":"A parsing library."}`; string(out) != want {
		t.Fatalf("the run read %s; want %s", out, want)
	}
}
