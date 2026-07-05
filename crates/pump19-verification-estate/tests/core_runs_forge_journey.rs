#![allow(
    clippy::expect_used,
    clippy::unwrap_used,
    reason = "journey tests keep fixtures direct and failure messages local"
)]

use std::{
    cell::RefCell,
    collections::{BTreeMap, BTreeSet, VecDeque},
    fs,
    path::PathBuf,
    rc::Rc,
};

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BarCheckRecord,
    BranchCurrency, CertaintyClass, ContractEvent, ContractVersion, CoverageRecord, EventPayload,
    FamilySplit, Finding, FindingId, FindingLocation, FindingVerification, FinishLabel, ForgeFacts,
    IndependenceDegradation, Mergeability, ModelFamily, ModelLineage, ModelProvenance, Patch,
    PatchChange, PriorityClass, ProvenanceVerification, PullRequestRef, ReviewCleanliness,
    ReviewVerdict, Revision, RunId, RunKind, RunOutcome, SessionArchiveKind, SessionArchiveRef,
    SessionFreshness, SessionId, VerificationStatus,
};
use pump19_core::{
    AgentEngine, AgentLaunchSpec, AgentLaunchTarget, AgentPlan, AuthorisedComment,
    AuthorisedCommentResolution, AuthorisedCommentUpdate, AuthorisedFixPush, AuthorisedLabel,
    AuthorisedMerge, CommentFormatter, Core, CoreError, CorePolicy, Criteria, DispatchOutcome,
    EventKind, FinishLabelApplicationPolicy, ForgeOperationError, ForgeOperationReceipt,
    ForgeOperations, LaunchProof, NoopSourcePreparer, OperatorLog, OperatorLogEvent, PreparedAgent,
    RunLaunchOutcome, RunLaunchRequest, RunLauncher, RunStateKey, RunStateStore, StateCriterion,
    TriggerRule, WorkspaceExecOutput, WorkspaceExecRequest, WorkspaceExecutor, WorkspaceIsolation,
    WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
};
use tempfile::tempdir;

#[test]
fn journey_review_posts_only_verified_findings_at_policy_threshold()
-> Result<(), Box<dyn std::error::Error>> {
    let material = finding("material", PriorityClass::P1, VerificationStatus::Verified);
    let suppressed_priority = finding("low", PriorityClass::P3, VerificationStatus::Verified);
    let suppressed_verification = finding(
        "unverified",
        PriorityClass::P0,
        VerificationStatus::Unverified {
            reason: "verifier could not reproduce".to_owned(),
        },
    );
    let outcome = review_outcome(
        vec![material, suppressed_priority, suppressed_verification],
        ReviewVerdict::FindingsPosted {
            material: 1,
            suppressed: 2,
        },
    );
    let harness = Harness::new(ScriptedLauncher::with_outcomes([outcome]));

    let outcomes = harness.process(&pull_opened(), &[review_rule()])?;

    assert_launched(&outcomes, "review-on-pr-change");
    assert_eq!(harness.forge.comments.borrow().len(), 1);
    assert!(harness.forge.comments.borrow()[0].body.contains("material"));
    let state = harness.latest_state();
    assert_eq!(state.findings.len(), 3);
    assert!(matches!(
        state.verdict,
        Some(ReviewVerdict::FindingsPosted {
            material: 1,
            suppressed: 2
        })
    ));
    assert_eq!(state.publication.finding_comments.len(), 1);
    assert_eq!(
        state.publication.finding_comments[0].finding_dedup_key,
        "material"
    );
    Ok(())
}

