#![allow(clippy::expect_used, clippy::too_many_lines, clippy::unwrap_used)]

use std::{cell::RefCell, path::PathBuf, rc::Rc};

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
    CertaintyClass, Confidence, ContractEvent, ContractVersion, DecisionSubject, DecisionVerdict,
    EventPayload, Extensions, Finding, FindingId, FindingLocation, FinishLabel, ForgeFacts,
    Mergeability, ModelFamily, ModelLineage, ModelProvenance, PatchChange, PrRunState,
    ProvenanceVerification, PullRequestRef, ReviewCleanliness, Revision, RunId, RunKind,
    RunOutcome, RunStatus, SessionFreshness, SessionId, Severity,
};
use pump19_core::{
    AgentLaunchSpec, AgentLaunchTarget, AgentPlan, AuthorisationEvidence, AuthorisedComment,
    AuthorisedLabel, AuthorisedMerge, Core, CoreError, Criteria, DispatchOutcome, EventKind,
    EventSource, ForgeOperationError, ForgeOperationReceipt, ForgeOperations, LaunchProof,
    LaunchRefusal, PreparedAgent, RunLaunchOutcome, RunLaunchRequest, RunLauncher, RunStateKey,
    RunStateStore, StateCriterion, TriggerRule, WorkspaceExecOutput, WorkspaceExecRequest,
    WorkspaceExecutor, WorkspaceIsolation, WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
};
use pump19_forge_forgejo::{
    ForgejoActor, ForgejoActorPermission, ForgejoBranchCurrency, ForgejoLabelApplication,
    ForgejoMergeability, ForgejoNormalisationConfig, ForgejoPullRequestSnapshot,
    ForgejoReviewCleanliness, NormalisationError, contract_event, forge_facts,
};
use pump19_runs::{
    AgentSessionPreparer, FinishRunBody, FixRunBody, JsonCommandFixBody, JsonCommandJudgeBody,
    JudgeRunBody, JudgementReviewBody, MergeGateFinishBody, Pump19RunLauncher, ReviewRunBody,
    RunBodyError,
};
use pump19_workspace::{
    CapabilityPolicy, ContainerRuntime, ContainerSpec, ContainerWorkspaceProvider, NetworkPolicy,
    PrivilegeMode, RootFilesystem, WorkspaceConfig, WorkspaceError,
};
use tempfile::tempdir;

#[derive(Clone, Debug, Default)]
struct EmptyEventSource;

impl EventSource for EmptyEventSource {
    fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError> {
        Ok(None)
    }
}

#[derive(Clone, Debug)]
struct EstateWorkspaceProvider {
    lease: WorkspaceLease,
}

impl WorkspaceProvider for EstateWorkspaceProvider {
    fn prepare(&mut self, _request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
        Ok(self.lease.clone())
    }

    fn exec(
        &mut self,
        _lease: &WorkspaceLease,
        _request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError> {
        Ok(WorkspaceExecOutput {
            exit_code: 0,
            stdout: b"[]".to_vec(),
            stderr: Vec::new(),
        })
    }

    fn cleanup(&mut self, _lease: &WorkspaceLease) -> Result<(), CoreError> {
        Ok(())
    }
}

#[derive(Clone, Debug)]
struct PlannedLauncher {
    proofs: Vec<LaunchProof>,
    launches: usize,
}

#[derive(Clone, Debug)]
struct RecordingLauncher {
    proofs: Rc<RefCell<Vec<LaunchProof>>>,
    launched: Rc<RefCell<Vec<RunLaunchRequest>>>,
}

#[derive(Clone, Debug, Default)]
struct RecordingForgeOperations {
    comments: Rc<RefCell<Vec<AuthorisedComment>>>,
    labels: Rc<RefCell<Vec<AuthorisedLabel>>>,
    merges: Rc<RefCell<Vec<AuthorisedMerge>>>,
}

impl ForgeOperations for RecordingForgeOperations {
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.comments.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "comment".to_owned(),
            idempotency_key,
        })
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.labels.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "label".to_owned(),
            idempotency_key,
        })
    }

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        self.merges.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id: "merge".to_owned(),
            idempotency_key,
        })
    }
}

impl RecordingLauncher {
    fn new(proofs: Vec<LaunchProof>) -> Self {
        Self {
            proofs: Rc::new(RefCell::new(proofs)),
            launched: Rc::new(RefCell::new(Vec::new())),
        }
    }
}

impl RunLauncher for RecordingLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        let proof = self.proofs.borrow_mut().remove(0);
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}", spec.pass_index)),
            proof,
        })
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        self.launched.borrow_mut().push(request);
        Ok(RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            token_usage: None,
        })
    }
}

#[derive(Clone, Debug, Default)]
struct RecordingRuntime {
    created: Rc<RefCell<Vec<ContainerSpec>>>,
    execs: Rc<RefCell<Vec<(String, WorkspaceExecRequest)>>>,
    removed: Rc<RefCell<Vec<String>>>,
    outputs: Rc<RefCell<Vec<WorkspaceExecOutput>>>,
}

#[derive(Debug)]
struct EstateExecutor {
    outputs: Vec<WorkspaceExecOutput>,
    execs: Vec<WorkspaceExecRequest>,
}

