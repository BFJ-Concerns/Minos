package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const runHandoffKind = "minos-run-handoff-v1"

type runHandoff struct {
	Kind        string           `json:"kind"`
	PullRequest handoffPull      `json:"pullRequest"`
	Head        string           `json:"head"`
	RunDir      string           `json:"runDir"`
	StoppedAt   string           `json:"stoppedAt"`
	WrittenAt   string           `json:"writtenAt"`
	RunRecord   json.RawMessage  `json:"runRecord"`
	Predecessor *handoffProgress `json:"predecessorProgress,omitempty"`
	Progress    *handoffProgress `json:"progress,omitempty"`
}

type handoffProgress struct {
	Stage        string `json:"stage"`
	Round        int    `json:"round"`
	Head         string `json:"head"`
	LatestReview int64  `json:"latestReview"`
}

type handoffPull struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number string `json:"number"`
}

type handoffRunRecord struct {
	Round            *int               `json:"round"`
	ConfirmedFixed   json.RawMessage    `json:"confirmedFixed"`
	ConfirmedUnfixed *[]json.RawMessage `json:"confirmedUnfixed"`
}

type savedReviewResult struct {
	Status   string `json:"status"`
	Reviewed struct {
		Head string `json:"head"`
	} `json:"reviewed"`
}

func handoffPath(runsDir, unit string) string {
	return filepath.Join(runsDir, ".handoffs", unit+".json")
}

func readRunHandoff(path string, facts Facts) (*runHandoff, error) {
	handoff, err := readRunHandoffStructure(path)
	if err != nil {
		return nil, err
	}
	if handoff.PullRequest.Owner != facts.Owner || handoff.PullRequest.Repo != facts.Repo ||
		handoff.PullRequest.Number != facts.PR {
		return nil, fmt.Errorf("pull request is %s/%s#%s, want %s/%s#%s",
			handoff.PullRequest.Owner, handoff.PullRequest.Repo, handoff.PullRequest.Number,
			facts.Owner, facts.Repo, facts.PR)
	}
	return handoff, nil
}

func readRunHandoffStructure(path string) (*runHandoff, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var handoff runHandoff
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&handoff); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if handoff.Kind != runHandoffKind {
		return nil, fmt.Errorf("kind is %q, want %q", handoff.Kind, runHandoffKind)
	}
	if handoff.PullRequest.Owner == "" || handoff.PullRequest.Repo == "" || handoff.PullRequest.Number == "" {
		return nil, fmt.Errorf("pull request identity must be complete")
	}
	var record handoffRunRecord
	if err := json.Unmarshal(handoff.RunRecord, &record); err != nil {
		return nil, fmt.Errorf("runRecord: %w", err)
	}
	if record.Round == nil || *record.Round < 0 {
		return nil, fmt.Errorf("runRecord.round must be a non-negative integer")
	}
	if len(record.ConfirmedFixed) > 0 {
		var confirmedFixed []json.RawMessage
		if err := json.Unmarshal(record.ConfirmedFixed, &confirmedFixed); err != nil || confirmedFixed == nil {
			return nil, fmt.Errorf("runRecord.confirmedFixed must be an array")
		}
	}
	if record.ConfirmedUnfixed == nil {
		return nil, fmt.Errorf("runRecord.confirmedUnfixed must be an array")
	}
	if handoff.Progress != nil {
		if err := validateHandoffProgress("progress", handoff.Progress); err != nil {
			return nil, err
		}
		if handoff.Progress.Round != *record.Round {
			return nil, fmt.Errorf("progress.round must match runRecord.round")
		}
		if handoff.Progress.Head != handoff.Head {
			return nil, fmt.Errorf("progress.head must match head")
		}
	}
	if handoff.Predecessor != nil {
		if handoff.Progress == nil {
			return nil, fmt.Errorf("predecessorProgress requires progress")
		}
		if err := validateHandoffProgress("predecessorProgress", handoff.Predecessor); err != nil {
			return nil, err
		}
	}
	return &handoff, nil
}

func validateHandoffProgress(name string, progress *handoffProgress) error {
	if strings.TrimSpace(progress.Stage) == "" {
		return fmt.Errorf("%s.stage must be non-empty", name)
	}
	if progress.Round < 0 {
		return fmt.Errorf("%s.round must be a non-negative integer", name)
	}
	if strings.TrimSpace(progress.Head) == "" {
		return fmt.Errorf("%s.head must be non-empty", name)
	}
	if progress.LatestReview < 0 {
		return fmt.Errorf("%s.latestReview must be a non-negative integer", name)
	}
	return nil
}

