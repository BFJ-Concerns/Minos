package upload

import (
	"os"
	"path/filepath"
)

func Store(root, name string, data []byte) error {
	return os.WriteFile(filepath.Join(root, name), data, 0o600)
}