impl EstateExecutor {
    const fn new(outputs: Vec<WorkspaceExecOutput>) -> Self {
        Self {
            outputs,
            execs: Vec::new(),
        }
    }

    fn json(stdout: &str) -> WorkspaceExecOutput {
        WorkspaceExecOutput {
            exit_code: 0,
            stdout: stdout.as_bytes().to_vec(),
            stderr: Vec::new(),
        }
    }
}

impl WorkspaceExecutor for EstateExecutor {
    fn exec(
        &mut self,
        _lease: &WorkspaceLease,
        request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError> {
        self.execs.push(request);
        Ok(self.outputs.remove(0))
    }
}

impl ContainerRuntime for RecordingRuntime {
    fn create(&mut self, spec: &ContainerSpec) -> Result<(), WorkspaceError> {
        self.created.borrow_mut().push(spec.clone());
        Ok(())
    }

    fn exec(
        &mut self,
        container_id: &str,
        request: &WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, WorkspaceError> {
        self.execs
            .borrow_mut()
            .push((container_id.to_owned(), request.clone()));
        Ok(self
            .outputs
            .borrow_mut()
            .pop()
            .unwrap_or_else(|| WorkspaceExecOutput {
                exit_code: 0,
                stdout: b"[]".to_vec(),
                stderr: Vec::new(),
            }))
    }

    fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError> {
        self.removed.borrow_mut().push(container_id.to_owned());
        Ok(())
    }
}

#[derive(Debug, Default)]
struct EstateLoopLauncher;

#[derive(Debug)]
struct EstateSessions;

impl AgentSessionPreparer for EstateSessions {
    fn prepare(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, RunBodyError> {
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}", spec.pass_index)),
            proof: LaunchProof::EstablishedFresh,
        })
    }
}

impl RunLauncher for EstateLoopLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        let agent_id = spec.target.agent_id.clone();
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}-{}", spec.pass_index, agent_id.0)),
            proof: LaunchProof::EstablishedFresh,
        })
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        match request.run_kind {
            RunKind::Review => Ok(review_outcome(&request)),
            RunKind::Judge => Ok(judge_outcome(&request)),
            RunKind::Fix => Ok(fix_outcome(&request)),
            RunKind::Finish => Ok(RunLaunchOutcome {
                outcome: RunOutcome::Succeeded,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
            }),
        }
    }
}

impl PlannedLauncher {
    const fn new(proofs: Vec<LaunchProof>) -> Self {
        Self {
            proofs,
            launches: 0,
        }
    }
}

impl RunLauncher for PlannedLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        let proof = self.proofs.remove(0);
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}", spec.pass_index)),
            proof,
        })
    }

    fn launch_run(
        &mut self,
        _request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        self.launches += 1;
        Ok(RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            token_usage: None,
        })
    }
}

#[derive(Clone, Debug)]
struct EstateStateStore {
    states: Vec<PrRunState>,
}

impl EstateStateStore {
    const fn empty() -> Self {
        Self { states: Vec::new() }
    }

    fn with_state(state: PrRunState) -> Self {
        Self {
            states: vec![state],
        }
    }
}

impl RunStateStore for EstateStateStore {
    fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .iter()
            .find(|state| state.pr == key.pr && state.commit_sha == key.commit_sha)
            .cloned())
    }

    fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .iter()
            .filter(|state| state.pr == *pr)
            .max_by_key(|state| state.pass_index)
            .cloned())
    }

    fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .iter()
            .find(|state| {
                state
                    .extensions
                    .get("pump19.core.running_run_id")
                    .and_then(serde_json::Value::as_str)
                    == Some(run_id.0.as_str())
            })
            .cloned())
    }

    fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
        if let Some(existing) = self
            .states
            .iter_mut()
            .find(|candidate| candidate.pr == state.pr && candidate.commit_sha == state.commit_sha)
        {
            *existing = state.clone();
        } else {
            self.states.push(state.clone());
        }
        Ok(())
    }
}

const fn version() -> ContractVersion {
    ContractVersion::current()
}

const fn extensions() -> Extensions {
    Extensions::new()
}

fn pr() -> PullRequestRef {
    PullRequestRef {
        repository: "acme/widgets".to_owned(),
        id: "42".to_owned(),
    }
}

fn actor(id: &str) -> ActorRef {
    ActorRef {
        id: id.to_owned(),
        display_name: id.to_owned(),
    }
}

fn contract_facts(
    branch_currency: BranchCurrency,
    cleanliness: ReviewCleanliness,
    actor_permissions: Vec<ActorPermissions>,
) -> ForgeFacts {
    ForgeFacts {
        contract_version: version(),
        pr: pr(),
        head: Revision {
            sha: "head-sha-1".to_owned(),
        },
        base: Revision {
            sha: "base-sha-1".to_owned(),
        },
        branch_currency,
        cleanliness,
        mergeability: Mergeability::Mergeable,
        finish_label: Some(FinishLabel {
            name: "pump19-finish".to_owned(),
            applied_by: actor("pump19-core"),
        }),
        actor_permissions,
        extensions: extensions(),
    }
}

