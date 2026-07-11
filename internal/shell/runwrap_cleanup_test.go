package shell

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRunWrapCleansDescendantsFromItsProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 &")
	configureRunProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	leaderPID := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-leaderPID, 0); err != nil {
		t.Fatalf("fixture did not leave a process-group descendant: %v", err)
	}
	if err := cleanupRunProcessGroup(leaderPID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-leaderPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run process group still exists after cleanup")
}
