#![allow(clippy::expect_used, clippy::too_many_lines, clippy::unwrap_used)]

use std::{
    cell::RefCell,
    collections::{BTreeMap, VecDeque},
    fs,
    path::{Path, PathBuf},
    rc::Rc,
};

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
    CertaintyClass, Confidence, ContractEvent, ContractVersion, DecisionSubject, DecisionVerdict,
    EventPayload, Extensions, Finding, FindingCommentPublication, FindingCommentStatus, FindingId,
    FindingLocation, FinishLabel, ForgeFacts, ForgeReceipt, LoopPassRecord, Mergeability,
    ModelFamily, ModelLineage, ModelProvenance, PatchChange, PrRunState, ProvenanceVerification,
    PublicationAttemptStatus, PublicationOperation, PublicationState, PullRequestRef,
    ReviewCleanliness, Revision, RunCeiling, RunId, RunKind, RunOutcome, RunRecord,
    RunRefusalReason, RunStatus, SessionFreshness, SessionId, Severity,
};
use pump19_core::{
    AgentEngine, AgentLaunchSpec, AgentLaunchTarget, AgentPlan, AuthorisationEvidence,
    AuthorisedComment, AuthorisedCommentResolution, AuthorisedCommentUpdate, AuthorisedFixPush,
    AuthorisedLabel, AuthorisedMerge, CommentFormatter, Core, CoreError, CorePolicy, Criteria,
    DispatchOutcome, EventKind, EventSource, FindingCommentFormatRequest,
    FinishLabelApplicationPolicy, ForgeOperationError, ForgeOperationReceipt, ForgeOperations,
    LaunchProof, LaunchRefusal, PreparedAgent, PreparedSource, RunLaunchOutcome, RunLaunchRequest,
    RunLauncher, RunStateKey, RunStateStore, SkipReason, SourcePreparationRequest, SourcePreparer,
    StateCriterion, TriggerRule, WorkspaceExecOutput, WorkspaceExecRequest, WorkspaceExecutor,
    WorkspaceIsolation, WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
};
use pump19_daemon::{DaemonConfig, run_from_config};
use pump19_forge_forgejo::{
    ForgejoActivityError, ForgejoActor, ForgejoActorPermission, ForgejoBranchCurrency,
    ForgejoEventSource, ForgejoLabelApplication, ForgejoMergeability, ForgejoNormalisationConfig,
    ForgejoPollingClient, ForgejoPollingConfig, ForgejoPullRequestSnapshot,
    ForgejoReviewCleanliness, NormalisationError, PollingForgejoActivitySource, contract_event,
    forge_facts,
};
use pump19_runs::{
    AgentSessionPreparer, EnsembleFixBody, EnsembleJudgeBody, EnsembleReviewBody,
    EnsembleWorkflowConfig, EnsembleWorkflowOutput, EnsembleWorkflowRequest,
    EnsembleWorkflowRunner, FinishRunBody, FixRunBody, JudgeRunBody, MergeGateFinishBody,
    Pump19RunLauncher, ReviewRunBody, RunBodyError, SubjectIntent,
};
use pump19_workspace::{
    CapabilityPolicy, ContainerRuntime, ContainerSpec, ContainerWorkspaceProvider, NetworkPolicy,
    PrivilegeMode, RootFilesystem, WorkspaceConfig, WorkspaceError,
};
use serde_json::{Value, json};
use tempfile::tempdir;

#[derive(Clone, Debug, Default)]
struct EmptyEventSource;

impl EventSource for EmptyEventSource {
    fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError> {
        Ok(None)
    }
}

#[derive(Clone, Debug)]
struct EstatePollingClient {
    polls: VecDeque<Vec<ForgejoPullRequestSnapshot>>,
}

impl ForgejoPollingClient for EstatePollingClient {
    fn open_pull_requests(
        &mut self,
        _repository: &str,
    ) -> Result<Vec<ForgejoPullRequestSnapshot>, ForgejoActivityError> {
        Ok(self.polls.pop_front().unwrap_or_default())
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
struct SourceRecordingWorkspaceProvider {
    lease: WorkspaceLease,
    injections: Rc<RefCell<Vec<PreparedSource>>>,
    cleanups: Rc<RefCell<Vec<String>>>,
}

impl WorkspaceProvider for SourceRecordingWorkspaceProvider {
    fn prepare(&mut self, _request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
        fs::create_dir_all(&self.lease.root).map_err(|source| CoreError::Io {
            path: self.lease.root.display().to_string(),
            source,
        })?;
        Ok(self.lease.clone())
    }

    fn inject_source(
        &mut self,
        _lease: &WorkspaceLease,
        source: &PreparedSource,
    ) -> Result<(), CoreError> {
        fs::create_dir_all(self.lease.root.join("src")).map_err(|source| CoreError::Io {
            path: self.lease.root.join("src").display().to_string(),
            source,
        })?;
        fs::copy(
            source.tree.join("src/lib.rs"),
            self.lease.root.join("src/lib.rs"),
        )
        .map_err(|source| CoreError::Io {
            path: self.lease.root.join("src/lib.rs").display().to_string(),
            source,
        })?;
        self.injections.borrow_mut().push(source.clone());
        Ok(())
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

    fn cleanup(&mut self, lease: &WorkspaceLease) -> Result<(), CoreError> {
        self.cleanups.borrow_mut().push(lease.id.clone());
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

#[derive(Clone, Debug)]
struct FailingLauncher {
    proofs: Rc<RefCell<Vec<LaunchProof>>>,
}

#[derive(Clone, Debug)]
struct RequiredFamilyUnavailableLauncher;

#[derive(Clone, Debug)]
struct SourceCheckingLauncher {
    launched: Rc<RefCell<Vec<RunLaunchRequest>>>,
    expected_source: String,
}

#[derive(Clone, Debug)]
struct PreparedTreeSourcePreparer {
    tree: PathBuf,
    requests: Rc<RefCell<Vec<SourcePreparationRequest>>>,
}

#[derive(Clone, Debug, Default)]
struct FailingSourcePreparer;

#[derive(Clone, Debug, Default)]
struct RecordingForgeOperations {
    comments: Rc<RefCell<Vec<AuthorisedComment>>>,
    comment_updates: Rc<RefCell<Vec<AuthorisedCommentUpdate>>>,
    comment_resolutions: Rc<RefCell<Vec<AuthorisedCommentResolution>>>,
    labels: Rc<RefCell<Vec<AuthorisedLabel>>>,
    merges: Rc<RefCell<Vec<AuthorisedMerge>>>,
    fix_pushes: Rc<RefCell<Vec<AuthorisedFixPush>>>,
}

#[derive(Clone, Debug, Default)]
struct EstateCommentFormatter;

impl CommentFormatter for EstateCommentFormatter {
    fn format_finding_comment(
        &mut self,
        request: FindingCommentFormatRequest,
    ) -> Result<String, CoreError> {
        Ok(format!(
            "estate formatted finding {} via {}",
            request.finding.id.0, request.decision.id
        ))
    }
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
            new_head_sha: None,
        })
    }

    fn update_comment(
        &mut self,
        request: AuthorisedCommentUpdate,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        let operation_id = request.comment_operation_id.clone();
        self.comment_updates.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id,
            idempotency_key,
            new_head_sha: None,
        })
    }

    fn resolve_comment(
        &mut self,
        request: AuthorisedCommentResolution,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        let idempotency_key = request.authorisation.idempotency_key.clone();
        let operation_id = request.comment_operation_id.clone();
        self.comment_resolutions.borrow_mut().push(request);
        Ok(ForgeOperationReceipt {
            operation_id,
            idempotency_key,
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
            operation_id: "label".to_owned(),
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
            operation_id: "merge".to_owned(),
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
            operation_id: "fix-push".to_owned(),
            idempotency_key,
            new_head_sha: Some("head-sha-after-fix".to_owned()),
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

impl FailingLauncher {
    fn new(proofs: Vec<LaunchProof>) -> Self {
        Self {
            proofs: Rc::new(RefCell::new(proofs)),
        }
    }
}

impl SourceCheckingLauncher {
    fn new(expected_source: &str) -> Self {
        Self {
            launched: Rc::new(RefCell::new(Vec::new())),
            expected_source: expected_source.to_owned(),
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
            ensemble_archive_path: None,
        })
    }
}

impl RunLauncher for SourceCheckingLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!("session-{}", spec.pass_index)),
            proof: LaunchProof::EstablishedFresh,
        })
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        let source =
            fs::read_to_string(request.workspace.root.join("src/lib.rs")).map_err(|source| {
                CoreError::Io {
                    path: request
                        .workspace
                        .root
                        .join("src/lib.rs")
                        .display()
                        .to_string(),
                    source,
                }
            })?;
        assert_eq!(source, self.expected_source);
        assert!(request.workspace.isolation.credential_free);
        assert!(request.workspace.isolation.egress_bounded);
        self.launched.borrow_mut().push(request);
        Ok(RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            token_usage: None,
            ensemble_archive_path: None,
        })
    }
}

impl RunLauncher for FailingLauncher {
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
        _request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        Err(CoreError::Launcher(
            "estate forced launch failure".to_owned(),
        ))
    }
}

impl RunLauncher for RequiredFamilyUnavailableLauncher {
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        Err(CoreError::RequiredFamilyUnavailable {
            agent_id: spec.target.agent_id,
            family: spec.target.lineage.family,
            reason: "estate required family unavailable".to_owned(),
        })
    }

    fn launch_run(
        &mut self,
        _request: RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        unreachable!("required-family refusal happens before launch")
    }
}

