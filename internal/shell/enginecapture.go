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
	"strings"
	"sync"

	"bfj/minos/internal/atomicreplace"
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
		Model string     `json:"model"`
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
	Usage tokenUsage `json:"usage"`
}

type tokenUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
}

type leadMetrics struct {
	Schema                   int   `json:"schema"`
	InputTokens              int64 `json:"input_tokens"`
	DirectInputTokens        int64 `json:"direct_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	PromptStart              int64 `json:"prompt_start_tokens"`
	PromptPeak               int64 `json:"prompt_peak_tokens"`
	PromptGrowth             int64 `json:"prompt_growth_tokens"`
	CompactionEvents         int   `json:"compaction_events"`
	StreamBytes              int64 `json:"stream_bytes"`
}

func (u tokenUsage) totalInput() int64 {
	return u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
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
	metricsPath := fs.String("metrics", "", "lead lifecycle metrics JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if *pinsPath == "" || *outputPath == "" || *resolvedPath == "" || *metricsPath == "" || len(command) == 0 {
		return fmt.Errorf("usage: minos capture-claude --pins FILE --output FILE --resolved FILE --metrics FILE COMMAND [ARG...]")
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
	metrics := leadMetrics{Schema: 1}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		metrics.StreamBytes += int64(len(line) + 1)
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
		if strings.Contains(strings.ToLower(event.Type), "compact") || strings.Contains(strings.ToLower(event.Subtype), "compact") {
			metrics.CompactionEvents++
		}
		messageInput := event.Message.Usage.totalInput()
		if messageInput > 0 {
			if metrics.PromptStart == 0 {
				metrics.PromptStart = messageInput
			}
			if messageInput > metrics.PromptPeak {
				metrics.PromptPeak = messageInput
			}
		}
		// Claude's result envelope carries aggregate run usage. Include cache
		// creation and reads: they are real prompt input even when they are
		// billed differently from uncached tokens.
		if event.Type == "result" {
			metrics.DirectInputTokens = event.Usage.InputTokens
			metrics.CacheCreationInputTokens = event.Usage.CacheCreationTokens
			metrics.CacheReadInputTokens = event.Usage.CacheReadTokens
			metrics.InputTokens = event.Usage.totalInput()
			metrics.OutputTokens = event.Usage.OutputTokens
		}
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
	metrics.PromptGrowth = metrics.PromptPeak - metrics.PromptStart
	if err := writeLeadMetrics(*metricsPath, metrics); err != nil {
		return err
	}
	return output.Sync()
}

func writeLeadMetrics(path string, metrics leadMetrics) error {
	data, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	return atomicreplace.Write(path, append(data, '\n'), 0o644)
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