fn core_actor_permissions() -> Vec<ActorPermissions> {
    vec![ActorPermissions {
        actor: actor("pump19-core"),
        capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
            .into_iter()
            .collect(),
    }]
}

fn opened_event(facts: ForgeFacts) -> ContractEvent {
    ContractEvent {
        contract_version: version(),
        id: "forgejo-pr-opened".to_owned(),
        payload: EventPayload::PullRequestOpened { facts },
        extensions: extensions(),
    }
}

fn finish_label_event() -> ContractEvent {
    ContractEvent {
        contract_version: version(),
        id: "finish-label".to_owned(),
        payload: EventPayload::LabelApplied {
            pr: pr(),
            label: FinishLabel {
                name: "pump19-finish".to_owned(),
                applied_by: actor("pump19-core"),
            },
        },
        extensions: extensions(),
    }
}

fn workspace(root: PathBuf) -> WorkspaceLease {
    WorkspaceLease {
        id: "workspace-1".to_owned(),
        root,
        isolation: WorkspaceIsolation {
            isolated: true,
            credential_free: true,
            egress_bounded: true,
            resource_bounded: true,
            ephemeral: true,
        },
    }
}

fn target(agent_id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
    AgentLaunchTarget {
        agent_id: AgentId(agent_id.to_owned()),
        role,
        vendor: "local".to_owned(),
        control_plane: "pump19-core".to_owned(),
        lineage: ModelLineage {
            family: ModelFamily(family.to_owned()),
            model: format!("{family}-2026-06"),
        },
    }
}

fn standard_plan() -> AgentPlan {
    AgentPlan {
        reviewers: vec![
            target("reviewer-codex", AgentRole::Reviewer, "codex"),
            target("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        fixers: Vec::new(),
        judge: Some(target("judge-gemini", AgentRole::Judge, "gemini")),
        finishers: Vec::new(),
    }
}

fn review_rule(agent_plan: AgentPlan) -> TriggerRule {
    TriggerRule {
        id: "review-on-pr-opened".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Event {
            event: EventKind::PullRequestOpened,
        },
        agent_plan,
    }
}

fn review_after_fix_rule() -> TriggerRule {
    TriggerRule {
        id: "review-after-fix".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Event {
            event: EventKind::RunCompleted {
                run_kind: Some(RunKind::Fix),
                outcome: Some(RunOutcome::Succeeded),
            },
        },
        agent_plan: AgentPlan {
            reviewers: vec![
                target("reviewer-codex", AgentRole::Reviewer, "codex"),
                target("reviewer-claude", AgentRole::Reviewer, "claude"),
            ],
            fixers: Vec::new(),
            judge: None,
            finishers: Vec::new(),
        },
    }
}

fn judge_after_review_rule() -> TriggerRule {
    TriggerRule {
        id: "judge-after-review".to_owned(),
        run_kind: RunKind::Judge,
        criteria: Criteria::Event {
            event: EventKind::RunCompleted {
                run_kind: Some(RunKind::Review),
                outcome: Some(RunOutcome::Succeeded),
            },
        },
        agent_plan: AgentPlan {
            reviewers: Vec::new(),
            fixers: Vec::new(),
            judge: Some(target("judge-gemini", AgentRole::Judge, "gemini")),
            finishers: Vec::new(),
        },
    }
}

fn fix_after_material_judge_rule() -> TriggerRule {
    TriggerRule {
        id: "fix-after-material-judge".to_owned(),
        run_kind: RunKind::Fix,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::RunCompleted {
                        run_kind: Some(RunKind::Judge),
                        outcome: Some(RunOutcome::Succeeded),
                    },
                },
                Criteria::State {
                    state: StateCriterion::HasMaterialDecision,
                },
            ],
        },
        agent_plan: AgentPlan {
            reviewers: Vec::new(),
            fixers: vec![target("fixer-codex", AgentRole::Fixer, "codex")],
            judge: None,
            finishers: Vec::new(),
        },
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
            ],
        },
        agent_plan: AgentPlan::default(),
    }
}

fn loop_rules() -> Vec<TriggerRule> {
    vec![
        review_rule(standard_plan()),
        review_after_fix_rule(),
        judge_after_review_rule(),
        fix_after_material_judge_rule(),
        finish_on_label_rule(),
    ]
}

fn core_with(
    proofs: Vec<LaunchProof>,
    state_store: EstateStateStore,
    workspace_root: PathBuf,
) -> Core<EmptyEventSource, EstateWorkspaceProvider, PlannedLauncher, EstateStateStore> {
    Core::new(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(workspace_root),
        },
        PlannedLauncher::new(proofs),
        state_store,
    )
}

fn refusal_for(agent_plan: AgentPlan, proofs: Vec<LaunchProof>) -> LaunchRefusal {
    let dir = tempdir().expect("temp workspace");
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    );
    let event = opened_event(facts);
    let mut core = core_with(proofs, EstateStateStore::empty(), dir.path().to_path_buf());
    let outcomes = core
        .process_event(&event, &[review_rule(agent_plan)])
        .expect("process event");
    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Refused { .. }]
    ));
    let [DispatchOutcome::Refused { reason, .. }] = outcomes.as_slice() else {
        unreachable!("asserted refused outcome shape");
    };
    reason.clone()
}

