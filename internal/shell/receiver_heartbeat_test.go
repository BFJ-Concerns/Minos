package shell

import (
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestProjectionReceiverListening(t *testing.T) {
	state := newForgejoFixtureState(t)
	cfg := writeSweepFixtureConfig(t, state)
	cfg.Listener.Bind = "127.0.0.1:0"
	writeServiceConfig(t, cfg)
	original := serveReceiver
	t.Cleanup(func() { serveReceiver = original })
	stopped := errors.New("test listener stopped")
	var observed bool
	serveReceiver = func(listener net.Listener, handler http.Handler) error {
		observed = true
		doc := projectionJSON(t, receiverHeartbeatPath(cfg))
		if doc["activity"] != "listening" || doc["bind"] != listener.Addr().String() {
			t.Fatalf("successful listen not recorded before serving: %#v", doc)
		}
		if _, err := time.Parse(time.RFC3339Nano, doc["recorded_at"].(string)); err != nil {
			t.Fatal(err)
		}
		return stopped
	}
	if err := ReceiveCommand(t.Context(), []string{"-config", cfg.Root}); !errors.Is(err, stopped) {
		t.Fatalf("receive = %v", err)
	}
	if !observed {
		t.Fatal("listener never reached serving")
	}
}

func TestProjectionHeartbeatAtomicReplacement(t *testing.T) {
	cfg := receiverTestConfig(t)
	cfg.Runs.Dir = t.TempDir()
	if _, err := sendAuthenticatedHook(t, cfg, "push", `{}`); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(receiverHeartbeatPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	original := renameReceiverHeartbeat
	t.Cleanup(func() { renameReceiverHeartbeat = original })
	renameReceiverHeartbeat = func(temporary, destination string) error {
		before, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(previous) {
			t.Fatal("heartbeat changed before rename")
		}
		doc := projectionJSON(t, temporary)
		if doc["activity"] != "delivery" || doc["forge"] != "local" {
			t.Fatalf("incomplete replacement: %#v", doc)
		}
		return os.ErrPermission
	}
	response, err := sendAuthenticatedHook(t, cfg, "push", `{}`)
	if err != nil || response.Code != http.StatusAccepted {
		t.Fatalf("heartbeat failure changed delivery: %d, %v", response.Code, err)
	}
	after, err := os.ReadFile(receiverHeartbeatPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(previous) {
		t.Fatal("failed rename lost previous heartbeat")
	}
	entries, err := os.ReadDir(cfg.Runs.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed replacement left temporary files: %v", entries)
	}
}