impl SourcePreparer for PreparedTreeSourcePreparer {
    fn prepare_source(
        &mut self,
        request: SourcePreparationRequest,
    ) -> Result<PreparedSource, CoreError> {
        self.requests.borrow_mut().push(request.clone());
        Ok(PreparedSource {
            tree: self.tree.clone(),
            revision: request.commit_sha,
            cleanup_root: self.tree.parent().map(Path::to_path_buf),
        })
    }
}

impl SourcePreparer for FailingSourcePreparer {
    fn prepare_source(
        &mut self,
        _request: SourcePreparationRequest,
    ) -> Result<PreparedSource, CoreError> {
        Err(CoreError::SourcePreparation(
            "estate source preparation failed".to_owned(),
        ))
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

#[derive(Clone, Debug)]
struct ArchiveAgentFixture {
    label: String,
    engine: String,
    model: String,
}

#[derive(Debug)]
struct EstateEnsembleRunner {
    value: Value,
    agents: Vec<ArchiveAgentFixture>,
    requests: Rc<RefCell<Vec<EnsembleWorkflowRequest>>>,
}

impl EstateEnsembleRunner {
    fn new(value: Value, agents: Vec<ArchiveAgentFixture>) -> Self {
        Self {
            value,
            agents,
            requests: Rc::new(RefCell::new(Vec::new())),
        }
    }

    fn requests(&self) -> Rc<RefCell<Vec<EnsembleWorkflowRequest>>> {
        Rc::clone(&self.requests)
    }
}

impl EnsembleWorkflowRunner for EstateEnsembleRunner {
    fn run_workflow(
        &mut self,
        request: EnsembleWorkflowRequest,
    ) -> Result<EnsembleWorkflowOutput, RunBodyError> {
        write_archive(&request.archive_dir, &self.agents);
        self.requests.borrow_mut().push(request.clone());
        Ok(EnsembleWorkflowOutput {
            value: self.value.clone(),
            archive_dir: request.archive_dir,
        })
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

    fn copy_into(
        &mut self,
        _container_id: &str,
        _host_source: &Path,
        _container_dest: &str,
    ) -> Result<(), WorkspaceError> {
        Ok(())
    }

    fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError> {
        self.removed.borrow_mut().push(container_id.to_owned());
        Ok(())
    }
}

#[derive(Debug, Default)]
struct EstateLoopLauncher;

#[derive(Clone, Debug, Default)]
struct NoOpFixLoopLauncher {
    launched: Rc<RefCell<Vec<RunKind>>>,
}

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
                ensemble_archive_path: None,
            }),
        }
    }
}

impl NoOpFixLoopLauncher {
    fn launched(&self) -> Rc<RefCell<Vec<RunKind>>> {
        Rc::clone(&self.launched)
    }
}

impl RunLauncher for NoOpFixLoopLauncher {
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
        self.launched.borrow_mut().push(request.run_kind);
        match request.run_kind {
            RunKind::Review => Ok(review_outcome(&request)),
            RunKind::Judge => Ok(judge_outcome(&request)),
            RunKind::Fix => Ok(RunLaunchOutcome {
                outcome: RunOutcome::NoOp,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
                ensemble_archive_path: None,
            }),
            RunKind::Finish => Ok(RunLaunchOutcome {
                outcome: RunOutcome::Succeeded,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
                ensemble_archive_path: None,
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
            ensemble_archive_path: None,
        })
    }
}

#[derive(Clone, Debug)]
struct EstateStateStore {
    states: Vec<PrRunState>,
}

#[derive(Clone, Debug, Default)]
struct SharedEstateStateStore {
    states: Rc<RefCell<Vec<PrRunState>>>,
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

impl SharedEstateStateStore {
    fn with_state(state: PrRunState) -> Self {
        Self {
            states: Rc::new(RefCell::new(vec![state])),
        }
    }

    fn states(&self) -> Vec<PrRunState> {
        self.states.borrow().clone()
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
                    || state
                        .run_history
                        .iter()
                        .any(|record| record.run_id == *run_id)
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

impl RunStateStore for SharedEstateStateStore {
    fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .find(|state| state.pr == key.pr && state.commit_sha == key.commit_sha)
            .cloned())
    }

    fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .filter(|state| state.pr == *pr)
            .max_by_key(|state| state.pass_index)
            .cloned())
    }

    fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
        Ok(self
            .states
            .borrow()
            .iter()
            .find(|state| {
                state
                    .extensions
                    .get("pump19.core.running_run_id")
                    .and_then(serde_json::Value::as_str)
                    == Some(run_id.0.as_str())
                    || state
                        .run_history
                        .iter()
                        .any(|record| record.run_id == *run_id)
            })
            .cloned())
    }

    fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
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

    fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
        Ok(self.states())
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

fn finish_label_actor() -> ActorRef {
    actor("maintainer")
}

fn core_service_actor() -> ActorRef {
    ActorRef {
        id: "pump19-core".to_owned(),
        display_name: "Pump-19 Core".to_owned(),
    }
}

fn contract_facts(
    branch_currency: BranchCurrency,
    cleanliness: ReviewCleanliness,
    actor_permissions: Vec<ActorPermissions>,
) -> ForgeFacts {
    contract_facts_with_head(
        "head-sha-1",
        branch_currency,
        cleanliness,
        actor_permissions,
    )
}

fn contract_facts_with_head(
    head_sha: &str,
    branch_currency: BranchCurrency,
    cleanliness: ReviewCleanliness,
    actor_permissions: Vec<ActorPermissions>,
) -> ForgeFacts {
    ForgeFacts {
        contract_version: version(),
        pr: pr(),
        head: Revision {
            sha: head_sha.to_owned(),
        },
        base: Revision {
            sha: "base-sha-1".to_owned(),
        },
        branch_currency,
        cleanliness,
        mergeability: Mergeability::Mergeable,
        finish_label: Some(FinishLabel {
            name: "pump19-finish".to_owned(),
            applied_by: finish_label_actor(),
        }),
        actor_permissions,
        author_login: None,
        work_in_progress: false,
        extensions: extensions(),
    }
}

fn finish_label_actor_permissions() -> Vec<ActorPermissions> {
    vec![ActorPermissions {
        actor: finish_label_actor(),
        capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
            .into_iter()
            .collect(),
    }]
}

