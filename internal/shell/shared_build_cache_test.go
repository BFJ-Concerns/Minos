package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoBuildCacheWarmsAcrossWorktrees(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	modules := filepath.Join(root, "modules")
	first := writeGoCacheFixture(t, root, "first")
	second := writeGoCacheFixture(t, root, "second")

	cold := runGoCacheBuild(t, first, cache, modules)
	warm := runGoCacheBuild(t, second, cache, modules)
	coldCompiles := strings.Count(cold, "/compile ")
	warmCompiles := strings.Count(warm, "/compile ")
	if coldCompiles == 0 {
		t.Fatalf("cold build performed no observed compile work:\n%s", cold)
	}
	if warmCompiles*4 >= coldCompiles {
		t.Fatalf("warm build compile count = %d, want less than one quarter of cold count %d\ncold:\n%s\nwarm:\n%s", warmCompiles, coldCompiles, cold, warm)
	}
}

func TestGoBuildCacheSupportsConcurrentWorktrees(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	modules := filepath.Join(root, "modules")
	worktrees := []string{
		writeGoCacheFixture(t, root, "first"),
		writeGoCacheFixture(t, root, "second"),
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	type result struct {
		worktree string
		output   []byte
		err      error
	}
	results := make(chan result, len(worktrees))
	for _, worktree := range worktrees {
		go func() {
			cmd := goCacheBuildCommand(ctx, worktree, cache, modules)
			output, err := cmd.CombinedOutput()
			results <- result{worktree: worktree, output: output, err: err}
		}()
	}
	for range worktrees {
		built := <-results
		if built.err != nil {
			t.Fatalf("concurrent build in %s: %v\n%s", built.worktree, built.err, built.output)
		}
		if _, err := os.Stat(filepath.Join(built.worktree, "fixture")); err != nil {
			t.Fatalf("concurrent build output in %s: %v", built.worktree, err)
		}
	}
}

func writeGoCacheFixture(t *testing.T, root, name string) string {
	t.Helper()
	worktree := filepath.Join(root, name)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":  "module example.test/shared-cache-fixture\n\ngo 1.24\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"cache fixture\") }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return worktree
}

func runGoCacheBuild(t *testing.T, worktree, cache, modules string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	output, err := goCacheBuildCommand(ctx, worktree, cache, modules).CombinedOutput()
	if err != nil {
		t.Fatalf("go build in %s: %v\n%s", worktree, err, output)
	}
	return string(output)
}

func goCacheBuildCommand(ctx context.Context, worktree, cache, modules string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", "build", "-x", "-o", "fixture", ".")
	cmd.Dir = worktree
	cmd.Env = environmentWithOverrides(map[string]string{
		"GOCACHE":     cache,
		"GOMODCACHE":  modules,
		"GOTOOLCHAIN": "local",
		"GOWORK":      "off",
		"CGO_ENABLED": "0",
	})
	return cmd
}
