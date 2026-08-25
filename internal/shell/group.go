package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
)

// groupCandidatesJSON surfaces sweep siblings only.  It deliberately makes no
// judgement about which (if any) should become members.
func groupCandidatesJSON(ctx context.Context, cfg ServiceConfig, repo RepoConfig, primary Facts, all []sweepCandidate) (string, error) {
	eligibleInRepo := 0
	for _, candidate := range all {
		if candidate.repo.Forge == repo.Forge && candidate.repo.Owner == repo.Owner && candidate.repo.Repo == repo.Repo {
			eligibleInRepo++
		}
	}
	if eligibleInRepo < 2 {
		return "[]", nil
	}
	adapter, err := newBehaviouralForge(cfg, primary.Forge)
	if err != nil {
		return "", err
	}
	snapshots := map[string]forge.Snapshot{}
	for _, candidate := range all {
		if candidate.repo.Forge != repo.Forge || candidate.repo.Owner != repo.Owner || candidate.repo.Repo != repo.Repo {
			continue
		}
		pr, err := strconv.ParseInt(candidate.facts.PR, 10, 64)
		if err != nil {
			continue
		}
		snapshot, err := adapter.Snapshot(ctx, forge.Repository{Owner: repo.Owner, Name: repo.Repo}, pr)
		if err != nil {
			// Candidate surfacing is optional input to the lead, never a reason to
			// abandon reconciliation of the pull request being considered.
			continue
		}
		snapshots[candidate.facts.PR] = snapshot
	}
	primarySnapshot, ok := snapshots[primary.PR]
	if !ok {
		return "[]", nil
	}
	primaryTarget := resolvedGroupTarget(primarySnapshot, snapshots, map[string]bool{})
	members := make([]Facts, 0)
	for number, snapshot := range snapshots {
		if number == primary.PR || snapshot.State != "open" || snapshot.Merged || snapshot.Draft || snapshot.HeadRepository != repo.Owner+"/"+repo.Repo || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) || resolvedGroupTarget(snapshot, snapshots, map[string]bool{}) != primaryTarget {
			continue
		}
		facts := Facts{Forge: primary.Forge, Owner: repo.Owner, Repo: repo.Repo, PR: number, HeadSHA: snapshot.HeadSHA, BaseRef: snapshot.TargetBranch, BaseSHA: snapshot.TargetSHA, HeadRef: snapshot.HeadBranch}
		eligible, eligibilityErr := groupMemberEligible(ctx, cfg, repo, facts, adapter, snapshot)
		if eligibilityErr != nil || !eligible {
			continue
		}
		if groupMemberGuardActive(ctx, cfg.Runs.Dir, UnitName(facts)) {
			continue
		}
		members = append(members, facts)
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// groupMemberEligible is the non-writing admission predicate shared by
// candidate surfacing and member claiming. Reconciliation may repair a missing
// terminal status; grouping must never re-claim a pull request with a terminal
// Minos result while that repair is pending.
func groupMemberEligible(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot) (bool, error) {
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft || snapshot.HeadRepository != facts.Owner+"/"+facts.Repo || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) || workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
		return false, nil
	}
	pr, err := strconv.ParseInt(facts.PR, 10, 64)
	if err != nil {
		return false, err
	}
	repository := forge.Repository{Owner: facts.Owner, Name: facts.Repo}
	commits, err := adapter.PullRequestCommits(ctx, repository, pr)
	if err != nil {
		return false, err
	}
	if review, reviewed := trustedReview(snapshot, commits, cfg.Service.BotLogin); reviewed {
		if _, terminal := terminalState(review); terminal && !checkCausedVerdictSpent(review, snapshot) {
			return false, nil
		}
	}
	if trustedCompletedRunStatus(ctx, adapter, repository, snapshot, commits, cfg.Service.BotLogin, statusTargetURL(cfg.Forges[facts.Forge].APIBase, facts), currentEnvironmentStamp(cfg)) {
		return false, nil
	}
	if _, deferred := dependencyDeferral(snapshot); deferred {
		return false, nil
	}
	return true, nil
}

