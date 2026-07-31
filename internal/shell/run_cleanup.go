package shell

import (
	"io/fs"
	"os"
	"path/filepath"
)

func removeRunDir(runDir string) error {
	_ = filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			_ = os.Chmod(path, info.Mode().Perm()|0o200)
		}
		return nil
	})
	return os.RemoveAll(runDir)
}
