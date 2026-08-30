package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"bfj/minos/internal/forge"
)

// groupCandidatesJSON surfaces sweep siblings only.  It deliberately makes no
// judgement about which (if any) should become members.
// groupCandidatesByPrimary snapshots each sweep candidate once and returns the
// lead-facing sibling list for every primary. The active-unit snapshot comes
// from the sweep so candidates already running on their own are not surfaced.
type groupedCandidateSnapshot struct {
	adapter  *forge.Adapter
	snapshot forge.Snapshot
}

func groupCandidatesByPrimary(ctx context.Context, cfg ServiceConfig, all []sweepCandidate, active []string) (map[string]string, map[string]groupedCandidateSnapshot, error) {
	groups := make(map[string]string, len(all))
	snapshots := make(map[string]groupedCandidateSnapshot, len(all))
	for _, candidate := range all {
		groups[UnitName(candidate.facts)] = "[]"
	}
	byRepo := make(map[string][]sweepCandidate)
	repos := make(map[string]RepoConfig)
	for _, candidate := range all {
		key := candidate.repo.Forge + "\x00" + candidate.repo.Owner + "\x00" + candidate.repo.Repo
		byRepo[key] = append(byRepo[key], candidate)
		repos[key] = candidate.repo
	}
	for key, candidates := range byRepo {
		repo := repos[key]
		if err := groupCandidatesForRepo(ctx, cfg, repo, candidates, active, groups, snapshots); err != nil {
			return nil, nil, err
		}
	}
	return groups, snapshots, nil
}

func groupCandidatesForRepo(ctx context.Context, cfg ServiceConfig, repo RepoConfig, candidates []sweepCandidate, active []string, groups map[string]string, passSnapshots map[string]groupedCandidateSnapshot) error {
	if len(candidates) < 2 {
		return nil
	}
	adapter, err := newBehaviouralForge(cfg, repo.Forge)
	if err != nil {
		return err
	}
	snapshots := map[string]forge.Snapshot{}
	for _, candidate := range candidates {
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
		passSnapshots[UnitName(candidate.facts)] = groupedCandidateSnapshot{adapter: adapter, snapshot: snapshot}
	}
	membersByTarget := make(map[string][]Facts)
	for number, snapshot := range snapshots {
		facts := Facts{Forge: repo.Forge, Owner: repo.Owner, Repo: repo.Repo, PR: number, HeadSHA: snapshot.HeadSHA, BaseRef: snapshot.TargetBranch, BaseSHA: snapshot.TargetSHA, HeadRef: snapshot.HeadBranch}
		eligible, eligibilityErr := groupMemberEligible(ctx, cfg, repo, facts, adapter, snapshot)
		if eligibilityErr != nil || !eligible {
			continue
		}
		if activeRunContains(active, UnitName(facts)) || groupMemberGuardedBy(cfg.Runs.Dir, UnitName(facts), active) {
			continue
		}
		target := resolvedGroupTarget(snapshot, snapshots, map[string]bool{})
		membersByTarget[target] = append(membersByTarget[target], facts)
	}
	for _, primary := range candidates {
		snapshot, ok := snapshots[primary.facts.PR]
		if !ok || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) {
			continue
		}
		members := make([]Facts, 0, len(membersByTarget[resolvedGroupTarget(snapshot, snapshots, map[string]bool{})]))
		for _, member := range membersByTarget[resolvedGroupTarget(snapshot, snapshots, map[string]bool{})] {
			if member.PR != primary.facts.PR {
				members = append(members, member)
			}
		}
		encoded, err := json.Marshal(members)
		if err != nil {
			return err
		}
		groups[UnitName(primary.facts)] = string(encoded)
	}
	return nil
}

