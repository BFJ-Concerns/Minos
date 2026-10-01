package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const receiverHeartbeatKind = "minos-receiver-heartbeat-v1"

// receiverHeartbeatDocument is the receiver's most recent observed activity.
// It grants no lease and is never read by service admission.
type receiverHeartbeatDocument struct {
	Kind       string `json:"kind"`
	RecordedAt string `json:"recorded_at"`
	Activity   string `json:"activity"`
	Forge      string `json:"forge,omitempty"`
	Bind       string `json:"bind,omitempty"`
}

var renameReceiverHeartbeat = os.Rename

func receiverHeartbeatPath(cfg ServiceConfig) string {
	return filepath.Join(cfg.Runs.Dir, ".receiver-heartbeat.json")
}

func publishReceiverHeartbeat(cfg ServiceConfig, activity, forge, bind string) error {
	document := receiverHeartbeatDocument{Kind: receiverHeartbeatKind, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Activity: activity, Forge: forge, Bind: bind}
	content, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Runs.Dir, 0o755); err != nil {
		return fmt.Errorf("create receiver state directory: %w", err)
	}
	temporary, err := os.CreateTemp(cfg.Runs.Dir, ".receiver-heartbeat-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	return writeServiceStateAtomically(temporary, temporaryPath, receiverHeartbeatPath(cfg), content, renameReceiverHeartbeat)
}

func readReceiverHeartbeat(cfg ServiceConfig) *receiverHeartbeatDocument {
	content, err := os.ReadFile(receiverHeartbeatPath(cfg))
	if err != nil {
		return nil
	}
	var document receiverHeartbeatDocument
	if json.Unmarshal(content, &document) != nil || document.Kind != receiverHeartbeatKind {
		return nil
	}
	return &document
}
