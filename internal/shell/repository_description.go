package shell

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const repositoryDescriptionKind = "minos-repository-description-v1"

// RepositoryDescription is the description the service keeps of one
// enrolled repository: an account of its layout, its components and how
// they connect, written by the repository-description stage
// (workflows/judgements) from the default branch at Commit. The two are one
// record, written whole, so a description is never read apart from the
// commit it describes.
//
// The record is a cache beside the runs, as the reviewed changes are:
// everything in it can be written again from the repository, and losing it
// costs one regeneration. A run reads it through
// workflows/repository-description.mjs, which holds the same kind and field
// names, and a surface through StateReader.RepositoryDescription.
type RepositoryDescription struct {
	Kind        string `json:"kind"`
	Commit      string `json:"commit"`
	Description string `json:"description"`
	// WrittenAt is when the service kept the record, in RFC 3339 UTC.
	WrittenAt string `json:"written_at"`
}

// repositoryDescriptionPath is where the description of one repository is
// kept under the runs directory. The name is the forge, the owner and the
// repository, each escaped and joined by an escaped slash, so two
// repositories never share a record whatever characters their names hold:
// a hyphen as the join would give forge "a-b" with owner "c" the record of
// forge "a" with owner "b-c".
func repositoryDescriptionPath(cfg ServiceConfig, repo RepoConfig) string {
	parts := []string{url.PathEscape(repo.Forge), url.PathEscape(repo.Owner), url.PathEscape(repo.Repo)}
	return filepath.Join(cfg.Runs.Dir, ".descriptions", strings.Join(parts, "%2F")+".json")
}

// readRepositoryDescription reads the record at path. Nil with no error
// means none is kept. A file that is not a whole record is an error, which
// the caller words: nothing is to be read from it, and the next
// regeneration replaces it.
func readRepositoryDescription(path string) (*RepositoryDescription, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read repository description %s: %w", filepath.Base(path), err)
	}
	var record RepositoryDescription
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("repository description %s is not JSON: %w", filepath.Base(path), err)
	}
	if record.Kind != repositoryDescriptionKind || record.Commit == "" || strings.TrimSpace(record.Description) == "" {
		return nil, fmt.Errorf("repository description %s is not a whole %s record", filepath.Base(path), repositoryDescriptionKind)
	}
	return &record, nil
}

// keepRepositoryDescription replaces the record at path with description as
// written from commit. The record appears whole or not at all, so a reader
// never sees a new description under the earlier one's commit.
func keepRepositoryDescription(path, commit, description string) error {
	if commit == "" || strings.TrimSpace(description) == "" {
		return fmt.Errorf("keep repository description %s: a record needs a commit and a description", filepath.Base(path))
	}
	record, err := json.Marshal(RepositoryDescription{
		Kind:        repositoryDescriptionKind,
		Commit:      commit,
		Description: description,
		WrittenAt:   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	staging := path + ".next"
	if err := os.WriteFile(staging, append(record, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(staging, path)
}

// repositoryDescriptionDue reports whether a repository's description
// needs writing: none is kept, or the one kept was made from another commit
// than tip, the commit the repository's default branch stands at now. It is
// the whole of what starts a regeneration. The question is asked of current
// forge state, as a pull request's eligibility is, so a movement nobody
// delivered an event for is seen all the same, and a branch that moved and
// moved back asks for nothing.
func repositoryDescriptionDue(kept *RepositoryDescription, tip string) bool {
	return kept == nil || kept.Commit != tip
}
