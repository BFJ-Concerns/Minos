package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const salvageTailBytes = 64 * 1024

func sweepRunResidue(ctx context.Context, cfg ServiceConfig, factsByUnit map[string]Facts) error {
	active, err := activeRunUnits(ctx)
	if err != nil {
		return err
	}
	preserved := validHandoffRunDirs(cfg, factsByUnit)
	entries, err := os.ReadDir(cfg.Runs.Dir)
	if err != nil {
		return fmt.Errorf("read runs directory: %w", err)
	}
	var sweepErrors []error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "minos-run-") {
			continue
		}
		runDir := filepath.Join(cfg.Runs.Dir, entry.Name())
		if preserved[runDir] || runDirectoryHasActiveUnit(entry.Name(), active) {
			continue
		}
		if err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo); err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("preserve %s after salvage failed: %w", entry.Name(), err))
			continue
		}
		archiveRunEvidence(ctx, cfg.Runs.ArchiveCommand, runDir, entry.Name())
		if err := removeRunDir(runDir); err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("remove %s: %w", entry.Name(), err))
			continue
		}
		if err := publishFailureDigest(ctx, cfg); err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("publish salvaged failure for %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(sweepErrors...)
}

func activeRunUnits(ctx context.Context) (map[string]bool, error) {
	out, err := commandCombinedOutput(ctx, "systemctl", "--user", "list-units",
		"--type=service", "--state=activating,active", "--no-legend", "--plain", "--full", "--no-pager", "minos-run-*.service")
	if err != nil {
		return nil, fmt.Errorf("inspect active Minos units: %w: %s", err, strings.TrimSpace(string(out)))
	}
	units := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			units[strings.TrimSuffix(fields[0], ".service")] = true
		}
	}
	return units, scanner.Err()
}

func runDirectoryHasActiveUnit(name string, active map[string]bool) bool {
	for unit := range active {
		if strings.HasPrefix(name, unit+"-") {
			return true
		}
	}
	return false
}

func validHandoffRunDirs(cfg ServiceConfig, factsByUnit map[string]Facts) map[string]bool {
	preserved := make(map[string]bool)
	paths, _ := filepath.Glob(filepath.Join(cfg.Runs.Dir, ".handoffs", "*.json"))
	for _, path := range paths {
		unit := strings.TrimSuffix(filepath.Base(path), ".json")
		facts, ok := factsByUnit[unit]
		if !ok {
			continue
		}
		handoff, err := readRunHandoff(path, facts)
		if err != nil {
			continue
		}
		if _, valid := adoptableRunDirectory(cfg, unit, facts, handoff); valid {
			preserved[handoff.RunDir] = true
		}
	}
	return preserved
}

func appendFailureDigest(runDir, failuresRepo string) error {
	if failuresRepo == "" {
		return fmt.Errorf("runs.failures-repo is not configured")
	}
	if insideDirectory(failuresRepo, filepath.Dir(runDir)) {
		return fmt.Errorf("failures repository must be outside runs.dir")
	}
	failuresPath := filepath.Join(failuresRepo, "FAILURES.md")
	info, err := os.Stat(failuresPath)
	if err != nil {
		return fmt.Errorf("inspect Minos FAILURES.md: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Minos FAILURES.md is not a regular file")
	}
	evidence, err := salvageEvidence(runDir)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(failuresPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open Minos FAILURES.md: %w", err)
	}
	defer file.Close()
	stamp := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(file, "\n## %s — %s\n\n", stamp, filepath.Base(runDir)); err != nil {
		return err
	}
	if len(evidence) == 0 {
		if _, err := io.WriteString(file, "No readable failure log, lead transcript, run report, or Ensemble run record was found.\n"); err != nil {
			return err
		}
	}
	for _, item := range evidence {
		if _, err := fmt.Fprintf(file, "### %s\n\n", item.path); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(item.data), "\n"), "\n") {
			if _, err := fmt.Fprintf(file, "    %s\n", line); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(file, "\n"); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush Minos FAILURES.md: %w", err)
	}
	return nil
}

func insideDirectory(path, directory string) bool {
	rel, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type salvagedEvidence struct {
	path string
	data []byte
}

func salvageEvidence(runDir string) ([]salvagedEvidence, error) {
	var paths []string
	err := filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		base := strings.ToLower(entry.Name())
		if base == "failure.log" || base == "failures.log" || base == "run-report.md" || base == "report.md" ||
			(strings.HasPrefix(filepath.ToSlash(rel), "home/.claude/projects/") && strings.HasSuffix(base, ".jsonl")) ||
			strings.Contains(strings.ToLower(filepath.ToSlash(rel)), "ensemble-record") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("extract failure evidence: %w", err)
	}
	sort.Strings(paths)
	items := make([]salvagedEvidence, 0, len(paths))
	for _, path := range paths {
		data, err := readTail(path, salvageTailBytes)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		rel, _ := filepath.Rel(runDir, path)
		items = append(items, salvagedEvidence{path: filepath.ToSlash(rel), data: data})
	}
	return items, nil
}

func readTail(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - maximum
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, maximum))
}

func archiveRunEvidence(ctx context.Context, command, runDir, label string) {
	if command == "" {
		return
	}
	cmd := exec.CommandContext(ctx, command, runDir, label)
	cmd.Env = os.Environ()
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			fmt.Fprintf(os.Stderr, "minos: archive run evidence: %v\n", err)
		}
	}
}

func publishFailureDigest(ctx context.Context, cfg ServiceConfig) error {
	if cfg.Runs.FailuresRepo == "" || cfg.Runs.FailuresCredentialFile == "" || cfg.Runs.GitAskPass == "" {
		return fmt.Errorf("Minos annexe push is not fully configured; durable local append retained")
	}
	env := append(os.Environ(),
		"GIT_ASKPASS="+cfg.Runs.GitAskPass,
		"GIT_TERMINAL_PROMPT=0",
		"MINOS_FORGE_CREDENTIAL_FILE="+cfg.Runs.FailuresCredentialFile,
	)
	commands := [][]string{
		{"add", "--", "FAILURES.md"},
		{"commit", "--only", "--quiet", "-m", "docs: record salvaged Minos run failure", "--", "FAILURES.md"},
		{"push", "origin", "HEAD:main"},
	}
	for _, arguments := range commands {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cfg.Runs.FailuresRepo}, arguments...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
