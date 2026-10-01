package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// publishProjectionDocument replaces operator residue only after the complete
// document is written and closed. Failed publication preserves the prior record.
func publishProjectionDocument(directory, filename string, document any, rename func(string, string) error) error {
	content, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode projection document: %w", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create projection directory: %w", err)
	}
	path := filepath.Join(directory, filename)
	prefix := strings.TrimSuffix(filename, ".json")
	temporary, err := os.CreateTemp(directory, prefix+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create projection temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	return writeServiceStateAtomically(temporary, temporaryPath, path, content, rename)
}

// readProjectionDocument omits missing, unreadable, malformed and unrelated
// records. The reader contributes no timestamp or decision of its own.
func readProjectionDocument[T any](path, kind string) *T {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(content, &envelope) != nil || envelope.Kind != kind {
		return nil
	}
	var document T
	if json.Unmarshal(content, &document) != nil {
		return nil
	}
	return &document
}
