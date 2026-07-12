package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type claudeStreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// resolvedModel is the small run-evidence record written by the lead capture
// interlock. It is not combined with worker data or posted to the pull request.
type resolvedModel struct {
	ID            string `json:"id"`
	ResolvedModel string `json:"resolved_model"`
}

// CaptureClaudeCommand records Claude's JSONL audit stream and establishes the
// served lead model from the engine's init event before the session can publish.
// It observes event envelopes only; review prose remains opaque to machinery.
func CaptureClaudeCommand(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer) error {
	fs := flag.NewFlagSet("capture-claude", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pinsPath := fs.String("pins", "", "model pins TOML")
	outputPath := fs.String("output", "", "Claude JSONL audit output")
	resolvedPath := fs.String("resolved", "", "resolved lead-model JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if *pinsPath == "" || *outputPath == "" || *resolvedPath == "" || len(command) == 0 {
		return fmt.Errorf("usage: minos capture-claude --pins FILE --output FILE --resolved FILE COMMAND [ARG...]")
	}

	pins, err := loadModelPins(*pinsPath)
	if err != nil {
		return err
	}
	if err := validateLeadPin(pins.Roles.Lead); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(*outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer output.Close()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = stdin
	// os/exec copies child stderr concurrently with the stream reader below.
	// Serialise both sources because callers are not required to supply a
	// concurrency-safe writer (bytes.Buffer, quite reasonably, is not one).
	safeStderr := &lockedWriter{w: stderr}
	cmd.Stderr = safeStderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	servedModel := ""
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if _, err := output.Write(append(line, '\n')); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
		var event claudeStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("decode Claude stream event: %w", err)
		}
		fmt.Fprintf(safeStderr, "minos lead stream event type=%s subtype=%s\n", event.Type, event.Subtype)
		model := ""
		if event.Type == "system" && event.Subtype == "init" {
			model = event.Model
		} else if event.Type == "assistant" {
			model = event.Message.Model
		}
		if model == "" {
			continue
		}
		if model != pins.Roles.Lead.Model {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("lead model pin mismatch: requested %s, resolved %s", pins.Roles.Lead.Model, model)
		}
		if servedModel == "" {
			servedModel = model
			if err := writeResolvedLead(*resolvedPath, pins.Roles.Lead.ID, model); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}
	if servedModel == "" {
		return fmt.Errorf("Claude stream ended without a resolved lead model")
	}
	return output.Sync()
}

func writeResolvedLead(path, id, model string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal([]resolvedModel{{ID: id, ResolvedModel: model}})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