fn verified_launches(agent_plan: AgentPlan) -> Vec<DispatchOutcome> {
    let dir = tempdir().expect("temp workspace");
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    );
    let event = opened_event(facts);
    let proofs = vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ];
    let mut core = core_with(proofs, EstateStateStore::empty(), dir.path().to_path_buf());
    core.process_event(&event, &[review_rule(agent_plan)])
        .expect("process event")
}

fn verified_provenance(agent_id: &str, role: AgentRole, family: &str) -> ModelProvenance {
    ModelProvenance {
        contract_version: version(),
        agent_id: AgentId(agent_id.to_owned()),
        role,
        session_id: SessionId(format!("{agent_id}-session")),
        freshness: SessionFreshness::FreshForPass { pass_index: 1 },
        verification: ProvenanceVerification::Verified {
            vendor: "local".to_owned(),
            control_plane: "pump19-core".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: format!("{family}-2026-06"),
            },
        },
        extensions: extensions(),
    }
}

fn finding() -> Finding {
    Finding {
        contract_version: version(),
        id: FindingId("finding-material-1".to_owned()),
        dedup_key: "review:material:1".to_owned(),
        source_brief: "purpose".to_owned(),
        dimension: "judgement".to_owned(),
        summary: "The merge gate can accept stale state.".to_owned(),
        severity: Severity::High,
        confidence: Confidence::High,
        certainty: CertaintyClass::Advisory,
        provenance: verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
        locations: vec![FindingLocation::General {
            description: "whole change".to_owned(),
        }],
        extensions: extensions(),
    }
}

fn finding_with(id: &str, provenance: ModelProvenance, pass_index: u32) -> Finding {
    Finding {
        contract_version: version(),
        id: FindingId(id.to_owned()),
        dedup_key: format!("loop:{id}"),
        source_brief: "loop".to_owned(),
        dimension: "correctness".to_owned(),
        summary: format!("finding for pass {pass_index}"),
        severity: Severity::High,
        confidence: Confidence::High,
        certainty: CertaintyClass::Advisory,
        provenance,
        locations: vec![FindingLocation::General {
            description: "whole change".to_owned(),
        }],
        extensions: extensions(),
    }
}

fn review_outcome(request: &RunLaunchRequest) -> RunLaunchOutcome {
    let reviewers = request
        .provenance
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
        .cloned()
        .collect::<Vec<_>>();
    let first = reviewers.first().expect("first reviewer").clone();
    let second = reviewers.get(1).expect("second reviewer").clone();
    let judged_id = if request.state.pass_index == 1 {
        "finding-material"
    } else {
        "finding-minor"
    };
    let supporting_id = if request.state.pass_index == 1 {
        "finding-supporting"
    } else {
        "finding-minor-supporting"
    };
    RunLaunchOutcome {
        outcome: RunOutcome::Succeeded,
        findings: vec![
            finding_with(supporting_id, first, request.state.pass_index),
            finding_with(judged_id, second, request.state.pass_index),
        ],
        decisions: Vec::new(),
        patches: Vec::new(),
        token_usage: None,
    }
}

fn judge_outcome(request: &RunLaunchRequest) -> RunLaunchOutcome {
    let judge = request
        .provenance
        .iter()
        .find(|provenance| provenance.role == AgentRole::Judge)
        .expect("judge provenance")
        .clone();
    let verdict = if request.state.pass_index == 1 {
        DecisionVerdict::Material
    } else {
        DecisionVerdict::Minor
    };
    let finding = request.state.findings.last().expect("finding to judge");
    RunLaunchOutcome {
        outcome: RunOutcome::Succeeded,
        findings: Vec::new(),
        decisions: vec![pump19_contract::Decision {
            contract_version: version(),
            id: format!("decision-pass-{}", request.state.pass_index),
            subject: DecisionSubject::Finding {
                finding_id: finding.id.clone(),
            },
            verdict,
            rationale: "deterministic loop verdict".to_owned(),
            provenance: judge,
            extensions: extensions(),
        }],
        patches: Vec::new(),
        token_usage: None,
    }
}

fn fix_outcome(request: &RunLaunchRequest) -> RunLaunchOutcome {
    let fixer = request
        .provenance
        .iter()
        .find(|provenance| provenance.role == AgentRole::Fixer)
        .expect("fixer provenance")
        .clone();
    RunLaunchOutcome {
        outcome: RunOutcome::Succeeded,
        findings: Vec::new(),
        decisions: Vec::new(),
        patches: vec![pump19_contract::Patch {
            contract_version: version(),
            id: pump19_contract::PatchId("patch-material".to_owned()),
            run_id: request.run_id.clone(),
            commit_sha: request.state.commit_sha.clone(),
            idempotency_key: "patch-material".to_owned(),
            answers_findings: vec![FindingId("finding-material".to_owned())],
            change: PatchChange::Description {
                summary: "fixed material finding".to_owned(),
            },
            provenance: fixer,
            extensions: extensions(),
        }],
        token_usage: None,
    }
}

