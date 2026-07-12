package heartbeat

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeatIsExternallyObservableAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat.json")
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := Write(path, now); err != nil {
		t.Fatal(err)
	}
	if err := Check(path, now.Add(30*time.Second), time.Minute); err != nil {
		t.Fatalf("fresh heartbeat rejected: %v", err)
	}
	if err := Check(path, now.Add(2*time.Minute), time.Minute); err == nil {
		t.Fatal("stale heartbeat accepted")
	}
}