#[test]
fn journey_material_review_launches_fix_and_pushes_patch() -> Result<(), Box<dyn std::error::Error>>
{
    let review = review_outcome(
        vec![finding(
            "material",
            PriorityClass::P1,
            VerificationStatus::Verified,
        )],
        ReviewVerdict::FindingsPosted {
            material: 1,
            suppressed: 0,
        },
    );
    let fix = fix_outcome("material", RunOutcome::Succeeded);
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review, fix]));

    harness.process(&pull_opened(), &[review_rule()])?;
    let batches = harness.drain(&[fix_after_material_review_rule()])?;

    assert!(batches.iter().flatten().any(
        |outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == "fix-after-material-review")
    ));
    assert_eq!(harness.forge.fix_pushes.borrow().len(), 1);
    let state = harness.latest_state();
    assert_eq!(state.pass_index, 2);
    assert_eq!(state.loop_history.len(), 1);
    assert_eq!(
        state.loop_history[0].fix_outcome,
        Some(RunOutcome::Succeeded)
    );
    Ok(())
}

#[test]
fn journey_noop_fix_rechecks_standing_findings() -> Result<(), Box<dyn std::error::Error>> {
    let review = review_outcome(
        vec![finding(
            "material",
            PriorityClass::P1,
            VerificationStatus::Verified,
        )],
        ReviewVerdict::FindingsPosted {
            material: 1,
            suppressed: 0,
        },
    );
    let noop_fix = fix_outcome("material", RunOutcome::NoOp);
    let recheck = review_outcome(
        vec![finding(
            "material",
            PriorityClass::P1,
            VerificationStatus::Verified,
        )],
        ReviewVerdict::StandingFindings {
            finding_dedup_keys: vec!["material".to_owned()],
            rationale: "the finding survived the no-op fix".to_owned(),
        },
    );
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review, noop_fix, recheck]));

    harness.process(&pull_opened(), &[review_rule()])?;
    let batches = harness.drain(&[
        fix_after_material_review_rule(),
        review_after_noop_fix_rule(),
    ])?;

    assert!(batches.iter().flatten().any(
        |outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == "review-after-noop-fix")
    ));
    assert!(
        harness
            .latest_state()
            .loop_history
            .iter()
            .any(|pass| matches!(pass.verdict, Some(ReviewVerdict::StandingFindings { .. })))
    );
    Ok(())
}

#[test]
fn journey_converged_review_can_apply_finish_label() -> Result<(), Box<dyn std::error::Error>> {
    let harness = Harness::with_policy(
        ScriptedLauncher::with_outcomes([review_outcome(
            Vec::new(),
            ReviewVerdict::Converged {
                bar_check: bar_check(true),
            },
        )]),
        CorePolicy {
            finish_label_application: FinishLabelApplicationPolicy::CoreOnConvergence {
                label: "pump19-finish".to_owned(),
            },
            ..CorePolicy::human_gate()
        },
    );

    harness.process(&pull_opened(), &[review_rule()])?;

    let state = harness.latest_state();
    assert!(matches!(
        state.verdict,
        Some(ReviewVerdict::Converged { .. })
    ));
    assert_eq!(harness.forge.labels.borrow().len(), 1);
    assert_eq!(harness.forge.labels.borrow()[0].label, "pump19-finish");
    Ok(())
}

#[test]
fn journey_finish_label_merges_clean_current_converged_pr() -> Result<(), Box<dyn std::error::Error>>
{
    let harness = Harness::new(ScriptedLauncher::with_outcomes([
        review_outcome(
            Vec::new(),
            ReviewVerdict::Converged {
                bar_check: bar_check(true),
            },
        ),
        empty_outcome(RunOutcome::Succeeded),
    ]));

    harness.process(&pull_opened(), &[review_rule()])?;
    let label = ContractEvent {
        contract_version: ContractVersion::current(),
        id: "label-finish".to_owned(),
        payload: EventPayload::LabelApplied {
            pr: pr(),
            label: finish_label(),
        },
        extensions: BTreeMap::new(),
    };
    let outcomes = harness.process(&label, &[finish_on_label_rule()])?;

    assert_launched(&outcomes, "finish-on-label");
    assert_eq!(harness.forge.merges.borrow().len(), 1);
    Ok(())
}