fn spurious_core_actor_permissions() -> Vec<ActorPermissions> {
    vec![ActorPermissions {
        actor: core_service_actor(),
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

fn updated_event(id: &str, facts: ForgeFacts) -> ContractEvent {
    ContractEvent {
        contract_version: version(),
        id: id.to_owned(),
        payload: EventPayload::PullRequestUpdated { facts },
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
                applied_by: finish_label_actor(),
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

fn deployment_example_config_path() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join("examples/deployment/pump19-daemon.toml")
}

fn target(agent_id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
    AgentLaunchTarget {
        agent_id: AgentId(agent_id.to_owned()),
        role,
        engine: engine_for_family(family),
        vendor: "local".to_owned(),
        control_plane: "pump19-core".to_owned(),
        lineage: ModelLineage {
            family: ModelFamily(family.to_owned()),
            model: model_for_family(family),
        },
    }
}

fn model_for_family(family: &str) -> String {
    match family {
        "glm" => "openrouter/z-ai/glm-5.2".to_owned(),
        _ => format!("{family}-2026-06"),
    }
}

fn engine_for_family(family: &str) -> AgentEngine {
    match family {
        "claude" => AgentEngine::Claude,
        "codex" => AgentEngine::Codex,
        _ => AgentEngine::Opencode,
    }
}

fn archive_agent(agent_id: &str, family: &str) -> ArchiveAgentFixture {
    ArchiveAgentFixture {
        label: format!("{agent_id}:purpose"),
        engine: engine_for_family(family).as_str().to_owned(),
        model: model_for_family(family),
    }
}

fn ensemble_config(root: &Path, run_kind: &str) -> EnsembleWorkflowConfig {
    let prompt_template = if run_kind == "review" {
        "configured estate prompt\n{{subject_name}}\n{{subject_slug}}\n{{subject_purpose}}\n{{evidence}}"
    } else {
        "configured estate prompt"
    };
    EnsembleWorkflowConfig {
        script: root.join(format!("{run_kind}.js")),
        archive_root: root.join("archives"),
        timeout_ms: 5_000,
        prompt_template: prompt_template.to_owned(),
        briefs: pump19_judgement::baseline_judgement_briefs(),
    }
}

fn write_review_diff(root: &Path, diff: &str) {
    let evidence_dir = root.join(".pump19/review");
    fs::create_dir_all(&evidence_dir).expect("create review evidence dir");
    fs::write(evidence_dir.join("diff.patch"), diff).expect("write review diff");
}

fn write_archive(root: &Path, agents: &[ArchiveAgentFixture]) {
    let run_dir = root.join("runs/cwd/test/test-run");
    fs::create_dir_all(run_dir.join("agents")).expect("create archive agents");
    let files = agents
        .iter()
        .enumerate()
        .map(|(index, agent)| {
            let agent_dir = run_dir.join(format!("agents/{:06}", index + 1));
            fs::create_dir_all(&agent_dir).expect("create agent dir");
            let path = format!("agents/{:06}/agent.json", index + 1);
            let record = json!({
                "id": index + 1,
                "kind": "agent_record",
                "engine": agent.engine,
                "label": agent.label,
                "model": agent.model,
                "resolved_model": agent.model,
                "status": "complete",
                "validated_output": {"ok": true},
            });
            fs::write(
                agent_dir.join("agent.json"),
                serde_json::to_string_pretty(&record).expect("serialise agent"),
            )
            .expect("write agent");
            json!({"path": path, "sha256": "fixture", "size": 1})
        })
        .collect::<Vec<_>>();
    let manifest = json!({
        "kind": "run_manifest",
        "schema_version": 1,
        "status": "complete",
        "run_id": "cwd:test:test-run",
        "result": {"archive_path": "result.json", "exit_code": 0},
        "files": files,
    });
    fs::write(
        run_dir.join("manifest.json"),
        serde_json::to_string_pretty(&manifest).expect("serialise manifest"),
    )
    .expect("write manifest");
}

fn standard_plan() -> AgentPlan {
    AgentPlan {
        reviewers: vec![
            target("reviewer-codex", AgentRole::Reviewer, "codex"),
            target("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        fixers: Vec::new(),
        judge: Some(target("judge-glm", AgentRole::Judge, "glm")),
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

fn review_on_pr_updated_rule() -> TriggerRule {
    TriggerRule {
        id: "review-on-pr-updated".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Event {
            event: EventKind::PullRequestUpdated,
        },
        agent_plan: AgentPlan {
            reviewers: vec![
                target("reviewer-codex", AgentRole::Reviewer, "codex"),
                target("reviewer-claude", AgentRole::Reviewer, "claude"),
            ],
            fixers: Vec::new(),
            judge: Some(target("judge-glm", AgentRole::Judge, "glm")),
            finishers: Vec::new(),
        },
    }
}

fn ready_review_on_pr_change_rule() -> TriggerRule {
    TriggerRule {
        id: "review-on-pr-change".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Any {
                    criteria: vec![
                        Criteria::Event {
                            event: EventKind::PullRequestOpened,
                        },
                        Criteria::Event {
                            event: EventKind::PullRequestUpdated,
                        },
                    ],
                },
                Criteria::PrReady,
            ],
        },
        agent_plan: standard_plan(),
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
            judge: Some(target("judge-glm", AgentRole::Judge, "glm")),
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
        review_on_pr_updated_rule(),
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
    let mut extensions = extensions();
    extensions.insert(
        "pump19.core.agent_engine".to_owned(),
        Value::String(engine_for_family(family).as_str().to_owned()),
    );
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
                model: model_for_family(family),
            },
        },
        extensions,
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
    if request.state.pass_index > 1 {
        return RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            token_usage: None,
            ensemble_archive_path: None,
        };
    }
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
        ensemble_archive_path: None,
    }
}

fn judge_outcome(request: &RunLaunchRequest) -> RunLaunchOutcome {
    let judge = request
        .provenance
        .iter()
        .find(|provenance| provenance.role == AgentRole::Judge)
        .expect("judge provenance")
        .clone();
    if request.state.findings.is_empty() {
        return RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: vec![pump19_contract::Decision {
                contract_version: version(),
                id: format!("decision-pass-{}-converged", request.state.pass_index),
                subject: DecisionSubject::FindingSet {
                    finding_ids: Vec::new(),
                },
                verdict: DecisionVerdict::Converged,
                rationale: "no material findings remain".to_owned(),
                provenance: judge,
                extensions: extensions(),
            }],
            patches: Vec::new(),
            token_usage: None,
            ensemble_archive_path: None,
        };
    }
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
        ensemble_archive_path: None,
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
        ensemble_archive_path: None,
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
        current_head_sha: Some("head-sha-1".to_owned()),
        pass_index: 1,
        status: RunStatus::Running,
        active_run: None,
        run_history: Vec::new(),
        loop_history: Vec::new(),
        superseded_by: None,
        findings: vec![finding()],
        decisions: Vec::new(),
        patches: Vec::new(),
        publication: PublicationState::default(),
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
            agent_id: AgentId("judge-glm".to_owned())
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
    let comment_resolutions = Rc::clone(&forge_operations.comment_resolutions);
    let labels = Rc::clone(&forge_operations.labels);
    let fix_pushes = Rc::clone(&forge_operations.fix_pushes);
    let merges = Rc::clone(&forge_operations.merges);
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        finish_label_actor_permissions(),
    );
    let mut core = Core::with_forge_operations_and_comment_formatter(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-verification-loop")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
        EstateCommentFormatter,
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
        .expect("fix completion publishes commits");
    assert!(outcomes.is_empty());
    assert_eq!(fix_pushes.borrow().len(), 1);
    assert_eq!(fix_pushes.borrow()[0].expected_head_sha, "head-sha-1");
    assert_eq!(fix_pushes.borrow()[0].commits.len(), 1);
    assert_eq!(
        fix_pushes.borrow()[0].commits[0].author_agent_id.0,
        "fixer-codex"
    );

    let updated_facts = contract_facts_with_head(
        "head-sha-after-fix",
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        finish_label_actor_permissions(),
    );
    let outcomes = core
        .process_event(
            &updated_event("forgejo-pr-updated-after-fix", updated_facts),
            &rules,
        )
        .expect("PR update launches fresh review");
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
    assert!(labels.borrow().is_empty());
    assert!(merges.borrow().is_empty());
    assert_eq!(comments.borrow().len(), 1);
    assert_eq!(comment_resolutions.borrow().len(), 1);
    assert_eq!(
        comment_resolutions.borrow()[0].comment_operation_id,
        "comment"
    );
    let converged_state = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest state")
        .expect("converged state");
    assert_eq!(converged_state.publication.fix_pushes.len(), 1);
    assert_eq!(
        converged_state.publication.fix_pushes[0]
            .receipt
            .new_head_sha
            .as_deref(),
        Some("head-sha-after-fix")
    );
    assert!(
        converged_state
            .publication
            .finding_comments
            .iter()
            .any(|comment| comment.status == pump19_contract::FindingCommentStatus::Resolved)
    );
    assert!(
        Criteria::State {
            state: StateCriterion::HasConverged
        }
        .matches(&finish_label_event(), Some(&converged_state))
    );

    let outcomes = core
        .process_event(&finish_label_event(), &rules)
        .expect("finish label launches finish");
    let finish = launched_run_id(&outcomes).expect("finish launched");
    assert!(finish.0.contains("finish-on-label"));
    assert_eq!(merges.borrow().len(), 1);
    assert_eq!(merges.borrow()[0].authorisation.actor, finish_label_actor());
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
    assert!(
        merges.borrow()[0]
            .authorisation
            .evidence
            .iter()
            .any(|evidence| matches!(
                evidence,
                AuthorisationEvidence::ActorCapability {
                    actor,
                    capability: ActorCapability::Merge,
                } if actor == &finish_label_actor()
            ))
    );
}

#[test]
fn convergence_applies_finish_label_and_merges_when_core_has_policy_authority() {
    let forge_operations = RecordingForgeOperations::default();
    let labels = Rc::clone(&forge_operations.labels);
    let merges = Rc::clone(&forge_operations.merges);
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let mut facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        Vec::new(),
    );
    facts.finish_label = None;
    let mut core = Core::with_forge_operations_source_preparer_comment_formatter_and_policy(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-core-applied-finish-estate")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
        pump19_core::NoopSourcePreparer,
        EstateCommentFormatter,
        CorePolicy {
            finish_label_application: FinishLabelApplicationPolicy::CoreOnConvergence {
                label: "pump19-finish".to_owned(),
            },
        },
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
    let outcomes = core
        .process_event(
            &run_completed_event("judge-one-done", judge_one, RunKind::Judge),
            &rules,
        )
        .expect("material judge launches fix");
    let fix_one = launched_run_id(&outcomes).expect("fix launched");
    core.process_event(
        &run_completed_event("fix-one-done", fix_one, RunKind::Fix),
        &rules,
    )
    .expect("fix completion publishes commits");

    let mut updated_facts = contract_facts_with_head(
        "head-sha-after-fix",
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        Vec::new(),
    );
    updated_facts.finish_label = None;
    let outcomes = core
        .process_event(
            &updated_event("forgejo-pr-updated-after-fix", updated_facts),
            &rules,
        )
        .expect("PR update launches re-review");
    let review_two = launched_run_id(&outcomes).expect("second review launched");
    let outcomes = core
        .process_event(
            &run_completed_event("review-two-done", review_two, RunKind::Review),
            &rules,
        )
        .expect("second review completion launches judge");
    let judge_two = launched_run_id(&outcomes).expect("second judge launched");

    core.process_event(
        &run_completed_event("judge-two-done", judge_two, RunKind::Judge),
        &rules,
    )
    .expect("convergence applies finish label");
    let converged = state_observer
        .load_latest_for_pr(&pr())
        .expect("load converged state")
        .expect("converged state saved");
    assert!(
        converged
            .decisions
            .iter()
            .any(|decision| decision.verdict == DecisionVerdict::Converged)
    );
    assert_eq!(
        converged
            .extensions
            .get("pump19.core.forge_facts")
            .and_then(|value| serde_json::from_value::<ForgeFacts>(value.clone()).ok())
            .map(|facts| {
                assert!(facts.actor_permissions.is_empty());
                facts.finish_label.map(|label| label.name)
            }),
        Some(Some("pump19-finish".to_owned()))
    );
    let drained = core
        .drain_available(&rules)
        .expect("core-applied finish label launches finish");

    assert_eq!(labels.borrow().len(), 1);
    assert_eq!(labels.borrow()[0].label, "pump19-finish");
    assert_eq!(labels.borrow()[0].authorisation.actor, core_service_actor());
    assert!(
        labels.borrow()[0]
            .authorisation
            .evidence
            .iter()
            .any(|evidence| matches!(
                evidence,
                AuthorisationEvidence::ActorCapability {
                    actor,
                    capability: ActorCapability::ApplyFinishLabel,
                    ..
                } if actor == &core_service_actor()
            ))
    );
    assert_eq!(merges.borrow().len(), 1);
    assert_eq!(merges.borrow()[0].authorisation.actor, core_service_actor());
    assert!(
        merges.borrow()[0]
            .authorisation
            .evidence
            .iter()
            .any(|evidence| matches!(
                evidence,
                AuthorisationEvidence::ActorCapability {
                    actor,
                    capability: ActorCapability::Merge,
                } if actor == &core_service_actor()
            ))
    );
    assert!(
        drained
            .iter()
            .flatten()
            .any(|outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == "finish-on-label"))
    );
}

#[test]
fn spurious_core_finish_label_does_not_merge_without_policy_grant() {
    let forge_operations = RecordingForgeOperations::default();
    let merges = Rc::clone(&forge_operations.merges);
    let mut facts = contract_facts_with_head(
        "head-sha-1",
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        spurious_core_actor_permissions(),
    );
    facts.finish_label = Some(FinishLabel {
        name: "pump19-finish".to_owned(),
        applied_by: core_service_actor(),
    });
    let mut state = run_state();
    state.status = RunStatus::Completed;
    state.findings = vec![
        finding_with(
            "spurious-core-reviewer-codex",
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            1,
        ),
        finding_with(
            "spurious-core-reviewer-claude",
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            1,
        ),
    ];
    state.decisions = vec![pump19_contract::Decision {
        contract_version: version(),
        id: "decision-converged-spurious-core".to_owned(),
        subject: DecisionSubject::FindingSet {
            finding_ids: Vec::new(),
        },
        verdict: DecisionVerdict::Converged,
        rationale: "estate seeded convergence".to_owned(),
        provenance: verified_provenance("judge-glm", AgentRole::Judge, "glm"),
        extensions: extensions(),
    }];
    state.extensions.insert(
        "pump19.core.forge_facts".to_owned(),
        serde_json::to_value(&facts).expect("serialise forge facts"),
    );
    let state_store = SharedEstateStateStore::with_state(state);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-spurious-core-finish-estate")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
    );
    let event = ContractEvent {
        contract_version: version(),
        id: "spurious-core-finish-label".to_owned(),
        payload: EventPayload::LabelApplied {
            pr: pr(),
            label: facts.finish_label.expect("finish label"),
        },
        extensions: extensions(),
    };

    let error = core
        .process_event(&event, &[finish_on_label_rule()])
        .expect_err("forge-supplied pump19-core actor is ignored without policy grant");

    assert!(matches!(
        error,
        CoreError::ForgeOperation(message)
            if message == "finish label actor lacks merge capability"
    ));
    assert!(merges.borrow().is_empty());
}

