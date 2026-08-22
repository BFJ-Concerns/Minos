package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startBuildJobserver(t *testing.T, fifo, slots string) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "build-jobserver"))
	cmd.Env = append(os.Environ(),
		"MINOS_BUILD_JOBSERVER="+fifo,
		"MINOS_BUILD_SLOTS="+slots,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
}

func awaitStockedPool(t *testing.T, fifo string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fifo + ".stocked"); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("jobserver never reported a stocked pool")
}

// drainStockedTokens reads every token out of a stocked pool. The script
// holds the FIFO open read-write, so an empty pool answers EAGAIN rather
// than EOF, which is the read that ends the drain. Raw syscalls, because
// os.File would hand the EAGAIN to the runtime poller and park the read
// until a token arrives.
func drainStockedTokens(t *testing.T, fifo string) int {
	t.Helper()
	fd, err := syscall.Open(fifo, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	total := 0
	buffer := make([]byte, 64)
	for {
		n, err := syscall.Read(fd, buffer)
		if n > 0 {
			total += n
		}
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) {
				return total
			}
			t.Fatalf("draining the token pool: %v", err)
		}
		if n == 0 && err == nil {
			return total
		}
	}
}

func TestBuildJobserverStocksOneTokenUnderItsSlotCount(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pool")
	startBuildJobserver(t, fifo, "4")
	awaitStockedPool(t, fifo)
	if tokens := drainStockedTokens(t, fifo); tokens != 3 {
		t.Fatalf("token pool holds %d tokens, want slots-1 = 3", tokens)
	}
	stocked, err := os.ReadFile(fifo + ".stocked")
	if err != nil || string(stocked) != "4\n" {
		t.Fatalf("stocked marker = %q, %v; want the configured slot count", stocked, err)
	}
}

func TestBuildJobserverResetsAStalePoolWithoutReplacingItsPipe(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pool")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// A client that survived the previous holder keeps the pipe and its
	// stale tokens alive; the restarted service must reuse that inode and
	// restock it to exactly the configured size.
	survivor, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer survivor.Close()
	if _, err := survivor.WriteString("+++++"); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(fifo)
	if err != nil {
		t.Fatal(err)
	}

	startBuildJobserver(t, fifo, "4")
	awaitStockedPool(t, fifo)
	if tokens := drainStockedTokens(t, fifo); tokens != 3 {
		t.Fatalf("restocked pool holds %d tokens, want slots-1 = 3", tokens)
	}
	after, err := os.Stat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("restart replaced the FIFO inode; surviving clients would block on the orphaned pipe")
	}
}
