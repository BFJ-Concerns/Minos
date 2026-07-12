package atomicreplace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicallyReplacesRecordAndPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	if err := Write(path, []byte("first\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second\n" {
		t.Fatalf("got %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode is %o", info.Mode().Perm())
	}
}
