#![allow(clippy::expect_used, clippy::too_many_lines, clippy::unwrap_used)]

use std::collections::BTreeSet;

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
    CertaintyClass, Comment, CommentId, CommentPayload, CommentTarget, Confidence, ContractEvent,
    ContractVersion, Decision, DecisionSubject, DecisionVerdict, EventPayload, Extensions, Finding,
    FindingId, FindingLocation, FinishLabel, ForgeFacts, Mergeability, ModelFamily, ModelLineage,
    ModelProvenance, Patch, PatchChange, PatchId, PrRunState, ProvenanceVerification,
    PublicationState, PullRequestRef, ReviewCleanliness, Revision, RunId, RunOutcome, RunStatus,
    SessionFreshness, SessionId, Severity, has_two_verified_reviewer_families,
    judge_independent_of_reviewers, merge_gate_clean_and_current, reviewers_disjoint_from_fixers,
    sessions_fresh_for_pass,
};

const fn version() -> ContractVersion {
    ContractVersion::current()
}

const fn extensions() -> Extensions {
    Extensions::new()
}

fn pr() -> PullRequestRef {
    PullRequestRef {
        repository: "forgejo.example/acme/widgets".to_owned(),
        id: "19".to_owned(),
    }
}

fn actor(id: &str) -> ActorRef {
    ActorRef {
        id: id.to_owned(),
        display_name: id.to_owned(),
    }
}

fn finish_label(applied_by: &str) -> FinishLabel {
    FinishLabel {
        name: "pump19-finish".to_owned(),
        applied_by: actor(applied_by),
    }
}

fn forge_facts(
    head_sha: &str,
    base_sha: &str,
    branch_currency: BranchCurrency,
    cleanliness: ReviewCleanliness,
    finish_label: Option<FinishLabel>,
) -> ForgeFacts {
    ForgeFacts {
        contract_version: version(),
        pr: pr(),
        head: Revision {
            sha: head_sha.to_owned(),
        },
        base: Revision {
            sha: base_sha.to_owned(),
        },
        branch_currency,
        cleanliness,
        mergeability: Mergeability::Mergeable,
        finish_label,
        actor_permissions: vec![ActorPermissions {
            actor: actor("pump19-core"),
            capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
                .into_iter()
                .collect::<BTreeSet<_>>(),
        }],
        author_login: None,
        work_in_progress: false,
        extensions: extensions(),
    }
}

fn verified_agent(
    agent_id: &str,
    role: AgentRole,
    family: &str,
    pass_index: u32,
) -> ModelProvenance {
    ModelProvenance {
        contract_version: version(),
        agent_id: AgentId(agent_id.to_owned()),
        role,
        session_id: SessionId(format!("{agent_id}-pass-{pass_index}")),
        freshness: SessionFreshness::FreshForPass { pass_index },
        verification: ProvenanceVerification::Verified {
            vendor: "local".to_owned(),
            control_plane: "pump19-core".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: model_for_family(family),
            },
        },
        extensions: extensions(),
    }
}

fn model_for_family(family: &str) -> String {
    match family {
        "glm" => "openrouter/z-ai/glm-5.2".to_owned(),
        _ => format!("{family}-2026-06"),
    }
}

fn unverified_agent(agent_id: &str, role: AgentRole, pass_index: u32) -> ModelProvenance {
    ModelProvenance {
        contract_version: version(),
        agent_id: AgentId(agent_id.to_owned()),
        role,
        session_id: SessionId(format!("{agent_id}-pass-{pass_index}")),
        freshness: SessionFreshness::FreshForPass { pass_index },
        verification: ProvenanceVerification::Unverified {
            reason: "provider lineage proof unavailable".to_owned(),
        },
        extensions: extensions(),
    }
}

fn reused_agent(
    agent_id: &str,
    role: AgentRole,
    family: &str,
    original_session_id: &str,
) -> ModelProvenance {
    let mut provenance = verified_agent(agent_id, role, family, 1);
    provenance.freshness = SessionFreshness::Reused {
        original_session_id: SessionId(original_session_id.to_owned()),
    };
    provenance
}

fn finding(id: &str, provenance: ModelProvenance, summary: &str) -> Finding {
    Finding {
        contract_version: version(),
        id: FindingId(id.to_owned()),
        dedup_key: format!("review-fix:{id}:src/lib.rs"),
        source_brief: "review-fix-loop".to_owned(),
        dimension: "correctness".to_owned(),
        summary: summary.to_owned(),
        severity: Severity::High,
        confidence: Confidence::High,
        certainty: CertaintyClass::Advisory,
        provenance,
        locations: vec![FindingLocation::File {
            path: "src/lib.rs".to_owned(),
            line: Some(42),
            range: None,
        }],
        extensions: extensions(),
    }
}

fn decision(
    id: &str,
    finding_id: &FindingId,
    verdict: DecisionVerdict,
    judge: ModelProvenance,
) -> Decision {
    Decision {
        contract_version: version(),
        id: id.to_owned(),
        subject: DecisionSubject::Finding {
            finding_id: finding_id.clone(),
        },
        verdict,
        rationale: match verdict {
            DecisionVerdict::Material => "The finding changes merge safety.".to_owned(),
            DecisionVerdict::Minor => {
                "The remaining issue is below the action threshold.".to_owned()
            }
            DecisionVerdict::Converged => "No material findings remain for this pass.".to_owned(),
        },
        provenance: judge,
        extensions: extensions(),
    }
}

