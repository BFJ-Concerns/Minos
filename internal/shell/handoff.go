package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const runHandoffKind = "minos-run-handoff-v1"

type runHandoff struct {
	Kind        string          `json:"kind"`
	PullRequest handoffPull     `json:"pullRequest"`
	Head        string          `json:"head"`
	RunDir      string          `json:"runDir"`
	Attempt     int             `json:"attempt"`
	StoppedAt   string          `json:"stoppedAt"`
	WrittenAt   string          `json:"writtenAt"`
	RunRecord   json.RawMessage `json:"runRecord"`
}

type handoffPull struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number string `json:"number"`
}

type handoffRunRecord struct {
	Round            *int               `json:"round"`
	ConfirmedUnfixed *[]json.RawMessage `json:"confirmedUnfixed"`
}

func handoffPath(runsDir, unit string) string {
	return filepath.Join(runsDir, ".handoffs", unit+".json")
}

func readRunHandoff(path string, facts Facts) (*runHandoff, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var handoff runHandoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if handoff.Kind != runHandoffKind {
		return nil, fmt.Errorf("kind is %q, want %q", handoff.Kind, runHandoffKind)
	}
	if handoff.PullRequest.Owner != facts.Owner || handoff.PullRequest.Repo != facts.Repo ||
		handoff.PullRequest.Number != facts.PR {
		return nil, fmt.Errorf("pull request is %s/%s#%s, want %s/%s#%s",
			handoff.PullRequest.Owner, handoff.PullRequest.Repo, handoff.PullRequest.Number,
			facts.Owner, facts.Repo, facts.PR)
	}
	var record handoffRunRecord
	if err := json.Unmarshal(handoff.RunRecord, &record); err != nil {
		return nil, fmt.Errorf("runRecord: %w", err)
	}
	if record.Round == nil || *record.Round < 0 {
		return nil, fmt.Errorf("runRecord.round must be a non-negative integer")
	}
	if record.ConfirmedUnfixed == nil {
		return nil, fmt.Errorf("runRecord.confirmedUnfixed must be an array")
	}
	if handoff.Attempt < 0 {
		return nil, fmt.Errorf("attempt must be a non-negative integer")
	}
	return &handoff, nil
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
	runDir := handoff.RunDir
	cleanRunDir := filepath.Clean(runDir)
	cleanRunsDir := filepath.Clean(cfg.Runs.Dir)
	if runDir == "" || !filepath.IsAbs(runDir) || cleanRunDir != runDir ||
		filepath.Dir(cleanRunDir) != cleanRunsDir || !strings.HasPrefix(filepath.Base(cleanRunDir), unit+"-") {
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
