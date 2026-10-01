package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const salvageTailBytes = 64 * 1024

const (
	salvageDigestBytes     = 256 * 1024
	salvageMetadataBytes   = 4 * 1024
	salvageFailureLogBytes = 32 * 1024
	salvageEnsembleBytes   = 64 * 1024
	salvageRunReportBytes  = 32 * 1024
	salvageTranscriptBytes = salvageDigestBytes - salvageMetadataBytes - salvageFailureLogBytes - salvageEnsembleBytes - salvageRunReportBytes
	archiveRunTimeout      = 30 * time.Minute
)

var walkEvidenceRoot = filepath.WalkDir
var failurePublishTimeout = 5 * time.Minute

func sweepRunResidue(ctx context.Context, cfg ServiceConfig, factsByUnit map[string]Facts) error {
	if cfg.Runs.ArchiveCommand == "" {
		return fmt.Errorf("runs.archive-command is not configured; preserve run residue until archiving is provisioned")
	}
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
		dead, err := confirmRunDirDead(ctx, cfg, factsByUnit, runDir, entry.Name())
		if err != nil {
			sweepErrors = append(sweepErrors, err)
			continue
		}
		if !dead {
			continue
		}
		appended, err := appendFailureDigest(runDir, cfg.Runs.FailuresRepo)
		if err != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("preserve %s after salvage failed: %w", entry.Name(), err))
			continue
		}
		if !appended {
			log.Printf("run %s: no salvage evidence found; no FAILURES.md digest appended", entry.Name())
		}
		_ = archiveRunEvidence(ctx, cfg.Runs.ArchiveCommand, runDir, entry.Name())
		_, removeErr := removeRunDirIfStillDead(ctx, cfg, factsByUnit, runDir, entry.Name())
		if removeErr != nil {
			sweepErrors = append(sweepErrors, removeErr)
		}
		if appended {
			if err := publishFailureDigest(ctx, cfg); err != nil {
				sweepErrors = append(sweepErrors, fmt.Errorf("publish salvaged failure for %s: %w", entry.Name(), err))
			}
		}
	}
	return errors.Join(sweepErrors...)
}

func confirmRunDirDead(ctx context.Context, cfg ServiceConfig, factsByUnit map[string]Facts, runDir, name string) (bool, error) {
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return false, fmt.Errorf("lock residue inspection for %s: %w", name, err)
	}
	defer unlock()
	return runDirIsDead(ctx, cfg, factsByUnit, runDir, name)
}

func removeRunDirIfStillDead(ctx context.Context, cfg ServiceConfig, factsByUnit map[string]Facts, runDir, name string) (bool, error) {
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return false, fmt.Errorf("lock residue deletion for %s: %w", name, err)
	}
	defer unlock()
	dead, err := runDirIsDead(ctx, cfg, factsByUnit, runDir, name)
	if err != nil || !dead {
		return false, err
	}
	if err := removeRunDir(runDir); err != nil {
		return false, fmt.Errorf("remove %s: %w", name, err)
	}
	return true, nil
}

func runDirIsDead(ctx context.Context, cfg ServiceConfig, factsByUnit map[string]Facts, runDir, name string) (bool, error) {
	active, err := activeRunUnits(ctx)
	if err != nil {
		return false, fmt.Errorf("recheck liveness for %s: %w", name, err)
	}
	if runDirectoryHasActiveUnit(name, active) || validHandoffRunDirs(cfg, factsByUnit)[runDir] {
		return false, nil
	}
	if _, err := os.Lstat(filepath.Join(runDir, runOwnerMarker)); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect run owner marker for %s: %w", name, err)
	}
	return true, nil
}

func activeRunUnits(ctx context.Context) (map[string]bool, error) {
	names, err := activeRunUnitNames(ctx)
	if err != nil {
		return nil, err
	}
	units := make(map[string]bool, len(names))
	for _, name := range names {
		units[strings.TrimSuffix(name, ".service")] = true
	}
	return units, nil
}

