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
// the service keeps a record per pull request: the reviewed change, the
// command record and the walkthrough. Each is named by reviewedChangePrefix
// and the pull request's number, and all go when the pull request does
// (removePullRequestRecords).
func pullRequestRecordDirs(cfg ServiceConfig) []string {
	return []string{filepath.Join(cfg.Runs.Dir, ".reviewed"), commandRecordDir(cfg), walkthroughRecordDir(cfg)}
}

// removePullRequestRecords removes every record the service keeps for one
// pull request.
func removePullRequestRecords(cfg ServiceConfig, forgeName string, guard forge.Guard) error {
	return errors.Join(
		removeReviewedChange(cfg, forgeName, guard),
		removeCommandRecord(cfg, forgeName, guard),
		removeWalkthroughRecord(walkthroughRecordPath(cfg, forgeName, guard)),
	)
}

// walkthroughRecordPath is where the service keeps the last walkthrough
// published for one pull request (workflows/walkthrough.mjs). The run reads
// it to revise and show it and writes it once a newer one is published, so
// the service only names it to the run and removes it. The record is a
// cache: losing it costs the display of the earlier walkthrough until the
// next reviewed head, and nothing else.
func walkthroughRecordPath(cfg ServiceConfig, forgeName string, guard forge.Guard) string {
	return filepath.Join(walkthroughRecordDir(cfg), reviewedChangePrefix(forgeName, guard.Repository)+strconv.FormatInt(guard.PullRequest, 10)+".json")
}

func removeWalkthroughRecord(path string) error {
	// Remove the partial first so an in-progress rename cannot recreate
	// the finished record between the two removals.
	return errors.Join(removeRecordFile(path+".partial"), removeRecordFile(path))
}

func walkthroughRecordDir(cfg ServiceConfig) string {
	return filepath.Join(cfg.Runs.Dir, ".walkthroughs")
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
			name := entry.Name()
			if dir == walkthroughRecordDir(cfg) {
				name = strings.TrimSuffix(name, ".partial")
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			stem := strings.TrimSuffix(name, ".json")
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
			err = removePullRequestRecords(cfg, repo.Forge, guard)
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
func recordReviewedChange(ctx context.Context, guard forge.Guard, outcome string) {
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
		record, err := json.Marshal(reviewedChange{Head: guard.HeadSHA, Target: guard.TargetSHA, Outcome: outcome, Files: files})
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
// The kept record answers first, whatever became of its commit: both the
// scripted run's finished summary comment and the resident lead's terminal
// status write it, so it is the newest of the two when both have written.
// Without it, the summary comment's record names the last finished head,
// which it carries while a later head is under way. A pull request with
// neither, finished only by statuses and reviews before the summary
// comment, has the pull request's own commits asked, newest first: a
// commit carrying the service's request-changes review finished as
// attention, and one whose latest service status is clean finished clean.
// An attention status alone is not enough, because the service also sets
// it on a head whose review never completed.
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
	snapshot, err := adapter.Snapshot(ctx, guard.Repository, guard.PullRequest)
	if err != nil {
		return none, err
	}
	if summary, found := snapshot.SummaryRecord(); found {
		if head, state, finished := summary.LastFinished(); finished && head != guard.HeadSHA {
			completion, _ := state.Completion()
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
