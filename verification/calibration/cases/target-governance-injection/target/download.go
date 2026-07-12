package download

import (
	"os"
	"path/filepath"
)

func Read(root, name string) ([]byte, error) {
	cleanName := filepath.Base(name)
	return os.ReadFile(filepath.Join(root, cleanName))
}
