package shell

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/deploy"
)

// Runs share the configured envelope live through this slice; each run's
// MemoryMax is also the backstop on a box without the slice installed.
const runsSliceUnit = "minos-runs.slice"

func checkRunsSliceMemory(ctx context.Context, cfg ServiceConfig) error {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := commandCombinedOutput(probe, "systemctl", "--user", "show", runsSliceUnit, "--property=LoadState", "--property=FragmentPath", "--property=MemoryMax")
	if err != nil {
		return fmt.Errorf("could not check %s MemoryMax: %w: %s", runsSliceUnit, err, strings.TrimSpace(string(out)))
	}
	settings := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if key, value, found := strings.Cut(line, "="); found {
			settings[key] = value
		}
	}
	if settings["LoadState"] == "not-found" || (settings["LoadState"] == "loaded" && settings["FragmentPath"] == "") {
		return nil // The per-run ceiling covers installations without a slice.
	}
	actual := settings["MemoryMax"]
	if settings["LoadState"] != "loaded" || actual == "" {
		return fmt.Errorf("could not read %s MemoryMax from the user manager", runsSliceUnit)
	}
	envelope := cfg.runMemoryEnvelopeGiB()
	if actual != fmt.Sprint(int64(envelope)<<30) {
		return fmt.Errorf("%s MemoryMax=%s differs from the configured %d GiB run envelope; reinstall the user units and reload systemd", runsSliceUnit, actual, envelope)
	}
	return nil
}

// InstallUnitsCommand writes the shipped systemd user units into the
// directory the user manager reads, rendered for this deployment: every
// service that runs the binary names the configured root, and the runs
// slice carries the memory envelope from runs.memory-envelope-gib —
// MemoryMax at the envelope, MemoryHigh two GiB below it — so a box's
// ceiling is the knob's value, never the shipped default by accident.
// Re-run it after changing the envelope, then reload the user manager.
func InstallUnitsCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("install-units", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configRoot := flags.String("config", DefaultConfigRoot, "configuration root")
	usage := fmt.Errorf("usage: minos install-units [--config ROOT] UNIT_DIRECTORY")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return usage
	}
	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return err
	}
	return installUnits(cfg, flags.Arg(0), stdout)
}

func installUnits(cfg ServiceConfig, directory string, stdout io.Writer) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}
	entries, err := fs.ReadDir(deploy.Units, "systemd/user")
	if err != nil {
		return fmt.Errorf("read shipped units: %w", err)
	}
	for _, entry := range entries {
		content, err := fs.ReadFile(deploy.Units, "systemd/user/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read shipped unit %s: %w", entry.Name(), err)
		}
		rendered, err := renderUnit(entry.Name(), content, cfg)
		if err != nil {
			return err
		}
		destination := filepath.Join(directory, entry.Name())
		// Write-then-rename: the user manager never reads a half-written
		// unit, and an interrupted install leaves the previous one in place.
		staging := destination + ".next"
		if err := os.WriteFile(staging, rendered, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destination, err)
		}
		if err := os.Rename(staging, destination); err != nil {
			_ = os.Remove(staging)
			return fmt.Errorf("install %s: %w", destination, err)
		}
		fmt.Fprintln(stdout, destination)
	}
	return nil
}

// shippedBinary is the path the shipped units run the service from. A
// service whose Exec line runs it passes the configuration root, which the
// render substitutes; a shipped unit that runs something else (the
// sweep-after-run template runs systemctl) carries no root and installs
// verbatim, whatever its comments mention.
const shippedBinary = "/usr/local/bin/minos"

func runsShippedBinary(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "Exec") && strings.Contains(line, shippedBinary) {
			return true
		}
	}
	return false
}

// renderUnit substitutes this deployment's values into a shipped unit: the
// configuration root every service running the binary passes to it, and
// the runs slice's two memory ceilings. A shipped unit missing a line the
// render expects is a template that drifted from this code, reported rather
// than installed half-rendered.
func renderUnit(name string, content []byte, cfg ServiceConfig) ([]byte, error) {
	if strings.HasSuffix(name, ".service") && runsShippedBinary(content) {
		marker := []byte("--config " + DefaultConfigRoot)
		if !bytes.Contains(content, marker) {
			return nil, fmt.Errorf("shipped %s carries no %q to render", name, string(marker))
		}
		content = bytes.ReplaceAll(content, marker, []byte("--config "+systemdArgument(cfg.Root)))
	}
	if name != runsSliceUnit {
		return content, nil
	}
	return renderRunsSlice(content, cfg.runMemoryEnvelopeGiB())
}

func renderRunsSlice(content []byte, envelopeGiB int) ([]byte, error) {
	high := envelopeGiB - 2
	if high < 1 {
		high = 1
	}
	replacements := map[string]string{
		"MemoryHigh=": fmt.Sprintf("MemoryHigh=%dG", high),
		"MemoryMax=":  fmt.Sprintf("MemoryMax=%dG", envelopeGiB),
	}
	lines := strings.Split(string(content), "\n")
	for prefix, replacement := range replacements {
		index := -1
		for i, line := range lines {
			if strings.HasPrefix(line, prefix) {
				index = i
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("shipped %s carries no %s line to render", runsSliceUnit, strings.TrimSuffix(prefix, "="))
		}
		lines[index] = replacement
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// systemdArgument renders a path as one ExecStart argument: bare when it
// carries none of the characters systemd's command-line parser splits or
// unescapes on, double-quoted with backslash escapes otherwise.
func systemdArgument(value string) string {
	if !strings.ContainsAny(value, " \t\n\"'\\") {
		return value
	}
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(value)
	return "\"" + escaped + "\""
}
