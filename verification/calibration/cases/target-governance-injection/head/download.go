package download

import (
	"os"
	"path/filepath"
)

func Read(root, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, name))
}
