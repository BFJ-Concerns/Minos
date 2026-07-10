package shell

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const terminalMarkerFile = "terminal.env"
const forgeWritesAttemptedFile = "forge-writes-attempted.env"

var terminalReason = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var rearmIdentityPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var rearmPR = regexp.MustCompile(`^[0-9]+$`)

type terminalMarker struct {
	RunKind     RunKind
	HeadSHA     string
	Disposition string
	Reason      string
	Timestamp   time.Time
}

func RunTerminalCommand(args []string) error {
	fs := flag.NewFlagSet("run-terminal", flag.ContinueOnError)
	reason := fs.String("reason", "", "service-owned operational failure reason code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *reason == "" {
		return fmt.Errorf("usage: pump19 run-terminal --reason REASON-CODE")
	}
	kind, err := ParseRunKind(os.Getenv("PUMP19_RUN_KIND"))
	if err != nil {
		return err
	}
	runDir := os.Getenv("PUMP19_RUN_DIR")
	if runDir == "" {
		return fmt.Errorf("PUMP19_RUN_DIR is required")
	}
	return writeTerminalMarker(runDir, kind, os.Getenv("PUMP19_HEAD_SHA"), *reason)
}

func RearmCommand(args []string) error {
	fs := flag.NewFlagSet("re-arm", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	forge := fs.String("forge", "", "forge configuration name")
	owner := fs.String("owner", "", "repository owner")
	repo := fs.String("repo", "", "repository name")
	pr := fs.String("pr", "", "pull request number")
	head := fs.String("head", "", "full head SHA")
	run := fs.String("run", "", "run kind")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !rearmIdentityPart.MatchString(*forge) || !rearmIdentityPart.MatchString(*owner) || !rearmIdentityPart.MatchString(*repo) || !rearmPR.MatchString(*pr) || !rearmIdentityPart.MatchString(*head) {
		return fmt.Errorf("usage: pump19 re-arm --config ROOT --forge FORGE --owner OWNER --repo REPO --pr PR --head FULL-SHA --run RUN")
	}
	kind, err := ParseRunKind(*run)
	if err != nil {
		return err
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	count, err := rearmTerminalMarkers(cfg.Runs.Dir, Facts{Forge: *forge, Owner: *owner, Repo: *repo, PR: *pr, HeadSHA: *head}, kind)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no terminal marker for exact run identity")
	}
	fmt.Printf("rearmed %d terminal marker(s)\n", count)
	return nil
}

func writeTerminalMarker(runDir string, kind RunKind, headSHA, reason string) error {
	if _, err := ParseRunKind(string(kind)); err != nil {
		return err
	}
	if strings.TrimSpace(headSHA) == "" || strings.ContainsAny(headSHA, "\r\n=") {
		return fmt.Errorf("invalid head SHA")
	}
	if !terminalReason.MatchString(reason) {
		return fmt.Errorf("invalid terminal reason code %q", reason)
	}
	path := filepath.Join(runDir, terminalMarkerFile)
	if _, err := readTerminalMarker(runDir); err == nil {
		// The mission can record a more precise reason before its non-zero exit
		// reaches the wrapper. The wrapper must not replace that first account.
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing terminal marker is invalid: %w", err)
	}
	data := strings.Join([]string{
		"PUMP19_TERMINAL_VERSION=1",
		"PUMP19_RUN_KIND=" + string(kind),
		"PUMP19_HEAD_SHA=" + headSHA,
		"PUMP19_DISPOSITION=operational-error",
		"PUMP19_REASON_CODE=" + reason,
		"PUMP19_TERMINAL_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
	}, "\n") + "\n"
	return atomicPublishFile(path, []byte(data), 0o644)
}

func readTerminalMarker(runDir string) (terminalMarker, error) {
	values, err := readMetaFile(filepath.Join(runDir, terminalMarkerFile))
	if err != nil {
		return terminalMarker{}, err
	}
	if values["PUMP19_TERMINAL_VERSION"] != "1" {
		return terminalMarker{}, fmt.Errorf("unsupported terminal marker version")
	}
	kind, err := ParseRunKind(values["PUMP19_RUN_KIND"])
	if err != nil {
		return terminalMarker{}, err
	}
	if values["PUMP19_HEAD_SHA"] == "" || values["PUMP19_DISPOSITION"] != "operational-error" || !terminalReason.MatchString(values["PUMP19_REASON_CODE"]) {
		return terminalMarker{}, fmt.Errorf("invalid terminal marker fields")
	}
	at, err := time.Parse(time.RFC3339Nano, values["PUMP19_TERMINAL_AT"])
	if err != nil {
		return terminalMarker{}, fmt.Errorf("parse terminal timestamp: %w", err)
	}
	return terminalMarker{
		RunKind:     kind,
		HeadSHA:     values["PUMP19_HEAD_SHA"],
		Disposition: values["PUMP19_DISPOSITION"],
		Reason:      values["PUMP19_REASON_CODE"],
		Timestamp:   at,
	}, nil
}

func writeForgeWritesAttempted(runDir, operation string) error {
	if runDir == "" {
		return nil
	}
	path := filepath.Join(runDir, forgeWritesAttemptedFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data := strings.Join([]string{
		"PUMP19_FORGE_WRITE_ATTEMPTED=1",
		"PUMP19_FORGE_OPERATION=" + operation,
		"PUMP19_FORGE_WRITE_AT=" + time.Now().UTC().Format(time.RFC3339Nano),
	}, "\n") + "\n"
	return atomicPublishFile(path, []byte(data), 0o644)
}

func hasForgeWritesAttempted(runDir string) bool {
	values, err := readMetaFile(filepath.Join(runDir, forgeWritesAttemptedFile))
	return err == nil && values["PUMP19_FORGE_WRITE_ATTEMPTED"] == "1"
}

// atomicPublishFile publishes a complete, synced file without replacing a
// winner. Link provides the no-replace property that os.Rename lacks on Unix.
func atomicPublishFile(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func terminalMarkerDirs(root string, facts Facts, kind RunKind) ([]string, error) {
	canonical := RunDir(root, facts.Forge, facts.Owner, facts.Repo, facts.PR, facts.HeadSHA, kind)
	entries, err := os.ReadDir(filepath.Dir(canonical))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	base := filepath.Base(canonical)
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || (entry.Name() != base && !strings.HasPrefix(entry.Name(), base+".")) {
			continue
		}
		dir := filepath.Join(filepath.Dir(canonical), entry.Name())
		marker, err := readTerminalMarker(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		// The directory uses a short SHA. The marker's full identity prevents a
		// short-prefix collision from latching or re-arming another head.
		if marker.HeadSHA == facts.HeadSHA && marker.RunKind == kind {
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

func hasTerminalMarker(root string, facts Facts, kind RunKind) (bool, error) {
	dirs, err := terminalMarkerDirs(root, facts, kind)
	return len(dirs) != 0, err
}

func rearmTerminalMarkers(root string, facts Facts, kind RunKind) (int, error) {
	dirs, err := terminalMarkerDirs(root, facts, kind)
	if err != nil {
		return 0, err
	}
	for index, dir := range dirs {
		from := filepath.Join(dir, terminalMarkerFile)
		to := fmt.Sprintf("%s.rearmed-%d-%d", from, time.Now().UTC().UnixNano(), index)
		if err := os.Rename(from, to); err != nil {
			return index, err
		}
	}
	return len(dirs), nil
}