#[test]
fn journey_degraded_bar_check_requests_retry_review() -> Result<(), Box<dyn std::error::Error>> {
    let harness = Harness::new(ScriptedLauncher::with_outcomes([
        review_outcome(
            Vec::new(),
            ReviewVerdict::BarCheckDegraded {
                attempts: 2,
                last_error: "bar checker unavailable".to_owned(),
            },
        ),
        review_outcome(
            Vec::new(),
            ReviewVerdict::Converged {
                bar_check: bar_check(true),
            },
        ),
    ]));

    harness.process(&pull_opened(), &[review_rule()])?;
    let batches = harness.drain(&[review_after_degraded_bar_check_rule()])?;

    assert!(batches.iter().flatten().any(
        |outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == "review-after-degraded-bar-check")
    ));
    assert!(matches!(
        harness.latest_state().verdict,
        Some(ReviewVerdict::Converged { .. })
    ));
    Ok(())
}

#[test]
fn adversarial_family_unknown_launches_but_records_operator_degradation()
-> Result<(), Box<dyn std::error::Error>> {
    let mut rule = review_rule();
    rule.agent_plan.reviewers[0].lineage.model = "unmapped-family-model".to_owned();
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review_outcome(
        Vec::new(),
        ReviewVerdict::Converged {
            bar_check: bar_check(true),
        },
    )]));

    let outcomes = harness.process(&pull_opened(), &[rule])?;

    assert_launched(&outcomes, "review-on-pr-change");
    let state = harness.latest_state();
    assert!(state.run_history.iter().any(|record| {
        record.independence_degradations.iter().any(|degradation| {
            matches!(
                degradation,
                IndependenceDegradation::FamilyUnknown { agent_id }
                    if agent_id.0 == "reviewer-codex"
            )
        })
    }));
    assert!(harness.operator_events.borrow().iter().any(|event| {
        event.kind == pump19_core::OperatorLogEventKind::IndependenceDegradation
            && event.message.contains("reviewer-codex")
    }));
    Ok(())
}

#[test]
fn adversarial_same_family_verification_is_recorded_not_refused()
-> Result<(), Box<dyn std::error::Error>> {
    let finding = finding(
        "same-family",
        PriorityClass::P1,
        VerificationStatus::Verified,
    );
    let outcome = RunLaunchOutcome {
        independence_degradations: vec![IndependenceDegradation::SameFamilyVerification {
            finding_id: finding.id.clone(),
        }],
        ..review_outcome(
            vec![finding],
            ReviewVerdict::FindingsPosted {
                material: 1,
                suppressed: 0,
            },
        )
    };
    let harness = Harness::new(ScriptedLauncher::with_outcomes([outcome]));

    let outcomes = harness.process(&pull_opened(), &[review_rule()])?;

    assert_launched(&outcomes, "review-on-pr-change");
    assert_eq!(harness.forge.comments.borrow().len(), 1);
    assert!(matches!(
        harness.latest_state().run_history[0].independence_degradations[0],
        IndependenceDegradation::SameFamilyVerification { .. }
    ));
    Ok(())
}

#[test]
fn adversarial_partial_coverage_persists_without_posting() -> Result<(), Box<dyn std::error::Error>>
{
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review_outcome(
        Vec::new(),
        ReviewVerdict::PartialCoverage,
    )]));

    harness.process(&pull_opened(), &[review_rule()])?;

    let state = harness.latest_state();
    assert!(matches!(
        state.verdict,
        Some(ReviewVerdict::PartialCoverage)
    ));
    assert!(matches!(
        state.coverage,
        Some(CoverageRecord {
            complete: false,
            ..
        })
    ));
    assert!(harness.forge.comments.borrow().is_empty());
    Ok(())
}