func resolvedGroupTarget(snapshot forge.Snapshot, snapshots map[string]forge.Snapshot, seen map[string]bool) string {
	if seen[snapshot.HeadBranch] {
		return snapshot.TargetBranch
	}
	seen[snapshot.HeadBranch] = true
	for _, parent := range snapshots {
		if snapshot.TargetBranch == parent.HeadBranch {
			return resolvedGroupTarget(parent, snapshots, seen)
		}
	}
	return snapshot.TargetBranch
}

func groupMemberGuardPath(runsDir, unit string) string {
	return filepath.Join(runsDir, ".group-members", unit)
}
func groupMemberGuardActive(ctx context.Context, runsDir, memberUnit string) bool {
	active, err := activeRunUnitNames(ctx)
	if err != nil {
		return true
	}
	_, guarded := liveGroupMemberHolder(runsDir, memberUnit, active)
	return guarded
}

func liveGroupMemberHolder(runsDir, memberUnit string, active []string) (string, bool) {
	data, err := os.ReadFile(groupMemberGuardPath(runsDir, memberUnit))
	if err != nil {
		return "", false
	}
	holder := strings.TrimSpace(string(data))
	for _, unit := range active {
		if strings.TrimSuffix(unit, ".service") == holder {
			return unit, true
		}
	}
	return "", false
}

func sweepDeadGroupMemberGuards(runsDir string, active []string) error {
	paths, err := filepath.Glob(filepath.Join(runsDir, ".group-members", "*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, live := liveGroupMemberHolder(runsDir, filepath.Base(path), active); !live {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func claimGroupMember(ctx context.Context, cfg ServiceConfig, repo RepoConfig, primary Facts, owner, name, number string) (Facts, error) {
	if owner != repo.Owner || name != repo.Repo {
		return Facts{}, fmt.Errorf("member is outside this run repository")
	}
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return Facts{}, err
	}
	defer unlock()
	pr, err := strconv.ParseInt(number, 10, 64)
	if err != nil {
		return Facts{}, err
	}
	adapter, err := newBehaviouralForge(cfg, primary.Forge)
	if err != nil {
		return Facts{}, err
	}
	snapshot, err := adapter.Snapshot(ctx, forge.Repository{Owner: owner, Name: name}, pr)
	if err != nil {
		return Facts{}, err
	}
	facts := Facts{Forge: primary.Forge, Owner: owner, Repo: name, PR: number, HeadSHA: snapshot.HeadSHA, BaseRef: snapshot.TargetBranch, BaseSHA: snapshot.TargetSHA, HeadRef: snapshot.HeadBranch}
	eligible, err := groupMemberEligible(ctx, cfg, repo, facts, adapter, snapshot)
	if err != nil {
		return Facts{}, err
	}
	if !eligible {
		return Facts{}, fmt.Errorf("member is no longer eligible")
	}
	active, err := activeRunUnitNames(ctx)
	if err != nil {
		return Facts{}, err
	}
	for _, unit := range active {
		if strings.TrimSuffix(unit, ".service") == UnitName(facts) {
			return Facts{}, fmt.Errorf("member already has a live run")
		}
	}
	if _, guarded := liveGroupMemberHolder(cfg.Runs.Dir, UnitName(facts), active); guarded {
		return Facts{}, fmt.Errorf("member is guarded by a live run")
	}
	if err := os.MkdirAll(filepath.Join(cfg.Runs.Dir, ".group-members"), 0o700); err != nil {
		return Facts{}, err
	}
	if err := os.WriteFile(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts)), []byte(UnitName(primary)+"\n"), 0o600); err != nil {
		return Facts{}, err
	}
	result := adapter.Claim(ctx, forge.Repository{Owner: owner, Name: name}, pr)
	if result.Outcome != forge.WriteApplied {
		_ = os.Remove(groupMemberGuardPath(cfg.Runs.Dir, UnitName(facts)))
		return Facts{}, fmt.Errorf("claim member: %s", result.Reason)
	}
	return facts, nil
}
