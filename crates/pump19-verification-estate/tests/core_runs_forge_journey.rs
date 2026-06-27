#![allow(clippy::expect_used, clippy::too_many_lines, clippy::unwrap_used)]

use std::{cell::RefCell, path::PathBuf, rc::Rc};

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
    CertaintyClass, Confidence, ContractEvent, ContractVersion, DecisionSubject, DecisionVerdict,
    EventPayload, Extensions, Finding, FindingId, FindingLocation, FinishLabel, ForgeFacts,
    Mergeability, ModelFamily, ModelLineage, ModelProvenance, PatchChange, PrRunState,
    ProvenanceVerification, PullRequestRef, ReviewCleanliness, Revision, RunId, RunOutcome,
    RunStatus, SessionFreshness, SessionId, Severity,
};
use pump19_core::{
    AgentLaunchSpec, AgentLaunchTarget, AgentPlan, Core, CoreError, Criteria, DispatchOutcome,
    EventKind, EventSource, LaunchProof, LaunchRefusal, PreparedAgent, RunKind, RunLaunchOutcome,
    RunLaunchRequest, RunLauncher, RunStateKey, RunStateStore, StateCriterion, TriggerRule,
    WorkspaceIsolation, WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
};
use pump19_forge_forgejo::{
    ForgejoActor, ForgejoActorPermission, ForgejoBranchCurrency, ForgejoLabelApplication,
    ForgejoMergeability, ForgejoNormalisationConfig, ForgejoPullRequestSnapshot,
    ForgejoReviewCleanliness, NormalisationError, contract_event, forge_facts,
};
use pump19_judgement::{
    IntentApp, IntentSpec, IntentStatement, JudgementBrief, Reviewer, ReviewerConfig, save_intent,
};
use pump19_runs::{
    FinishRunBody, FixRunBody, JsonCommandFixBody, JsonCommandJudgeBody, JudgeRunBody,
    JudgementReviewBody, MergeGateFinishBody, ReviewRunBody,
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

    fn launch_run(&mut self, request: RunLaunchRequest) -> Result<RunLaunchOutcome, CoreError> {
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
    removed: Rc<RefCell<Vec<String>>>,
}

impl ContainerRuntime for RecordingRuntime {
    fn create(&mut self, spec: &ContainerSpec) -> Result<(), WorkspaceError> {
        self.created.borrow_mut().push(spec.clone());
        Ok(())
    }

    fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError> {
        self.removed.borrow_mut().push(container_id.to_owned());
        Ok(())
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

    fn launch_run(&mut self, _request: RunLaunchRequest) -> Result<RunLaunchOutcome, CoreError> {
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

fn opened_event(facts: ForgeFacts) -> ContractEvent {
    ContractEvent {
        contract_version: version(),
        id: "forgejo-pr-opened".to_owned(),
        payload: EventPayload::PullRequestOpened { facts },
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
fn real_run_bodies_produce_findings_decisions_patches_and_finish_outcomes() {
    let review_workspace = tempdir().expect("review workspace");
    install_review_workspace(review_workspace.path());
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

    let findings = review
        .run_review(&review_request)
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
    let judge_request = run_request(
        RunKind::Judge,
        vec![verified_provenance(
            "judge-gemini",
            AgentRole::Judge,
            "gemini",
        )],
    );
    let decisions = judge
        .run_judge(&judge_request)
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
    let mut fix_request = run_request(
        RunKind::Fix,
        vec![verified_provenance(
            "fixer-codex",
            AgentRole::Fixer,
            "codex",
        )],
    );
    fix_request.state.decisions = decisions;
    let patches = fix.run_fix(&fix_request).expect("fix body emits patch");
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
    assert_eq!(
        finish
            .run_finish(&finish_request)
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
fn workspace_lease_boundary_is_currently_assertion_only_for_run_bodies() {
    let host_workspace = tempdir().expect("host workspace");
    install_review_workspace(host_workspace.path());
    let marker = host_workspace.path().join("host-visible-marker.txt");
    std::fs::write(&marker, "visible from host path").expect("write marker");

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

    let findings = review
        .run_review(&request)
        .expect("review body executes against lease root");

    assert_eq!(findings.len(), 1);
    assert!(marker.exists());
}

#[test]
fn real_workspace_provider_prepares_container_but_launcher_still_receives_host_control_path() {
    let root = tempdir().expect("workspace root");
    let runtime = RecordingRuntime::default();
    let created_specs = Rc::clone(&runtime.created);
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

    let launch_requests = launch_requests.borrow();
    let request = launch_requests.first().expect("run launched");
    assert!(request.workspace.isolation.present());
    assert!(request.workspace.root.starts_with(root.path()));
    assert_ne!(request.workspace.root, PathBuf::from("/workspace"));
}

fn install_review_workspace(root: &std::path::Path) {
    save_intent(
        root,
        &IntentSpec {
            app: IntentApp {
                slug: "widgets".to_owned(),
                name: "Widgets".to_owned(),
                purpose: "Exercise real Pump-19 review body".to_owned(),
                author_agent_id: Some("author-agent".to_owned()),
            },
            invariants: Vec::new(),
            behaviours: vec![IntentStatement {
                id: "purpose".to_owned(),
                statement: "The implementation should be reviewed.".to_owned(),
            }],
        },
    )
    .expect("save intent");
    write_toml(
        &root.join("verification/judgement/purpose.toml"),
        &JudgementBrief {
            id: "purpose".to_owned(),
            title: "Purpose".to_owned(),
            intent_ref: "behaviours.purpose".to_owned(),
            brief: "Fail one reviewer to produce a contract finding.".to_owned(),
            evidence_paths: Vec::new(),
        },
    );
    write_toml(
        &root.join("verification/reviewers.toml"),
        &ReviewerConfig {
            reviewers: vec![
                Reviewer {
                    agent_id: "reviewer-codex".to_owned(),
                    model_family: "codex".to_owned(),
                    command: vec![
                        "sh".to_owned(),
                        "-c".to_owned(),
                        "printf '%s\n' 'PUMP19_JUDGEMENT: FAIL stale merge state'".to_owned(),
                    ],
                },
                Reviewer {
                    agent_id: "reviewer-claude".to_owned(),
                    model_family: "claude".to_owned(),
                    command: vec![
                        "sh".to_owned(),
                        "-c".to_owned(),
                        "printf '%s\n' 'PUMP19_JUDGEMENT: PASS no concern'".to_owned(),
                    ],
                },
            ],
        },
    );
}

fn write_toml<T: serde::Serialize>(path: &std::path::Path, value: &T) {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).expect("create parent");
    }
    let text = toml::to_string_pretty(value).expect("serialise toml");
    std::fs::write(path, text).expect("write toml");
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