#[test]
fn adversarial_standing_findings_do_not_publish_without_new_verified_material()
-> Result<(), Box<dyn std::error::Error>> {
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review_outcome(
        Vec::new(),
        ReviewVerdict::StandingFindings {
            finding_dedup_keys: vec!["old-material".to_owned()],
            rationale: "prior finding still stands".to_owned(),
        },
    )]));

    harness.process(&pull_opened(), &[review_rule()])?;

    assert!(matches!(
        harness.latest_state().verdict,
        Some(ReviewVerdict::StandingFindings { .. })
    ));
    assert!(harness.forge.comments.borrow().is_empty());
    Ok(())
}

#[test]
fn adversarial_bar_check_unavailability_records_degraded_verdict()
-> Result<(), Box<dyn std::error::Error>> {
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review_outcome(
        Vec::new(),
        ReviewVerdict::BarCheckDegraded {
            attempts: 2,
            last_error: "workflow timeout".to_owned(),
        },
    )]));

    harness.process(&pull_opened(), &[review_rule()])?;

    assert!(matches!(
        harness.latest_state().verdict,
        Some(ReviewVerdict::BarCheckDegraded { attempts: 2, .. })
    ));
    Ok(())
}

#[test]
fn adversarial_single_family_launch_records_degradation() -> Result<(), Box<dyn std::error::Error>>
{
    let rule = TriggerRule {
        agent_plan: AgentPlan {
            lead: Some(target("lead-codex", AgentRole::Lead, "codex")),
            reviewers: vec![target("reviewer-codex", AgentRole::Reviewer, "codex")],
            verifiers: vec![target("verifier-codex", AgentRole::Verifier, "codex")],
            bar_check: Some(target("bar-codex", AgentRole::BarCheck, "codex")),
            fixers: Vec::new(),
            finishers: Vec::new(),
        },
        ..review_rule()
    };
    let harness = Harness::new(ScriptedLauncher::with_outcomes([review_outcome(
        Vec::new(),
        ReviewVerdict::Converged {
            bar_check: bar_check(true),
        },
    )]));

    let outcomes = harness.process(&pull_opened(), &[rule])?;

    assert_launched(&outcomes, "review-on-pr-change");
    assert!(
        harness.latest_state().run_history[0]
            .independence_degradations
            .contains(&IndependenceDegradation::SingleFamilyDeployment)
    );
    Ok(())
}

fn assert_launched(outcomes: &[DispatchOutcome], rule: &str) {
    assert!(outcomes.iter().any(
        |outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == rule)
    ));
}

type HarnessCore = Core<
    NoEvents,
    TempWorkspace,
    ScriptedLauncher,
    SharedStore,
    RecordingForge,
    NoopSourcePreparer,
    FormattingComments,
    RecordingOperatorLog,
>;

struct Harness {
    core: RefCell<HarnessCore>,
    store: SharedStore,
    forge: RecordingForge,
    operator_events: Rc<RefCell<Vec<OperatorLogEvent>>>,
}

impl Harness {
    fn new(launcher: ScriptedLauncher) -> Self {
        Self::with_policy(launcher, CorePolicy::human_gate())
    }

    fn with_policy(launcher: ScriptedLauncher, policy: CorePolicy) -> Self {
        let workspace = TempWorkspace::new();
        let store = SharedStore::default();
        let forge = RecordingForge::default();
        let operator_events = Rc::<RefCell<Vec<OperatorLogEvent>>>::default();
        let core = Core::with_forge_operations_source_preparer_comment_formatter_and_policy(
            NoEvents,
            workspace,
            launcher,
            store.clone(),
            forge.clone(),
            NoopSourcePreparer,
            FormattingComments,
            RecordingOperatorLog {
                events: Rc::clone(&operator_events),
            },
            policy,
        );
        Self {
            core: RefCell::new(core),
            store,
            forge,
            operator_events,
        }
    }

    fn process(
        &self,
        event: &ContractEvent,
        rules: &[TriggerRule],
    ) -> Result<Vec<DispatchOutcome>, CoreError> {
        self.core.borrow_mut().process_event(event, rules)
    }