fn run_completed_event(id: &str, run_id: RunId, run_kind: RunKind) -> ContractEvent {
    ContractEvent {
        contract_version: version(),
        id: id.to_owned(),
        payload: EventPayload::RunCompleted {
            run_id,
            run_kind: Some(run_kind),
            outcome: RunOutcome::Succeeded,
        },
        extensions: extensions(),
    }
}

fn launched_run_id(outcomes: &[DispatchOutcome]) -> Option<RunId> {
    let [DispatchOutcome::Launched { run_id, .. }] = outcomes else {
        return None;
    };
    Some(run_id.clone())
}

fn run_state() -> PrRunState {
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        Vec::new(),
    );
    let mut extensions = extensions();
    extensions.insert(
        "pump19.core.forge_facts".to_owned(),
        serde_json::to_value(facts).expect("forge facts serialise"),
    );
    PrRunState {
        contract_version: version(),
        pr: pr(),
        commit_sha: "head-sha-1".to_owned(),
        pass_index: 1,
        status: RunStatus::Running,
        findings: vec![finding()],
        decisions: Vec::new(),
        patches: Vec::new(),
        ceiling: None,
        extensions,
    }
}

fn run_request(run_kind: RunKind, provenance: Vec<ModelProvenance>) -> RunLaunchRequest {
    let dir = tempdir().expect("temp workspace");
    RunLaunchRequest {
        run_id: RunId(format!("run-{run_kind:?}")),
        run_kind,
        event: ContractEvent {
            contract_version: version(),
            id: "run-completed".to_owned(),
            payload: EventPayload::RunCompleted {
                run_id: RunId("previous-run".to_owned()),
                run_kind: None,
                outcome: RunOutcome::Succeeded,
            },
            extensions: extensions(),
        },
        state: run_state(),
        workspace: workspace(dir.keep()),
        provenance,
    }
}

#[test]
fn core_launch_gate_refuses_each_soundness_violation_and_launches_independent_set() {
    let mut single_family = standard_plan();
    single_family.reviewers[1] = target("reviewer-codex-2", AgentRole::Reviewer, "codex");
    assert_eq!(
        refusal_for(
            single_family,
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        ),
        LaunchRefusal::InsufficientReviewerFamilies
    );

    let mut overlap = standard_plan();
    overlap.fixers = vec![target("reviewer-codex", AgentRole::Fixer, "codex")];
    assert_eq!(
        refusal_for(
            overlap,
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        ),
        LaunchRefusal::ReviewerFixerOverlap
    );

    let mut shared_judge_family = standard_plan();
    shared_judge_family.judge = Some(target("judge-codex", AgentRole::Judge, "codex"));
    assert_eq!(
        refusal_for(
            shared_judge_family,
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        ),
        LaunchRefusal::MissingIndependentJudge
    );

    assert_eq!(
        refusal_for(
            standard_plan(),
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::Reused {
                    original_session_id: SessionId("old-judge".to_owned()),
                },
            ],
        ),
        LaunchRefusal::NonFreshSession {
            agent_id: AgentId("judge-gemini".to_owned())
        }
    );

    assert_eq!(
        refusal_for(
            standard_plan(),
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::Unverified {
                    reason: "lineage proof unavailable".to_owned(),
                },
                LaunchProof::EstablishedFresh,
            ],
        ),
        LaunchRefusal::UnverifiedProvenance {
            agent_id: AgentId("reviewer-claude".to_owned()),
            reason: "lineage proof unavailable".to_owned(),
        }
    );

    let outcomes = verified_launches(standard_plan());
    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched {
            rule_id,
            run_id: _
        }] if rule_id == "review-on-pr-opened"
    ));
}

#[test]
fn criteria_triggered_loop_posts_fixes_rereviews_converges_and_merges() {
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let merges = Rc::clone(&forge_operations.merges);
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        core_actor_permissions(),
    );
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-verification-loop")),
        },
        EstateLoopLauncher,
        EstateStateStore::empty(),
        forge_operations,
    );
    let rules = loop_rules();

    let outcomes = core
        .process_event(&opened_event(facts), &rules)
        .expect("PR open launches review");
    let review_one = launched_run_id(&outcomes).expect("review launched");

    let outcomes = core
        .process_event(
            &run_completed_event("review-one-done", review_one, RunKind::Review),
            &rules,
        )
        .expect("review completion launches judge");
    let judge_one = launched_run_id(&outcomes).expect("judge launched");
    assert_eq!(comments.borrow().len(), 1);
    assert!(matches!(
        comments.borrow()[0].authorisation.evidence.as_slice(),
        [
            AuthorisationEvidence::Decision {
                verdict: DecisionVerdict::Material,
                ..
            },
            AuthorisationEvidence::Finding { .. }
        ]
    ));

    let outcomes = core
        .process_event(
            &run_completed_event("judge-one-done", judge_one, RunKind::Judge),
            &rules,
        )
        .expect("material judge completion launches fix");
    let fix_one = launched_run_id(&outcomes).expect("fix launched");

    let outcomes = core
        .process_event(
            &run_completed_event("fix-one-done", fix_one, RunKind::Fix),
            &rules,
        )
        .expect("fix completion launches fresh review");
    let review_two = launched_run_id(&outcomes).expect("second review launched");

    let outcomes = core
        .process_event(
            &run_completed_event("review-two-done", review_two, RunKind::Review),
            &rules,
        )
        .expect("second review completion launches judge");
    let judge_two = launched_run_id(&outcomes).expect("second judge launched");

    let no_fix = core
        .process_event(
            &run_completed_event("judge-two-done", judge_two, RunKind::Judge),
            &rules,
        )
        .expect("minor judge completion is processed");
    assert!(no_fix.is_empty());
    assert_eq!(comments.borrow().len(), 1);

    let outcomes = core
        .process_event(&finish_label_event(), &rules)
        .expect("finish label launches finish");
    let finish = launched_run_id(&outcomes).expect("finish launched");
    assert!(finish.0.contains("finish-on-label"));
    assert_eq!(merges.borrow().len(), 1);
    assert!(
        merges.borrow()[0]
            .authorisation
            .evidence
            .iter()
            .any(|evidence| matches!(
                evidence,
                AuthorisationEvidence::MergeGateCleanAndCurrent { .. }
            ))
    );
}

