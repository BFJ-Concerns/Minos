package shell

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestClaimRunDirIsAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs", "forge--owner--repo", "pr1", "abcdef-review")
	var wg sync.WaitGroup
	results := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := ClaimRunDir(dir)
			if err != nil {
				t.Errorf("claim failed: %v", err)
			}
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}
