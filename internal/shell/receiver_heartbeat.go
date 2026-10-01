package shell

import (
	"os"
	"path/filepath"
	"time"
)

const receiverHeartbeatKind = "minos-receiver-heartbeat-v1"
const receiverHeartbeatFilename = ".receiver-heartbeat.json"

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
	return filepath.Join(cfg.Runs.Dir, receiverHeartbeatFilename)
}

func publishReceiverHeartbeat(cfg ServiceConfig, activity, forge, bind string) error {
	document := receiverHeartbeatDocument{Kind: receiverHeartbeatKind, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Activity: activity, Forge: forge, Bind: bind}
	return publishProjectionDocument(cfg.Runs.Dir, receiverHeartbeatFilename, document, renameReceiverHeartbeat)
}

func readReceiverHeartbeat(cfg ServiceConfig) *receiverHeartbeatDocument {
	return readProjectionDocument[receiverHeartbeatDocument](receiverHeartbeatPath(cfg), receiverHeartbeatKind)
}
