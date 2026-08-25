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
			return "", err
		}
		snapshots[candidate.facts.PR] = snapshot
	}
	root := func(snapshot forge.Snapshot) string { return snapshot.TargetBranch }
	// Resolve a stacked target through the current list's head branches.
	for changed := true; changed; {
		changed = false
		for _, snapshot := range snapshots {
			for _, parent := range snapshots {
				if root(snapshot) == parent.HeadBranch && root(parent) != root(snapshot) {
					changed = true
				}
			}
		}
		break // roots below follow the chain without mutating snapshots
	}
	resolve := func(snapshot forge.Snapshot) string { return root(snapshot) }
	var resolveTarget func(forge.Snapshot, map[string]bool) string
	resolveTarget = func(snapshot forge.Snapshot, seen map[string]bool) string {
		if seen[snapshot.HeadBranch] {
			return snapshot.TargetBranch
		}
		seen[snapshot.HeadBranch] = true
		for _, parent := range snapshots {
			if snapshot.TargetBranch == parent.HeadBranch {
				return resolveTarget(parent, seen)
			}
		}
		return resolve(snapshot)
	}
	primarySnapshot, ok := snapshots[primary.PR]
	if !ok {
		return "[]", nil
	}
	primaryTarget := resolveTarget(primarySnapshot, map[string]bool{})
	members := make([]Facts, 0)
	for number, snapshot := range snapshots {
		if number == primary.PR || snapshot.State != "open" || snapshot.Merged || snapshot.Draft || snapshot.HeadRepository != repo.Owner+"/"+repo.Repo || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) || resolveTarget(snapshot, map[string]bool{}) != primaryTarget {
			continue
		}
		facts := Facts{Forge: primary.Forge, Owner: repo.Owner, Repo: repo.Repo, PR: number, HeadSHA: snapshot.HeadSHA, BaseRef: snapshot.TargetBranch, BaseSHA: snapshot.TargetSHA, HeadRef: snapshot.HeadBranch}
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

func groupMemberGuardPath(runsDir, unit string) string {
	return filepath.Join(runsDir, ".group-members", unit)
}
func groupMemberGuardActive(ctx context.Context, runsDir, memberUnit string) bool {
	data, err := os.ReadFile(groupMemberGuardPath(runsDir, memberUnit))
	if err != nil {
		return false
	}
	holder := strings.TrimSpace(string(data))
	if holder == "" {
		return false
	}
	active, err := activeRunUnitNames(ctx)
	if err != nil {
		return true
	}
	for _, unit := range active {
		if strings.TrimSuffix(unit, ".service") == holder {
			return true
		}
	}
	return false
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
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft || snapshot.HeadRepository != owner+"/"+name || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) {
		return Facts{}, fmt.Errorf("member is no longer eligible")
	}
	facts := Facts{Forge: primary.Forge, Owner: owner, Repo: name, PR: number, HeadSHA: snapshot.HeadSHA, BaseRef: snapshot.TargetBranch, BaseSHA: snapshot.TargetSHA, HeadRef: snapshot.HeadBranch}
	if groupMemberGuardActive(ctx, cfg.Runs.Dir, UnitName(facts)) {
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
