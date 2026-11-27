package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BFJ-Concerns/Minos/internal/forge"
)

// The upkeep of repository descriptions. Each sweep pass asks, of every
// repository with description.enabled on, whether its description is due
// (repositoryDescriptionDue) against the commit its default branch stands
// at now, and starts one description unit for a repository whose
// description is due. The unit runs `minos describe`, which runs the
// describe body beside the repository's run body and keeps the stage's
// answer as the repository's record. The pass also removes the record of
// every repository that has the setting off or is no longer enrolled.
//
// A description unit is named outside the minos-run- prefix: it serves no
// pull request, so it takes no run place under runs.max-concurrent, and the
// residue sweep, the recent-runs listing and the sweep's ordering, which
// all key on that prefix, never take it for a run.

const descriptionUnitPrefix = "minos-describe-"

// describeBodyName is the describe body's file name. It is found beside the
// repository's configured run body, the directory the shipped scripts are
// installed in together.
const describeBodyName = "describe-body"

// descriptionRetryDelay is how long a failed description of one commit
// waits before it is tried again. A default branch that has moved since is
// described at once; the delay keeps a description that fails every time,
// an answer the stage cannot shorten enough say, from being paid for on
// every sweep pass.
const descriptionRetryDelay = time.Hour

// descriptionResultName is the stage's answer as dispatch-stage leaves it
// in the unit's directory.
const descriptionResultName = "repository-description-result.json"

// descriptionUnitName is the unit that describes repo, one per repository,
// so systemd itself refuses a second while one is live.
func descriptionUnitName(repo RepoConfig) string {
	name := descriptionUnitPrefix + repo.Forge + "-" + repo.Owner + "-" + repo.Repo
	key := strings.Join([]string{repo.Forge, repo.Owner, repo.Repo}, "\x00")
	return boundedUnitNamePart(name, key, maxRunUnitStem)
}

func describeBodyPath(repo RepoConfig) string {
	return filepath.Join(filepath.Dir(repo.Adaptation.RunBody), describeBodyName)
}

// descriptionFailurePath records the commit whose description last failed
// and when, beside the repository's record.
func descriptionFailurePath(cfg ServiceConfig, repo RepoConfig) string {
	return strings.TrimSuffix(repositoryDescriptionPath(cfg, repo), ".json") + ".failed"
}

// DescriptionFailure records the default branch commit and time of a failed write.
type DescriptionFailure struct {
	Commit   string    `json:"commit"`
	FailedAt time.Time `json:"failed_at"`
}

func recordDescriptionFailure(cfg ServiceConfig, repo RepoConfig, commit string, now time.Time) error {
	path := descriptionFailurePath(cfg, repo)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	failure := DescriptionFailure{Commit: commit, FailedAt: now.UTC().Truncate(time.Second)}
	return publishProjectionDocument(dir, filepath.Base(path), failure, os.Rename)
}

func readDescriptionFailure(cfg ServiceConfig, repo RepoConfig) (*DescriptionFailure, error) {
	raw, err := os.ReadFile(descriptionFailurePath(cfg, repo))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var failure DescriptionFailure
	if err := json.Unmarshal(raw, &failure); err != nil {
		return nil, fmt.Errorf("decode repository description failure: %w", err)
	}
	if failure.Commit == "" {
		return nil, fmt.Errorf("repository description failure has no commit")
	}
	if failure.FailedAt.IsZero() {
		return nil, fmt.Errorf("repository description failure has no timestamp")
	}
	return &failure, nil
}

// descriptionRetryHeld reports whether the description of commit failed
// within descriptionRetryDelay of now. An unreadable failure record holds
// nothing back.
func descriptionRetryHeld(cfg ServiceConfig, repo RepoConfig, commit string, now time.Time) bool {
	failure, err := readDescriptionFailure(cfg, repo)
	if err != nil || failure == nil || failure.Commit != commit {
		return false
	}
	return now.Sub(failure.FailedAt) < descriptionRetryDelay
}