#[test]
fn real_run_bodies_produce_findings_decisions_patches_and_finish_outcomes() {
    let review_workspace = tempdir().expect("review workspace");
    let mut review = JudgementReviewBody;
    let review_request = RunLaunchRequest {
        run_kind: RunKind::Review,
        workspace: workspace(review_workspace.path().to_path_buf()),
        provenance: vec![
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        ..run_request(RunKind::Review, Vec::new())
    };
    let judgement_json = serde_json::to_string(&pump19_judgement::JudgementRun {
        status: pump19_judgement::JudgementStatus::Failed,
        model_families: vec!["codex".to_owned(), "claude".to_owned()],
        briefs: vec![pump19_judgement::JudgementBriefResult {
            brief_id: "purpose".to_owned(),
            status: pump19_judgement::JudgementStatus::Failed,
            reviews: vec![
                pump19_judgement::ReviewerResult {
                    agent_id: "reviewer-codex".to_owned(),
                    model_family: "codex".to_owned(),
                    status: pump19_judgement::JudgementStatus::Failed,
                    stdout: "PUMP19_JUDGEMENT: FAIL stale merge state".to_owned(),
                    stderr: String::new(),
                },
                pump19_judgement::ReviewerResult {
                    agent_id: "reviewer-claude".to_owned(),
                    model_family: "claude".to_owned(),
                    status: pump19_judgement::JudgementStatus::Passed,
                    stdout: "PUMP19_JUDGEMENT: PASS no concern".to_owned(),
                    stderr: String::new(),
                },
            ],
        }],
    })
    .expect("judgement serialises");
    let mut review_executor = EstateExecutor::new(vec![EstateExecutor::json(&judgement_json)]);

    let findings = review
        .run_review(&review_request, &mut review_executor)
        .expect("review body maps judgement failures");
    assert_eq!(findings.len(), 1);
    assert_eq!(findings[0].source_brief, "purpose");
    assert_eq!(
        findings[0].provenance.agent_id,
        AgentId("reviewer-codex".to_owned())
    );

    let mut judge = JsonCommandJudgeBody::new(json_stdout_command(
        r#"[{"finding_id":"finding-material-1","verdict":"material","rationale":"worth another pass"}]"#,
    ));
    let mut judge_executor = EstateExecutor::new(vec![EstateExecutor::json(
        r#"[{"finding_id":"finding-material-1","verdict":"material","rationale":"worth another pass"}]"#,
    )]);
    let judge_request = run_request(
        RunKind::Judge,
        vec![verified_provenance(
            "judge-gemini",
            AgentRole::Judge,
            "gemini",
        )],
    );
    let decisions = judge
        .run_judge(&judge_request, &mut judge_executor)
        .expect("judge body emits decisions");
    assert_eq!(decisions.len(), 1);
    assert_eq!(decisions[0].verdict, DecisionVerdict::Material);
    assert!(matches!(
        decisions[0].subject,
        DecisionSubject::Finding { ref finding_id } if finding_id == &FindingId("finding-material-1".to_owned())
    ));

    let mut fix = JsonCommandFixBody::new(json_stdout_command(
        r#"{"kind":"description","summary":"fixed stale merge gate"}"#,
    ));
    let mut fix_executor = EstateExecutor::new(vec![EstateExecutor::json(
        r#"{"kind":"description","summary":"fixed stale merge gate"}"#,
    )]);
    let mut fix_request = run_request(
        RunKind::Fix,
        vec![verified_provenance(
            "fixer-codex",
            AgentRole::Fixer,
            "codex",
        )],
    );
    fix_request.state.decisions = decisions;
    let patches = fix
        .run_fix(&fix_request, &mut fix_executor)
        .expect("fix body emits patch");
    assert_eq!(
        patches[0].answers_findings,
        vec![FindingId("finding-material-1".to_owned())]
    );
    assert!(matches!(
        patches[0].change,
        PatchChange::Description { ref summary } if summary == "fixed stale merge gate"
    ));

    let mut finish = MergeGateFinishBody;
    let finish_request = run_request(RunKind::Finish, Vec::new());
    let mut finish_executor = EstateExecutor::new(Vec::new());
    assert_eq!(
        finish
            .run_finish(&finish_request, &mut finish_executor)
            .expect("finish body uses merge gate"),
        RunOutcome::Succeeded
    );
}

#[test]
fn forgejo_normalisation_feeds_core_finish_gate_and_fails_closed_on_missing_authority() {
    let config = ForgejoNormalisationConfig::new("pump19-finish");
    let snapshot = forgejo_snapshot(
        ForgejoBranchCurrency::Current,
        ForgejoReviewCleanliness::Clean,
        Some(ForgejoActor {
            id: "pump19-core".to_owned(),
            display_name: "Pump-19 Core".to_owned(),
        }),
        vec![ForgejoActorPermission {
            actor: ForgejoActor {
                id: "pump19-core".to_owned(),
                display_name: "Pump-19 Core".to_owned(),
            },
            can_apply_finish_label: true,
            can_merge: true,
        }],
    );
    let event = contract_event(
        &config,
        &pump19_forge_forgejo::ForgejoActivity::PullRequestOpened {
            event_id: "forgejo-pr-opened".to_owned(),
            snapshot: snapshot.clone(),
        },
    )
    .expect("normalise activity")
    .expect("PR event is relevant");
    assert!(matches!(
        event.payload,
        EventPayload::PullRequestOpened { .. }
    ));
    let EventPayload::PullRequestOpened { facts } = &event.payload else {
        unreachable!("asserted pull-request event shape");
    };
    assert_eq!(facts.branch_currency, BranchCurrency::Current);
    assert!(facts.actor_permissions.iter().any(|permission| {
        permission
            .capabilities
            .contains(&ActorCapability::ApplyFinishLabel)
            && permission.capabilities.contains(&ActorCapability::Merge)
    }));

    let mut state = run_state();
    state.extensions.insert(
        "pump19.core.forge_facts".to_owned(),
        serde_json::to_value(facts).expect("facts serialise"),
    );
    let label_event = ContractEvent {
        contract_version: version(),
        id: "finish-label".to_owned(),
        payload: EventPayload::LabelApplied {
            pr: pr(),
            label: FinishLabel {
                name: "pump19-finish".to_owned(),
                applied_by: actor("pump19-core"),
            },
        },
        extensions: extensions(),
    };
    let finish_rule = TriggerRule {
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
            ],
        },
        agent_plan: standard_plan(),
    };
    let dir = tempdir().expect("workspace");
    let mut core = core_with(
        vec![
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
        ],
        EstateStateStore::with_state(state),
        dir.path().to_path_buf(),
    );
    let outcomes = core
        .process_event(&label_event, &[finish_rule])
        .expect("finish event processes");
    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));

    let mut unknown_currency = snapshot.clone();
    unknown_currency.branch_currency = ForgejoBranchCurrency::Unknown;
    let stale_facts = forge_facts(&config, &unknown_currency).expect("normalise unknown currency");
    assert_eq!(stale_facts.branch_currency, BranchCurrency::Stale);

    let mut missing_actor = snapshot;
    missing_actor.labels[0].applied_by = None;
    let error = forge_facts(&config, &missing_actor).expect_err("missing label actor fails closed");
    assert!(matches!(
        error,
        NormalisationError::MissingFinishLabelActor { .. }
    ));
}