#[test]
fn repeated_material_finding_updates_existing_comment_identity() {
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let comment_updates = Rc::clone(&forge_operations.comment_updates);
    let mut state = run_state();
    let material = state.findings[0].clone();
    state.findings.insert(
        0,
        finding_with(
            "supporting-finding",
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            1,
        ),
    );
    state.status = RunStatus::Completed;
    state.run_history.push(RunRecord {
        run_id: RunId("review-pass-1".to_owned()),
        run_kind: RunKind::Review,
        event_id: "forgejo-pr-opened".to_owned(),
        rule_id: "review-on-pr-opened".to_owned(),
        pass_index: 1,
        commit_sha: state.commit_sha.clone(),
        status: RunStatus::Completed,
        outcome: Some(RunOutcome::Succeeded),
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    });
    state.extensions.insert(
        "pump19.core.running_run_id".to_owned(),
        serde_json::Value::String("review-pass-1".to_owned()),
    );
    state.publication = PublicationState {
        attempts: Vec::new(),
        finding_comments: vec![FindingCommentPublication {
            finding_dedup_key: material.dedup_key.clone(),
            latest_finding_id: material.id,
            comment_operation_id: "comment-existing".to_owned(),
            status: FindingCommentStatus::Open,
            last_receipt: ForgeReceipt {
                operation_id: "comment-existing".to_owned(),
                idempotency_key: "comment-existing-key".to_owned(),
                new_head_sha: None,
            },
        }],
        fix_pushes: Vec::new(),
        merges: Vec::new(),
    };
    let state_store = SharedEstateStateStore::with_state(state);
    let state_observer = state_store.clone();
    let mut core = Core::with_forge_operations_and_comment_formatter(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-verification-comment-update")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
        EstateCommentFormatter,
    );

    let outcomes = core
        .process_event(
            &run_completed_event(
                "review-pass-1-completed",
                RunId("review-pass-1".to_owned()),
                RunKind::Review,
            ),
            &[judge_after_review_rule()],
        )
        .expect("review completion launches judge");

    assert!(
        launched_run_id(&outcomes).is_some(),
        "unexpected outcomes: {outcomes:?}"
    );
    assert!(comments.borrow().is_empty());
    assert_eq!(comment_updates.borrow().len(), 1);
    assert_eq!(
        comment_updates.borrow()[0].comment_operation_id,
        "comment-existing"
    );
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load state")
        .expect("state saved");
    assert_eq!(latest.publication.attempts.len(), 1);
    assert_eq!(
        latest.publication.finding_comments[0].comment_operation_id,
        "comment-existing"
    );
    assert_eq!(
        latest.publication.finding_comments[0].status,
        FindingCommentStatus::Open
    );
}

#[test]
fn minor_judge_finding_is_recorded_but_suppressed_from_pr_publication() {
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let comment_updates = Rc::clone(&forge_operations.comment_updates);
    let comment_resolutions = Rc::clone(&forge_operations.comment_resolutions);
    let mut state = run_state();
    state.status = RunStatus::Completed;
    state.pass_index = 2;
    state.findings = vec![
        finding_with(
            "minor-codex",
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            2,
        ),
        finding_with(
            "minor-claude",
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            2,
        ),
    ];
    state.run_history.push(RunRecord {
        run_id: RunId("review-pass-2".to_owned()),
        run_kind: RunKind::Review,
        event_id: "forgejo-pr-updated-after-fix".to_owned(),
        rule_id: "review-on-pr-updated".to_owned(),
        pass_index: 2,
        commit_sha: state.commit_sha.clone(),
        status: RunStatus::Completed,
        outcome: Some(RunOutcome::Succeeded),
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    });
    state.extensions.insert(
        "pump19.core.running_run_id".to_owned(),
        serde_json::Value::String("review-pass-2".to_owned()),
    );
    let state_store = SharedEstateStateStore::with_state(state);
    let state_observer = state_store.clone();
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-verification-suppressed-finding")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
    );

    let outcomes = core
        .process_event(
            &run_completed_event(
                "review-pass-2-completed",
                RunId("review-pass-2".to_owned()),
                RunKind::Review,
            ),
            &[judge_after_review_rule()],
        )
        .expect("review completion launches judge");

    assert!(
        launched_run_id(&outcomes).is_some(),
        "unexpected outcomes: {outcomes:?}"
    );
    assert!(comments.borrow().is_empty());
    assert!(comment_updates.borrow().is_empty());
    assert!(comment_resolutions.borrow().is_empty());
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load state")
        .expect("state saved");
    assert!(
        latest
            .decisions
            .iter()
            .any(|decision| decision.verdict == DecisionVerdict::Minor)
    );
    assert!(latest.publication.attempts.is_empty());
    assert!(latest.publication.finding_comments.is_empty());
}

#[test]
fn run_ceiling_trip_is_skipped_recorded_and_posted_to_the_pr() {
    let mut state = run_state();
    state.status = RunStatus::Completed;
    state.pass_index = 1;
    state.ceiling = Some(RunCeiling {
        max_passes: Some(1),
        token_budget: None,
    });
    let state_store = SharedEstateStateStore::with_state(state);
    let observer = state_store.clone();
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let recorded_launches = Rc::clone(&launcher.launched);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-ceiling-publication-estate")),
        },
        launcher,
        state_store,
        forge_operations,
    );

    let event = opened_event(contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    ));
    let mut replay_event = event.clone();
    replay_event.id = "forgejo-pr-opened-again".to_owned();
    let outcomes = core
        .process_event(&event, &[review_rule(standard_plan())])
        .expect("ceiling refusal is handled");
    let replay = core
        .process_event(&replay_event, &[review_rule(standard_plan())])
        .expect("replayed ceiling refusal is handled");

    assert_eq!(
        outcomes,
        vec![DispatchOutcome::Refused {
            rule_id: "review-on-pr-opened".to_owned(),
            reason: LaunchRefusal::RunCeilingReached,
        }]
    );
    assert_eq!(
        replay,
        vec![DispatchOutcome::Skipped {
            rule_id: "review-on-pr-opened".to_owned(),
            reason: SkipReason::DuplicateDispatch,
        }]
    );
    assert!(recorded_launches.borrow().is_empty());
    assert_eq!(comments.borrow().len(), 1);
    assert!(comments.borrow()[0].body.contains("RunCeilingReached"));
    let latest = observer
        .load_latest_for_pr(&pr())
        .expect("load state")
        .expect("state saved");
    assert_eq!(latest.status, RunStatus::Skipped);
    assert_eq!(latest.run_history.len(), 1);
    let record = &latest.run_history[0];
    assert_eq!(record.status, RunStatus::Skipped);
    assert_eq!(
        record.refusal.as_ref().map(|refusal| &refusal.reason),
        Some(&RunRefusalReason::RunCeilingReached)
    );
    assert_eq!(latest.publication.attempts.len(), 1);
    assert_eq!(
        latest.publication.attempts[0].status,
        PublicationAttemptStatus::Succeeded
    );
    assert!(matches!(
        latest.publication.attempts[0].operation,
        PublicationOperation::PostRefusalComment { .. }
    ));
}

