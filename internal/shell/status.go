package shell

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The status endpoint is a read-only projection of state a run already writes
// to its own directory: which pull requests are live, how far each has got,
// and the timing record collect-timings assembles from the same residue. It
// stores nothing, schedules nothing, and no decision reads it — it exists so
// an operator surface can show a run's progress without opening a shell on the
// box.
const statusDocumentKind = "minos-status-v1"

// errRunDirectoryAbsent separates a run whose directory has not appeared yet
// from one whose residue cannot be read. The first is how every run begins.
var errRunDirectoryAbsent = errors.New("run directory absent")

// A dispatched lifecycle stage announces itself by writing <name>-args.json
// before the workflow starts, and its completion flag <name>-result.done once
// the command exits — success or failure alike. The result itself,
// <name>-result.json, appears only on a clean exit, with a partial stdout
// capture left beside it otherwise. Deriving stages from that convention
// rather than from a fixed list means a lifecycle that grows a new dispatch
// is reported without a change here.
const (
	dispatchArgsSuffix = "-args.json"
	stageStarting      = "starting"
	stageRunning       = "running"
	stagePassed        = "passed"
	stageFailed        = "failed"
	stageUnknown       = "unknown"
)

type statusDocument struct {
	Kind          string                 `json:"kind"`
	GeneratedAt   string                 `json:"generated_at"`
	MaxConcurrent int                    `json:"max_concurrent"`
	Repos         []statusRepo           `json:"repos"`
	Runs          []statusRun            `json:"runs"`
	Sweep         *sweepDeferralDocument `json:"sweep,omitempty"`
}

const sweepDeferralDocumentKind = "minos-sweep-deferrals-v1"

// sweepDeferralDocument is the sweep's one-pass operator record. Its
// CompletedAt is deliberately written by the sweep, rather than inferred by
// the request that happens to read it.
type sweepDeferralDocument struct {
	Kind        string          `json:"kind"`
	CompletedAt string          `json:"completed_at"`
	Deferrals   []sweepDeferral `json:"deferrals"`
}

