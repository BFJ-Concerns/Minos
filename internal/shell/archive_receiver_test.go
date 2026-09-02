package shell

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// archive-receiver is the forced command the archive account binds the Minos
// box's key to: the key reaches the archive write and the timing-sidecar
// listing over one destination, and nothing else on the host. These tests
// drive the real script the way sshd does — the client's request arrives in
// SSH_ORIGINAL_COMMAND, the content on stdin — and read the destination.

func runArchiveReceiver(t *testing.T, destination, request string, stdin []byte) ([]byte, []byte, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join("..", "..", "scripts", "archive-receiver"), destination)
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+request)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func TestArchiveReceiverLandsAnArtefactWholeOrNotAtAll(t *testing.T) {
	destination := t.TempDir()

	if _, stderr, err := runArchiveReceiver(t, destination, "receive 20260902T210000Z-owner-repo-pr1.tar.zst", []byte("archive bytes")); err != nil {
		t.Fatalf("receive: %v\n%s", err, stderr)
	}
	assertContainsFile(t, filepath.Join(destination, "20260902T210000Z-owner-repo-pr1.tar.zst"), "archive bytes")
	if _, err := os.Stat(filepath.Join(destination, "20260902T210000Z-owner-repo-pr1.tar.zst.partial")); !os.IsNotExist(err) {
		t.Fatalf("partial left beside the landed artefact: %v", err)
	}

	_, stderr, err := runArchiveReceiver(t, destination, "receive 20260902T210100Z-owner-repo-pr2.timings.json", nil)
	if err == nil || !strings.Contains(string(stderr), "transfer was empty") {
		t.Fatalf("empty transfer: err = %v\n%s", err, stderr)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("destination after an empty transfer = %v, want only the landed artefact", entries)
	}
}

func TestArchiveReceiverListsSidecarsNewestFirstFromTheCutoff(t *testing.T) {
	destination := t.TempDir()
	for name, content := range map[string]string{
		"20260901T000000Z-a.timings.json": `{"run":"a"}`,
		"20260902T000000Z-b.timings.json": `{"run":"b"}`,
		"20260903T000000Z-c.timings.json": `{"run":"c"}`,
		"20260903T000000Z-c.tar.zst":      "not a sidecar",
	} {
		if err := os.WriteFile(filepath.Join(destination, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, err := runArchiveReceiver(t, destination, "list 20260902T000000Z 1", nil)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, stderr)
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) != 1 {
		t.Fatalf("listing = %q, want the limit of one record", stdout)
	}
	name, encoded, found := strings.Cut(lines[0], "\t")
	if !found || name != "20260903T000000Z-c.timings.json" {
		t.Fatalf("listing line = %q, want the newest sidecar at or after the cutoff", lines[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != `{"run":"c"}` {
		t.Fatalf("decoded sidecar = %q, %v", decoded, err)
	}
}

func TestArchiveReceiverRefusesEverythingButTheTwoRequests(t *testing.T) {
	destination := t.TempDir()
	for _, request := range []string{
		"",
		"sh",
		"cat /etc/passwd",
		"receive ../escape.tar.zst",
		"receive .hidden.tar.zst",
		"receive notes.txt",
		"receive a.tar.zst extra",
		"list 20260902T000000Z",
		"list 2026-09-02 1",
		"list 20260902T000000Z many",
		"receive a.tar.zst; touch " + filepath.Join(destination, "injected"),
	} {
		_, stderr, err := runArchiveReceiver(t, destination, request, []byte("content"))
		if err == nil || !strings.Contains(string(stderr), "refused") {
			t.Fatalf("request %q: err = %v, want a refusal\n%s", request, err, stderr)
		}
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused requests wrote into the destination: %v", entries)
	}
}