#[test]
fn review_run_body_executes_through_workspace_executor() {
    let host_workspace = tempdir().expect("host workspace");
    let mut review = JudgementReviewBody;
    let request = RunLaunchRequest {
        run_kind: RunKind::Review,
        workspace: workspace(host_workspace.path().to_path_buf()),
        provenance: vec![
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        ..run_request(RunKind::Review, Vec::new())
    };
    let judgement_json = serde_json::to_string(&pump19_judgement::JudgementRun {
        status: pump19_judgement::JudgementStatus::Failed,
        model_families: vec!["codex".to_owned(), "claude".to_owned()],
        briefs: vec![pump19_judgement::JudgementBriefResult {
            brief_id: "purpose".to_owned(),
            status: pump19_judgement::JudgementStatus::Failed,
            reviews: vec![pump19_judgement::ReviewerResult {
                agent_id: "reviewer-codex".to_owned(),
                model_family: "codex".to_owned(),
                status: pump19_judgement::JudgementStatus::Failed,
                stdout: "PUMP19_JUDGEMENT: FAIL stale state".to_owned(),
                stderr: String::new(),
            }],
        }],
    })
    .expect("judgement serialises");
    let mut executor = EstateExecutor::new(vec![EstateExecutor::json(&judgement_json)]);

    let findings = review
        .run_review(&request, &mut executor)
        .expect("review body executes inside workspace");

    assert_eq!(findings.len(), 1);
    assert_eq!(executor.execs.len(), 1);
    assert_eq!(executor.execs[0].program, "pump19-judgement-run");
    assert_eq!(executor.execs[0].cwd_inside_container, "/workspace");
}

#[test]
fn real_workspace_provider_prepares_container_and_cleans_up_after_launch() {
    let root = tempdir().expect("workspace root");
    let runtime = RecordingRuntime::default();
    let created_specs = Rc::clone(&runtime.created);
    let removed_containers = Rc::clone(&runtime.removed);
    let provider = ContainerWorkspaceProvider::with_runtime(
        WorkspaceConfig::new(root.path(), "localhost/pump19-workspace:stable"),
        runtime,
    );
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let launch_requests = Rc::clone(&launcher.launched);
    let event = opened_event(contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    ));
    let mut core = Core::new(
        EmptyEventSource,
        provider,
        launcher,
        EstateStateStore::empty(),
    );

    let outcomes = core
        .process_event(&event, &[review_rule(standard_plan())])
        .expect("core processes with real workspace provider");

    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    let specs = created_specs.borrow();
    let spec = specs.first().expect("container spec created");
    assert_eq!(spec.network, NetworkPolicy::Disabled);
    assert_eq!(spec.security.root_filesystem, RootFilesystem::ReadOnly);
    assert_eq!(spec.security.capabilities, CapabilityPolicy::DropAll);
    assert_eq!(spec.security.privilege, PrivilegeMode::NoNewPrivileges);
    assert!(!spec.env.keys().any(|key| key.contains("TOKEN")));
    assert_eq!(spec.workdir, "/workspace");
    assert_eq!(
        removed_containers.borrow().as_slice(),
        std::slice::from_ref(&spec.id)
    );

    let launch_requests = launch_requests.borrow();
    let request = launch_requests.first().expect("run launched");
    assert!(request.workspace.isolation.present());
    assert!(request.workspace.root.starts_with(root.path()));
    assert_ne!(request.workspace.root, PathBuf::from("/workspace"));
}

