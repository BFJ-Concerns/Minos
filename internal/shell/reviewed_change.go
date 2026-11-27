package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	Head        string            `json:"head"`
	Target      string            `json:"target"`
	Outcome     string            `json:"outcome"`
	Files       map[string]string `json:"files"`
	FromSummary bool              `json:"from_summary,omitempty"`
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
	name := reviewedChangePrefix(forgeName, guard.Repository) + strconv.FormatInt(guard.PullRequest, 10) + ".json"
	return filepath.Join(cfg.Runs.Dir, ".reviewed", name)
}

func reviewedChangePrefix(forgeName string, repo forge.Repository) string {
	return forgeName + "-" + url.PathEscape(repo.Owner+"/"+repo.Name) + "-"
}

func removeReviewedChange(cfg ServiceConfig, forgeName string, guard forge.Guard) error {
	return removeRecordFile(reviewedChangePath(cfg, forgeName, guard))
}

func removeRecordFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// pullRequestRecordDirs are the directories under the runs directory where
// the service keeps a record per pull request: the reviewed change and the
// command record. Both are named by reviewedChangePrefix and the pull
// request's number, and both go when the pull request does.
func pullRequestRecordDirs(cfg ServiceConfig) []string {
	return []string{filepath.Join(cfg.Runs.Dir, ".reviewed"), commandRecordDir(cfg)}
}

// pruneUnenrolledRecords collects, by repository prefix, the pull requests
// that have a record in any pull-request record directory, for forge
// checks, and discards the records of repositories no longer enrolled.
// Split off the final numeric suffix so existing file names work even when
// a forge, owner or repository name contains hyphens.
func pruneUnenrolledRecords(cfg ServiceConfig, repos []RepoConfig) map[string][]int64 {
	enrolled := make(map[string][]int64, len(repos))
	for _, repo := range repos {
		enrolled[reviewedChangePrefix(repo.Forge, forge.Repository{Owner: repo.Owner, Name: repo.Repo})] = nil
	}
	for _, dir := range pullRequestRecordDirs(cfg) {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			log.Printf("read pull-request records: %v", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			stem := strings.TrimSuffix(entry.Name(), ".json")
			split := strings.LastIndexByte(stem, '-') + 1
			if !strings.Contains(stem[:split], "%2F") {
				continue
			}
			prefix, pr := stem[:split], stem[split:]
			number, err := strconv.ParseInt(pr, 10, 64)
			if err != nil || number <= 0 || strconv.FormatInt(number, 10) != pr {
				continue
			}
			if _, ok := enrolled[prefix]; !ok {
				if err := removeRecordFile(filepath.Join(dir, entry.Name())); err != nil {
					log.Print(err)
				}
			} else if !slices.Contains(enrolled[prefix], number) {
				enrolled[prefix] = append(enrolled[prefix], number)
			}
		}
	}
	return enrolled
}

// pruneRetiredRecords removes every record of a pull request the forge
// confirms closed or gone. An open listing can race a reopen or omit a pull
// request, so only a direct lookup can confirm that a pull request outside
// the list is retired.
func pruneRetiredRecords(ctx context.Context, cfg ServiceConfig, repo RepoConfig, numbers []int64, open []Facts) {
	openNumbers := make(map[int64]bool, len(open))
	for _, facts := range open {
		openNumbers[pullRequestNumber(facts)] = true
	}
	repository := forge.Repository{Owner: repo.Owner, Name: repo.Repo}
	adapter, err := newBehaviouralForge(cfg, repo.Forge)
	if err != nil {
		log.Printf("%s/%s: check pull-request records: %v", repo.Owner, repo.Repo, err)
		return
	}
	for _, number := range numbers {
		if openNumbers[number] {
			continue
		}
		retired, err := adapter.PullRequestRetired(ctx, repository, number)
		if err == nil && retired {
			guard := forge.Guard{Repository: repository, PullRequest: number}
			err = errors.Join(removeReviewedChange(cfg, repo.Forge, guard), removeCommandRecord(cfg, repo.Forge, guard))
		}
		if err != nil {
			log.Printf("%s/%s#%d: prune pull-request records: %v", repo.Owner, repo.Repo, number, err)
		}
	}
}