#[test]
fn deployment_example_assets_compose_into_startable_daemon() {
    let runtime = tempdir().expect("runtime dir");
    let mut config = DaemonConfig::load(&deployment_example_config_path())
        .expect("load checked-in deployment example");
    config.forgejo.repositories = vec!["acme/widgets".to_owned()];
    config.forgejo.poll_command.args = vec![
        "--base-url".to_owned(),
        "http://127.0.0.1:9".to_owned(),
        "--finish-label".to_owned(),
        "pump19-finish".to_owned(),
    ];
    config
        .forgejo
        .poll_command
        .env
        .insert("FORGEJO_TOKEN".to_owned(), "estate-token".to_owned());
    config.forgejo.operation_command.args =
        vec!["--base-url".to_owned(), "http://127.0.0.1:9".to_owned()];
    config
        .forgejo
        .operation_command
        .env
        .insert("FORGEJO_TOKEN".to_owned(), "estate-token".to_owned());
    config.state_root = runtime.path().join("state");
    config.workspace.root = runtime.path().join("workspaces");
    config.ensemble.archive_root = runtime.path().join("archives");
    config.loop_control.poll_interval_ms = 0;
    config.loop_control.stop_after_quiet_polls = Some(1);

    run_from_config(config).expect("checked-in example assets compose into daemon");
}

#[test]
fn required_family_unavailable_is_typed_recorded_and_posted_to_the_pr() {
    let state_store = SharedEstateStateStore::default();
    let observer = state_store.clone();
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from(
                "/tmp/pump19-required-family-unavailable-estate",
            )),
        },
        RequiredFamilyUnavailableLauncher,
        state_store,
        forge_operations,
    );

    let outcomes = core
        .process_event(
            &opened_event(contract_facts(
                BranchCurrency::Current,
                ReviewCleanliness::Dirty,
                Vec::new(),
            )),
            &[review_rule(standard_plan())],
        )
        .expect("typed family refusal is handled");

    assert_eq!(
        outcomes,
        vec![DispatchOutcome::Refused {
            rule_id: "review-on-pr-opened".to_owned(),
            reason: LaunchRefusal::RequiredFamilyUnavailable {
                agent_id: AgentId("reviewer-codex".to_owned()),
                family: ModelFamily("codex".to_owned()),
                reason: "estate required family unavailable".to_owned(),
            },
        }]
    );
    assert_eq!(comments.borrow().len(), 1);
    assert!(
        comments.borrow()[0]
            .body
            .contains("RequiredFamilyUnavailable")
    );
    let latest = observer
        .load_latest_for_pr(&pr())
        .expect("load state")
        .expect("state saved");
    assert_eq!(latest.status, RunStatus::Failed);
    assert_eq!(latest.run_history.len(), 1);
    assert_eq!(
        latest.run_history[0]
            .refusal
            .as_ref()
            .map(|refusal| &refusal.reason),
        Some(&RunRefusalReason::RequiredFamilyUnavailable)
    );
    assert_eq!(latest.publication.attempts.len(), 1);
    assert!(matches!(
        latest.publication.attempts[0].operation,
        PublicationOperation::PostFailureComment
    ));
}

#[test]
fn polling_ingress_and_self_emitted_completions_close_the_review_loop() {
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let comment_resolutions = Rc::clone(&forge_operations.comment_resolutions);
    let fix_pushes = Rc::clone(&forge_operations.fix_pushes);
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let polling = PollingForgejoActivitySource::new(
        EstatePollingClient {
            polls: VecDeque::from([
                vec![polling_snapshot("head-sha-1")],
                vec![polling_snapshot("head-sha-after-fix")],
                Vec::new(),
            ]),
        },
        ForgejoPollingConfig::new(vec!["acme/widgets".to_owned()], "pump19-finish"),
    );
    let source = ForgejoEventSource::new(polling, ForgejoNormalisationConfig::new("pump19-finish"));
    let mut core = Core::with_forge_operations_and_comment_formatter(
        source,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-verification-daemon-loop")),
        },
        EstateLoopLauncher,
        state_store,
        forge_operations,
        EstateCommentFormatter,
    );

    let batches = core
        .drain_available(&loop_rules())
        .expect("polling ingress drains self-emitted loop");

    assert_eq!(
        batches
            .iter()
            .filter(|batch| matches!(batch.as_slice(), [DispatchOutcome::Launched { .. }]))
            .count(),
        5,
        "review, judge, fix, re-review, and final judge should launch without external completion echoes"
    );
    assert_eq!(comments.borrow().len(), 1);
    assert_eq!(fix_pushes.borrow().len(), 1);
    assert_eq!(fix_pushes.borrow()[0].expected_head_sha, "head-sha-1");
    assert_eq!(comment_resolutions.borrow().len(), 1);
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest")
        .expect("latest state");
    assert_eq!(latest.commit_sha, "head-sha-after-fix");
    assert_eq!(
        latest.publication.fix_pushes[0]
            .receipt
            .new_head_sha
            .as_deref(),
        Some("head-sha-after-fix")
    );
    assert!(
        Criteria::State {
            state: StateCriterion::HasConverged
        }
        .matches(&finish_label_event(), Some(&latest))
    );
}

#[test]
fn opt_in_poll_reviews_ready_standing_prs_once_and_leaves_drafts_waiting() {
    let state_store = SharedEstateStateStore::default();
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let launch_requests = Rc::clone(&launcher.launched);
    let polling = PollingForgejoActivitySource::new(
        EstatePollingClient {
            polls: VecDeque::from([
                vec![
                    polling_snapshot_for("40", "ready-head-1", false),
                    polling_snapshot_for("41", "draft-head", true),
                    polling_snapshot_for("42", "ready-head-2", false),
                ],
                Vec::new(),
            ]),
        },
        ForgejoPollingConfig::new(vec!["acme/widgets".to_owned()], "pump19-finish"),
    );
    let source = ForgejoEventSource::new(polling, ForgejoNormalisationConfig::new("pump19-finish"));
    let mut core = Core::new(
        source,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-opt-in-draft-estate")),
        },
        launcher,
        state_store,
    );

    core.drain_available(&[ready_review_on_pr_change_rule()])
        .expect("polling ingress drains standing PR set");

    let recorded_requests = launch_requests.borrow();
    let launched_prs = recorded_requests
        .iter()
        .map(|request| request.state.pr.id.as_str())
        .collect::<Vec<_>>();
    assert_eq!(launched_prs, vec!["40", "42"]);
}

#[test]
fn noop_fix_completion_routes_back_to_judge_without_waiting_for_pr_update() {
    let trigger_pack_root = tempdir().expect("baseline trigger pack root");
    let trigger_pack_path =
        pump19_adaptations::write_baseline_trigger_pack(trigger_pack_root.path(), "estate-loop")
            .expect("write baseline trigger pack");
    let rules =
        pump19_adaptations::load_trigger_rules(&trigger_pack_path).expect("load trigger rules");
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let launcher = NoOpFixLoopLauncher::default();
    let launched_kinds = launcher.launched();
    let mut core = Core::with_forge_operations_and_comment_formatter(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-noop-fix-estate")),
        },
        launcher,
        state_store,
        forge_operations,
        EstateCommentFormatter,
    );
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Clean,
        finish_label_actor_permissions(),
    );

    let opened = core
        .process_event(&opened_event(facts), &rules)
        .expect("PR open launches review");
    assert!(matches!(
        opened.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));

    let drained = core
        .drain_available(&rules)
        .expect("self-emitted completions keep the NoOp fix path moving");

    assert_eq!(
        launched_kinds.borrow().as_slice(),
        [
            RunKind::Review,
            RunKind::Judge,
            RunKind::Fix,
            RunKind::Judge,
        ]
    );
    assert_eq!(
        drained
            .iter()
            .filter(|batch| matches!(batch.as_slice(), [DispatchOutcome::Launched { .. }]))
            .count(),
        3,
        "review completion should launch judge, material verdict should launch fix, and NoOp fix completion should launch judge"
    );
    assert!(rules.iter().any(|rule| rule.id == "judge-after-noop-fix"
        && rule.run_kind == RunKind::Judge
        && matches!(
            rule.criteria,
            Criteria::Event {
                event: EventKind::RunCompleted {
                    run_kind: Some(RunKind::Fix),
                    outcome: Some(RunOutcome::NoOp),
                }
            }
        )));
    assert_eq!(comments.borrow().len(), 1);
    assert!(
        comments.borrow()[0]
            .authorisation
            .evidence
            .iter()
            .any(|evidence| matches!(
                evidence,
                AuthorisationEvidence::Decision {
                    verdict: DecisionVerdict::Material,
                    ..
                }
            ))
    );
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest state")
        .expect("state saved");
    assert_eq!(
        latest.pass_index, 1,
        "NoOp fixes do not advance the PR head"
    );
    assert!(
        latest
            .loop_history
            .iter()
            .any(|pass| pass.fix_outcome == Some(RunOutcome::NoOp))
    );
    assert!(
        latest
            .decisions
            .iter()
            .any(|decision| decision.verdict == DecisionVerdict::Converged)
    );
}

#[test]
fn shipped_baseline_trigger_pack_pins_loop_composition() {
    let root = tempdir().expect("baseline trigger pack root");
    let pack_path = pump19_adaptations::write_baseline_trigger_pack(root.path(), "estate-baseline")
        .expect("write baseline trigger pack");
    let rules = pump19_adaptations::load_trigger_rules(&pack_path).expect("load baseline rules");

    assert_eq!(
        rules
            .iter()
            .map(|rule| rule.id.as_str())
            .collect::<Vec<_>>(),
        vec![
            "review-on-pr-change",
            "judge-after-review",
            "judge-after-noop-fix",
            "fix-after-material-judge",
            "finish-on-label",
        ]
    );
    assert!(rules.iter().any(|rule| {
        rule.id == "judge-after-noop-fix"
            && rule.run_kind == RunKind::Judge
            && matches!(
                rule.criteria,
                Criteria::Event {
                    event: EventKind::RunCompleted {
                        run_kind: Some(RunKind::Fix),
                        outcome: Some(RunOutcome::NoOp),
                    }
                }
            )
            && rule
                .agent_plan
                .judge
                .as_ref()
                .is_some_and(|target| target.agent_id.0 == "judge-glm")
    }));
    let finish = rules
        .iter()
        .find(|rule| rule.id == "finish-on-label")
        .expect("finish rule exists");
    let Criteria::All { criteria } = &finish.criteria else {
        assert!(matches!(finish.criteria, Criteria::All { .. }));
        return;
    };
    assert!(criteria.iter().any(|criterion| matches!(
        criterion,
        Criteria::State {
            state: StateCriterion::HasConverged
        }
    )));
    assert!(criteria.iter().any(|criterion| matches!(
        criterion,
        Criteria::State {
            state: StateCriterion::CleanAndCurrent
        }
    )));
}