// activeRunUnitNames lists the live run units as systemd names them, suffix
// included, sorted so that a caller reporting one of them reports the same one
// every pass.
func activeRunUnitNames(ctx context.Context) ([]string, error) {
	out, err := commandCombinedOutput(ctx, "systemctl", "--user", "list-units",
		"--type=service", "--state=activating,active", "--no-legend", "--plain", "--full", "--no-pager", "minos-run-*.service")
	if err != nil {
		return nil, fmt.Errorf("inspect active Minos units: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
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
		handoff, err := readRunHandoffStructure(path)
		if err != nil {
			continue
		}
		embeddedFacts := Facts{
			Owner:   handoff.PullRequest.Owner,
			Repo:    handoff.PullRequest.Repo,
			PR:      handoff.PullRequest.Number,
			HeadSHA: handoff.Head,
		}
		if UnitName(embeddedFacts) != unit {
			continue
		}
		facts, found := factsByUnit[unit]
		if found {
			validated, err := readRunHandoff(path, facts)
			if err != nil {
				continue
			}
			handoff = validated
		} else {
			facts = embeddedFacts
		}
		if _, valid := adoptableRunDirectory(cfg, unit, facts, handoff); valid {
			preserved[handoff.RunDir] = true
		}
	}
	return preserved
}

func appendFailureDigest(runDir, failuresRepo string) (bool, error) {
	if failuresRepo == "" {
		return false, fmt.Errorf("runs.failures-repo is not configured")
	}
	if insideDirectory(failuresRepo, filepath.Dir(runDir)) {
		return false, fmt.Errorf("failures repository must be outside runs.dir")
	}
	failuresPath := filepath.Join(failuresRepo, "FAILURES.md")
	info, err := os.Stat(failuresPath)
	if err != nil {
		return false, fmt.Errorf("inspect Minos FAILURES.md: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("Minos FAILURES.md is not a regular file")
	}
	digest, found, err := buildFailureDigest(runDir)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	file, err := os.OpenFile(failuresPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return false, fmt.Errorf("open Minos FAILURES.md: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(digest); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("flush Minos FAILURES.md: %w", err)
	}
	return true, nil
}

func insideDirectory(path, directory string) bool {
	rel, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func buildFailureDigest(runDir string) ([]byte, bool, error) {
	paths, skipped, err := salvageEvidencePaths(runDir)
	if err != nil {
		return nil, false, err
	}
	if len(paths) == 0 && len(skipped) == 0 {
		return nil, false, nil
	}
	var metadata bytes.Buffer
	appendBudgetedDigestText(&metadata, fmt.Sprintf("\n## %s — %s\n\n", time.Now().UTC().Format(time.RFC3339), filepath.Base(runDir)), salvageMetadataBytes, "metadata")
	sort.Strings(skipped)
	for _, note := range skipped {
		if !appendBudgetedDigestText(&metadata, fmt.Sprintf("- Evidence skipped: `%s`\n", note), salvageMetadataBytes, "metadata") {
			break
		}
	}
	if len(skipped) > 0 {
		appendBudgetedDigestText(&metadata, "\n", salvageMetadataBytes, "metadata")
	}
	type sourceDigest struct {
		name   string
		budget int
		data   bytes.Buffer
	}
	sources := []*sourceDigest{
		{name: "failure-log", budget: salvageFailureLogBytes},
		{name: "ensemble", budget: salvageEnsembleBytes},
		{name: "run-report", budget: salvageRunReportBytes},
		{name: "transcript", budget: salvageTranscriptBytes},
	}
	sourceByName := make(map[string]*sourceDigest, len(sources))
	for _, source := range sources {
		sourceByName[source.name] = source
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := readTail(path, salvageTailBytes)
		rel, relErr := filepath.Rel(runDir, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		source := sourceByName[evidenceSource(rel)]
		if err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return nil, false, fmt.Errorf("read salvage evidence %s: %w", rel, err)
			}
			appendBudgetedDigestText(&source.data, fmt.Sprintf("- Evidence skipped: `%s` (%v)\n", rel, err), source.budget, source.name)
			continue
		}
		var section strings.Builder
		fmt.Fprintf(&section, "### %s\n\n", rel)
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			fmt.Fprintf(&section, "    %s\n", line)
		}
		section.WriteString("\n")
		appendBudgetedDigestText(&source.data, section.String(), source.budget, source.name)
	}
	var digest bytes.Buffer
	digest.Write(metadata.Bytes())
	for _, source := range sources {
		digest.Write(source.data.Bytes())
	}
	return digest.Bytes(), true, nil
}

func evidenceSource(rel string) string {
	switch {
	case rel == "failure.log" || rel == "failures.log":
		return "failure-log"
	case strings.HasPrefix(rel, "ensemble-records/"):
		return "ensemble"
	case rel == "run-report.md" || rel == "report.md":
		return "run-report"
	default:
		return "transcript"
	}
}

func appendBudgetedDigestText(digest *bytes.Buffer, text string, budget int, source string) bool {
	truncated := fmt.Sprintf("\n[Digest truncated at 256 KiB. Additional %s evidence omitted.]\n", source)
	remaining := budget - digest.Len()
	if len(text) <= remaining {
		digest.WriteString(text)
		return true
	}
	contentBytes := remaining - len(truncated)
	if contentBytes > 0 {
		digest.WriteString(text[:contentBytes])
	}
	if digest.Len() < budget {
		digest.WriteString(truncated[:min(len(truncated), budget-digest.Len())])
	}
	return false
}

func salvageEvidencePaths(runDir string) ([]string, []string, error) {
	var paths []string
	var skipped []string
	for _, name := range []string{"failure.log", "failures.log", "run-report.md", "report.md"} {
		path := filepath.Join(runDir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return nil, nil, fmt.Errorf("inspect salvage evidence %s: %w", name, err)
			}
			skipped = append(skipped, name+": "+err.Error())
			continue
		}
		if info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	roots := []struct {
		path   string
		accept func(string) bool
	}{
		{path: filepath.Join(runDir, "home", ".claude", "projects"), accept: func(string) bool { return true }},
		{path: filepath.Join(runDir, "home", ".codex", "sessions"), accept: func(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".jsonl") }},
		{path: filepath.Join(runDir, "ensemble-records"), accept: func(string) bool { return true }},
	}
	for _, root := range roots {
		if _, err := os.Lstat(root.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return nil, nil, fmt.Errorf("inspect salvage evidence root %s: %w", root.path, err)
			}
			rel, _ := filepath.Rel(runDir, root.path)
			skipped = append(skipped, filepath.ToSlash(rel)+": "+err.Error())
			continue
		}
		walkErr := walkEvidenceRoot(root.path, func(path string, entry fs.DirEntry, walkErr error) error {
			rel, _ := filepath.Rel(runDir, path)
			rel = filepath.ToSlash(rel)
			if walkErr != nil {
				if !errors.Is(walkErr, fs.ErrPermission) {
					return walkErr
				}
				skipped = append(skipped, rel+": "+walkErr.Error())
				return nil
			}
			if entry.Type().IsRegular() && root.accept(path) {
				paths = append(paths, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, fmt.Errorf("walk salvage evidence root %s: %w", root.path, walkErr)
		}
	}
	return paths, skipped, nil
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

func archiveRunEvidence(ctx context.Context, command, runDir, label string) error {
	if command == "" {
		return fmt.Errorf("archive command is not configured")
	}
	archiveCtx, cancel := context.WithTimeout(ctx, archiveRunTimeout)
	defer cancel()
	cmd := processGroupCommandContext(archiveCtx, command, runDir, label)
	cmd.Env = os.Environ()
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		diagnostic := strings.TrimSpace(stderr.String())
		if line, _, found := strings.Cut(diagnostic, "\n"); found {
			diagnostic = line
		}
		if diagnostic == "" {
			diagnostic = fmt.Sprintf("minos: could not archive run evidence for %s", label)
		}
		fmt.Fprintln(os.Stderr, diagnostic)
		return err
	}
	return nil
}

func processGroupCommandContext(ctx context.Context, name string, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arguments...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return cmd
}

func publishFailureDigest(ctx context.Context, cfg ServiceConfig) error {
	if cfg.Runs.FailuresRepo == "" || cfg.Runs.FailuresCredentialFile == "" {
		return fmt.Errorf("Minos annexe push is not fully configured; durable local append retained")
	}
	tokenBytes, err := os.ReadFile(cfg.Runs.FailuresCredentialFile)
	if err != nil {
		return fmt.Errorf("read Minos annexe credential: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("Minos annexe credential is empty")
	}
	publishCtx, cancel := context.WithTimeout(ctx, failurePublishTimeout)
	defer cancel()
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: token "+token,
		"GIT_AUTHOR_NAME="+cfg.Service.CommitAuthorName,
		"GIT_AUTHOR_EMAIL="+cfg.Service.CommitAuthorEmail,
		"GIT_COMMITTER_NAME="+cfg.Service.CommitAuthorName,
		"GIT_COMMITTER_EMAIL="+cfg.Service.CommitAuthorEmail,
	)
	commands := [][]string{
		{"add", "--", "FAILURES.md"},
		{"commit", "--only", "--quiet", "-m", "docs: record salvaged Minos run failure", "--", "FAILURES.md"},
		{"fetch", "origin", "main"},
		{"rebase", "origin/main"},
		{"push", "origin", "HEAD:main"},
	}
	for _, arguments := range commands {
		cmd := processGroupCommandContext(publishCtx, "git", append([]string{"-C", cfg.Runs.FailuresRepo}, arguments...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			if arguments[0] == "rebase" {
				abort := processGroupCommandContext(publishCtx, "git", "-C", cfg.Runs.FailuresRepo, "rebase", "--abort")
				abort.Env = env
				if abortOut, abortErr := abort.CombinedOutput(); abortErr != nil {
					return fmt.Errorf("git rebase: %w: %s; preserve local commit but rebase abort failed: %v: %s",
						err, strings.TrimSpace(string(out)), abortErr, strings.TrimSpace(string(abortOut)))
				}
			}
			return fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
