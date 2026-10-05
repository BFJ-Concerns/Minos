package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func provisionArchiveDestination(t *testing.T, root, keys string) ([]byte, error) {
	t.Helper()
	publicKey := filepath.Join(root, "public-key")
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 AAAAfixture minos\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command(filepath.Join("..", "..", "scripts", "provision-archive-transport"),
		"destination", publicKey, filepath.Join(root, "archive"), keys, "192.0.2.1", "/usr/local/bin/archive-receiver").CombinedOutput()
}

func TestArchiveProvisioningRefusesUnreadableKeysWithoutMutation(t *testing.T) {
	root := t.TempDir()
	keys := filepath.Join(root, "authorized_keys")
	if err := os.Mkdir(keys, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keys, 0o750) })
	marker := filepath.Join(keys, "keep")
	if err := os.WriteFile(marker, []byte("untouched\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(keys, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(keys)
	if err != nil {
		t.Fatal(err)
	}
	output, runErr := provisionArchiveDestination(t, root, keys)
	after, err := os.Stat(keys)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || !reflect.DeepEqual(before.Sys(), after.Sys()) {
		t.Fatalf("unreadable keys metadata changed: before=%v/%v after=%v/%v; command err=%v\n%s", before.Mode(), before.ModTime(), after.Mode(), after.ModTime(), runErr, output)
	}
	if runErr == nil || !strings.Contains(string(output), "could not read authorized_keys") {
		t.Fatalf("unreadable keys did not produce a read refusal: err=%v\n%s", runErr, output)
	}
	assertContainsFile(t, marker, "untouched\n")
	entries, err := os.ReadDir(keys)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("unreadable keys contents changed: %v, %v", entries, err)
	}
	for _, path := range []string{keys + ".next", filepath.Join(root, "archive")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("read refusal created %s: %v", path, err)
		}
	}
}

func TestArchiveProvisioningRetainsOtherKeysAndReplacesItsOwnKey(t *testing.T) {
	for _, initial := range []string{"", "ssh-ed25519 AAAAfixture old\n", "ssh-ed25519 AAAAother keep\nssh-ed25519 AAAAfixture old\n"} {
		t.Run(initial, func(t *testing.T) {
			root := t.TempDir()
			keys := filepath.Join(root, "authorized_keys")
			if initial != "" {
				if err := os.WriteFile(keys, []byte(initial), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if output, err := provisionArchiveDestination(t, root, keys); err != nil {
				t.Fatalf("provision: %v\n%s", err, output)
			}
			content, err := os.ReadFile(keys)
			if err != nil {
				t.Fatal(err)
			}
			want := "restrict,from=\"192.0.2.1\",command=\"/usr/local/bin/archive-receiver '" + filepath.Join(root, "archive") + "'\" ssh-ed25519 AAAAfixture minos\n"
			if strings.Contains(initial, "AAAAother") {
				want = "ssh-ed25519 AAAAother keep\n" + want
			}
			if string(content) != want {
				t.Fatalf("authorized_keys=%q, want %q", content, want)
			}
			info, err := os.Stat(keys)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("keys permissions: %v, %v", info, err)
			}
		})
	}
}