#[test]
fn real_judge_body_replays_noop_loop_history_findings() {
    let root = tempdir().expect("noop judge estate root");
    let standing = finding();
    let mut request = run_request(
        RunKind::Judge,
        vec![verified_provenance("judge-glm", AgentRole::Judge, "glm")],
    );
    request.state.findings.clear();
    request.state.decisions.clear();
    request.state.loop_history.push(LoopPassRecord {
        pass_index: 1,
        commit_sha: "head-sha-1".to_owned(),
        findings: vec![standing.clone()],
        decisions: vec![pump19_contract::Decision {
            contract_version: version(),
            id: "decision-before-noop".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: standing.id.clone(),
            },
            verdict: DecisionVerdict::Material,
            rationale: "standing before NoOp fix".to_owned(),
            provenance: verified_provenance("judge-glm", AgentRole::Judge, "glm"),
            extensions: extensions(),
        }],
        patches: Vec::new(),
        judge_verdict: Some(DecisionVerdict::Material),
        fix_outcome: Some(RunOutcome::NoOp),
    });
    let runner = EstateEnsembleRunner::new(
        json!([{
            "finding_id": standing.id.0,
            "verdict": "minor",
            "rationale": "NoOp fix exhausted useful changes"
        }]),
        vec![archive_agent("judge-glm", "glm")],
    );
    let workflow_requests = runner.requests();
    let mut judge = EnsembleJudgeBody::new(runner, ensemble_config(root.path(), "judge"));
    let mut executor = EstateExecutor::new(Vec::new());

    let decisions = judge
        .run_judge(&request, &mut executor)
        .expect("real judge body replays NoOp history");

    assert_eq!(decisions.len(), 1);
    assert_eq!(decisions[0].verdict, DecisionVerdict::Minor);
    assert!(matches!(
        decisions[0].subject,
        DecisionSubject::Finding { ref finding_id } if finding_id == &standing.id
    ));
    let workflow_args = &workflow_requests.borrow()[0].args;
    assert_eq!(
        workflow_args["current_findings"][0]["id"],
        json!(standing.id.0)
    );
    assert_eq!(workflow_args["fix_outcomes"], json!(["no_op"]));
    assert_eq!(
        workflow_args["loop_history"][0]["fix_outcome"],
        json!("no_op")
    );
    assert!(executor.execs.is_empty());
}

#[test]
fn restart_rederives_unprocessed_completion_and_drives_next_trigger_once() {
    let mut state = run_state();
    let review_run = RunId("review-completed-before-restart".to_owned());
    state.status = RunStatus::Completed;
    state.active_run = None;
    state.findings = vec![
        finding_with(
            "recovered-codex-finding",
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            1,
        ),
        finding_with(
            "recovered-claude-finding",
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            1,
        ),
    ];
    state.run_history.push(RunRecord {
        run_id: review_run,
        run_kind: RunKind::Review,
        event_id: "forgejo-pr-opened".to_owned(),
        rule_id: "review-on-pr-opened".to_owned(),
        pass_index: 1,
        commit_sha: state.commit_sha.clone(),
        status: RunStatus::Completed,
        outcome: Some(RunOutcome::Succeeded),
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    });
    let state_store = SharedEstateStateStore::with_state(state);
    let state_observer = state_store.clone();
    let launcher = RecordingLauncher::new(vec![LaunchProof::EstablishedFresh]);
    let launch_requests = Rc::clone(&launcher.launched);
    let mut core = Core::new(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-completion-recovery-estate")),
        },
        launcher,
        state_store,
    );

    let recovered = core
        .rederive_pending_completions()
        .expect("completion recovery succeeds");
    assert_eq!(recovered.queued, 1);
    assert_eq!(recovered.terminal_replays, 1);
    assert_eq!(recovered.stale_running_failures, 0);
    let recovered = core
        .rederive_pending_completions()
        .expect("completion recovery is idempotent");
    assert_eq!(recovered.queued, 0);
    assert_eq!(core.pending_event_count(), 1);

    let batches = core
        .drain_available(&[judge_after_review_rule()])
        .expect("recovered completion is processed");

    assert_eq!(
        batches
            .iter()
            .filter(|batch| matches!(batch.as_slice(), [DispatchOutcome::Launched { .. }]))
            .count(),
        1
    );
    assert_eq!(launch_requests.borrow().len(), 1);
    assert_eq!(launch_requests.borrow()[0].run_kind, RunKind::Judge);
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest state")
        .expect("state saved");
    assert!(
        latest
            .run_history
            .iter()
            .any(|record| record.run_kind == RunKind::Judge)
    );
}

#[test]
fn prepared_source_is_injected_into_credential_free_workspace_before_launch() {
    let temp = tempdir().expect("temp source estate");
    let prepared_tree = temp.path().join("prepared-source");
    let workspace_root = temp.path().join("workspace");
    fs::create_dir_all(prepared_tree.join("src")).expect("create prepared source");
    fs::write(
        prepared_tree.join("src/lib.rs"),
        "pub fn injected_source() -> &'static str { \"head-sha-1\" }\n",
    )
    .expect("write prepared source");
    let requests = Rc::new(RefCell::new(Vec::new()));
    let injections = Rc::new(RefCell::new(Vec::new()));
    let cleanups = Rc::new(RefCell::new(Vec::new()));
    let launcher = SourceCheckingLauncher::new(
        "pub fn injected_source() -> &'static str { \"head-sha-1\" }\n",
    );
    let source_launches = Rc::clone(&launcher.launched);
    let mut core = Core::with_forge_operations_and_source_preparer(
        EmptyEventSource,
        SourceRecordingWorkspaceProvider {
            lease: workspace(workspace_root.clone()),
            injections: Rc::clone(&injections),
            cleanups: Rc::clone(&cleanups),
        },
        launcher,
        SharedEstateStateStore::default(),
        RecordingForgeOperations::default(),
        PreparedTreeSourcePreparer {
            tree: prepared_tree.clone(),
            requests: Rc::clone(&requests),
        },
    );

    let outcomes = core
        .process_event(
            &opened_event(contract_facts(
                BranchCurrency::Current,
                ReviewCleanliness::Dirty,
                Vec::new(),
            )),
            &[review_rule(standard_plan())],
        )
        .expect("prepared source launches review");

    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    assert_eq!(requests.borrow().len(), 1);
    assert_eq!(requests.borrow()[0].commit_sha, "head-sha-1");
    assert_eq!(requests.borrow()[0].workspace.root, workspace_root);
    assert_eq!(injections.borrow().len(), 1);
    assert_eq!(injections.borrow()[0].revision, "head-sha-1");
    assert!(!prepared_tree.exists());
    assert_eq!(cleanups.borrow().as_slice(), ["workspace-1"]);
    assert_eq!(source_launches.borrow().len(), 1);
}

#[test]
fn source_preparation_failure_records_surfaces_and_cleans_without_launching() {
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let state_store = SharedEstateStateStore::default();
    let observer = state_store.clone();
    let cleanups = Rc::new(RefCell::new(Vec::new()));
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let failed_prep_launches = Rc::clone(&launcher.launched);
    let mut core = Core::with_forge_operations_and_source_preparer(
        EmptyEventSource,
        SourceRecordingWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-source-prep-failure-estate")),
            injections: Rc::new(RefCell::new(Vec::new())),
            cleanups: Rc::clone(&cleanups),
        },
        launcher,
        state_store,
        forge_operations,
        FailingSourcePreparer,
    );

    let result = core.process_event(
        &opened_event(contract_facts(
            BranchCurrency::Current,
            ReviewCleanliness::Dirty,
            Vec::new(),
        )),
        &[review_rule(standard_plan())],
    );

    assert!(matches!(
        result,
        Err(CoreError::SourcePreparation(message))
            if message == "estate source preparation failed"
    ));
    assert!(failed_prep_launches.borrow().is_empty());
    assert_eq!(cleanups.borrow().as_slice(), ["workspace-1"]);
    assert_eq!(comments.borrow().len(), 1);
    assert!(
        comments.borrow()[0]
            .body
            .contains("estate source preparation failed")
    );
    assert!(
        observer
            .states()
            .iter()
            .any(|state| state.status == RunStatus::Failed)
    );
}

