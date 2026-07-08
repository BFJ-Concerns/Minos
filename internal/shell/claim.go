package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func RunDir(root, forge, owner, repo, pr, sha string, kind RunKind) string {
	return filepath.Join(root, forge+"--"+owner+"--"+repo, "pr"+pr, shortSHA(sha)+"-"+string(kind))
}

func ClaimRunDir(dir string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return false, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func isReapedRunDir(path string) bool {
	return strings.Contains(filepath.Base(path), ".reaped-")
}