fn patch(id: &str, run_id: &str, finding_id: &FindingId, fixer: ModelProvenance) -> Patch {
    Patch {
        contract_version: version(),
        id: PatchId(id.to_owned()),
        run_id: RunId(run_id.to_owned()),
        commit_sha: "fix-sha-1".to_owned(),
        idempotency_key: format!("{run_id}:fix-sha-1:{}", finding_id.0),
        answers_findings: vec![finding_id.clone()],
        change: PatchChange::UnifiedDiff {
            diff: "--- a/src/lib.rs\n+++ b/src/lib.rs\n".to_owned(),
        },
        provenance: fixer,
        extensions: extensions(),
    }
}

fn posted_comment(id: &str, finding_id: &FindingId) -> Comment {
    Comment {
        contract_version: version(),
        id: CommentId(id.to_owned()),
        finding_ids: vec![finding_id.clone()],
        target: CommentTarget::PullRequest { pr: pr() },
        payload: CommentPayload {
            summary: "Pump-19 material finding".to_owned(),
            details: vec![
                "Posted only after the significance judge marked it material.".to_owned(),
            ],
        },
        extensions: extensions(),
    }
}

const fn finding_id_from_decision(decision: &Decision) -> Option<&FindingId> {
    match &decision.subject {
        DecisionSubject::Finding { finding_id } => Some(finding_id),
        DecisionSubject::FindingSet { .. } => None,
    }
}

#[test]
fn material_review_finding_drives_fix_then_fresh_rereview_until_minor_convergence() {
    let opened = ContractEvent {
        contract_version: version(),
        id: "event-pr-opened".to_owned(),
        payload: EventPayload::PullRequestOpened {
            facts: forge_facts(
                "author-sha-1",
                "main-sha-1",
                BranchCurrency::Current,
                ReviewCleanliness::Dirty,
                None,
            ),
        },
        extensions: extensions(),
    };
    assert!(matches!(
        opened.payload,
        EventPayload::PullRequestOpened { .. }
    ));

    let pass_one_reviewers = vec![
        verified_agent("codex-reviewer-pass-1", AgentRole::Reviewer, "codex", 1),
        verified_agent("claude-reviewer-pass-1", AgentRole::Reviewer, "claude", 1),
    ];
    let pass_one_judge = verified_agent("glm-judge-pass-1", AgentRole::Judge, "glm", 1);
    let material = finding(
        "finding-stale-merge-state",
        pass_one_reviewers[0].clone(),
        "The merge gate accepts review state from an earlier base revision.",
    );
    let material_decision = decision(
        "decision-pass-1-material",
        &material.id,
        DecisionVerdict::Material,
        pass_one_judge.clone(),
    );
    let material_comment = posted_comment("comment-material-1", &material.id);
    let fixer = verified_agent("codex-fixer-pass-1", AgentRole::Fixer, "codex", 1);
    let fix_patch = patch(
        "patch-stale-merge-state",
        "run-fix-pass-1",
        &material.id,
        fixer,
    );
    let pass_one_state = PrRunState {
        contract_version: version(),
        pr: pr(),
        commit_sha: "author-sha-1".to_owned(),
        current_head_sha: Some("author-sha-1".to_owned()),
        pass_index: 1,
        status: RunStatus::Completed,
        active_run: None,
        run_history: Vec::new(),
        loop_history: Vec::new(),
        superseded_by: None,
        findings: vec![material.clone()],
        decisions: vec![material_decision.clone()],
        patches: vec![fix_patch.clone()],
        publication: PublicationState::default(),
        ceiling: None,
        extensions: extensions(),
    };

    let mut pass_one_launch_provenance = pass_one_reviewers.clone();
    pass_one_launch_provenance.push(pass_one_judge.clone());
    pass_one_launch_provenance.push(fix_patch.provenance.clone());
    assert!(has_two_verified_reviewer_families(
        &pass_one_launch_provenance
    ));
    assert!(judge_independent_of_reviewers(
        &pass_one_judge,
        &pass_one_reviewers
    ));
    assert!(reviewers_disjoint_from_fixers(&pass_one_launch_provenance));
    assert!(sessions_fresh_for_pass(&pass_one_launch_provenance, 1));
    assert_eq!(pass_one_state.ceiling, None);
    assert_eq!(
        finding_id_from_decision(&material_decision),
        Some(&material.id)
    );
    assert_eq!(fix_patch.answers_findings, vec![material.id.clone()]);
    assert_eq!(material_comment.finding_ids, vec![material.id]);

    let fix_completed = ContractEvent {
        contract_version: version(),
        id: "event-fix-completed-pass-1".to_owned(),
        payload: EventPayload::RunCompleted {
            run_id: fix_patch.run_id.clone(),
            run_kind: Some(pump19_contract::RunKind::Fix),
            outcome: RunOutcome::Succeeded,
        },
        extensions: extensions(),
    };
    assert!(matches!(
        fix_completed.payload,
        EventPayload::RunCompleted {
            outcome: RunOutcome::Succeeded,
            ..
        }
    ));

    let pass_two_reviewers = vec![
        verified_agent("codex-reviewer-pass-2", AgentRole::Reviewer, "codex", 2),
        verified_agent("claude-reviewer-pass-2", AgentRole::Reviewer, "claude", 2),
    ];
    let pass_two_judge = verified_agent("glm-judge-pass-2", AgentRole::Judge, "glm", 2);
    let minor = finding(
        "finding-wording-only",
        pass_two_reviewers[1].clone(),
        "A comment could describe the freshness check more directly.",
    );
    let minor_decision = decision(
        "decision-pass-2-minor",
        &minor.id,
        DecisionVerdict::Minor,
        pass_two_judge.clone(),
    );
    let pass_two_state = PrRunState {
        contract_version: version(),
        pr: pr(),
        commit_sha: "fix-sha-1".to_owned(),
        current_head_sha: Some("fix-sha-1".to_owned()),
        pass_index: 2,
        status: RunStatus::Completed,
        active_run: None,
        run_history: Vec::new(),
        loop_history: Vec::new(),
        superseded_by: None,
        findings: vec![minor.clone()],
        decisions: vec![minor_decision.clone()],
        patches: Vec::new(),
        publication: PublicationState::default(),
        ceiling: None,
        extensions: extensions(),
    };

    let mut pass_two_launch_provenance = pass_two_reviewers.clone();
    pass_two_launch_provenance.push(pass_two_judge.clone());
    pass_two_launch_provenance.push(fix_patch.provenance);
    assert!(has_two_verified_reviewer_families(
        &pass_two_launch_provenance
    ));
    assert!(judge_independent_of_reviewers(
        &pass_two_judge,
        &pass_two_reviewers
    ));
    assert!(reviewers_disjoint_from_fixers(&pass_two_launch_provenance));
    assert!(sessions_fresh_for_pass(&pass_two_reviewers, 2));
    assert_eq!(minor_decision.verdict, DecisionVerdict::Minor);
    assert_eq!(pass_two_state.patches, Vec::new());
    assert!(!material_comment.finding_ids.contains(&minor.id));

    let converged_facts = forge_facts(
        "fix-sha-1",
        "main-sha-1",
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        Some(finish_label("pump19-core")),
    );
    let finish_applied = ContractEvent {
        contract_version: version(),
        id: "event-finish-label".to_owned(),
        payload: EventPayload::LabelApplied {
            pr: pr(),
            label: finish_label("pump19-core"),
        },
        extensions: extensions(),
    };
    assert!(matches!(
        finish_applied.payload,
        EventPayload::LabelApplied { .. }
    ));
    assert!(merge_gate_clean_and_current(&converged_facts));
}

