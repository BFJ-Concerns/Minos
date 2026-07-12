package upload

import (
	"os"
	"path/filepath"
)

func Store(root, name string, data []byte) error {
	cleanName := filepath.Base(name)
	return os.WriteFile(filepath.Join(root, cleanName), data, 0o600)
}