func pullRequestClosed(snapshot forge.Snapshot) bool {
	return snapshot.State == "closed" || snapshot.State == "merged" || snapshot.Merged
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
// after the forge accepted the durable outcome: the summary comment's write
// of a finished state, or a clean or attention status, which the resident
// lead writes in its place. A record that cannot be written is said on
// stderr, and the record it would have replaced is removed: left in place,
// it would name an older head than the one just finished and answer the
// next earlier-head search ahead of the summary comment, which names the
// right one.
func recordReviewedChange(ctx context.Context, guard forge.Guard, outcome string, fromSummary bool) {
	workspace := os.Getenv("MINOS_WORKSPACE")
	if workspace == "" {
		return
	}
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "minos: the reviewed change at %s could not be recorded: %v\n", guard.HeadSHA, err)
		return
	}
	path := reviewedChangePath(cfg, os.Getenv("MINOS_FORGE"), guard)
	err = func() error {
		files, err := changeFingerprints(ctx, workspace, guard.TargetSHA, guard.HeadSHA)
		if err != nil {
			return err
		}
		record, err := json.Marshal(reviewedChange{Head: guard.HeadSHA, Target: guard.TargetSHA, Outcome: outcome, Files: files, FromSummary: fromSummary})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		staging := path + ".next"
		if err := os.WriteFile(staging, append(record, '\n'), 0o600); err != nil {
			return err
		}
		return os.Rename(staging, path)
	}()
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "minos: the reviewed change at %s could not be recorded: %v\n", guard.HeadSHA, err)
	if removeErr := removeRecordFile(path); removeErr != nil {
		fmt.Fprintf(os.Stderr, "minos: the earlier reviewed change could not be removed either: %v; the next head may be compared with an older finished head\n", removeErr)
	}
}

// findEarlierHead names the head Minos last finished on before this one.
// A summary-backed cache preserves file fingerprints across rewritten
// commits. Without it, the summary gives the previous finished head, or
// the walk finds the newest commit with a terminal review or an enabled
// clean status. A historic cache without known summary provenance can
// still supply fingerprints when the forge confirms its outcome.
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
	var cached reviewedChange
	if raw, readErr := os.ReadFile(reviewedChangePath(cfg, os.Getenv("MINOS_FORGE"), guard)); readErr == nil {
		if json.Unmarshal(raw, &cached) != nil || cached.Head == guard.HeadSHA ||
			(cached.Outcome != "clean" && cached.Outcome != "attention") || cached.Files == nil {
			cached = reviewedChange{}
		}
	}
	commitStatus := runCommitStatus()
	fromCache := func() earlierHead {
		return earlierHead{Head: cached.Head, Outcome: cached.Outcome, Ancestor: ancestor(cached.Head), ChangedPaths: changedPaths(cached.Files, current)}
	}
	if cached.Head != "" && (cached.FromSummary || commitStatus) {
		return fromCache(), nil
	}
	snapshot, err := pullRequestSnapshot(ctx, adapter, guard.Repository, guard.PullRequest, commitStatus)
	if err != nil {
		return none, err
	}
	if summary, found := snapshot.SummaryRecord(); found {
		if head, state, finished := summary.LastFinished(); finished && head != guard.HeadSHA {
			completion, _ := state.Completion()
			if head == cached.Head && completion.Name() == cached.Outcome {
				return fromCache(), nil
			}
			earlier := earlierHead{Head: head, Outcome: completion.Name(), Ancestor: ancestor(head)}
			// The head may be gone from the clone, rewritten away; its
			// changed paths are then null, as for any pair not compared.
			if files, err := changeFingerprints(ctx, workspace, guard.TargetSHA, head); err == nil {
				earlier.ChangedPaths = changedPaths(files, current)
			}
			return earlier, nil
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-list", fmt.Sprintf("--max-count=%d", earlierHeadWalk+1), guard.TargetSHA+".."+guard.HeadSHA).Output()
	if err != nil {
		return none, fmt.Errorf("list the pull request's commits: %w", err)
	}
	reviews := make(map[string]forge.Review)
	for _, review := range snapshot.Reviews {
		if _, terminal := terminalState(review); terminal && review.User == snapshot.AuthenticatedUser && review.ID > reviews[review.CommitID].ID {
			reviews[review.CommitID] = review
		}
	}
	for _, sha := range strings.Fields(string(out)) {
		if sha == guard.HeadSHA {
			continue
		}
		var outcome string
		if state, terminal := terminalState(reviews[sha]); terminal {
			outcome = state.Name()
		} else if commitStatus {
			recorded, err := recordedOutcome(ctx, adapter, guard.Repository, sha)
			if err != nil {
				return none, err
			}
			// Admission can set attention without a finished review.
			if recorded == "clean" {
				outcome = recorded
			}
		}
		if outcome == "" {
			continue
		}
		if sha == cached.Head && outcome == cached.Outcome {
			return fromCache(), nil
		}
		found := earlierHead{Head: sha, Outcome: outcome, Ancestor: true}
		if files, err := changeFingerprints(ctx, workspace, guard.TargetSHA, sha); err == nil {
			found.ChangedPaths = changedPaths(files, current)
		}
		return found, nil
	}
	return none, nil
}