    fn drain(&self, rules: &[TriggerRule]) -> Result<Vec<Vec<DispatchOutcome>>, CoreError> {
        self.core.borrow_mut().drain_available(rules)
    }

    fn latest_state(&self) -> pump19_contract::PrRunState {
        self.store
            .states
            .borrow()
            .iter()
            .max_by_key(|state| state.run_history.len())
            .expect("stored state")
            .clone()
    }
}

#[derive(Debug)]
struct NoEvents;

impl pump19_core::EventSource for NoEvents {
    fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError> {
        Ok(None)
    }
}

#[derive(Debug)]
struct TempWorkspace {
    root: PathBuf,
}

impl TempWorkspace {
    fn new() -> Self {
        Self {
            root: tempdir().expect("workspace root").keep(),
        }
    }
}

impl WorkspaceProvider for TempWorkspace {
    fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
        let run_id = request.run_id.0;
        let root = self.root.join(&run_id);
        fs::create_dir_all(&root).map_err(|source| CoreError::Io {
            path: root.display().to_string(),
            source,
        })?;
        Ok(WorkspaceLease {
            id: run_id,
            root,
            isolation: WorkspaceIsolation {
                isolated: true,
                credential_free: true,
                resource_bounded: true,
                ephemeral: true,
            },
        })
    }

    fn exec(
        &mut self,
        _lease: &WorkspaceLease,
        _request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError> {
        Ok(WorkspaceExecOutput {
            exit_code: 0,
            stdout: Vec::new(),
            stderr: Vec::new(),
        })
    }

    fn cleanup(&mut self, _lease: &WorkspaceLease) -> Result<(), CoreError> {
        Ok(())
    }
}

#[derive(Clone, Debug, Default)]
struct SharedStore {
    states: Rc<RefCell<Vec<pump19_contract::PrRunState>>>,
}

impl RunStateStore for SharedStore {
    fn load(&self, key: &RunStateKey) -> Result<Option<pump19_contract::PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .find(|state| state.pr == key.pr && state.commit_sha == key.commit_sha)
            .cloned())
    }

    fn load_latest_for_pr(
        &self,
        pr: &PullRequestRef,
    ) -> Result<Option<pump19_contract::PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .filter(|state| state.pr == *pr)
            .max_by_key(|state| state.pass_index)
            .cloned())
    }

    fn load_by_run_id(
        &self,
        run_id: &RunId,
    ) -> Result<Option<pump19_contract::PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .find(|state| {
                state
                    .run_history
                    .iter()
                    .chain(state.active_run.iter())
                    .any(|record| record.run_id == *run_id)
            })
            .cloned())
    }

    fn completion_recovery_states(&self) -> Result<Vec<pump19_contract::PrRunState>, CoreError> {
        Ok(self.states.borrow().clone())
    }

    fn save(&mut self, state: &pump19_contract::PrRunState) -> Result<(), CoreError> {
        let mut states = self.states.borrow_mut();
        if let Some(existing) = states
            .iter_mut()
            .find(|candidate| candidate.pr == state.pr && candidate.commit_sha == state.commit_sha)
        {
            *existing = state.clone();
        } else {
            states.push(state.clone());
        }
        Ok(())
    }
}

#[derive(Clone, Debug)]
struct ScriptedLauncher {
    outcomes: Rc<RefCell<VecDeque<RunLaunchOutcome>>>,
}

impl ScriptedLauncher {
    fn with_outcomes<const N: usize>(outcomes: [RunLaunchOutcome; N]) -> Self {
        Self {
            outcomes: Rc::new(RefCell::new(outcomes.into())),
        }
    }
}

