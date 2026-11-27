package shell

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// A setting changed away from the deployer's editor is written to the file
// the deployer would have edited: service.toml, or the repository's own file
// under repos/. There is no second store, so the next run reads the change
// the way it reads a hand edit, and the deployer who opens the file finds
// the surrounding comments and layout where they left them. Comments
// inside a replaced or removed multiline value can be lost.
//
// A setting is named by its key path in the file it is written to. A
// per-repository setting has the same key in a repository's file and, for
// its service-wide value, the key ServiceWideKey gives in service.toml:
// {"admission"} and {"repositories", "admission"}, but {"routing", "proposer"}
// in both. The value is the whole setting, because
// a repository overrides a service-wide value whole; nil removes the key, so
// the next level out applies again: the service-wide value for a repository,
// the documented default for the service.
//
// Each candidate must pass the loader's validation for the file being
// edited. A fault in another file can be repaired independently.

// settingsWrites serialises the writes of one process. A hand edit made at
// the same moment is caught by the comparison before the rename.
var settingsWrites sync.Mutex

// WriteServiceSetting sets or removes one key of root's service.toml.
func WriteServiceSetting(root string, key []string, value any) error {
	return writeSetting(filepath.Join(root, "service.toml"), key, value, func(candidate string) error {
		_, err := loadServiceConfigFile(root, candidate)
		return err
	})
}

// WriteRepositorySetting sets or removes one key of an enrolled repository's
// own file. repo identifies the file under this root's repos directory.
func WriteRepositorySetting(root string, repo RepoConfig, key []string, value any) error {
	if filepath.Dir(repo.Path) != filepath.Join(root, "repos") {
		return fmt.Errorf("%s is not a repository file of %s", repo.Path, root)
	}
	return writeSetting(repo.Path, key, value, func(candidate string) error {
		_, _, err := readRepoConfigFile(candidate)
		return err
	})
}

// writeSetting edits one key of the file at path and replaces the file whole,
// by rename, once loads has accepted the candidate. A reader, the sweep
// included, therefore sees the old file or the new one and never a part of
// either. The candidate is written beside the file under a name the loader's
// repos/*.toml glob does not match.
func writeSetting(path string, key []string, value any, loads func(candidate string) error) error {
	settingsWrites.Lock()
	defer settingsWrites.Unlock()
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	edited, err := editTOML(string(original), key, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if edited == string(original) {
		return loads(path)
	}
	candidate, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.candidate")
	if err != nil {
		return fmt.Errorf("the service cannot write beside %s, which a change to it needs: %w", path, err)
	}
	defer func() { _ = os.Remove(candidate.Name()) }()
	_, err = candidate.WriteString(edited)
	if err = errors.Join(err, candidate.Chmod(info.Mode().Perm()), candidate.Sync(), candidate.Close()); err != nil {
		return fmt.Errorf("write the change to %s: %w", path, err)
	}
	if err := loads(candidate.Name()); err != nil {
		// The loader names the file it read; the reader knows it by its own name.
		return errors.New(strings.ReplaceAll(err.Error(), candidate.Name(), path))
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("%s was changed on disk while this change was being made; nothing was written", path)
	}
	return os.Rename(candidate.Name(), path)
}

// ServiceWideKey is the key in service.toml of the service-wide value of the
// per-repository setting at key. Routing stands at the root of service.toml
// as it does in a repository's own file; every other per-repository setting
// stands under [repositories].
func ServiceWideKey(key []string) []string {
	if len(key) > 0 && key[0] == "routing" {
		return slices.Clone(key)
	}
	return append([]string{"repositories"}, key...)
}

// SettingLevel says where the value a repository's run will honour is set.
type SettingLevel string

const (
	// SetForRepository: the repository's own file sets the key.
	SetForRepository SettingLevel = "repository"
	// SetServiceWide: service.toml sets it, at its ServiceWideKey.
	SetServiceWide SettingLevel = "service"
	// SetNowhere: neither does, and the documented default applies.
	SetNowhere SettingLevel = "default"
)

// RepositorySettingLevel reports which level sets a per-repository setting
// for repo, by the same rule resolveRepositoryKnobs applies: a key the
// repository's file defines wins whole, even defined empty.
func RepositorySettingLevel(root string, repo RepoConfig, key []string) (SettingLevel, error) {
	for _, level := range []struct {
		level SettingLevel
		path  string
		key   []string
	}{
		{SetForRepository, repo.Path, key},
		{SetServiceWide, filepath.Join(root, "service.toml"), ServiceWideKey(key)},
	} {
		decoded, err := decodeTOMLFile(level.path)
		if err != nil {
			return "", err
		}
		if _, defined := lookupTOMLPath(decoded, level.key); defined {
			return level.level, nil
		}
	}
	return SetNowhere, nil
}