type sweepDeferral struct {
	Forge  string `json:"forge"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	PR     string `json:"pr"`
	Reason string `json:"reason"`
}

type statusRepo struct {
	Forge string `json:"forge"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

type statusRun struct {
	Unit      string          `json:"unit"`
	RunDir    string          `json:"run_dir"`
	Forge     string          `json:"forge,omitempty"`
	Owner     string          `json:"owner,omitempty"`
	Repo      string          `json:"repo,omitempty"`
	PR        string          `json:"pr,omitempty"`
	StartedAt string          `json:"started_at,omitempty"`
	Head      string          `json:"head,omitempty"`
	Stage     string          `json:"stage"`
	Stages    []statusStage   `json:"stages"`
	Timings   json.RawMessage `json:"timings"`
	Error     string          `json:"error,omitempty"`
}

type statusStage struct {
	Name       string `json:"name"`
	Attempt    int    `json:"attempt"`
	State      string `json:"state"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
	ExitStatus *int   `json:"exit_status,omitempty"`
}

// stepName is the lifecycle's one spelling for a run step. A first execution
// is its base name; subsequent executions use @N, beginning at @2.
type stepName struct {
	Base    string
	Attempt int
}

// parseStepName keeps residue consumers from guessing at old improvised step
// names. Base names are lower-case words separated by hyphens; @ only starts a
// repeat suffix, whose attempt number is at least two and has no leading zero.
func parseStepName(name string) (stepName, bool) {
	base, suffix, hasSuffix := strings.Cut(name, "@")
	if base == "" || strings.Contains(suffix, "@") || !validStepBase(base) {
		return stepName{}, false
	}
	if !hasSuffix {
		return stepName{Base: base, Attempt: 1}, true
	}
	if len(suffix) == 0 || suffix[0] == '0' {
		return stepName{}, false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return stepName{}, false
		}
	}
	attempt, err := strconv.Atoi(suffix)
	if err != nil || attempt < 2 {
		return stepName{}, false
	}
	return stepName{Base: base, Attempt: attempt}, true
}

func validStepBase(base string) bool {
	for index, character := range base {
		if character >= 'a' && character <= 'z' {
			continue
		}
		if character == '-' && index > 0 && index < len(base)-1 && base[index-1] != '-' && base[index+1] != '-' {
			continue
		}
		return false
	}
	return true
}

// orientationDocument is the run's own record of which pull request it serves,
// written during setup. Reading identity from it rather than from the unit
// name keeps the mapping exact: UnitName rewrites characters an owner or
// repository is allowed to contain.
type orientationDocument struct {
	Head   string `json:"head"`
	Source struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		PR    string `json:"pr"`
	} `json:"source"`
}

func handleStatus(ctx context.Context, cfg ServiceConfig, w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	token, err := ReadSecret(cfg.Listener.StatusTokenFile)
	if err != nil {
		http.Error(w, "status token unavailable", http.StatusInternalServerError)
		return err
	}
	if !authorisedStatusRequest(r, token) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return nil
	}
	document, err := statusSnapshot(ctx, cfg)
	if err != nil {
		http.Error(w, "status unavailable", http.StatusInternalServerError)
		return err
	}
	body, err := json.Marshal(document)
	if err != nil {
		http.Error(w, "status unavailable", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(body, '\n'))
	return nil
}

func authorisedStatusRequest(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	presented, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), []byte(token)) == 1
}

// statusSnapshot fails only when the live-run inventory itself cannot be read.
// A single unreadable run reports its error in place, so one broken run
// directory never hides the others.
func statusSnapshot(ctx context.Context, cfg ServiceConfig) (statusDocument, error) {
	units, err := activeRunUnitNames(ctx)
	if err != nil {
		return statusDocument{}, err
	}
	document := statusDocument{
		Kind:          statusDocumentKind,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		MaxConcurrent: cfg.MaxConcurrentRuns(),
		Repos:         configuredStatusRepos(cfg),
		Runs:          make([]statusRun, 0, len(units)),
		Sweep:         readSweepDeferrals(cfg),
	}
	for _, unitName := range units {
		document.Runs = append(document.Runs, describeRun(ctx, cfg, unitName))
	}
	return document, nil
}

func sweepDeferralsPath(cfg ServiceConfig) string {
	return filepath.Join(cfg.Runs.Dir, ".sweep-deferrals.json")
}

// A failed read is intentionally absent from the projection. A stale, whole
// record remains useful; a malformed one must not make the status request fail.
func readSweepDeferrals(cfg ServiceConfig) *sweepDeferralDocument {
	content, err := os.ReadFile(sweepDeferralsPath(cfg))
	if err != nil {
		return nil
	}
	var document sweepDeferralDocument
	if json.Unmarshal(content, &document) != nil || document.Kind != sweepDeferralDocumentKind {
		return nil
	}
	return &document
}

func configuredStatusRepos(cfg ServiceConfig) []statusRepo {
	configs, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return []statusRepo{}
	}
	repos := make([]statusRepo, 0, len(configs))
	for _, repo := range configs {
		repos = append(repos, statusRepo{Forge: repo.Forge, Owner: repo.Owner, Repo: repo.Repo})
	}
	return repos
}

// describeRun reports a run from the residue it has written so far. A run
// that has only just started has written almost none of it — the run
// directory appears before the unit does its first work, and orientation.json
// only lands once setup has resolved which head it is reviewing. Neither
// absence is a fault, so both report the run as starting rather than as
// unreadable: identity comes from the unit name in the meantime, which is
// enough to say which pull request is under way.
func describeRun(ctx context.Context, cfg ServiceConfig, unitName string) statusRun {
	unit := strings.TrimSuffix(unitName, ".service")
	run := statusRun{
		Unit:      unitName,
		StartedAt: unitActiveEnterTime(ctx, unitName),
		Stage:     stageStarting,
		Stages:    []statusStage{},
	}
	run.Forge, run.Owner, run.Repo, run.PR = identityFromUnit(cfg, unit)
	runDir, err := runDirectoryForUnit(cfg, unit)
	if err != nil {
		if !errors.Is(err, errRunDirectoryAbsent) {
			run.Error = err.Error()
		}
		return run
	}
	run.RunDir = runDir
	orientation, err := readRunOrientation(runDir)
	switch {
	case err == nil:
		run.Forge = orientationForge(cfg, orientation)
		run.Owner = orientation.Source.Owner
		run.Repo = orientation.Source.Repo
		run.PR = orientation.Source.PR
		run.Head = orientation.Head
	case !errors.Is(err, os.ErrNotExist):
		run.Error = err.Error()
	}
	run.Stages = runStages(runDir)
	if stage := currentStage(run.Stages); stage != stageUnknown {
		run.Stage = stage
	}
	run.Timings = runTimings(ctx, cfg, runDir, run)
	return run
}

// runDirectoryForUnit resolves the single run directory a live unit owns.
// Spawn names it "<unit>-<random>", and a unit that is live has exactly one;
// more than one means an earlier attempt's residue outlived its unit, which is
// reported rather than guessed at.
func runDirectoryForUnit(cfg ServiceConfig, unit string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(cfg.Runs.Dir, unit+"-*"))
	if err != nil {
		return "", fmt.Errorf("locate run directory for %s: %w", unit, err)
	}
	directories := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.IsDir() {
			directories = append(directories, match)
		}
	}
	switch len(directories) {
	case 0:
		return "", fmt.Errorf("%w: %s", errRunDirectoryAbsent, unit)
	case 1:
		return directories[0], nil
	default:
		sort.Strings(directories)
		return "", fmt.Errorf("%d run directories for %s", len(directories), unit)
	}
}

// identityFromUnit recovers which pull request a unit serves before the run
// has written anything saying so. The unit name is built from the pull
// request by UnitName, which rewrites characters an owner or repository may
// contain — so rather than split the name back apart, each configured
// repository's own unit name is recomputed and compared.
func identityFromUnit(cfg ServiceConfig, unit string) (forge, owner, repo, pr string) {
	number, found := strings.CutPrefix(unit[strings.LastIndex(unit, "-pr")+1:], "pr")
	if !found {
		return "", "", "", ""
	}
	repos, err := LoadRepoConfigs(cfg.Root)
	if err != nil {
		return "", "", "", ""
	}
	for _, configured := range repos {
		facts := Facts{Owner: configured.Owner, Repo: configured.Repo, PR: number}
		if UnitName(facts) == unit {
			return configured.Forge, configured.Owner, configured.Repo, number
		}
	}
	return "", "", "", ""
}

func readRunOrientation(runDir string) (orientationDocument, error) {
	content, err := os.ReadFile(filepath.Join(runDir, "orientation.json"))
	if err != nil {
		return orientationDocument{}, err
	}
	var orientation orientationDocument
	if err := json.Unmarshal(content, &orientation); err != nil {
		return orientationDocument{}, fmt.Errorf("parse orientation: %w", err)
	}
	return orientation, nil
}

// orientationForge names which configured forge the run's repository belongs
// to. Orientation records the pull request, not the forge, so the answer comes
// from the repository configuration that admitted the run; a single configured
// forge answers on its own.
func orientationForge(cfg ServiceConfig, orientation orientationDocument) string {
	repos, err := LoadRepoConfigs(cfg.Root)
	if err == nil {
		for _, repo := range repos {
			if repo.Owner == orientation.Source.Owner && repo.Repo == orientation.Source.Repo {
				return repo.Forge
			}
		}
	}
	if len(cfg.Forges) == 1 {
		for name := range cfg.Forges {
			return name
		}
	}
	return ""
}

// runStages reports every lifecycle stage the run directory shows evidence of,
// oldest first: each dispatched workflow follows the args/flag/result
// convention the lifecycle prescribes.
func runStages(runDir string) []statusStage {
	stages := dispatchStages(runDir)
	sort.SliceStable(stages, func(i, j int) bool {
		return stages[i].StartedAt < stages[j].StartedAt
	})
	return stages
}

func dispatchStages(runDir string) []statusStage {
	matches, err := filepath.Glob(filepath.Join(runDir, "*"+dispatchArgsSuffix))
	if err != nil {
		return nil
	}
	stages := make([]statusStage, 0, len(matches))
	for _, match := range matches {
		rawName := strings.TrimSuffix(filepath.Base(match), dispatchArgsSuffix)
		name, ok := parseStepName(rawName)
		if !ok {
			continue
		}
		stage := statusStage{Name: name.Base, Attempt: name.Attempt, State: stageRunning, StartedAt: fileModifiedTime(match)}
		// The result file is published only on a clean exit, so its presence
		// settles the stage on its own. The completion flag is written
		// whatever became of the command, but only a dispatch waited on in
		// the background carries one — so a flag standing alone is a stage
		// that ran and failed, while neither file means it is still going.
		if ended := fileModifiedTime(filepath.Join(runDir, rawName+"-result.json")); ended != "" {
			stage.EndedAt = ended
			stage.State = stagePassed
		} else if ended := fileModifiedTime(filepath.Join(runDir, rawName+"-result.done")); ended != "" {
			stage.EndedAt = ended
			stage.State = stageFailed
		}
		stages = append(stages, stage)
	}
	return stages
}

// currentStage names what the run is doing now: the most recently started
// stage still running, or failing that the most recently finished one, so a
// run between stages reports where it just was rather than nothing at all.
func currentStage(stages []statusStage) string {
	current := stageUnknown
	for _, stage := range stages {
		if stage.State == stageRunning {
			current = stage.Name
		}
	}
	if current != stageUnknown {
		return current
	}
	for _, stage := range stages {
		if stage.State != stageRunning {
			current = stage.Name
		}
	}
	return current
}

// runTimings hands back the timing record collect-timings assembles from the
// run's event log and the Ensemble run records already on disk — command spans
// and per-worker engine, model and status. It is presentation residue: a run
// whose record cannot be assembled reports a null timing block rather than
// failing the request.
func runTimings(ctx context.Context, cfg ServiceConfig, runDir string, run statusRun) json.RawMessage {
	if cfg.Runs.TimingsCommand == "" {
		return nil
	}
	// collect-timings publishes through a temporary file and a rename, so it
	// needs a real path to write to, and it names the pull request from the
	// run environment a run body would have given it.
	destination, err := os.CreateTemp("", "minos-timings-*.json")
	if err != nil {
		return nil
	}
	path := destination.Name()
	_ = destination.Close()
	defer func() { _ = os.Remove(path) }()
	if _, err := commandCombinedOutput(ctx, "env",
		"MINOS_OWNER="+run.Owner, "MINOS_REPO_NAME="+run.Repo, "MINOS_PR="+run.PR,
		cfg.Runs.TimingsCommand, runDir, path); err != nil {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil || !json.Valid(content) {
		return nil
	}
	return json.RawMessage(content)
}

func unitActiveEnterTime(ctx context.Context, unitName string) string {
	out, err := commandCombinedOutput(ctx, "systemctl", "--user", "show", unitName,
		"--property=ActiveEnterTimestamp", "--value", "--timestamp=unix")
	if err != nil {
		return ""
	}
	seconds, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(out)), "@"), 10, 64)
	if err != nil || seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func fileModifiedTime(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}
