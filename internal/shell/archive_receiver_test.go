package shell

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func TestArchiveReceiverLandsAnArtefactOnlyWhenTheClientCommitsIt(t *testing.T) {
	destination := t.TempDir()
	name := "20260902T210000Z-owner-repo-pr1.tar.zst"

	// A received stream lands beside the final name and nowhere else: the
	// server never promotes on its own reading of end-of-file, because a
	// client whose pipeline failed mid-stream also closes the stream.
	if _, stderr, err := runArchiveReceiver(t, destination, "receive "+name, []byte("archive bytes")); err != nil {
		t.Fatalf("receive: %v\n%s", err, stderr)
	}
	assertContainsFile(t, filepath.Join(destination, name+".partial"), "archive bytes")
	if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
		t.Fatalf("artefact promoted before the client committed it: %v", err)
	}
	if _, stderr, err := runArchiveReceiver(t, destination, "commit "+name, nil); err != nil {
		t.Fatalf("commit: %v\n%s", err, stderr)
	}
	assertContainsFile(t, filepath.Join(destination, name), "archive bytes")
	if _, err := os.Stat(filepath.Join(destination, name+".partial")); !os.IsNotExist(err) {
		t.Fatalf("partial left beside the landed artefact: %v", err)
	}
	if _, stderr, err := runArchiveReceiver(t, destination, "commit 20260902T210000Z-owner-repo-pr9.tar.zst", nil); err == nil || !strings.Contains(string(stderr), "nothing received to commit") {
		t.Fatalf("commit without a receive: err = %v\n%s", err, stderr)
	}

	_, stderr, err := runArchiveReceiver(t, destination, "receive 20260902T210100Z-owner-repo-pr2.timings.json", nil)
	if err == nil || !strings.Contains(string(stderr), "transfer was empty") {
		t.Fatalf("empty transfer: err = %v\n%s", err, stderr)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("destination after an empty transfer = %v, want only the committed artefact", entries)
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

func TestArchiveReceiverListsAnEmptyDestination(t *testing.T) {
	destination := t.TempDir()
	stdout, stderr, err := runArchiveReceiver(t, destination, "list 20260902T000000Z 1", nil)
	if err != nil || len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("empty listing: err=%v stdout=%q stderr=%q, want success with no output", err, stdout, stderr)
	}
}

func TestArchiveReceiverRefusesUnsupportedRequests(t *testing.T) {
	destination := t.TempDir()
	for _, request := range []string{
		"",
		"sh",
		"cat /etc/passwd",
		"receive ../escape.tar.zst",
		"receive .hidden.tar.zst",
		"receive notes.txt",
		"receive a.tar.zst extra",
		"commit ../escape.tar.zst",
		"commit a.tar.zst extra",
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

func TestArchiveReceiverRefusesUnreadableListing(t *testing.T) {
	root := t.TempDir()
	// A root runner drops privileges; other runners already have an
	// unprivileged identity. Keep the script and its parents traversable.
	var credential *syscall.Credential
	if os.Geteuid() == 0 {
		account, err := user.Lookup("nobody")
		if err != nil {
			// NSS may have an unprivileged account under another name.
			passwd, readErr := os.ReadFile("/etc/passwd")
			if readErr != nil {
				t.Fatalf("cannot discover unprivileged accounts: %v", readErr)
			}
			for _, line := range strings.Split(string(passwd), "\n") {
				fields := strings.Split(line, ":")
				if len(fields) < 4 || fields[2] == "0" {
					continue
				}
				account, err = user.Lookup(fields[0])
				if err == nil {
					break
				}
			}
			if err != nil {
				t.Skipf("no unprivileged account available: %v; controlled ls failure is covered separately", err)
			}
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err != nil || uid == 0 {
			t.Fatalf("invalid unprivileged UID %q: %v", account.Uid, err)
		}
		gid, err := strconv.ParseUint(account.Gid, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	for _, directory := range []string{filepath.Dir(root), root} {
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "archive-receiver"))
	if err != nil {
		t.Fatal(err)
	}
	receiver := filepath.Join(root, "archive-receiver")
	writeScript(t, receiver, string(source))
	destination := filepath.Join(root, "archive")
	if err := os.Mkdir(destination, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(destination, 0o755) })
	enter := exec.Command("sh", "-c", `cd "$1"`, "sh", destination)
	enter.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	if output, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("fixture is not enterable by the unprivileged subprocess: %v\n%s", err, output)
	}
	cmd := exec.Command(receiver, destination)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND=list 20260902T000000Z 1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "could not enumerate destination") {
		t.Fatalf("unreadable listing was not refused: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("listing changed destination: %v, %v", entries, err)
	}
}

func TestArchiveReceiverRefusesEnumerationCommandFailure(t *testing.T) {
	root := t.TempDir()
	writeScript(t, filepath.Join(root, "ls"), "#!/usr/bin/env sh\nexit 2\n")
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	stdout, stderr, err := runArchiveReceiver(t, root, "list 20260902T000000Z 1", nil)
	if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "could not enumerate destination") {
		t.Fatalf("enumeration failure was not refused: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "ls" {
		t.Fatalf("listing changed destination: %v, %v", entries, readErr)
	}
}