#[test]
fn stale_running_recovery_surfaces_failure_and_reopens_pr_dispatch() {
    let review_run = RunId("review-running-before-restart".to_owned());
    let mut state = run_state();
    state.status = RunStatus::Running;
    state.active_run = Some(RunRecord {
        run_id: review_run.clone(),
        run_kind: RunKind::Review,
        event_id: "forgejo-pr-opened".to_owned(),
        rule_id: "review-on-pr-opened".to_owned(),
        pass_index: 1,
        commit_sha: state.commit_sha.clone(),
        status: RunStatus::Running,
        outcome: None,
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    });
    state.extensions.insert(
        "pump19.core.running_run_id".to_owned(),
        Value::String(review_run.0.clone()),
    );
    let state_store = SharedEstateStateStore::with_state(state);
    let observer = state_store.clone();
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let recovery_launches = Rc::clone(&launcher.launched);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-stale-running-recovery-estate")),
        },
        launcher,
        state_store,
        forge_operations,
    );

    let recovered = core
        .rederive_pending_completions()
        .expect("stale running recovery succeeds");
    assert_eq!(recovered.queued, 1);
    assert_eq!(recovered.terminal_replays, 0);
    assert_eq!(recovered.stale_running_failures, 1);
    assert_eq!(comments.borrow().len(), 1);
    assert!(comments.borrow()[0].body.contains("daemon restarted"));
    let failed = observer
        .load_by_run_id(&review_run)
        .expect("load failed state")
        .expect("failed state saved");
    assert_eq!(failed.status, RunStatus::Failed);
    assert_eq!(failed.run_history[0].outcome, Some(RunOutcome::Failed));

    let outcomes = core
        .process_event(
            &updated_event(
                "retry-after-stale-running-recovery",
                contract_facts(
                    BranchCurrency::Current,
                    ReviewCleanliness::Dirty,
                    Vec::new(),
                ),
            ),
            &[review_on_pr_updated_rule()],
        )
        .expect("PR update launches after recovery");

    assert!(matches!(
        outcomes.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    assert_eq!(recovery_launches.borrow().len(), 1);
}

#[test]
fn duplicate_pr_event_is_idempotent_and_does_not_launch_a_second_run() {
    let state_store = SharedEstateStateStore::default();
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let launch_requests = Rc::clone(&launcher.launched);
    let facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    );
    let event = opened_event(facts);
    let mut core = Core::new(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-idempotency-estate")),
        },
        launcher,
        state_store,
    );
    let rule = review_rule(standard_plan());

    let first = core
        .process_event(&event, std::slice::from_ref(&rule))
        .expect("first event launches review");
    let second = core
        .process_event(&event, &[rule])
        .expect("replayed event is accepted as already handled");

    assert!(matches!(
        first.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    assert_eq!(
        second,
        vec![DispatchOutcome::Skipped {
            rule_id: "review-on-pr-opened".to_owned(),
            reason: SkipReason::DuplicateDispatch,
        }]
    );
    assert_eq!(launch_requests.borrow().len(), 1);
}

#[test]
fn draft_pr_open_update_and_ready_transition_fire_only_when_ready() {
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let launch_requests = Rc::clone(&launcher.launched);
    let mut core = Core::new(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-draft-transition-estate")),
        },
        launcher,
        state_store,
    );
    let rule = ready_review_on_pr_change_rule();
    let mut draft_facts = contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    );
    draft_facts.work_in_progress = true;
    let mut ready_facts = draft_facts.clone();
    ready_facts.work_in_progress = false;

    let draft_open = core
        .process_event(
            &opened_event(draft_facts.clone()),
            std::slice::from_ref(&rule),
        )
        .expect("draft opening is evaluated");
    let draft_update = core
        .process_event(
            &updated_event("forgejo-pr-updated-draft", draft_facts),
            std::slice::from_ref(&rule),
        )
        .expect("draft update is evaluated");
    let ready_update = core
        .process_event(&updated_event("forgejo-pr-ready", ready_facts), &[rule])
        .expect("ready transition launches review");

    assert!(draft_open.is_empty());
    assert!(draft_update.is_empty());
    assert!(matches!(
        ready_update.as_slice(),
        [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review-on-pr-change"
    ));
    assert_eq!(launch_requests.borrow().len(), 1);
    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest")
        .expect("latest state");
    let facts = latest
        .extensions
        .get("pump19.core.forge_facts")
        .and_then(|value| serde_json::from_value::<ForgeFacts>(value.clone()).ok())
        .expect("state carries latest forge facts");
    assert!(!facts.work_in_progress);
}

#[test]
fn running_pr_state_serialises_new_runs_for_the_same_head() {
    let state_store = SharedEstateStateStore::with_state(run_state());
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
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-serialisation-estate")),
        },
        launcher,
        state_store,
    );

    let outcomes = core
        .process_event(&event, &[review_rule(standard_plan())])
        .expect("running state is handled without launching");

    assert_eq!(
        outcomes,
        vec![DispatchOutcome::Skipped {
            rule_id: "review-on-pr-opened".to_owned(),
            reason: SkipReason::SerialisedByActiveRun,
        }]
    );
    assert!(launch_requests.borrow().is_empty());
}

#[test]
fn moved_pr_head_supersedes_running_old_head_and_reenters_on_new_head() {
    let old_review = RunId("old-head-review".to_owned());
    let mut old_state = run_state();
    old_state.status = RunStatus::Running;
    old_state.active_run = Some(RunRecord {
        run_id: old_review.clone(),
        run_kind: RunKind::Review,
        event_id: "forgejo-pr-opened".to_owned(),
        rule_id: "review-on-pr-opened".to_owned(),
        pass_index: 1,
        commit_sha: "head-sha-1".to_owned(),
        status: RunStatus::Running,
        outcome: None,
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    });
    old_state.extensions.insert(
        "pump19.core.running_run_id".to_owned(),
        serde_json::Value::String(old_review.0.clone()),
    );
    let state_store = SharedEstateStateStore::with_state(old_state);
    let observer = state_store.clone();
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let launcher = RecordingLauncher::new(vec![
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
        LaunchProof::EstablishedFresh,
    ]);
    let launch_requests = Rc::clone(&launcher.launched);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-supersession-estate")),
        },
        launcher,
        state_store,
        forge_operations,
    );

    let moved = ContractEvent {
        contract_version: version(),
        id: "forgejo-pr-updated-new-head".to_owned(),
        payload: EventPayload::PullRequestUpdated {
            facts: contract_facts_with_head(
                "head-sha-2",
                BranchCurrency::Current,
                ReviewCleanliness::Dirty,
                Vec::new(),
            ),
        },
        extensions: extensions(),
    };

    let moved_outcome = core
        .process_event(&moved, &[review_on_pr_updated_rule()])
        .expect("new head re-enters review");
    let stale_completion = core
        .process_event(
            &run_completed_event("old-review-completed", old_review, RunKind::Review),
            &[judge_after_review_rule()],
        )
        .expect("stale completion is handled");

    assert!(matches!(
        moved_outcome.as_slice(),
        [DispatchOutcome::Launched { .. }]
    ));
    assert_eq!(launch_requests.borrow().len(), 1);
    let old_state = observer
        .states()
        .into_iter()
        .find(|state| state.commit_sha == "head-sha-1")
        .expect("old-head state exists");
    assert_eq!(old_state.status, RunStatus::Superseded);
    assert_eq!(old_state.superseded_by, Some("head-sha-2".to_owned()));
    assert_eq!(
        stale_completion,
        vec![DispatchOutcome::Skipped {
            rule_id: "judge-after-review".to_owned(),
            reason: SkipReason::SupersededHead,
        }]
    );
    assert!(comments.borrow().is_empty());
}

#[test]
fn launcher_failure_is_recorded_as_failed_pr_run_state() {
    let state_store = SharedEstateStateStore::default();
    let observer = state_store.clone();
    let forge_operations = RecordingForgeOperations::default();
    let comments = Rc::clone(&forge_operations.comments);
    let mut core = Core::with_forge_operations(
        EmptyEventSource,
        EstateWorkspaceProvider {
            lease: workspace(PathBuf::from("/tmp/pump19-failure-estate")),
        },
        FailingLauncher::new(vec![
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
        ]),
        state_store,
        forge_operations,
    );
    let event = opened_event(contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    ));

    let result = core.process_event(&event, &[review_rule(standard_plan())]);

    assert!(matches!(
        result,
        Err(CoreError::Launcher(message)) if message == "estate forced launch failure"
    ));
    assert!(
        observer
            .states()
            .iter()
            .any(|state| state.status == RunStatus::Failed)
    );
    assert_eq!(comments.borrow().len(), 1);
    assert!(
        comments.borrow()[0]
            .body
            .contains("estate forced launch failure")
    );
}