impl RunLauncher for ScriptedLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        let agent_id = spec.target.agent_id.0.clone();
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}-{agent_id}", spec.pass_index)),
            proof: LaunchProof::EstablishedFresh,
        })
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        let mut outcome = self
            .outcomes
            .borrow_mut()
            .pop_front()
            .unwrap_or_else(|| empty_outcome(RunOutcome::Succeeded));
        if outcome.session_archives.is_empty() {
            outcome.session_archives = request
                .provenance
                .iter()
                .map(|provenance| SessionArchiveRef {
                    role: provenance.role,
                    agent_id: provenance.agent_id.clone(),
                    path: format!("archive/{}.json", provenance.agent_id.0),
                    kind: if provenance.role == AgentRole::Lead {
                        SessionArchiveKind::LeadTranscript
                    } else {
                        SessionArchiveKind::EnsembleRun
                    },
                })
                .collect();
        }
        for patch in &mut outcome.patches {
            patch.run_id = request.run_id.clone();
        }
        Ok(outcome)
    }
}

#[derive(Clone, Debug, Default)]
struct RecordingForge {
    comments: Rc<RefCell<Vec<AuthorisedComment>>>,
    labels: Rc<RefCell<Vec<AuthorisedLabel>>>,
    merges: Rc<RefCell<Vec<AuthorisedMerge>>>,
    fix_pushes: Rc<RefCell<Vec<AuthorisedFixPush>>>,
}

impl ForgeOperations for RecordingForge {
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.comments.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "comment-1".to_owned(),
            idempotency_key,
            new_head_sha: None,
        })
    }

    fn update_comment(
        &mut self,
        request: AuthorisedCommentUpdate,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: request.comment_operation_id,
            idempotency_key: request.authorisation.idempotency_key,
            new_head_sha: None,
        })
    }

    fn resolve_comment(
        &mut self,
        request: AuthorisedCommentResolution,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: request.comment_operation_id,
            idempotency_key: request.authorisation.idempotency_key,
            new_head_sha: None,
        })
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.labels.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "label-1".to_owned(),
            idempotency_key,
            new_head_sha: None,
        })
    }

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.merges.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "merge-1".to_owned(),
            idempotency_key,
            new_head_sha: None,
        })
    }

    fn push_fix_commits(
        &mut self,
        request: AuthorisedFixPush,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.fix_pushes.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "push-1".to_owned(),
            idempotency_key,
            new_head_sha: Some("head-after-fix".to_owned()),
        })
    }
}

#[derive(Debug)]
struct FormattingComments;

impl CommentFormatter for FormattingComments {
    fn format_finding_comment(
        &mut self,
        request: pump19_core::FindingCommentFormatRequest,
    ) -> Result<String, CoreError> {
        Ok(format!(
            "{}: {:?}",
            request.finding.dedup_key, request.verification.status
        ))
    }
}

#[derive(Clone, Debug)]
struct RecordingOperatorLog {
    events: Rc<RefCell<Vec<OperatorLogEvent>>>,
}

impl OperatorLog for RecordingOperatorLog {
    fn record(&mut self, event: OperatorLogEvent) -> Result<(), CoreError> {
        self.events.borrow_mut().push(event);
        Ok(())
    }
}

fn review_rule() -> TriggerRule {
    TriggerRule {
        id: "review-on-pr-change".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Any {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::PullRequestOpened,
                },
                Criteria::Event {
                    event: EventKind::PullRequestUpdated,
                },
            ],
        },
        agent_plan: AgentPlan {
            lead: Some(target("lead-claude", AgentRole::Lead, "claude")),
            reviewers: vec![
                target("reviewer-codex", AgentRole::Reviewer, "codex"),
                target("reviewer-claude", AgentRole::Reviewer, "claude"),
            ],
            verifiers: vec![target("verifier-claude", AgentRole::Verifier, "claude")],
            bar_check: Some(target("bar-codex", AgentRole::BarCheck, "codex")),
            fixers: Vec::new(),
            finishers: Vec::new(),
        },
    }
}