// activeDescriptionUnitNames lists the live description units, without
// their .service suffix.
func activeDescriptionUnitNames(ctx context.Context) (map[string]bool, error) {
	out, err := commandCombinedOutput(ctx, "systemctl", "--user", "list-units",
		"--type=service", "--state=activating,active", "--no-legend", "--plain", "--full", "--no-pager", descriptionUnitPrefix+"*.service")
	if err != nil {
		return nil, fmt.Errorf("inspect active description units: %w: %s", err, strings.TrimSpace(string(out)))
	}
	names := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			names[strings.TrimSuffix(fields[0], ".service")] = true
		}
	}
	return names, scanner.Err()
}

// defaultBranchTip is the forge read the sweep asks of each repository with
// its description on; tests replace it.
var defaultBranchTip = func(ctx context.Context, cfg ServiceConfig, repo RepoConfig) (forge.BranchTip, error) {
	adapter, err := newBehaviouralForge(cfg, repo.Forge)
	if err != nil {
		return forge.BranchTip{}, err
	}
	return adapter.DefaultBranchTip(ctx, forge.Repository{Owner: repo.Owner, Name: repo.Repo})
}

// reconcileRepositoryDescriptions is the sweep's pass over descriptions:
// residue of dead description units is removed, the record of every
// repository not enrolled with the setting on is removed, and a unit is
// started for each enabled repository whose description is due. One
// repository's failure is logged and the others are still served.
func reconcileRepositoryDescriptions(ctx context.Context, cfg ServiceConfig, repos []RepoConfig) error {
	active, err := activeDescriptionUnitNames(ctx)
	if err != nil {
		return err
	}
	var passErrors []error
	if err := removeDescriptionResidue(cfg, active); err != nil {
		passErrors = append(passErrors, err)
	}
	if err := removeUnwantedDescriptions(cfg, repos); err != nil {
		passErrors = append(passErrors, err)
	}
	for _, repo := range repos {
		if !repo.Description.Enabled {
			continue
		}
		slug := repo.Forge + "/" + repo.Owner + "/" + repo.Repo
		started, err := describeIfDue(ctx, cfg, repo, active)
		if err != nil {
			log.Printf("%s: repository description: %v", slug, err)
			continue
		}
		if started != "" {
			log.Printf("%s: started its repository description at %s", slug, started)
		}
	}
	return errors.Join(passErrors...)
}

// describeIfDue starts repo's description unit when its description is due
// and returns the commit it was started for, or "" when none was started.
func describeIfDue(ctx context.Context, cfg ServiceConfig, repo RepoConfig, active map[string]bool) (string, error) {
	if active[descriptionUnitName(repo)] {
		return "", nil
	}
	tip, err := defaultBranchTip(ctx, cfg, repo)
	if err != nil {
		return "", fmt.Errorf("read the default branch's tip: %w", err)
	}
	if tip.Commit == "" {
		return "", nil
	}
	kept, err := readRepositoryDescription(repositoryDescriptionPath(cfg, repo))
	if err != nil {
		log.Printf("%s/%s/%s: %v; writing it again", repo.Forge, repo.Owner, repo.Repo, err)
		kept = nil
	}
	if !repositoryDescriptionDue(kept, tip.Commit) || descriptionRetryHeld(cfg, repo, tip.Commit, time.Now()) {
		return "", nil
	}
	if err := spawnDescription(ctx, cfg, repo, tip.Commit); err != nil {
		return "", err
	}
	return tip.Commit, nil
}