#[test]
fn real_run_bodies_produce_findings_decisions_patches_and_finish_outcomes() {
    let review_workspace = tempdir().expect("review workspace");
    write_review_diff(
        review_workspace.path(),
        "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
    );
    let judgement_value = serde_json::to_value(pump19_judgement::JudgementRun {
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
    let mut review = EnsembleReviewBody::new(
        EstateEnsembleRunner::new(
            judgement_value,
            vec![
                archive_agent("reviewer-codex", "codex"),
                archive_agent("reviewer-claude", "claude"),
            ],
        ),
        ensemble_config(review_workspace.path(), "review"),
    );
    let review_request = RunLaunchRequest {
        run_kind: RunKind::Review,
        workspace: workspace(review_workspace.path().to_path_buf()),
        provenance: vec![
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        ..run_request(RunKind::Review, Vec::new())
    };
    let mut review_executor = EstateExecutor::new(Vec::new());

    let findings = review
        .run_review(&review_request, &mut review_executor)
        .expect("review body maps judgement failures");
    assert_eq!(findings.len(), 1);
    assert_eq!(findings[0].source_brief, "purpose");
    assert_eq!(
        findings[0].provenance.agent_id,
        AgentId("reviewer-codex".to_owned())
    );
    assert!(review_executor.execs.is_empty());

    let judge_workspace = tempdir().expect("judge workspace");
    let mut judge = EnsembleJudgeBody::new(
        EstateEnsembleRunner::new(
            json!([{"finding_id":"finding-material-1","verdict":"material","rationale":"worth another pass"}]),
            vec![archive_agent("judge-glm", "glm")],
        ),
        ensemble_config(judge_workspace.path(), "judge"),
    );
    let mut judge_executor = EstateExecutor::new(Vec::new());
    let judge_request = run_request(
        RunKind::Judge,
        vec![verified_provenance("judge-glm", AgentRole::Judge, "glm")],
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
    assert!(judge_executor.execs.is_empty());

    let fix_workspace = tempdir().expect("fix workspace");
    let mut fix = EnsembleFixBody::new(
        EstateEnsembleRunner::new(
            json!({"kind":"description","summary":"fixed stale merge gate"}),
            vec![archive_agent("fixer-codex", "codex")],
        ),
        ensemble_config(fix_workspace.path(), "fix"),
    );
    let mut fix_executor = EstateExecutor::new(Vec::new());
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
    assert!(fix_executor.execs.is_empty());

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
            id: "maintainer".to_owned(),
            display_name: "maintainer".to_owned(),
        }),
        vec![ForgejoActorPermission {
            actor: ForgejoActor {
                id: "maintainer".to_owned(),
                display_name: "maintainer".to_owned(),
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
    state.status = RunStatus::Completed;
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
                applied_by: finish_label_actor(),
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
fn review_run_body_uses_host_ensemble_not_workspace_executor() {
    let host_workspace = tempdir().expect("host workspace");
    write_review_diff(
        host_workspace.path(),
        "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
    );
    let judgement_value = serde_json::to_value(pump19_judgement::JudgementRun {
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
    let runner = EstateEnsembleRunner::new(
        judgement_value,
        vec![
            archive_agent("reviewer-codex", "codex"),
            archive_agent("reviewer-claude", "claude"),
        ],
    );
    let requests = runner.requests();
    let mut subject_intents = BTreeMap::new();
    subject_intents.insert(
        "acme/widgets".to_owned(),
        SubjectIntent {
            slug: "widgets".to_owned(),
            name: "Acme Widgets".to_owned(),
            purpose: "Keep the widget service reviewable.".to_owned(),
            invariants: Vec::new(),
            behaviours: Vec::new(),
        },
    );
    let mut review = EnsembleReviewBody::with_subject_intents(
        runner,
        ensemble_config(host_workspace.path(), "review"),
        subject_intents,
    );
    let request = RunLaunchRequest {
        run_kind: RunKind::Review,
        workspace: workspace(host_workspace.path().to_path_buf()),
        provenance: vec![
            verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
        ],
        ..run_request(RunKind::Review, Vec::new())
    };
    let mut executor = EstateExecutor::new(Vec::new());

    let findings = review
        .run_review(&request, &mut executor)
        .expect("review body executes through host ensemble");

    assert_eq!(findings.len(), 1);
    assert!(executor.execs.is_empty());
    let requests = requests.borrow();
    let prompt = requests[0].args["briefs"][0]["prompt"]
        .as_str()
        .expect("rendered prompt");
    assert!(prompt.contains("Acme Widgets"));
    assert!(prompt.contains("Keep the widget service reviewable."));
}

#[test]
fn real_workspace_provider_prepares_container_and_cleans_up_after_launch() {
    let root = tempdir().expect("workspace root");
    let runtime = RecordingRuntime::default();
    let created_specs = Rc::clone(&runtime.created);
    let execs = Rc::clone(&runtime.execs);
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
    assert!(execs.borrow().is_empty());

    let launch_requests = launch_requests.borrow();
    let request = launch_requests.first().expect("run launched");
    assert!(request.workspace.isolation.present());
    assert!(request.workspace.root.starts_with(root.path()));
    assert_ne!(request.workspace.root, PathBuf::from("/workspace"));
}

#[test]
fn core_real_launcher_and_workspace_provider_run_review_via_host_ensemble() {
    let root = tempdir().expect("workspace root");
    let prepared_tree = root.path().join("prepared-source");
    fs::create_dir_all(prepared_tree.join("src")).expect("create prepared source");
    fs::write(prepared_tree.join("src/lib.rs"), "pub fn changed() {}\n")
        .expect("write prepared source");
    write_review_diff(
        &prepared_tree,
        "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
    );
    let runtime = RecordingRuntime::default();
    let created_specs = Rc::clone(&runtime.created);
    let execs = Rc::clone(&runtime.execs);
    let provider = ContainerWorkspaceProvider::with_runtime(
        WorkspaceConfig::new(root.path(), "localhost/pump19-workspace:stable"),
        runtime,
    );
    let judgement_value = serde_json::to_value(pump19_judgement::JudgementRun {
        status: pump19_judgement::JudgementStatus::Failed,
        model_families: vec!["codex".to_owned(), "claude".to_owned()],
        briefs: vec![pump19_judgement::JudgementBriefResult {
            brief_id: "purpose".to_owned(),
            status: pump19_judgement::JudgementStatus::Failed,
            reviews: vec![pump19_judgement::ReviewerResult {
                agent_id: "reviewer-codex".to_owned(),
                model_family: "codex".to_owned(),
                status: pump19_judgement::JudgementStatus::Failed,
                stdout: "PUMP19_JUDGEMENT: FAIL composed seam".to_owned(),
                stderr: String::new(),
            }],
        }],
    })
    .expect("judgement serialises");
    let review_runner = EstateEnsembleRunner::new(
        judgement_value,
        vec![
            archive_agent("reviewer-codex", "codex"),
            archive_agent("reviewer-claude", "claude"),
        ],
    );
    let review_requests = review_runner.requests();
    let launcher = Pump19RunLauncher::new(
        EstateSessions,
        EnsembleReviewBody::new(review_runner, ensemble_config(root.path(), "review")),
        EnsembleJudgeBody::new(
            EstateEnsembleRunner::new(json!([]), vec![archive_agent("judge-glm", "glm")]),
            ensemble_config(root.path(), "judge"),
        ),
        EnsembleFixBody::new(
            EstateEnsembleRunner::new(
                json!({"kind":"description","summary":"unused"}),
                vec![archive_agent("fixer-codex", "codex")],
            ),
            ensemble_config(root.path(), "fix"),
        ),
        MergeGateFinishBody,
    );
    let state_store = SharedEstateStateStore::default();
    let state_observer = state_store.clone();
    let source_requests = Rc::new(RefCell::new(Vec::new()));
    let event = opened_event(contract_facts(
        BranchCurrency::Current,
        ReviewCleanliness::Dirty,
        Vec::new(),
    ));
    let mut core = Core::with_forge_operations_and_source_preparer(
        EmptyEventSource,
        provider,
        launcher,
        state_store,
        RecordingForgeOperations::default(),
        PreparedTreeSourcePreparer {
            tree: prepared_tree,
            requests: Rc::clone(&source_requests),
        },
    );

    let outcomes = core
        .process_event(&event, &[review_rule(standard_plan())])
        .expect("core launches real ensemble review through real provider");

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
    assert_eq!(spec.workdir, "/workspace");
    assert!(!spec.host_control_dir.join("pump19.intent.toml").exists());
    assert!(execs.borrow().is_empty());
    assert_eq!(source_requests.borrow().len(), 1);

    let requests = review_requests.borrow();
    let request = requests.first().expect("ensemble workflow invoked");
    assert_eq!(request.script, root.path().join("review.js"));
    assert_eq!(request.timeout_ms, 5_000);
    assert!(
        request
            .archive_dir
            .starts_with(root.path().join("archives"))
    );
    assert_eq!(
        request.args["workspace_root"].as_str(),
        Some(spec.host_control_dir.to_string_lossy().as_ref())
    );
    let prompt = request.args["briefs"][0]["prompt"]
        .as_str()
        .expect("rendered prompt");
    assert!(prompt.contains("acme/widgets"));
    assert!(prompt.contains("Review changes to acme/widgets"));
    assert!(prompt.contains("Prepared workspace tree:"));
    assert!(prompt.contains("diff --git a/src/lib.rs b/src/lib.rs"));
    assert_eq!(
        request.args["evidence"]["workspace_root"].as_str(),
        Some(spec.host_control_dir.to_string_lossy().as_ref())
    );
    assert!(
        request.args["evidence"]["diff"]
            .as_str()
            .expect("diff evidence")
            .contains("+pub fn changed() {}")
    );
    assert_ne!(
        request.args["workspace_root"].as_str(),
        Some("/workspace"),
        "ensemble runs host-side against the lease root, not inside the container workdir"
    );

    let latest = state_observer
        .load_latest_for_pr(&pr())
        .expect("load latest state")
        .expect("review state");
    assert_eq!(latest.status, RunStatus::Completed);
    assert_eq!(latest.findings.len(), 1);
    assert_eq!(latest.findings[0].source_brief, "purpose");
    assert_eq!(
        latest.run_history[0].ensemble_archive_path.as_deref(),
        Some(request.archive_dir.to_string_lossy().as_ref())
    );
    assert_eq!(
        latest.findings[0].provenance.agent_id,
        AgentId("reviewer-codex".to_owned())
    );
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
        title: None,
        draft: None,
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
        author_login: None,
    }
}

fn polling_snapshot(head_sha: &str) -> ForgejoPullRequestSnapshot {
    polling_snapshot_for("42", head_sha, false)
}

fn polling_snapshot_for(id: &str, head_sha: &str, draft: bool) -> ForgejoPullRequestSnapshot {
    ForgejoPullRequestSnapshot {
        repository: "acme/widgets".to_owned(),
        id: id.to_owned(),
        title: Some(if draft {
            "WIP: still shaping".to_owned()
        } else {
            "Ready for review".to_owned()
        }),
        draft: Some(draft),
        head_sha: head_sha.to_owned(),
        base_sha: "base-sha-1".to_owned(),
        branch_currency: ForgejoBranchCurrency::Current,
        cleanliness: ForgejoReviewCleanliness::Clean,
        mergeability: ForgejoMergeability::Mergeable,
        labels: Vec::new(),
        actor_permissions: vec![ForgejoActorPermission {
            actor: ForgejoActor {
                id: "pump19-core".to_owned(),
                display_name: "Pump-19 Core".to_owned(),
            },
            can_apply_finish_label: true,
            can_merge: true,
        }],
        author_login: None,
    }
}