fn fix_after_material_review_rule() -> TriggerRule {
    TriggerRule {
        id: "fix-after-material-review".to_owned(),
        run_kind: RunKind::Fix,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::RunCompleted {
                        run_kind: Some(RunKind::Review),
                        outcome: Some(RunOutcome::Succeeded),
                    },
                },
                Criteria::State {
                    state: StateCriterion::HasVerifiedMaterialFindings,
                },
            ],
        },
        agent_plan: AgentPlan {
            fixers: vec![target("fixer-codex", AgentRole::Fixer, "codex")],
            ..AgentPlan::default()
        },
    }
}

fn review_after_noop_fix_rule() -> TriggerRule {
    TriggerRule {
        id: "review-after-noop-fix".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Event {
            event: EventKind::RunCompleted {
                run_kind: Some(RunKind::Fix),
                outcome: Some(RunOutcome::NoOp),
            },
        },
        agent_plan: review_rule().agent_plan,
    }
}

fn review_after_degraded_bar_check_rule() -> TriggerRule {
    TriggerRule {
        id: "review-after-degraded-bar-check".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::RunCompleted {
                        run_kind: Some(RunKind::Review),
                        outcome: Some(RunOutcome::Succeeded),
                    },
                },
                Criteria::State {
                    state: StateCriterion::BarCheckDegraded,
                },
            ],
        },
        agent_plan: review_rule().agent_plan,
    }
}

fn finish_on_label_rule() -> TriggerRule {
    TriggerRule {
        id: "finish-on-label".to_owned(),
        run_kind: RunKind::Finish,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::LabelApplied {
                        name: Some("pump19-finish".to_owned()),
                    },
                },
                Criteria::State {
                    state: StateCriterion::CleanAndCurrent,
                },
                Criteria::State {
                    state: StateCriterion::HasConverged,
                },
            ],
        },
        agent_plan: AgentPlan::default(),
    }
}

fn target(id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
    AgentLaunchTarget {
        agent_id: AgentId(id.to_owned()),
        role,
        engine: match family {
            "claude" => AgentEngine::Claude,
            _ => AgentEngine::Codex,
        },
        vendor: "fixture".to_owned(),
        control_plane: "pump19-core".to_owned(),
        lineage: ModelLineage {
            family: ModelFamily(family.to_owned()),
            model: format!("{family}-stable"),
        },
    }
}

fn review_outcome(findings: Vec<Finding>, verdict: ReviewVerdict) -> RunLaunchOutcome {
    let complete = !matches!(verdict, ReviewVerdict::PartialCoverage);
    RunLaunchOutcome {
        outcome: RunOutcome::Succeeded,
        findings,
        verdict: Some(verdict),
        coverage: Some(CoverageRecord {
            complete,
            visited: vec!["src/lib.rs".to_owned()],
            unvisited: if complete {
                Vec::new()
            } else {
                vec!["src/hidden.rs".to_owned()]
            },
            account: "fixture coverage".to_owned(),
            extensions: BTreeMap::new(),
        }),
        patches: Vec::new(),
        token_usage: None,
        session_archives: Vec::new(),
        independence_degradations: Vec::new(),
    }
}

fn fix_outcome(dedup_key: &str, outcome: RunOutcome) -> RunLaunchOutcome {
    RunLaunchOutcome {
        outcome,
        findings: Vec::new(),
        verdict: None,
        coverage: None,
        patches: if outcome == RunOutcome::Succeeded {
            vec![Patch {
                contract_version: ContractVersion::current(),
                id: pump19_contract::PatchId(format!("patch-{dedup_key}")),
                run_id: RunId("fixture-run".to_owned()),
                commit_sha: "head123".to_owned(),
                idempotency_key: format!("patch-{dedup_key}"),
                answers_findings: vec![FindingId(dedup_key.to_owned())],
                change: PatchChange::Description {
                    summary: format!("fixed {dedup_key}"),
                },
                provenance: provenance("fixer-codex", AgentRole::Fixer, "codex"),
                extensions: BTreeMap::new(),
            }]
        } else {
            Vec::new()
        },
        token_usage: None,
        session_archives: Vec::new(),
        independence_degradations: Vec::new(),
    }
}