// groupMemberEligible is the non-writing admission predicate shared by
// candidate surfacing and member claiming. Reconciliation may repair a missing
// terminal status; grouping must never re-claim a pull request with a terminal
// Minos result while that repair is pending.
func groupMemberEligible(ctx context.Context, cfg ServiceConfig, repo RepoConfig, facts Facts, adapter *forge.Adapter, snapshot forge.Snapshot) (bool, error) {
	if snapshot.State != "open" || snapshot.Merged || snapshot.Draft || snapshot.HeadBranch == "" || snapshot.HeadRepository != facts.Owner+"/"+facts.Repo || structuralBranch(snapshot.HeadBranch, repo.StructuralBranchPrefixes) || workInProgressBranch(snapshot.HeadBranch, repo.WorkInProgressBranchPrefixes) {
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
	return assessPullRequestAdmission(ctx, cfg, repo, facts, adapter, snapshot, commits).admitsNewRun(), nil
}

func activeRunContains(active []string, unit string) bool {
	for _, live := range active {
		if strings.TrimSuffix(live, ".service") == unit {
			return true
		}
	}
	return false
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
func groupMemberGuardedBy(runsDir, memberUnit string, active []string) bool {
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
	if validContinuationHolder(runsDir, holder) {
		return holder + ".service", true
	}
	return "", false
}

func sweepDeadGroupMemberGuards(ctx context.Context, cfg ServiceConfig, repos []RepoConfig) error {
	// Serialize cleanup with SpawnRun: a successor consumes its handoff before
	// systemd exposes the replacement unit, so cleanup must not observe that
	// internal transition as an abandoned group.
	unlock, err := lockAdmission(cfg.Runs.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	active, err := activeRunUnitNames(ctx)
	if err != nil {
		return err
	}

	paths, err := filepath.Glob(filepath.Join(cfg.Runs.Dir, ".group-members", "*"))
	if err != nil {
		return err
	}
	holderValidity := make(map[string]bool)
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return readErr
		}
		holder := strings.TrimSpace(string(data))
		if activeRunContains(active, holder) {
			continue
		}
		valid, inspected := holderValidity[holder]
		if !inspected {
			valid = validGroupContinuationHolder(ctx, cfg, repos, holder)
			holderValidity[holder] = valid
		}
		if !valid {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func validGroupContinuationHolder(ctx context.Context, cfg ServiceConfig, repos []RepoConfig, holder string) bool {
	handoff, err := readRunHandoffStructure(handoffPath(cfg.Runs.Dir, holder))
	if err != nil {
		return false
	}
	facts := Facts{
		Owner:   handoff.PullRequest.Owner,
		Repo:    handoff.PullRequest.Repo,
		PR:      handoff.PullRequest.Number,
		HeadSHA: handoff.Head,
	}
	if UnitName(facts) != holder {
		return false
	}
	for _, repo := range repos {
		if repo.Owner == facts.Owner && repo.Repo == facts.Repo {
			facts.Forge = repo.Forge
			break
		}
	}
	if facts.Forge == "" {
		return false
	}
	if _, valid := adoptableRunDirectory(cfg, holder, facts, handoff); !valid {
		return false
	}
	_, valid := adoptableGroupMembers(ctx, cfg, facts, holder, handoff)
	return valid
}

func releaseGroupMemberGuards(runsDir, holder string) error {
	paths, err := filepath.Glob(filepath.Join(runsDir, ".group-members", "*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if strings.TrimSpace(string(data)) != holder {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func adoptableGroupMembers(ctx context.Context, cfg ServiceConfig, facts Facts, unit string, handoff *runHandoff) (string, bool) {
	paths, err := filepath.Glob(filepath.Join(cfg.Runs.Dir, ".group-members", "*"))
	if err != nil {
		return fmt.Sprintf("list grouped members: %v", err), false
	}
	guarded := make(map[string]bool)
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return fmt.Sprintf("read grouped member guard %s: %v", filepath.Base(path), readErr), false
		}
		if strings.TrimSpace(string(data)) == unit {
			guarded[filepath.Base(path)] = true
		}
	}
	if len(guarded) == 0 && len(handoff.MemberHeads) == 0 {
		return "", true
	}
	if len(guarded) != len(handoff.MemberHeads) {
		return fmt.Sprintf("continuation records %d member heads for %d guarded members", len(handoff.MemberHeads), len(guarded)), false
	}
	for _, member := range handoff.MemberHeads {
		memberUnit := UnitName(Facts{Owner: member.Owner, Repo: member.Repo, PR: member.Number})
		if !guarded[memberUnit] {
			return fmt.Sprintf("continuation member %s has no matching group guard", member.Number), false
		}
	}

	adapter, err := newBehaviouralForge(cfg, facts.Forge)
	if err != nil {
		return fmt.Sprintf("prepare grouped member snapshots: %v", err), false
	}
	repository := forge.Repository{Owner: facts.Owner, Name: facts.Repo}
	for _, member := range handoff.MemberHeads {
		pullRequest, parseErr := strconv.ParseInt(member.Number, 10, 64)
		if parseErr != nil {
			return fmt.Sprintf("parse grouped member %q: %v", member.Number, parseErr), false
		}
		snapshot, snapshotErr := adapter.Snapshot(ctx, repository, pullRequest)
		if snapshotErr != nil {
			return fmt.Sprintf("snapshot grouped member %s: %v", member.Number, snapshotErr), false
		}
		if snapshot.HeadSHA != member.Head {
			return fmt.Sprintf("grouped member %s head moved from %s to %s", member.Number, member.Head, snapshot.HeadSHA), false
		}
	}
	return "", true
}

func validContinuationHolder(runsDir, holder string) bool {
	handoff, err := readRunHandoffStructure(handoffPath(runsDir, holder))
	if err != nil {
		return false
	}
	facts := Facts{
		Owner:   handoff.PullRequest.Owner,
		Repo:    handoff.PullRequest.Repo,
		PR:      handoff.PullRequest.Number,
		HeadSHA: handoff.Head,
	}
	if UnitName(facts) != holder {
		return false
	}
	cfg := ServiceConfig{}
	cfg.Runs.Dir = runsDir
	_, valid := adoptableRunDirectory(cfg, holder, facts, handoff)
	return valid
}

func claimGroupMember(ctx context.Context, cfg ServiceConfig, repo RepoConfig, primary Facts, owner, name, number, workspace string) (Facts, error) {
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
	protectedRefFile, err := registerMemberProtectedRef(ctx, workspace, facts.PR, facts.HeadRef)
	if err != nil {
		return Facts{}, err
	}
	keepProtectedRef := false
	defer func() {
		if !keepProtectedRef {
			_ = os.Remove(protectedRefFile)
		}
	}()
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
	keepProtectedRef = true
	return facts, nil
}

func registerMemberProtectedRef(ctx context.Context, workspace, number, branch string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("protect member branch: MINOS_WORKSPACE is not set")
	}
	protectedRef := "refs/heads/" + branch
	if output, err := exec.CommandContext(ctx, "git", "check-ref-format", protectedRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("protect member branch: invalid source branch %q: %s", branch, strings.TrimSpace(string(output)))
	}
	output, err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--path-format=absolute", "--git-common-dir").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("protect member branch: locate shared Git directory: %s", strings.TrimSpace(string(output)))
	}
	commonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(commonDir) {
		return "", fmt.Errorf("protect member branch: shared Git directory is not absolute")
	}
	protectedRefFile := filepath.Join(commonDir, "minos-protected-ref.member-"+number)
	file, err := os.OpenFile(protectedRefFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("protect member branch: create protected-ref record: %w", err)
	}
	if _, err := fmt.Fprintln(file, protectedRef); err != nil {
		_ = file.Close()
		_ = os.Remove(protectedRefFile)
		return "", fmt.Errorf("protect member branch: write protected-ref record: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(protectedRefFile)
		return "", fmt.Errorf("protect member branch: close protected-ref record: %w", err)
	}
	return protectedRefFile, nil
}