#[test]
fn real_workspace_provider_executes_review_body_inside_container() {
    let root = tempdir().expect("workspace root");
    let judgement_json = serde_json::to_string(&pump19_judgement::JudgementRun {
        status: pump19_judgement::JudgementStatus::Failed,
        model_families: vec!["codex".to_owned(), "claude".to_owned()],
        briefs: vec![pump19_judgement::JudgementBriefResult {
            brief_id: "purpose".to_owned(),
            status: pump19_judgement::JudgementStatus::Failed,
            reviews: vec![pump19_judgement::ReviewerResult {
                agent_id: "reviewer-codex".to_owned(),
                model_family: "codex".to_owned(),
                status: pump19_judgement::JudgementStatus::Failed,
                stdout: "PUMP19_JUDGEMENT: FAIL stale state".to_owned(),
                stderr: String::new(),
            }],
        }],
    })
    .expect("judgement serialises");
    let runtime = RecordingRuntime::default();
    runtime.outputs.borrow_mut().push(WorkspaceExecOutput {
        exit_code: 0,
        stdout: judgement_json.into_bytes(),
        stderr: Vec::new(),
    });
    let created_specs = Rc::clone(&runtime.created);
    let execs = Rc::clone(&runtime.execs);
    let removed_containers = Rc::clone(&runtime.removed);
    let provider = ContainerWorkspaceProvider::with_runtime(
        WorkspaceConfig::new(root.path(), "localhost/pump19-workspace:stable"),
        runtime,
    );
    let launcher = Pump19RunLauncher::new(
        EstateSessions,
        JudgementReviewBody,
        JsonCommandJudgeBody::new(vec!["judge-json".to_owned()]),
        JsonCommandFixBody::new(vec!["fix-json".to_owned()]),
        MergeGateFinishBody,
    );
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    );
    let mut core = Core::new(
        EmptyEventSource,
        provider,
        launcher,
        EstateStateStore::empty(),
    );

    let outcomes = core
        .process_event(&opened_event(facts), &[review_rule(standard_plan())])
        .expect("core launches review through real workspace provider");

    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    let specs = created_specs.borrow();
    let spec = specs.first().expect("container spec created");
    let execs = execs.borrow();
    let (container_id, exec_request) = execs.first().expect("workspace exec called");
    assert_eq!(container_id, &spec.id);
    assert_eq!(exec_request.program, "pump19-judgement-run");
    assert_eq!(exec_request.cwd_inside_container, "/workspace");
    assert!(exec_request.stdin.starts_with(b"{"));
    assert_eq!(
        removed_containers.borrow().as_slice(),
        std::slice::from_ref(&spec.id)
    );
}

fn json_stdout_command(json: &str) -> Vec<String> {
    vec![
        "sh".to_owned(),
        "-c".to_owned(),
        format!(
            "cat >/dev/null; printf '%s\n' '{}'",
            json.replace('\'', "'\\''")
        ),
    ]
}

fn forgejo_snapshot(
    branch_currency: ForgejoBranchCurrency,
    cleanliness: ForgejoReviewCleanliness,
    finish_actor: Option<ForgejoActor>,
    actor_permissions: Vec<ForgejoActorPermission>,
) -> ForgejoPullRequestSnapshot {
    ForgejoPullRequestSnapshot {
        repository: "acme/widgets".to_owned(),
        id: "42".to_owned(),
        head_sha: "head-sha-1".to_owned(),
        base_sha: "base-sha-1".to_owned(),
        branch_currency,
        cleanliness,
        mergeability: ForgejoMergeability::Mergeable,
        labels: vec![ForgejoLabelApplication {
            name: "pump19-finish".to_owned(),
            applied_by: finish_actor,
        }],
        actor_permissions,
    }
}