const fn empty_outcome(outcome: RunOutcome) -> RunLaunchOutcome {
    RunLaunchOutcome {
        outcome,
        findings: Vec::new(),
        verdict: None,
        coverage: None,
        patches: Vec::new(),
        token_usage: None,
        session_archives: Vec::new(),
        independence_degradations: Vec::new(),
    }
}

fn finding(id: &str, priority: PriorityClass, status: VerificationStatus) -> Finding {
    Finding {
        contract_version: ContractVersion::current(),
        id: FindingId(format!("finding-{id}")),
        dedup_key: id.to_owned(),
        source_brief: "fixture".to_owned(),
        title: format!("Fixture finding {id}"),
        explanation: "fixture finding explanation".to_owned(),
        suggestion: Some("apply the fixture fix".to_owned()),
        priority,
        certainty: CertaintyClass::Blocking,
        provenance: provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
        verification: FindingVerification {
            status,
            verifier: Some(provenance("verifier-claude", AgentRole::Verifier, "claude")),
            evidence: Vec::new(),
            cross_family: FamilySplit::CrossFamily,
            extensions: BTreeMap::new(),
        },
        locations: vec![FindingLocation::File {
            path: "src/lib.rs".to_owned(),
            line: Some(12),
            range: None,
        }],
        extensions: BTreeMap::new(),
    }
}

fn bar_check(passed: bool) -> BarCheckRecord {
    BarCheckRecord {
        passed,
        provenance: provenance("bar-codex", AgentRole::BarCheck, "codex"),
        rationale: "fixture bar check".to_owned(),
        extensions: BTreeMap::new(),
    }
}

fn provenance(id: &str, role: AgentRole, family: &str) -> ModelProvenance {
    ModelProvenance {
        contract_version: ContractVersion::current(),
        agent_id: AgentId(id.to_owned()),
        role,
        engine: "fixture".to_owned(),
        session_id: SessionId(format!("session-{id}")),
        freshness: SessionFreshness::FreshForPass { pass_index: 1 },
        verification: ProvenanceVerification::Verified {
            vendor: "fixture".to_owned(),
            control_plane: "pump19-core".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: format!("{family}-stable"),
            },
        },
        extensions: BTreeMap::new(),
    }
}

fn pull_opened() -> ContractEvent {
    ContractEvent {
        contract_version: ContractVersion::current(),
        id: "pull-opened".to_owned(),
        payload: EventPayload::PullRequestOpened { facts: facts() },
        extensions: BTreeMap::new(),
    }
}

fn facts() -> ForgeFacts {
    ForgeFacts {
        contract_version: ContractVersion::current(),
        pr: pr(),
        head: Revision {
            sha: "head123".to_owned(),
        },
        base: Revision {
            sha: "base123".to_owned(),
        },
        branch_currency: BranchCurrency::Current,
        cleanliness: ReviewCleanliness::Clean,
        mergeability: Mergeability::Mergeable,
        finish_label: None,
        actor_permissions: vec![ActorPermissions {
            actor: actor("maintainer"),
            capabilities: BTreeSet::from([
                ActorCapability::ApplyFinishLabel,
                ActorCapability::Merge,
            ]),
        }],
        author_login: Some("author".to_owned()),
        work_in_progress: false,
        extensions: BTreeMap::new(),
    }
}

fn pr() -> PullRequestRef {
    PullRequestRef {
        repository: "acme/widgets".to_owned(),
        id: "42".to_owned(),
    }
}

fn finish_label() -> FinishLabel {
    FinishLabel {
        name: "pump19-finish".to_owned(),
        applied_by: actor("maintainer"),
    }
}

fn actor(id: &str) -> ActorRef {
    ActorRef {
        id: id.to_owned(),
        display_name: id.to_owned(),
    }
}
