package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// reviewedChange is what the service keeps of the last head a run finished
// on for one pull request: the outcome, and a fingerprint of each file's
// part of the pull request's own diff. A later head is compared by those
// fingerprints, never by commit identity, so a rebase, squash or reworded
// history that leaves the files as they were is recognised as the change
// already judged. The record is a cache beside the runs: losing it costs a
// full review and nothing else.
type reviewedChange struct {
	Head    string            `json:"head"`
	Target  string            `json:"target"`
	Outcome string            `json:"outcome"`
	Files   map[string]string `json:"files"`
}

// earlierHead is the answer to `minos forge earlier`. ChangedPaths lists the
// files whose part of the pull request's own diff differs between the
// earlier head and this one; it is null when the two could not be compared.
type earlierHead struct {
	Head         string   `json:"head"`
	Outcome      string   `json:"outcome"`
	Ancestor     bool     `json:"ancestor"`
	ChangedPaths []string `json:"changed_paths"`
}

// earlierHeadWalk is how many of the pull request's own commits are asked
// for a recorded outcome when no record of an earlier head is kept.
const earlierHeadWalk = 20

func reviewedChangePath(cfg ServiceConfig, forgeName string, guard forge.Guard) string {
	name := fmt.Sprintf("%s-%s-%d.json", forgeName, url.PathEscape(guard.Repository.Owner+"/"+guard.Repository.Name), guard.PullRequest)
	return filepath.Join(cfg.Runs.Dir, ".reviewed", name)
}

// changeFingerprints maps every path the pull request's own diff touches at
// head to the modes and blobs on each side of it, read from the merge base
// with the target. Content decides: the same file change carried by other
// commits, or onto a moved target that left the file alone, fingerprints
// the same.
func changeFingerprints(ctx context.Context, workspace, target, head string) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", workspace, "diff", "--raw", "-z", "--no-renames", "--no-abbrev", target+"..."+head).Output()
	if err != nil {
		return nil, fmt.Errorf("read the change at %s: %w", head, err)
	}
	files := map[string]string{}
	fields := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	for index := 0; index+1 < len(fields); index += 2 {
		files[string(fields[index+1])] = string(fields[index])
	}
	return files, nil
}

func changedPaths(earlier, current map[string]string) []string {
	paths := []string{}
	for path, fingerprint := range current {
		if earlier[path] != fingerprint {
			paths = append(paths, path)
		}
	}
	for path := range earlier {
		if _, present := current[path]; !present {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

// recordReviewedChange keeps the head a run has just finished on. It runs
// after the forge accepted the terminal status, which is the durable
// outcome; a record that cannot be written is said on stderr and costs the
// next head a full review.
func recordReviewedChange(ctx context.Context, guard forge.Guard, outcome string) {
	workspace := os.Getenv("MINOS_WORKSPACE")
	if workspace == "" {
		return
	}
	err := func() error {
		cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
		if err != nil {
			return err
		}
		files, err := changeFingerprints(ctx, workspace, guard.TargetSHA, guard.HeadSHA)
		if err != nil {
			return err
		}
		record, err := json.Marshal(reviewedChange{Head: guard.HeadSHA, Target: guard.TargetSHA, Outcome: outcome, Files: files})
		if err != nil {
			return err
		}
		path := reviewedChangePath(cfg, os.Getenv("MINOS_FORGE"), guard)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		staging := path + ".next"
		if err := os.WriteFile(staging, append(record, '\n'), 0o600); err != nil {
			return err
		}
		return os.Rename(staging, path)
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "minos: the reviewed change at %s could not be recorded: %v; the next head gets a full review\n", guard.HeadSHA, err)
	}
}

// findEarlierHead names the head Minos last finished on before this one. The
// kept record answers first, whatever became of its commit. Without one, the
// pull request's own commits are asked, newest first: a commit carrying the
// service's request-changes review finished as attention, and one whose
// latest service status is clean finished clean. An attention status alone
// is not enough, because the service also sets it on a head whose review
// never completed.
func findEarlierHead(ctx context.Context, adapter *forge.Adapter, guard forge.Guard) (earlierHead, error) {
	none := earlierHead{Outcome: "none"}
	workspace := os.Getenv("MINOS_WORKSPACE")
	if workspace == "" {
		return none, fmt.Errorf("MINOS_WORKSPACE is required")
	}
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return none, err
	}
	current, err := changeFingerprints(ctx, workspace, guard.TargetSHA, guard.HeadSHA)
	if err != nil {
		return none, err
	}
	ancestor := func(sha string) bool {
		return exec.CommandContext(ctx, "git", "-C", workspace, "merge-base", "--is-ancestor", sha, guard.HeadSHA).Run() == nil
	}
	if raw, readErr := os.ReadFile(reviewedChangePath(cfg, os.Getenv("MINOS_FORGE"), guard)); readErr == nil {
		var record reviewedChange
		if json.Unmarshal(raw, &record) == nil && record.Head != "" && record.Head != guard.HeadSHA &&
			(record.Outcome == "clean" || record.Outcome == "attention") && record.Files != nil {
			return earlierHead{Head: record.Head, Outcome: record.Outcome, Ancestor: ancestor(record.Head), ChangedPaths: changedPaths(record.Files, current)}, nil
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-list", fmt.Sprintf("--max-count=%d", earlierHeadWalk+1), guard.TargetSHA+".."+guard.HeadSHA).Output()
	if err != nil {
		return none, fmt.Errorf("list the pull request's commits: %w", err)
	}
	snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
	if err != nil {
		return none, err
	}
	requestedChanges := map[string]bool{}
	for _, review := range snapshot.Reviews {
		if review.User == snapshot.AuthenticatedUser && review.State == "REQUEST_CHANGES" {
			requestedChanges[review.CommitID] = true
		}
	}
	for _, sha := range strings.Fields(string(out)) {
		if sha == guard.HeadSHA {
			continue
		}
		outcome := "attention"
		if !requestedChanges[sha] {
			recorded, err := recordedOutcome(ctx, adapter, guard.Repository, sha)
			if err != nil {
				return none, err
			}
			if recorded != "clean" {
				continue
			}
			outcome = "clean"
		}
		found := earlierHead{Head: sha, Outcome: outcome, Ancestor: true}
		if files, err := changeFingerprints(ctx, workspace, guard.TargetSHA, sha); err == nil {
			found.ChangedPaths = changedPaths(files, current)
		}
		return found, nil
	}
	return none, nil
}