type continuationProgressDecision string

const (
	continuationProgressUnknown  continuationProgressDecision = "unknown"
	continuationProgressAdvanced continuationProgressDecision = "advanced"
	continuationProgressStalled  continuationProgressDecision = "stalled"
)

func compareContinuationProgress(handoff *runHandoff) continuationProgressDecision {
	if handoff.Predecessor == nil || handoff.Progress == nil {
		return continuationProgressUnknown
	}
	previous, current := handoff.Predecessor, handoff.Progress
	if current.Head != previous.Head || current.LatestReview > previous.LatestReview ||
		current.Round > previous.Round ||
		(current.Round == previous.Round && current.Stage != previous.Stage) {
		return continuationProgressAdvanced
	}
	if current.Round == previous.Round && current.Stage == previous.Stage &&
		current.Head == previous.Head && current.LatestReview == previous.LatestReview {
		return continuationProgressStalled
	}
	return continuationProgressUnknown
}

func rejectRunHandoff(path string, reason error) {
	rejected := path + ".rejected"
	_ = os.Remove(rejected)
	if err := os.Rename(path, rejected); err != nil {
		fmt.Fprintf(os.Stderr, "minos: reject continuation handoff %s: %v; preserve rejected file: %v\n", path, reason, err)
		return
	}
	fmt.Fprintf(os.Stderr, "minos: rejected continuation handoff %s: %v\n", path, reason)
}

func adoptableRunDirectory(cfg ServiceConfig, unit string, facts Facts, handoff *runHandoff) (string, bool) {
	cleanRunDir, contained := containedRunDirectory(cfg, unit, handoff.RunDir)
	if !contained {
		return "runDir is not a direct, unit-named child of runs.dir", false
	}
	info, err := os.Stat(cleanRunDir)
	if err != nil || !info.IsDir() {
		return "runDir does not exist as a directory", false
	}
	if _, err := os.Stat(filepath.Join(cleanRunDir, runOwnerMarker)); err == nil || !os.IsNotExist(err) {
		return "runDir is still owned by another runwrap invocation", false
	}
	if handoff.Head != facts.HeadSHA {
		return fmt.Sprintf("pull-request head moved from %s to %s", handoff.Head, facts.HeadSHA), false
	}
	workspace := filepath.Join(cleanRunDir, "workspace")
	workspaceInfo, err := os.Stat(workspace)
	if err != nil || !workspaceInfo.IsDir() {
		return "workspace is missing", false
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return "workspace is not a Git repository", false
	}
	return "", true
}

func containedRunDirectory(cfg ServiceConfig, unit, runDir string) (string, bool) {
	cleanRunDir := filepath.Clean(runDir)
	cleanRunsDir := filepath.Clean(cfg.Runs.Dir)
	if runDir == "" || !filepath.IsAbs(runDir) || cleanRunDir != runDir ||
		filepath.Dir(cleanRunDir) != cleanRunsDir || !strings.HasPrefix(filepath.Base(cleanRunDir), unit+"-") {
		return "", false
	}
	return cleanRunDir, true
}

func adoptableReviewResult(path, head string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return false
	}
	var result savedReviewResult
	if err := json.Unmarshal(data, &result); err != nil || result.Status != "complete" || result.Reviewed.Head != head {
		return false
	}
	return true
}

func containedPredecessorReviewResult(cfg ServiceConfig, unit, head string) string {
	entries, err := os.ReadDir(cfg.Runs.Dir)
	if err != nil {
		return ""
	}
	adopted := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(cfg.Runs.Dir, entry.Name())
		if _, contained := containedRunDirectory(cfg, unit, runDir); !contained {
			continue
		}
		carried := filepath.Join(runDir, "carried-review-result.json")
		result := carried
		valid := adoptableReviewResult(carried, head)
		if !valid {
			if err := os.Remove(carried); err != nil && !os.IsNotExist(err) {
				continue
			}
			result = filepath.Join(runDir, "review-result.json")
			valid = adoptableReviewResult(result, head)
		}
		if !valid {
			continue
		}
		if adopted != "" {
			return ""
		}
		adopted = result
	}
	return adopted
}