#[test]
fn launch_story_fails_closed_when_provenance_cannot_establish_independence() {
    let opened = ContractEvent {
        contract_version: version(),
        id: "event-pr-opened-unverified".to_owned(),
        payload: EventPayload::PullRequestOpened {
            facts: forge_facts(
                "author-sha-2",
                "main-sha-1",
                BranchCurrency::Current,
                ReviewCleanliness::Dirty,
                None,
            ),
        },
        extensions: extensions(),
    };
    assert!(matches!(
        opened.payload,
        EventPayload::PullRequestOpened { .. }
    ));

    let reviewers = vec![
        verified_agent("codex-reviewer", AgentRole::Reviewer, "codex", 1),
        unverified_agent("claimed-claude-reviewer", AgentRole::Reviewer, 1),
    ];
    let judge = verified_agent("glm-judge", AgentRole::Judge, "glm", 1);
    let reused_fixer = reused_agent(
        "codex-reviewer",
        AgentRole::Fixer,
        "codex",
        "codex-reviewer-pass-1",
    );
    let mut launch_provenance = reviewers.clone();
    launch_provenance.push(judge.clone());
    launch_provenance.push(reused_fixer);

    assert!(!has_two_verified_reviewer_families(&launch_provenance));
    assert!(!reviewers_disjoint_from_fixers(&launch_provenance));
    assert!(!judge_independent_of_reviewers(&judge, &reviewers));
    assert!(!sessions_fresh_for_pass(&launch_provenance, 1));
}

#[test]
fn clean_but_stale_finish_facts_represent_reentry_not_auto_merge() {
    let stale_facts = forge_facts(
        "fix-sha-1",
        "main-sha-2",
        BranchCurrency::Stale,
        ReviewCleanliness::Clean,
        Some(finish_label("pump19-core")),
    );
    let main_moved_event = ContractEvent {
        contract_version: version(),
        id: "event-pr-updated-after-main-moved".to_owned(),
        payload: EventPayload::PullRequestUpdated {
            facts: stale_facts.clone(),
        },
        extensions: extensions(),
    };

    assert!(!merge_gate_clean_and_current(&stale_facts));
    assert!(matches!(
        main_moved_event.payload,
        EventPayload::PullRequestUpdated { .. }
    ));
}