// spawnDescription starts the description unit for repo at commit, in a
// directory of its own under the runs directory.
func spawnDescription(ctx context.Context, cfg ServiceConfig, repo RepoConfig, commit string) error {
	body := describeBodyPath(repo)
	if info, err := os.Stat(body); err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("no executable %s beside the run body %s", describeBodyName, repo.Adaptation.RunBody)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	unit := descriptionUnitName(repo)
	runDir, err := os.MkdirTemp(cfg.Runs.Dir, unit+"-")
	if err != nil {
		return err
	}
	routing, err := json.Marshal(repo.Routing.Roles())
	if err != nil {
		_ = removeRunDir(runDir)
		return fmt.Errorf("encode MINOS_ROUTING: %w", err)
	}
	forgeConfig := cfg.Forges[repo.Forge]
	env := map[string]string{
		"MINOS_CONFIG":                cfg.Root,
		"MINOS_RUN_DIR":               runDir,
		"MINOS_FORGE":                 repo.Forge,
		"MINOS_OWNER":                 repo.Owner,
		"MINOS_REPO_NAME":             repo.Repo,
		"MINOS_API_BASE":              forgeConfig.APIBase,
		"MINOS_WEB_BASE":              forgeConfig.WebBase,
		"MINOS_CREDENTIAL_FILE":       forgeConfig.CredentialFile,
		"MINOS_DESCRIPTION_COMMIT":    commit,
		"MINOS_DESCRIPTION_RECORD":    repositoryDescriptionPath(cfg, repo),
		"MINOS_DESCRIBE_BODY":         body,
		"MINOS_ROUTING":               string(routing),
		"ENSEMBLE_CONCURRENCY_CLAUDE": strconv.Itoa(cfg.Ensemble.ConcurrencyClaude),
		"ENSEMBLE_CONCURRENCY_CODEX":  strconv.Itoa(cfg.Ensemble.ConcurrencyCodex),
	}
	if cfg.Ensemble.AgentCeiling > 0 {
		env["ENSEMBLE_AGENT_CEILING"] = strconv.Itoa(cfg.Ensemble.AgentCeiling)
	}
	args := []string{
		"--user", "--collect", "--unit", unit,
		"--property=ExitType=main",
		"--property=KillMode=control-group",
		"--property=RuntimeMaxSec=" + cfg.runDurationCeiling(),
		"--slice=" + runsSliceUnit,
		"--property=MemoryMax=" + fmt.Sprintf("%dG", cfg.runMemoryEnvelopeGiB()),
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "--setenv", key+"="+env[key])
	}
	args = append(args, exe, "describe", "--config", cfg.Root)
	if out, err := commandCombinedOutput(ctx, "systemd-run", args...); err != nil {
		_ = removeRunDir(runDir)
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeDescriptionResidue removes each description unit's directory whose
// unit is no longer live: one stopped before `minos describe` could remove
// its own.
func removeDescriptionResidue(cfg ServiceConfig, active map[string]bool) error {
	entries, err := os.ReadDir(cfg.Runs.Dir)
	if err != nil {
		return fmt.Errorf("read runs directory: %w", err)
	}
	var residueErrors []error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), descriptionUnitPrefix) || runDirectoryHasActiveUnit(entry.Name(), active) {
			continue
		}
		if err := removeRunDir(filepath.Join(cfg.Runs.Dir, entry.Name())); err != nil {
			residueErrors = append(residueErrors, fmt.Errorf("remove description residue %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(residueErrors...)
}

// removeUnwantedDescriptions removes every kept record, and its failure
// record, that belongs to no repository enrolled with its description on.
func removeUnwantedDescriptions(cfg ServiceConfig, repos []RepoConfig) error {
	wanted := map[string]bool{}
	for _, repo := range repos {
		if repo.Description.Enabled {
			wanted[repositoryDescriptionPath(cfg, repo)] = true
			wanted[descriptionFailurePath(cfg, repo)] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Runs.Dir, ".descriptions"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read kept descriptions: %w", err)
	}
	var removeErrors []error
	for _, entry := range entries {
		path := filepath.Join(cfg.Runs.Dir, ".descriptions", entry.Name())
		if entry.IsDir() || wanted[path] {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			removeErrors = append(removeErrors, fmt.Errorf("remove description %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(removeErrors...)
}

// DescribeCommand is the description unit's own process. It runs the
// describe body and keeps the description it answers with as the
// repository's record, then removes the unit's directory. The record is
// kept only while the repository is still enrolled with its description
// on: one switched off while the unit ran keeps nothing.
func DescribeCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	configRoot := fs.String("config", DefaultConfigRoot, "configuration root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runDir, body, commit := os.Getenv("MINOS_RUN_DIR"), os.Getenv("MINOS_DESCRIBE_BODY"), os.Getenv("MINOS_DESCRIPTION_COMMIT")
	forgeName, owner, name := os.Getenv("MINOS_FORGE"), os.Getenv("MINOS_OWNER"), os.Getenv("MINOS_REPO_NAME")
	if runDir == "" || body == "" || commit == "" || forgeName == "" || owner == "" || name == "" {
		return fmt.Errorf("MINOS_RUN_DIR, MINOS_DESCRIBE_BODY, MINOS_DESCRIPTION_COMMIT, MINOS_FORGE, MINOS_OWNER and MINOS_REPO_NAME are required")
	}
	defer func() {
		if err := removeRunDir(runDir); err != nil {
			log.Printf("remove description directory %s: %v", runDir, err)
		}
	}()
	cmd := exec.CommandContext(ctx, body)
	cmd.Dir = runDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	cfg, err := LoadServiceConfig(*configRoot)
	if err != nil {
		return errors.Join(runErr, err)
	}
	repos, err := LoadRepoConfigs(cfg)
	if err != nil {
		return errors.Join(runErr, err)
	}
	index := slices.IndexFunc(repos, func(repo RepoConfig) bool {
		return repo.Forge == forgeName && repo.Owner == owner && repo.Repo == name
	})
	if index < 0 || !repos[index].Description.Enabled {
		log.Printf("%s/%s/%s no longer has its description on; nothing is kept", forgeName, owner, name)
		return runErr
	}
	repo := repos[index]
	description, err := describedRepository(runDir, runErr)
	if err == nil {
		err = keepRepositoryDescription(repositoryDescriptionPath(cfg, repo), commit, description)
	}
	if err != nil {
		if recordErr := recordDescriptionFailure(cfg, repo, commit, time.Now()); recordErr != nil {
			log.Printf("record the failed description: %v", recordErr)
		}
		printStageLogTail(filepath.Join(runDir, "repository-description.log"))
		return fmt.Errorf("describe %s/%s/%s at %s: %w", forgeName, owner, name, commit, err)
	}
	if err := os.Remove(descriptionFailurePath(cfg, repo)); err != nil && !os.IsNotExist(err) {
		log.Printf("clear the earlier failed description: %v", err)
	}
	log.Printf("kept the description of %s/%s/%s at %s", forgeName, owner, name, commit)
	return nil
}

// describedRepository is the description the stage answered with, read
// from the result dispatch-stage published.
func describedRepository(runDir string, runErr error) (string, error) {
	if runErr != nil {
		return "", fmt.Errorf("the describe body failed: %w", runErr)
	}
	raw, err := os.ReadFile(filepath.Join(runDir, descriptionResultName))
	if err != nil {
		return "", fmt.Errorf("the description stage left no answer: %w", err)
	}
	var result struct {
		Stage  string `json:"stage"`
		Answer struct {
			Description string `json:"description"`
		} `json:"answer"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("the description stage's answer is not JSON: %w", err)
	}
	if result.Stage != "repository-description" || strings.TrimSpace(result.Answer.Description) == "" {
		return "", fmt.Errorf("the description stage's answer holds no description")
	}
	return result.Answer.Description, nil
}

// printStageLogTail puts the end of the stage's diagnostics in the unit's
// journal, since the directory that holds them is about to go.
func printStageLogTail(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "repository-description.log: %s\n", line)
	}
}
