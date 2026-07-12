// Package atomicreplace durably replaces small filesystem records without
// exposing partially written content to concurrent readers.
package atomicreplace

import (
	"os"
	"path/filepath"
)

// Write publishes data by syncing a same-directory temporary file and then
// renaming it over path. Same-directory placement keeps rename atomic on the
// target filesystem; syncing the directory makes the new name durable.
func Write(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".atomic-replace-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}
