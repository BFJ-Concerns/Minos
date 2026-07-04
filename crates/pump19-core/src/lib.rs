#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::too_many_lines,
        clippy::unwrap_used,
        reason = "unit tests use small fakes and direct fixture assertions"
    )
)]

use std::{
    collections::{BTreeMap, BTreeSet, VecDeque},
    fs,
    path::{Path, PathBuf},
};

use pump19_contract::{
    ActorCapability, ActorRef, AgentId, AgentRole, ContractEvent, ContractVersion, Decision,
    DecisionVerdict, EventPayload, Extensions, Finding, FindingCommentPublication,
    FindingCommentStatus, FinishLabel, FixPushPublication, ForgeFacts, ForgeReceipt,
    LoopPassRecord, MergePublication, ModelFamily, ModelLineage, ModelProvenance, Patch,
    PrRunState, ProvenanceVerification, PublicationAttempt, PublicationAttemptStatus,
    PublicationOperation, PublicationRefusal, PublicationRefusalReason, PublicationState,
    PublishedFixCommit, PullRequestRef, RunCeiling, RunId, RunKind, RunOutcome, RunRecord,
    RunRefusal, RunRefusalReason, RunStatus, SessionFreshness, SessionId,
    has_two_verified_reviewer_families, judge_independent_of_reviewers,
    merge_gate_clean_and_current, reviewers_disjoint_from_fixers, sessions_fresh_for_pass,
};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use thiserror::Error;

const EXT_RUNNING_RUN_ID: &str = "pump19.core.running_run_id";
const EXT_LAST_EVENT_ID: &str = "pump19.core.last_event_id";
const EXT_LAST_RULE_ID: &str = "pump19.core.last_rule_id";
const EXT_LAST_RUN_KIND: &str = "pump19.core.last_run_kind";
const EXT_TOKENS_USED: &str = "pump19.core.tokens_used";
const EXT_FORGE_FACTS: &str = "pump19.core.forge_facts";
const EXT_LAST_FAILURE: &str = "pump19.core.last_failure";
const EXT_SELF_EMITTED_EVENT: &str = "pump19.core.self_emitted_event";
const CONTROL_COMMIT_SHA: &str = "__pump19_pr_control__";
const EXT_AGENT_ENGINE: &str = "pump19.core.agent_engine";

/// Core errors raised before a launch decision can be made.
#[derive(Debug, Error)]
pub enum CoreError {
    #[error("event source failed: {0}")]
    EventSource(String),
    #[error("workspace provider failed: {0}")]
    Workspace(String),
    #[error("source preparation failed: {0}")]
    SourcePreparation(String),
    #[error("comment formatting failed: {0}")]
    CommentFormatting(String),
    #[error("run launcher failed: {0}")]
    Launcher(String),
    #[error("required model family {family:?} for agent {agent_id:?} is unavailable: {reason}")]
    RequiredFamilyUnavailable {
        agent_id: AgentId,
        family: ModelFamily,
        reason: String,
    },
    #[error("state store failed: {0}")]
    StateStore(String),
    #[error("forge operation failed: {0}")]
    ForgeOperation(String),
    #[error("I/O error at {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("JSON error at {path}: {source}")]
    Json {
        path: String,
        #[source]
        source: serde_json::Error,
    },
}

/// Supplies normalised contract events to the deterministic core.
pub trait EventSource {
    /// Returns the next event, or `None` when no event is currently available.
    ///
    /// # Errors
    ///
    /// Returns an error when the event adapter cannot read or normalise its source.
    fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError>;
}

/// Provides an isolated, credential-free workspace for a run.
pub trait WorkspaceProvider {
    /// Prepares a workspace for the requested run.
    ///
    /// # Errors
    ///
    /// Returns an error when the provider cannot create or describe the workspace.
    fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError>;

    /// Injects a trusted, already-prepared source tree into the workspace boundary.
    ///
    /// # Errors
    ///
    /// Returns an error when the provider cannot make the source visible to
    /// commands executed inside the isolated workspace.
    fn inject_source(
        &mut self,
        _lease: &WorkspaceLease,
        _source: &PreparedSource,
    ) -> Result<(), CoreError> {
        Ok(())
    }

    /// Executes a command inside the prepared workspace boundary.
    ///
    /// # Errors
    ///
    /// Returns an error when the provider cannot execute the command inside the
    /// isolated workspace.
    fn exec(
        &mut self,
        lease: &WorkspaceLease,
        request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError>;

    /// Tears down a prepared workspace.
    ///
    /// # Errors
    ///
    /// Returns an error when the provider cannot remove the workspace boundary or
    /// host-side control residue.
    fn cleanup(&mut self, lease: &WorkspaceLease) -> Result<(), CoreError>;
}

/// Prepares agent sessions and launches already-authorised run bodies.
pub trait RunLauncher {
    /// Prepares an agent session without starting the run body.
    ///
    /// # Errors
    ///
    /// Returns an error when the launcher cannot allocate a session.
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError>;

    /// Launches a run after the core has established and enforced provenance.
    ///
    /// # Errors
    ///
    /// Returns an error when the authorised run body cannot be started or completed.
    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError>;
}

/// Checks out and prepares PR source on the trusted side of the workspace boundary.
pub trait SourcePreparer {
    /// Returns whether this preparer should run for dispatches.
    #[must_use]
    fn enabled(&self) -> bool {
        true
    }

    /// Produces a plain, credential-free tree for the requested PR revision.
    ///
    /// # Errors
    ///
    /// Returns an error when checkout, dependency prefetch, or any mechanical
    /// preparation step fails.
    fn prepare_source(
        &mut self,
        request: SourcePreparationRequest,
    ) -> Result<PreparedSource, CoreError>;
}

/// Renders material finding comments before the core posts them.
///
/// The core owns the posting decision and authorisation; this collaborator owns
/// the prose. Deployments normally back it with a mechanical adaptation step.
pub trait CommentFormatter {
    /// Renders a material finding and its judge decision into a PR comment body.
    ///
    /// # Errors
    ///
    /// Returns an error when the formatter step fails or returns malformed output.
    fn format_finding_comment(
        &mut self,
        request: FindingCommentFormatRequest,
    ) -> Result<String, CoreError>;
}

/// Data handed to the comment-formatting adaptation for one material finding.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FindingCommentFormatRequest {
    pub run_id: RunId,
    pub finding: Finding,
    pub decision: Decision,
    pub facts: ForgeFacts,
}

/// Durable operator-facing event emitted for operational failures.
///
/// These records are the audience-correct surface for failures, refusals and
/// ceiling trips. PR comments remain reserved for product output: material
/// findings, fix commits and verdicts.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct OperatorLogEvent {
    pub contract_version: ContractVersion,
    pub kind: OperatorLogEventKind,
    pub pr: PullRequestRef,
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub pass_index: u32,
    pub commit_sha: String,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refusal_reason: Option<RunRefusalReason>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OperatorLogEventKind {
    RunFailure,
    LaunchRefusal,
}

/// Records operational events for the deployment operator.
pub trait OperatorLog {
    /// Appends an operator-facing operational event.
    ///
    /// # Errors
    ///
    /// Returns an error when the durable deployment-owned record cannot be written.
    fn record(&mut self, event: OperatorLogEvent) -> Result<(), CoreError>;
}

/// Operator log used by tests and embedding code that does not need a runtime log.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct NoopOperatorLog;

impl OperatorLog for NoopOperatorLog {
    fn record(&mut self, _event: OperatorLogEvent) -> Result<(), CoreError> {
        Ok(())
    }
}

/// Formatter used when a core is constructed without a formatting adaptation.
///
/// Posting a finding comment without an explicit formatter would silently move
/// prose policy back into the core, so the default fails closed instead.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct MissingCommentFormatter;

impl CommentFormatter for MissingCommentFormatter {
    fn format_finding_comment(
        &mut self,
        _request: FindingCommentFormatRequest,
    ) -> Result<String, CoreError> {
        Err(CoreError::CommentFormatting(
            "no FormatComments mechanical step is configured".to_owned(),
        ))
    }
}

/// Source preparation disabled for tests and configurations that explicitly do
/// not need source material.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct NoopSourcePreparer;

impl SourcePreparer for NoopSourcePreparer {
    fn enabled(&self) -> bool {
        false
    }

    fn prepare_source(
        &mut self,
        request: SourcePreparationRequest,
    ) -> Result<PreparedSource, CoreError> {
        Ok(PreparedSource {
            tree: request.workspace.root,
            revision: request.commit_sha,
            cleanup_root: None,
        })
    }
}

/// Executes forge side effects after the core has already authorised them.
pub trait ForgeOperations {
    /// Posts an authorised PR comment.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or the forge rejects it.
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Updates an authorised PR comment that the core previously posted.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or the forge rejects it.
    fn update_comment(
        &mut self,
        request: AuthorisedCommentUpdate,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Resolves an authorised PR comment that the core previously posted.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or the forge rejects it.
    fn resolve_comment(
        &mut self,
        request: AuthorisedCommentResolution,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Applies an authorised PR label.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or the forge rejects it.
    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Merges an authorised PR.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or the forge rejects it.
    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Pushes authorised fix commits to the pull request head branch.
    ///
    /// # Errors
    ///
    /// Returns an error when the request shape is invalid or a plain, non-force
    /// push cannot be accepted by the forge.
    fn push_fix_commits(
        &mut self,
        request: AuthorisedFixPush,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;
}

/// No-op forge operation implementation for tests and side-effect-free wiring.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct NoopForgeOperations;

impl ForgeOperations for NoopForgeOperations {
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-comment".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
            new_head_sha: None,
        })
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-label".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
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

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-merge".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
            new_head_sha: None,
        })
    }

    fn push_fix_commits(
        &mut self,
        request: AuthorisedFixPush,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-fix-push".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
            new_head_sha: Some(request.expected_head_sha),
        })
    }
}

/// Authorisation evidence the core passes with an already-approved forge operation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum AuthorisationEvidence {
    ActorCapability {
        actor: ActorRef,
        capability: ActorCapability,
    },
    Decision {
        decision_id: String,
        verdict: DecisionVerdict,
    },
    Finding {
        finding_id: pump19_contract::FindingId,
    },
    Patch {
        patch_id: pump19_contract::PatchId,
    },
    RunFailure {
        run_id: RunId,
    },
    LaunchRefusal {
        run_id: RunId,
    },
    FinishLabelAuthority {
        label: FinishLabel,
    },
    MergeGateCleanAndCurrent {
        facts_head_sha: String,
    },
}

/// Core-issued authorisation context for one forge side effect.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisationContext {
    pub pr: PullRequestRef,
    pub observed_head_sha: String,
    pub idempotency_key: String,
    pub actor: ActorRef,
    pub reason: String,
    pub evidence: Vec<AuthorisationEvidence>,
}

/// Authorised request to post a PR comment.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedComment {
    pub authorisation: AuthorisationContext,
    pub expected_head_sha: String,
    pub body: String,
}

/// Authorised request to update a PR comment previously posted by the core.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedCommentUpdate {
    pub authorisation: AuthorisationContext,
    pub expected_head_sha: String,
    pub comment_operation_id: String,
    pub body: String,
}

/// Authorised request to resolve a PR comment previously posted by the core.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedCommentResolution {
    pub authorisation: AuthorisationContext,
    pub expected_head_sha: String,
    pub comment_operation_id: String,
    pub reason: String,
}

/// Authorised request to apply a PR label.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedLabel {
    pub authorisation: AuthorisationContext,
    pub label: String,
}

/// Authorised request to merge a PR.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedMerge {
    pub authorisation: AuthorisationContext,
    pub method: MergeMethod,
}

/// Authorised request to turn fix patches into ordinary commits on the PR branch.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedFixPush {
    pub authorisation: AuthorisationContext,
    pub expected_head_sha: String,
    pub commits: Vec<AuthorisedFixCommit>,
}

/// One attributed fix commit to create in the credentialed push step.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorisedFixCommit {
    pub patch: Patch,
    pub message: String,
    pub author_agent_id: AgentId,
    pub provenance: ModelProvenance,
}

/// Merge method requested from a forge adapter.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum MergeMethod {
    Merge,
    Squash,
    Rebase,
}

/// Core-side policy for applying the finish label after convergence.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CorePolicy {
    pub finish_label_application: FinishLabelApplicationPolicy,
}

impl CorePolicy {
    #[must_use]
    pub const fn human_gate() -> Self {
        Self {
            finish_label_application: FinishLabelApplicationPolicy::HumanOnly,
        }
    }
}

impl Default for CorePolicy {
    fn default() -> Self {
        Self::human_gate()
    }
}

/// Whether convergence may cause the core itself to apply the finish label.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum FinishLabelApplicationPolicy {
    HumanOnly,
    CoreOnConvergence { label: String },
}

/// Receipt returned by an outbound forge operation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgeOperationReceipt {
    pub operation_id: String,
    pub idempotency_key: String,
    pub new_head_sha: Option<String>,
}

/// Errors raised by the outbound operation seam.
#[derive(Debug, Error)]
pub enum ForgeOperationError {
    #[error("authorised forge operation has invalid shape: {0}")]
    InvalidRequest(&'static str),
    #[error(
        "PR head moved before operation: expected {expected_head_sha}, actual {actual_head_sha:?}"
    )]
    HeadMoved {
        expected_head_sha: String,
        actual_head_sha: Option<String>,
    },
    #[error("credentialed forge client failed: {0}")]
    Client(String),
}

/// Capability exposed to run bodies for executing inside an already-prepared workspace.
pub trait WorkspaceExecutor {
    /// Executes a command inside the workspace represented by `lease`.
    ///
    /// # Errors
    ///
    /// Returns an error when the workspace provider cannot execute the command.
    fn exec(
        &mut self,
        lease: &WorkspaceLease,
        request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError>;
}

impl<T> WorkspaceExecutor for T
where
    T: WorkspaceProvider,
{
    fn exec(
        &mut self,
        lease: &WorkspaceLease,
        request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError> {
        WorkspaceProvider::exec(self, lease, request)
    }
}

/// Persists per-PR run state for crash recovery and criteria evaluation.
///
/// The core assumes a single writer owns a PR's dispatch loop. That is the daemon
/// entrypoint's job: stores do not provide cross-process mutual exclusion, so a
/// second concurrent core process can still race between precheck and save.
pub trait RunStateStore {
    /// Loads state for a pull request at a specific commit.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot read or decode the state.
    fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError>;

    /// Loads the newest known state for a pull request.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot read or decode candidate states.
    fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError>;

    /// Loads the state that recorded the supplied running run id.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot read or decode candidate states.
    fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError>;

    /// Loads states that may contain terminal runs whose completion events need
    /// regenerating after a daemon restart.
    ///
    /// Stores that cannot enumerate safely may return an empty list. The JSON
    /// deployment store implements this so the self-emitted completion queue is
    /// recoverable from durable state rather than process memory.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot read or decode candidate states.
    fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
        Ok(Vec::new())
    }

    /// Saves state durably enough that a crash cannot be mistaken for success.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot encode or persist the state.
    fn save(&mut self, state: &PrRunState) -> Result<(), CoreError>;
}

/// Summary of completion events rederived from durable state after restart.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct CompletionRecoverySummary {
    pub queued: usize,
    pub terminal_replays: usize,
    pub stale_running_failures: usize,
    pub errors_continued: usize,
}

/// Deterministic dispatch-and-enforce core.
#[derive(Debug)]
pub struct Core<
    E,
    W,
    L,
    S,
    F = NoopForgeOperations,
    P = NoopSourcePreparer,
    C = MissingCommentFormatter,
    O = NoopOperatorLog,
> {
    event_source: E,
    pending_events: VecDeque<ContractEvent>,
    queued_completion_event_ids: BTreeSet<String>,
    workspace_provider: W,
    launcher: L,
    state_store: S,
    forge_operations: F,
    source_preparer: P,
    comment_formatter: C,
    operator_log: O,
    policy: CorePolicy,
}

impl<E, W, L, S>
    Core<
        E,
        W,
        L,
        S,
        NoopForgeOperations,
        NoopSourcePreparer,
        MissingCommentFormatter,
        NoopOperatorLog,
    >
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
{
    #[must_use]
    pub const fn new(event_source: E, workspace_provider: W, launcher: L, state_store: S) -> Self {
        Self::with_forge_operations(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            NoopForgeOperations,
        )
    }
}

impl<E, W, L, S, F> Core<E, W, L, S, F>
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
    F: ForgeOperations,
{
    #[must_use]
    pub const fn with_forge_operations(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
    ) -> Self {
        Self::with_forge_operations_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            CorePolicy::human_gate(),
        )
    }

    #[must_use]
    pub const fn with_forge_operations_and_policy(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        policy: CorePolicy,
    ) -> Self {
        Self::with_forge_operations_source_preparer_comment_formatter_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            NoopSourcePreparer,
            MissingCommentFormatter,
            NoopOperatorLog,
            policy,
        )
    }
}

impl<E, W, L, S, F, O> Core<E, W, L, S, F, NoopSourcePreparer, MissingCommentFormatter, O>
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
    F: ForgeOperations,
    O: OperatorLog,
{
    #[must_use]
    pub const fn with_forge_operations_and_operator_log(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        operator_log: O,
    ) -> Self {
        Self::with_forge_operations_source_preparer_comment_formatter_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            NoopSourcePreparer,
            MissingCommentFormatter,
            operator_log,
            CorePolicy::human_gate(),
        )
    }
}

impl<E, W, L, S, F, C> Core<E, W, L, S, F, NoopSourcePreparer, C>
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
    F: ForgeOperations,
    C: CommentFormatter,
{
    #[must_use]
    pub const fn with_forge_operations_and_comment_formatter(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        comment_formatter: C,
    ) -> Self {
        Self::with_forge_operations_source_preparer_comment_formatter_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            NoopSourcePreparer,
            comment_formatter,
            NoopOperatorLog,
            CorePolicy::human_gate(),
        )
    }
}

impl<E, W, L, S, F, P> Core<E, W, L, S, F, P, MissingCommentFormatter>
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
    F: ForgeOperations,
    P: SourcePreparer,
{
    #[must_use]
    pub const fn with_forge_operations_and_source_preparer(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        source_preparer: P,
    ) -> Self {
        Self::with_forge_operations_source_preparer_comment_formatter_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            source_preparer,
            MissingCommentFormatter,
            NoopOperatorLog,
            CorePolicy::human_gate(),
        )
    }

    #[must_use]
    pub const fn with_forge_operations_source_preparer_and_policy(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        source_preparer: P,
        policy: CorePolicy,
    ) -> Self {
        Self::with_forge_operations_source_preparer_comment_formatter_and_policy(
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            source_preparer,
            MissingCommentFormatter,
            NoopOperatorLog,
            policy,
        )
    }
}

impl<E, W, L, S, F, P, C, O> Core<E, W, L, S, F, P, C, O>
where
    E: EventSource,
    W: WorkspaceProvider,
    L: RunLauncher,
    S: RunStateStore,
    F: ForgeOperations,
    P: SourcePreparer,
    C: CommentFormatter,
    O: OperatorLog,
{
    #[must_use]
    #[allow(
        clippy::too_many_arguments,
        reason = "runtime assembly supplies each core collaborator explicitly"
    )]
    pub const fn with_forge_operations_source_preparer_comment_formatter_and_policy(
        event_source: E,
        workspace_provider: W,
        launcher: L,
        state_store: S,
        forge_operations: F,
        source_preparer: P,
        comment_formatter: C,
        operator_log: O,
        policy: CorePolicy,
    ) -> Self {
        Self {
            event_source,
            pending_events: VecDeque::new(),
            queued_completion_event_ids: BTreeSet::new(),
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
            source_preparer,
            comment_formatter,
            operator_log,
            policy,
        }
    }

    /// Processes one event from the configured event source.
    ///
    /// # Errors
    ///
    /// Returns an error when event ingestion, state persistence, workspace preparation,
    /// provenance preparation, or launch fails.
    pub fn process_next(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Option<Vec<DispatchOutcome>>, CoreError> {
        let event = if let Some(event) = self.pending_events.pop_front() {
            event
        } else {
            let Some(event) = self.event_source.next_event()? else {
                return Ok(None);
            };
            event
        };
        self.process_event(&event, rules).map(Some)
    }

    /// Processes events until both ingress and self-emitted queues are quiet.
    ///
    /// # Errors
    ///
    /// Returns an error when event ingestion or dispatch fails.
    pub fn drain_available(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Vec<Vec<DispatchOutcome>>, CoreError> {
        let mut batches = Vec::new();
        while let Some(outcomes) = self.process_next(rules)? {
            batches.push(outcomes);
        }
        Ok(batches)
    }

    /// Regenerates self-emitted run-completion events from durable run state.
    ///
    /// This is the crash-recovery counterpart to the in-process completion queue:
    /// after a restart, the completed run record is still present in the store,
    /// so the same stable completion event id can be derived and re-entered into
    /// ordinary dispatch. Duplicate prechecks then skip any completion that was
    /// already processed before the crash.
    ///
    /// # Errors
    ///
    /// Returns an error when the state store cannot enumerate or decode candidate
    /// states for recovery.
    pub fn rederive_pending_completions(&mut self) -> Result<CompletionRecoverySummary, CoreError> {
        let pending_ids = self
            .pending_events
            .iter()
            .map(|event| event.id.clone())
            .collect::<BTreeSet<_>>();
        let mut summary = CompletionRecoverySummary::default();
        for state in self.state_store.completion_recovery_states()? {
            if state.status == RunStatus::Running {
                match self.resolve_stale_running_state(state) {
                    Ok(Some(event)) => {
                        summary.stale_running_failures += 1;
                        if pending_ids.contains(&event.id)
                            || self.queued_completion_event_ids.contains(&event.id)
                        {
                            continue;
                        }
                        self.queue_completion_event(event);
                        summary.queued += 1;
                    }
                    Ok(None) => {
                        summary.stale_running_failures += 1;
                    }
                    Err(error) if recovery_error_is_fatal(&error) => return Err(error),
                    Err(_error) => {
                        summary.errors_continued += 1;
                    }
                }
                continue;
            }
            for record in &state.run_history {
                if record.status == RunStatus::Failed {
                    continue;
                }
                let Some(event) = run_completed_event_from_record(record) else {
                    continue;
                };
                let needs_retry = completion_event_has_failed_dispatch(&state, &event);
                if pending_ids.contains(&event.id)
                    || (self.queued_completion_event_ids.contains(&event.id) && !needs_retry)
                {
                    continue;
                }
                self.queue_completion_event(event);
                summary.terminal_replays += 1;
                summary.queued += 1;
            }
        }
        Ok(summary)
    }

    fn resolve_stale_running_state(
        &mut self,
        mut state: PrRunState,
    ) -> Result<Option<ContractEvent>, CoreError> {
        let Some(mut record) = state.active_run.clone() else {
            state.status = RunStatus::Failed;
            state.extensions.insert(
                EXT_LAST_FAILURE.to_owned(),
                Value::String(
                    "daemon restarted with stale running state but no active run record".to_owned(),
                ),
            );
            self.state_store.save(&state)?;
            return Ok(None);
        };
        let message = "daemon restarted while run was still recorded as running";
        state.status = RunStatus::Failed;
        state.extensions.insert(
            EXT_LAST_FAILURE.to_owned(),
            Value::String(message.to_owned()),
        );
        record_terminal_run(
            &mut state,
            record.clone(),
            RunStatus::Failed,
            Some(RunOutcome::Failed),
        );
        self.state_store.save(&state)?;
        self.record_operator_failure(&state, &record.run_id, message)?;
        self.state_store.save(&state)?;
        record.status = RunStatus::Failed;
        record.outcome = Some(RunOutcome::Failed);
        Ok(run_completed_event_from_record(&record))
    }

    #[must_use]
    pub fn pending_event_count(&self) -> usize {
        self.pending_events.len()
    }

    /// Evaluates trigger rules for one event and launches only authorised runs.
    ///
    /// # Errors
    ///
    /// Returns an error when state persistence, workspace preparation, provenance
    /// preparation, or launch fails.
    pub fn process_event(
        &mut self,
        event: &ContractEvent,
        rules: &[TriggerRule],
    ) -> Result<Vec<DispatchOutcome>, CoreError> {
        self.record_forge_event_state(event)?;
        let mut outcomes = Vec::new();
        for rule in rules {
            let state = self.load_state_for_event(event)?;
            if !rule.criteria.matches(event, state.as_ref()) {
                continue;
            }

            let Some(state) = state.clone().or_else(|| initial_state_from_event(event)) else {
                outcomes.push(DispatchOutcome::Skipped {
                    rule_id: rule.id.clone(),
                    reason: SkipReason::NoRunState,
                });
                continue;
            };

            outcomes.push(self.dispatch_rule(event, rule, state)?);
        }
        Ok(outcomes)
    }

    fn record_forge_event_state(&mut self, event: &ContractEvent) -> Result<(), CoreError> {
        if !matches!(
            event.payload,
            EventPayload::PullRequestOpened { .. } | EventPayload::PullRequestUpdated { .. }
        ) {
            return Ok(());
        }
        if let Some(state) = self.load_state_for_event(event)? {
            self.state_store.save(&state)?;
        }
        Ok(())
    }

    fn load_state_for_event(
        &mut self,
        event: &ContractEvent,
    ) -> Result<Option<PrRunState>, CoreError> {
        match &event.payload {
            EventPayload::PullRequestOpened { facts }
            | EventPayload::PullRequestUpdated { facts } => self.load_state_for_forge_event(facts),
            EventPayload::LabelApplied { pr, .. } => self.state_store.load_latest_for_pr(pr),
            EventPayload::RunCompleted { run_id, .. } => self.state_store.load_by_run_id(run_id),
        }
    }

    fn load_state_for_forge_event(
        &mut self,
        facts: &ForgeFacts,
    ) -> Result<Option<PrRunState>, CoreError> {
        let key = RunStateKey::from_facts(facts);
        let exact = self.state_store.load(&key)?;
        let Some(mut latest) = self.state_store.load_latest_for_pr(&facts.pr)? else {
            return Ok(exact);
        };
        if latest.commit_sha == facts.head.sha {
            sync_state_with_forge_facts(&mut latest, facts)?;
            return Ok(Some(latest));
        }

        if let Some(mut stale) = exact {
            mark_superseded(
                &mut stale,
                latest
                    .current_head_sha
                    .clone()
                    .unwrap_or_else(|| latest.commit_sha.clone()),
                None,
            );
            self.state_store.save(&stale)?;
            return Ok(Some(stale));
        }

        if latest.status == RunStatus::Running {
            mark_superseded(&mut latest, facts.head.sha.clone(), None);
            latest.current_head_sha = Some(facts.head.sha.clone());
            self.state_store.save(&latest)?;
        }

        let mut next = initial_state_from_facts(facts);
        next.pass_index = latest.pass_index;
        next.ceiling = latest.ceiling;
        next.publication = latest.publication.clone();
        if latest.status == RunStatus::Completed
            && (!latest.findings.is_empty()
                || !latest.decisions.is_empty()
                || !latest.patches.is_empty())
        {
            latest.loop_history.push(loop_record_from_state(
                &latest,
                latest.patches.clone(),
                None,
            ));
        }
        next.loop_history = latest.loop_history;
        Ok(Some(next))
    }

    fn dispatch_rule(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        state: PrRunState,
    ) -> Result<DispatchOutcome, CoreError> {
        let run_id = run_id_for(event, rule, state.pass_index);
        if let Some(outcome) = self.handle_dispatch_precheck(&state, event, rule, &run_id)? {
            return Ok(outcome);
        }

        let prepared = match self.prepare_provenance_or_refusal(event, rule, &state, &run_id)? {
            Ok(prepared) => prepared,
            Err(outcome) => return Ok(outcome),
        };
        let workspace = self.prepare_workspace_or_record_failure(event, rule, &state, &run_id)?;
        let mut gate_provenance = collect_state_provenance(&state);
        gate_provenance.extend(prepared.iter().cloned());

        if let Some(reason) =
            evaluate_gate(&gate_provenance, &prepared, state.pass_index, &workspace)
        {
            let failure = format!("launch refused: {reason:?}");
            let record_result = self.record_failed_launch_refusal(
                state,
                event,
                rule,
                &run_id,
                &reason,
                failure.as_str(),
            );
            let cleanup_result = self.workspace_provider.cleanup(&workspace);
            finish_before_cleanup(record_result, cleanup_result)?;
            return Ok(DispatchOutcome::Refused {
                rule_id: rule.id.clone(),
                reason,
            });
        }

        self.prepare_and_inject_source(event, rule, &state, &run_id, &workspace)?;

        let mut running_state = mark_running(state, event, rule, &run_id, prepared.clone());
        self.save_running_or_cleanup(&running_state, &workspace)?;

        let launch_result = self.launcher.launch_run(
            RunLaunchRequest {
                run_id: run_id.clone(),
                run_kind: rule.run_kind,
                event: event.clone(),
                state: running_state.clone(),
                workspace: workspace.clone(),
                provenance: prepared,
            },
            &mut self.workspace_provider,
        );
        let outcome = match launch_result {
            Ok(outcome) => outcome,
            Err(error) => {
                let message = error.to_string();
                let record_result = self.record_failed_running_state(
                    &mut running_state,
                    event,
                    rule,
                    &run_id,
                    &message,
                );
                let cleanup_result = self.workspace_provider.cleanup(&workspace);
                if let Err(record_error) = record_result {
                    finish_before_cleanup(Err(record_error), cleanup_result)?;
                    unreachable!("an explicit primary error cannot finish successfully");
                }
                finish_before_cleanup(Err(error), cleanup_result)?;
                unreachable!("an explicit primary error cannot finish successfully");
            }
        };

        if let Some(outcome) =
            self.handle_post_launch_supersession(rule, &run_id, &mut running_state, &workspace)?
        {
            return Ok(outcome);
        }

        apply_run_outcome(&mut running_state, rule.run_kind, outcome);
        self.state_store.save(&running_state)?;
        self.apply_forge_operations_or_record_failure(
            event,
            rule,
            &run_id,
            &mut running_state,
            &workspace,
        )?;
        let save_result = self.state_store.save(&running_state);
        let cleanup_result = self.workspace_provider.cleanup(&workspace);
        save_result?;
        cleanup_result?;
        if let Some(completion_event) = run_completed_event_from_state(&running_state, &run_id) {
            self.queue_completion_event(completion_event);
        }

        Ok(DispatchOutcome::Launched {
            rule_id: rule.id.clone(),
            run_id,
        })
    }

    fn save_running_or_cleanup(
        &mut self,
        running_state: &PrRunState,
        workspace: &WorkspaceLease,
    ) -> Result<(), CoreError> {
        if let Err(error) = self.state_store.save(running_state) {
            let cleanup_result = self.workspace_provider.cleanup(workspace);
            finish_before_cleanup(Err(error), cleanup_result)?;
            unreachable!("an explicit primary error cannot finish successfully");
        }
        Ok(())
    }

    fn apply_forge_operations_or_record_failure(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        running_state: &mut PrRunState,
        workspace: &WorkspaceLease,
    ) -> Result<(), CoreError> {
        if let Err(error) =
            self.apply_authorised_forge_operations(rule.run_kind, run_id, running_state)
        {
            let message = error.to_string();
            let record_result =
                self.record_failed_running_state(running_state, event, rule, run_id, &message);
            let cleanup_result = self.workspace_provider.cleanup(workspace);
            if let Err(record_error) = record_result {
                finish_before_cleanup(Err(record_error), cleanup_result)?;
                unreachable!("an explicit primary error cannot finish successfully");
            }
            finish_before_cleanup(Err(error), cleanup_result)?;
            unreachable!("an explicit primary error cannot finish successfully");
        }
        Ok(())
    }

    fn record_failed_running_state(
        &mut self,
        running_state: &mut PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        message: &str,
    ) -> Result<(), CoreError> {
        mark_failed(running_state, event, rule, run_id, message);
        self.state_store.save(running_state)?;
        self.record_operator_failure(running_state, run_id, message)
    }

    fn handle_post_launch_supersession(
        &mut self,
        rule: &TriggerRule,
        run_id: &RunId,
        running_state: &mut PrRunState,
        workspace: &WorkspaceLease,
    ) -> Result<Option<DispatchOutcome>, CoreError> {
        let Some(superseded_by) = self.head_was_superseded_by(running_state)? else {
            return Ok(None);
        };
        mark_superseded(running_state, superseded_by.clone(), None);
        let save_result = self.state_store.save(running_state);
        let cleanup_result = self.workspace_provider.cleanup(workspace);
        finish_before_cleanup(save_result, cleanup_result)?;
        Ok(Some(DispatchOutcome::Superseded {
            rule_id: rule.id.clone(),
            run_id: run_id.clone(),
            superseded_by,
        }))
    }

    fn handle_dispatch_precheck(
        &mut self,
        state: &PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
    ) -> Result<Option<DispatchOutcome>, CoreError> {
        let Some(outcome) = dispatch_precheck(state, event, rule) else {
            return Ok(None);
        };
        if let DispatchOutcome::Refused { reason, .. } = &outcome
            && *reason == LaunchRefusal::RunCeilingReached
            && !has_recorded_ceiling_refusal(state)
        {
            self.record_skipped_refusal(state.clone(), event, rule, run_id, reason)?;
        }
        Ok(Some(outcome))
    }

    fn prepare_provenance_or_refusal(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        state: &PrRunState,
        run_id: &RunId,
    ) -> Result<Result<Vec<ModelProvenance>, DispatchOutcome>, CoreError> {
        match self.prepare_provenance(&rule.agent_plan, state.pass_index) {
            Ok(prepared) => Ok(Ok(prepared)),
            Err(error) => {
                if let Some(reason) = launch_refusal_from_prepare_error(&error) {
                    let failure = format!("launch refused: {reason:?}");
                    self.record_failed_launch_refusal(
                        state.clone(),
                        event,
                        rule,
                        run_id,
                        &reason,
                        failure.as_str(),
                    )?;
                    return Ok(Err(DispatchOutcome::Refused {
                        rule_id: rule.id.clone(),
                        reason,
                    }));
                }
                let message = error.to_string();
                self.record_failed_dispatch(state.clone(), event, rule, run_id, message.as_str())?;
                Err(error)
            }
        }
    }

    fn prepare_workspace_or_record_failure(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        state: &PrRunState,
        run_id: &RunId,
    ) -> Result<WorkspaceLease, CoreError> {
        match self.workspace_provider.prepare(WorkspaceRequest {
            run_id: run_id.clone(),
            run_kind: rule.run_kind,
            pr: state.pr.clone(),
            commit_sha: state.commit_sha.clone(),
        }) {
            Ok(workspace) => Ok(workspace),
            Err(error) => {
                let message = error.to_string();
                self.record_failed_dispatch(state.clone(), event, rule, run_id, message.as_str())?;
                Err(error)
            }
        }
    }

    fn prepare_and_inject_source(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        state: &PrRunState,
        run_id: &RunId,
        workspace: &WorkspaceLease,
    ) -> Result<(), CoreError> {
        if !run_kind_requires_source(rule.run_kind) || !self.source_preparer.enabled() {
            return Ok(());
        }
        let prepared_source = match self
            .source_preparer
            .prepare_source(SourcePreparationRequest {
                run_id: run_id.clone(),
                run_kind: rule.run_kind,
                event: event.clone(),
                state: state.clone(),
                workspace: workspace.clone(),
                pr: state.pr.clone(),
                commit_sha: state.commit_sha.clone(),
            }) {
            Ok(source) => source,
            Err(error) => {
                self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
                return Err(error);
            }
        };
        if let Err(error) = validate_prepared_source(&prepared_source, &state.commit_sha) {
            self.cleanup_prepared_source_or_fail(
                &prepared_source,
                state,
                event,
                rule,
                run_id,
                workspace,
            )?;
            self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
            return Err(error);
        }
        if let Err(error) = self
            .workspace_provider
            .inject_source(workspace, &prepared_source)
        {
            self.cleanup_prepared_source_or_fail(
                &prepared_source,
                state,
                event,
                rule,
                run_id,
                workspace,
            )?;
            self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
            return Err(error);
        }
        if let Err(error) = cleanup_external_prepared_source(&prepared_source, workspace) {
            self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
            return Err(error);
        }
        if let Err(error) = reject_credential_residue(&workspace.root) {
            self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
            return Err(error);
        }
        Ok(())
    }

    fn cleanup_prepared_source_or_fail(
        &mut self,
        source: &PreparedSource,
        state: &PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        workspace: &WorkspaceLease,
    ) -> Result<(), CoreError> {
        if let Err(error) = cleanup_external_prepared_source(source, workspace) {
            self.fail_before_run(state, event, rule, run_id, workspace, &error)?;
            return Err(error);
        }
        Ok(())
    }

    fn fail_before_run(
        &mut self,
        state: &PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        workspace: &WorkspaceLease,
        error: &CoreError,
    ) -> Result<(), CoreError> {
        let message = error.to_string();
        let record_result =
            self.record_failed_dispatch(state.clone(), event, rule, run_id, message.as_str());
        let cleanup_result = self.workspace_provider.cleanup(workspace);
        finish_before_cleanup(record_result, cleanup_result)
    }

    fn record_failed_dispatch(
        &mut self,
        mut state: PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        message: &str,
    ) -> Result<(), CoreError> {
        mark_failed(&mut state, event, rule, run_id, message);
        self.state_store.save(&state)?;
        self.record_operator_failure(&state, run_id, message)?;
        self.state_store.save(&state)
    }

    fn record_failed_launch_refusal(
        &mut self,
        mut state: PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        refusal: &LaunchRefusal,
        message: &str,
    ) -> Result<(), CoreError> {
        mark_failed_refusal(&mut state, event, rule, run_id, refusal, message);
        self.state_store.save(&state)?;
        self.record_operator_refusal(&state, run_id, message)?;
        self.state_store.save(&state)
    }

    fn record_skipped_refusal(
        &mut self,
        mut state: PrRunState,
        event: &ContractEvent,
        rule: &TriggerRule,
        run_id: &RunId,
        refusal: &LaunchRefusal,
    ) -> Result<(), CoreError> {
        let message = format!("launch skipped: {refusal:?}");
        mark_skipped_refusal(&mut state, event, rule, run_id, refusal, message.as_str());
        self.state_store.save(&state)?;
        self.record_operator_refusal(&state, run_id, message.as_str())?;
        self.state_store.save(&state)
    }

    fn record_operator_failure(
        &mut self,
        state: &PrRunState,
        run_id: &RunId,
        message: &str,
    ) -> Result<(), CoreError> {
        self.operator_log.record(operator_log_event(
            state,
            run_id,
            OperatorLogEventKind::RunFailure,
            message,
            None,
        )?)
    }

    fn record_operator_refusal(
        &mut self,
        state: &PrRunState,
        run_id: &RunId,
        message: &str,
    ) -> Result<(), CoreError> {
        let refusal_reason = run_record_for(state, run_id)
            .and_then(|record| record.refusal.as_ref())
            .map(|refusal| refusal.reason.clone());
        self.operator_log.record(operator_log_event(
            state,
            run_id,
            OperatorLogEventKind::LaunchRefusal,
            message,
            refusal_reason,
        )?)
    }

    fn head_was_superseded_by(&self, state: &PrRunState) -> Result<Option<String>, CoreError> {
        let Some(current) = self.state_store.load_latest_for_pr(&state.pr)? else {
            return Ok(None);
        };
        if current.commit_sha != state.commit_sha {
            return Ok(Some(current.current_head_sha.unwrap_or(current.commit_sha)));
        }
        Ok(current
            .current_head_sha
            .filter(|head| head != &state.commit_sha))
    }

    fn apply_authorised_forge_operations(
        &mut self,
        run_kind: RunKind,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        match run_kind {
            RunKind::Judge => self.apply_judge_forge_operations(run_id, state),
            RunKind::Fix => self.push_fix_patches(run_id, state),
            RunKind::Finish if state.status == RunStatus::Completed => {
                self.merge_finished_pr(run_id, state)
            }
            RunKind::Review | RunKind::Finish => Ok(()),
        }
    }

    fn apply_judge_forge_operations(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        self.post_material_findings(run_id, state)?;
        self.apply_finish_label_after_convergence(run_id, state)
    }

    fn post_material_findings(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        let Some(facts) = forge_facts_from_state(state)? else {
            return Ok(());
        };
        let material = material_findings_for_pass(state);
        let material_keys = material
            .iter()
            .map(|(finding, _decision)| finding.dedup_key.clone())
            .collect::<std::collections::BTreeSet<_>>();

        for publication in state.publication.finding_comments.clone() {
            if publication.status == FindingCommentStatus::Open
                && !material_keys.contains(&publication.finding_dedup_key)
            {
                self.resolve_finding_comment(run_id, state, &facts, &publication)?;
            }
        }

        for (finding, decision) in material {
            if let Some(publication) = state
                .publication
                .finding_comments
                .iter()
                .find(|publication| publication.finding_dedup_key == finding.dedup_key)
                .cloned()
            {
                self.update_finding_comment(
                    run_id,
                    state,
                    &facts,
                    &finding,
                    &decision,
                    &publication,
                )?;
            } else {
                self.post_finding_comment(run_id, state, &facts, &finding, &decision)?;
            }
        }
        Ok(())
    }

    fn post_finding_comment(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
        facts: &ForgeFacts,
        finding: &Finding,
        decision: &Decision,
    ) -> Result<(), CoreError> {
        let idempotency_key = stable_id(
            "forge-comment",
            [
                state.pr.repository.as_str(),
                state.pr.id.as_str(),
                finding.dedup_key.as_str(),
            ],
        );
        let authorisation = finding_comment_authorisation(
            state,
            facts,
            idempotency_key.clone(),
            decision,
            finding,
            "core authorised material finding comment from judge decision",
        );
        let expected_head_sha = facts.head.sha.clone();
        if self.refuse_comment_if_head_moved(
            state,
            run_id,
            PublicationOperation::PostFindingComment {
                finding_id: finding.id.clone(),
                finding_dedup_key: finding.dedup_key.clone(),
            },
            idempotency_key.clone(),
            expected_head_sha.clone(),
        )? {
            self.state_store.save(state)?;
            return Ok(());
        }
        let operation = PublicationOperation::PostFindingComment {
            finding_id: finding.id.clone(),
            finding_dedup_key: finding.dedup_key.clone(),
        };
        let body = self.format_finding_comment_for_publication(
            state,
            run_id,
            operation.clone(),
            idempotency_key.clone(),
            expected_head_sha.clone(),
            finding,
            decision,
            facts,
        )?;
        let result = self.forge_operations.post_comment(AuthorisedComment {
            authorisation,
            expected_head_sha: expected_head_sha.clone(),
            body,
        });
        let receipt = self.record_finding_comment_attempt(
            state,
            run_id,
            operation,
            idempotency_key,
            expected_head_sha,
            result,
        )?;
        if let Some(receipt) = receipt {
            upsert_finding_comment_publication(state, finding, receipt, FindingCommentStatus::Open);
        }
        self.state_store.save(state)?;
        Ok(())
    }

    fn update_finding_comment(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
        facts: &ForgeFacts,
        finding: &Finding,
        decision: &Decision,
        publication: &FindingCommentPublication,
    ) -> Result<(), CoreError> {
        let idempotency_key = stable_id(
            "forge-comment-update",
            [
                state.pr.repository.as_str(),
                state.pr.id.as_str(),
                finding.dedup_key.as_str(),
                run_id.0.as_str(),
            ],
        );
        let authorisation = finding_comment_authorisation(
            state,
            facts,
            idempotency_key.clone(),
            decision,
            finding,
            "core authorised material finding comment update from judge decision",
        );
        let expected_head_sha = facts.head.sha.clone();
        if self.refuse_comment_if_head_moved(
            state,
            run_id,
            PublicationOperation::UpdateFindingComment {
                finding_id: finding.id.clone(),
                finding_dedup_key: finding.dedup_key.clone(),
                comment_operation_id: publication.comment_operation_id.clone(),
            },
            idempotency_key.clone(),
            expected_head_sha.clone(),
        )? {
            self.state_store.save(state)?;
            return Ok(());
        }
        let operation = PublicationOperation::UpdateFindingComment {
            finding_id: finding.id.clone(),
            finding_dedup_key: finding.dedup_key.clone(),
            comment_operation_id: publication.comment_operation_id.clone(),
        };
        let body = self.format_finding_comment_for_publication(
            state,
            run_id,
            operation.clone(),
            idempotency_key.clone(),
            expected_head_sha.clone(),
            finding,
            decision,
            facts,
        )?;
        let result = self
            .forge_operations
            .update_comment(AuthorisedCommentUpdate {
                authorisation,
                expected_head_sha: expected_head_sha.clone(),
                comment_operation_id: publication.comment_operation_id.clone(),
                body,
            });
        let receipt = self.record_finding_comment_attempt(
            state,
            run_id,
            operation,
            idempotency_key,
            expected_head_sha,
            result,
        )?;
        if let Some(receipt) = receipt {
            upsert_finding_comment_publication(state, finding, receipt, FindingCommentStatus::Open);
        }
        self.state_store.save(state)?;
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    fn format_finding_comment_for_publication(
        &mut self,
        state: &mut PrRunState,
        run_id: &RunId,
        operation: PublicationOperation,
        idempotency_key: String,
        expected_head_sha: String,
        finding: &Finding,
        decision: &Decision,
        facts: &ForgeFacts,
    ) -> Result<String, CoreError> {
        let result = self
            .comment_formatter
            .format_finding_comment(FindingCommentFormatRequest {
                run_id: run_id.clone(),
                finding: finding.clone(),
                decision: decision.clone(),
                facts: facts.clone(),
            });
        let body = match result {
            Ok(body) if !body.trim().is_empty() => body,
            Ok(_) => {
                return self.record_comment_formatting_failure(
                    state,
                    run_id,
                    operation,
                    idempotency_key,
                    expected_head_sha,
                    "comment formatter returned an empty body".to_owned(),
                );
            }
            Err(error) => {
                return self.record_comment_formatting_failure(
                    state,
                    run_id,
                    operation,
                    idempotency_key,
                    expected_head_sha,
                    error.to_string(),
                );
            }
        };
        Ok(body)
    }

    fn record_comment_formatting_failure(
        &mut self,
        state: &mut PrRunState,
        run_id: &RunId,
        operation: PublicationOperation,
        idempotency_key: String,
        expected_head_sha: String,
        message: String,
    ) -> Result<String, CoreError> {
        record_publication_attempt(
            state,
            run_id,
            operation,
            idempotency_key,
            Some(expected_head_sha),
            Err(message.clone()),
        );
        self.state_store.save(state)?;
        Err(CoreError::CommentFormatting(message))
    }

    fn resolve_finding_comment(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
        facts: &ForgeFacts,
        publication: &FindingCommentPublication,
    ) -> Result<(), CoreError> {
        let idempotency_key = stable_id(
            "forge-comment-resolve",
            [
                state.pr.repository.as_str(),
                state.pr.id.as_str(),
                publication.finding_dedup_key.as_str(),
                run_id.0.as_str(),
            ],
        );
        let authorisation = AuthorisationContext {
            pr: state.pr.clone(),
            observed_head_sha: facts.head.sha.clone(),
            idempotency_key: idempotency_key.clone(),
            actor: core_actor(),
            reason: "core authorised resolving previously material finding comment".to_owned(),
            evidence: vec![AuthorisationEvidence::Finding {
                finding_id: publication.latest_finding_id.clone(),
            }],
        };
        let expected_head_sha = facts.head.sha.clone();
        if self.refuse_comment_if_head_moved(
            state,
            run_id,
            PublicationOperation::ResolveFindingComment {
                finding_dedup_key: publication.finding_dedup_key.clone(),
                comment_operation_id: publication.comment_operation_id.clone(),
            },
            idempotency_key.clone(),
            expected_head_sha.clone(),
        )? {
            self.state_store.save(state)?;
            return Ok(());
        }
        let result = self
            .forge_operations
            .resolve_comment(AuthorisedCommentResolution {
                authorisation,
                expected_head_sha: expected_head_sha.clone(),
                comment_operation_id: publication.comment_operation_id.clone(),
                reason: "finding no longer material on this pass".to_owned(),
            });
        let receipt = self.record_finding_comment_attempt(
            state,
            run_id,
            PublicationOperation::ResolveFindingComment {
                finding_dedup_key: publication.finding_dedup_key.clone(),
                comment_operation_id: publication.comment_operation_id.clone(),
            },
            idempotency_key,
            expected_head_sha,
            result,
        )?;
        if let Some(receipt) = receipt
            && let Some(existing) = state
                .publication
                .finding_comments
                .iter_mut()
                .find(|existing| existing.finding_dedup_key == publication.finding_dedup_key)
        {
            existing.status = FindingCommentStatus::Resolved;
            existing.last_receipt = receipt;
        }
        self.state_store.save(state)?;
        Ok(())
    }

    fn record_finding_comment_attempt(
        &mut self,
        state: &mut PrRunState,
        run_id: &RunId,
        operation: PublicationOperation,
        idempotency_key: String,
        expected_head_sha: String,
        result: Result<ForgeOperationReceipt, ForgeOperationError>,
    ) -> Result<Option<ForgeReceipt>, CoreError> {
        let receipt = match result {
            Ok(receipt) => {
                let receipt = receipt_to_contract(receipt);
                record_publication_attempt(
                    state,
                    run_id,
                    operation,
                    idempotency_key,
                    Some(expected_head_sha),
                    Ok(receipt.clone()),
                );
                receipt
            }
            Err(error) => {
                let message = error.to_string();
                record_publication_error_attempt(
                    state,
                    run_id,
                    operation,
                    idempotency_key,
                    Some(expected_head_sha),
                    &error,
                    message.clone(),
                );
                mark_superseded_from_operation_error(state, &error);
                self.state_store.save(state)?;
                return Err(CoreError::ForgeOperation(message));
            }
        };
        Ok(Some(receipt))
    }

    fn refuse_comment_if_head_moved(
        &self,
        state: &mut PrRunState,
        run_id: &RunId,
        operation: PublicationOperation,
        idempotency_key: String,
        expected_head_sha: String,
    ) -> Result<bool, CoreError> {
        let actual_head_sha = self.head_was_superseded_by(state)?;
        let current_head_mismatch = state
            .current_head_sha
            .as_ref()
            .is_some_and(|head| head != &expected_head_sha);
        if actual_head_sha.is_none() && !current_head_mismatch {
            return Ok(false);
        }

        let actual_head_sha = actual_head_sha.or_else(|| state.current_head_sha.clone());
        let message = format!(
            "publication refused because PR head moved from {expected_head_sha} to {actual_head_sha:?}"
        );
        record_refused_publication_attempt(
            state,
            run_id,
            operation,
            idempotency_key,
            Some(expected_head_sha.clone()),
            PublicationRefusal {
                reason: PublicationRefusalReason::HeadMoved {
                    expected_head_sha,
                    actual_head_sha: actual_head_sha.clone(),
                },
                message,
            },
        );
        if let Some(actual_head_sha) = actual_head_sha {
            mark_superseded(state, actual_head_sha, None);
        }
        Ok(true)
    }

    fn push_fix_patches(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        let Some(facts) = forge_facts_from_state(state)? else {
            return Ok(());
        };
        let patches = fix_patches_for_run(state, run_id);
        if patches.is_empty()
            || state
                .publication
                .fix_pushes
                .iter()
                .any(|push| push.run_id == *run_id)
        {
            return Ok(());
        }

        let patch_ids = patches
            .iter()
            .map(|patch| patch.id.clone())
            .collect::<Vec<_>>();
        let idempotency_key = stable_id(
            "forge-fix-push",
            [
                state.pr.repository.as_str(),
                state.pr.id.as_str(),
                run_id.0.as_str(),
                facts.head.sha.as_str(),
            ],
        );
        let commits = fix_commits_for_patches(&patches);
        let authorisation = fix_push_authorisation(state, &facts, &idempotency_key, &patch_ids);
        let expected_head_sha = facts.head.sha;
        let result = self.forge_operations.push_fix_commits(AuthorisedFixPush {
            authorisation,
            expected_head_sha: expected_head_sha.clone(),
            commits: commits.clone(),
        });
        let receipt = match result {
            Ok(receipt) => {
                let receipt = receipt_to_contract(receipt);
                record_publication_attempt(
                    state,
                    run_id,
                    PublicationOperation::PushFixCommits {
                        patch_ids: patch_ids.clone(),
                    },
                    idempotency_key,
                    Some(expected_head_sha),
                    Ok(receipt.clone()),
                );
                receipt
            }
            Err(error) => {
                let message = error.to_string();
                record_publication_error_attempt(
                    state,
                    run_id,
                    PublicationOperation::PushFixCommits { patch_ids },
                    idempotency_key,
                    Some(expected_head_sha),
                    &error,
                    message.clone(),
                );
                mark_superseded_from_operation_error(state, &error);
                self.state_store.save(state)?;
                return Err(CoreError::ForgeOperation(message));
            }
        };
        state.publication.fix_pushes.push(FixPushPublication {
            run_id: run_id.clone(),
            patch_ids,
            commits: published_fix_commits(commits),
            receipt,
        });
        self.state_store.save(state)?;
        Ok(())
    }

    fn apply_finish_label_after_convergence(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        if !state_converged_for_current_pass(state) {
            return Ok(());
        }
        let label = match &self.policy.finish_label_application {
            FinishLabelApplicationPolicy::HumanOnly => return Ok(()),
            FinishLabelApplicationPolicy::CoreOnConvergence { label } => label.clone(),
        };
        let Some(mut facts) = forge_facts_from_state(state)? else {
            return Ok(());
        };
        let actor = core_actor();
        if !core_may_apply_finish_label(&facts, &label) {
            return Ok(());
        }

        let idempotency_key = stable_id(
            "forge-apply-finish-label",
            [run_id.0.as_str(), facts.head.sha.as_str(), label.as_str()],
        );
        let result = self.forge_operations.apply_label(AuthorisedLabel {
            authorisation: finish_label_authorisation(
                state,
                &facts,
                &actor,
                idempotency_key.clone(),
            ),
            label: label.clone(),
        });
        if let Err(error) =
            record_finish_label_attempt(state, run_id, &label, idempotency_key, result)
        {
            self.state_store.save(state)?;
            return Err(error);
        }

        let finish_label = FinishLabel {
            name: label.clone(),
            applied_by: actor,
        };
        facts.finish_label = Some(finish_label.clone());
        state.extensions.insert(
            EXT_FORGE_FACTS.to_owned(),
            serde_json::to_value(&facts).map_err(|source| CoreError::Json {
                path: EXT_FORGE_FACTS.to_owned(),
                source,
            })?,
        );
        self.state_store.save(state)?;
        self.queue_completion_event(ContractEvent {
            contract_version: ContractVersion::current(),
            id: stable_id(
                "event-finish-label-applied",
                [run_id.0.as_str(), facts.head.sha.as_str(), label.as_str()],
            ),
            payload: EventPayload::LabelApplied {
                pr: state.pr.clone(),
                label: finish_label,
            },
            extensions: BTreeMap::new(),
        });
        Ok(())
    }

    fn merge_finished_pr(
        &mut self,
        run_id: &RunId,
        state: &mut PrRunState,
    ) -> Result<(), CoreError> {
        let facts = forge_facts_from_state(state)?
            .ok_or_else(|| CoreError::ForgeOperation("missing forge facts for merge".to_owned()))?;
        if !merge_gate_clean_and_current(&facts) {
            return Err(CoreError::ForgeOperation(
                "merge refused because branch is not clean and current".to_owned(),
            ));
        }
        let label = facts.finish_label.clone().ok_or_else(|| {
            CoreError::ForgeOperation("missing finish label authority".to_owned())
        })?;
        if !finish_label_actor_may_merge(&self.policy, &facts, &label) {
            return Err(CoreError::ForgeOperation(
                "finish label actor lacks merge capability".to_owned(),
            ));
        }
        let idempotency_key =
            stable_id("forge-merge", [run_id.0.as_str(), facts.head.sha.as_str()]);
        let expected_head_sha = facts.head.sha;
        let authorisation = AuthorisationContext {
            pr: state.pr.clone(),
            observed_head_sha: expected_head_sha.clone(),
            idempotency_key: idempotency_key.clone(),
            actor: label.applied_by.clone(),
            reason: "core authorised merge from clean/current facts and finish label authority"
                .to_owned(),
            evidence: vec![
                AuthorisationEvidence::FinishLabelAuthority {
                    label: label.clone(),
                },
                AuthorisationEvidence::ActorCapability {
                    actor: label.applied_by,
                    capability: ActorCapability::Merge,
                },
                AuthorisationEvidence::MergeGateCleanAndCurrent {
                    facts_head_sha: expected_head_sha.clone(),
                },
            ],
        };
        let result = self.forge_operations.merge(AuthorisedMerge {
            authorisation,
            method: MergeMethod::Squash,
        });
        let receipt = match result {
            Ok(receipt) => {
                let receipt = receipt_to_contract(receipt);
                record_publication_attempt(
                    state,
                    run_id,
                    PublicationOperation::MergePullRequest,
                    idempotency_key,
                    Some(expected_head_sha),
                    Ok(receipt.clone()),
                );
                receipt
            }
            Err(error) => {
                let message = error.to_string();
                record_publication_attempt(
                    state,
                    run_id,
                    PublicationOperation::MergePullRequest,
                    idempotency_key,
                    Some(expected_head_sha),
                    Err(message.clone()),
                );
                self.state_store.save(state)?;
                return Err(CoreError::ForgeOperation(message));
            }
        };
        state.publication.merges.push(MergePublication {
            run_id: run_id.clone(),
            receipt,
        });
        self.state_store.save(state)?;
        Ok(())
    }

    fn prepare_provenance(
        &mut self,
        plan: &AgentPlan,
        pass_index: u32,
    ) -> Result<Vec<ModelProvenance>, CoreError> {
        let mut provenances = Vec::new();
        for mut target in plan.targets() {
            canonicalise_launch_target_model(&mut target);
            let spec = AgentLaunchSpec {
                target: target.clone(),
                pass_index,
            };
            let prepared = self.launcher.prepare_agent(spec)?;
            provenances.push(establish_provenance(&target, prepared, pass_index));
        }
        Ok(provenances)
    }

    fn queue_completion_event(&mut self, event: ContractEvent) {
        self.queued_completion_event_ids.insert(event.id.clone());
        self.pending_events.push_back(event);
    }
}

const fn recovery_error_is_fatal(error: &CoreError) -> bool {
    matches!(
        error,
        CoreError::StateStore(_) | CoreError::Io { .. } | CoreError::Json { .. }
    )
}

/// The durable key for a PR run state.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunStateKey {
    pub pr: PullRequestRef,
    pub commit_sha: String,
}

impl RunStateKey {
    #[must_use]
    pub fn from_facts(facts: &ForgeFacts) -> Self {
        Self {
            pr: facts.pr.clone(),
            commit_sha: facts.head.sha.clone(),
        }
    }

    #[must_use]
    fn from_state(state: &PrRunState) -> Self {
        Self {
            pr: state.pr.clone(),
            commit_sha: state.commit_sha.clone(),
        }
    }
}

/// JSON-backed run-state store using atomic replacement on save.
#[derive(Clone, Debug)]
pub struct JsonRunStateStore {
    root: PathBuf,
}

impl JsonRunStateStore {
    /// Creates a state store rooted at the supplied directory.
    ///
    /// # Errors
    ///
    /// Returns an error when the state directory cannot be created.
    pub fn new(root: impl Into<PathBuf>) -> Result<Self, CoreError> {
        let root = root.into();
        fs::create_dir_all(&root).map_err(|source| CoreError::Io {
            path: root.display().to_string(),
            source,
        })?;
        Ok(Self { root })
    }

    #[must_use]
    fn path_for(&self, key: &RunStateKey) -> PathBuf {
        self.root.join(format!(
            "{}__{}__{}.json",
            sanitise_path_component(&key.pr.repository),
            sanitise_path_component(&key.pr.id),
            sanitise_path_component(&key.commit_sha)
        ))
    }

    #[must_use]
    fn control_path_for(&self, pr: &PullRequestRef) -> PathBuf {
        self.path_for(&RunStateKey {
            pr: pr.clone(),
            commit_sha: CONTROL_COMMIT_SHA.to_owned(),
        })
    }

    fn all_states(&self) -> Result<Vec<PrRunState>, CoreError> {
        let mut states = Vec::new();
        for entry in fs::read_dir(&self.root).map_err(|source| CoreError::Io {
            path: self.root.display().to_string(),
            source,
        })? {
            let entry = entry.map_err(|source| CoreError::Io {
                path: self.root.display().to_string(),
                source,
            })?;
            let path = entry.path();
            if path.extension().and_then(std::ffi::OsStr::to_str) != Some("json") {
                continue;
            }
            let bytes = fs::read(&path).map_err(|source| CoreError::Io {
                path: path.display().to_string(),
                source,
            })?;
            let state = serde_json::from_slice(&bytes).map_err(|source| CoreError::Json {
                path: path.display().to_string(),
                source,
            })?;
            // The PR-control mirror serialises the current evidence state, so its
            // embedded commit SHA is not the reserved control key. Filter by the
            // path that should own the state; otherwise scans double-count it.
            if path != self.path_for(&RunStateKey::from_state(&state)) {
                continue;
            }
            states.push(state);
        }
        Ok(states)
    }
}

impl RunStateStore for JsonRunStateStore {
    fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
        let path = self.path_for(key);
        if !path.exists() {
            return Ok(None);
        }
        let bytes = fs::read(&path).map_err(|source| CoreError::Io {
            path: path.display().to_string(),
            source,
        })?;
        serde_json::from_slice(&bytes)
            .map(Some)
            .map_err(|source| CoreError::Json {
                path: path.display().to_string(),
                source,
            })
    }

    fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError> {
        let control_path = self.control_path_for(pr);
        if control_path.exists() {
            let bytes = fs::read(&control_path).map_err(|source| CoreError::Io {
                path: control_path.display().to_string(),
                source,
            })?;
            return serde_json::from_slice(&bytes)
                .map(Some)
                .map_err(|source| CoreError::Json {
                    path: control_path.display().to_string(),
                    source,
                });
        }
        Ok(self
            .all_states()?
            .into_iter()
            .filter(|state| state.pr == *pr)
            .filter(|state| state.commit_sha != CONTROL_COMMIT_SHA)
            .max_by_key(|state| state.pass_index))
    }

    fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
        Ok(self.all_states()?.into_iter().find(|state| {
            state.commit_sha != CONTROL_COMMIT_SHA && state_records_run(state, run_id)
        }))
    }

    fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
        Ok(self
            .all_states()?
            .into_iter()
            .filter(|state| state.commit_sha != CONTROL_COMMIT_SHA)
            .collect())
    }

    fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
        fs::create_dir_all(&self.root).map_err(|source| CoreError::Io {
            path: self.root.display().to_string(),
            source,
        })?;
        let key = RunStateKey::from_state(state);
        let path = self.path_for(&key);
        let tmp_path = path.with_extension("json.tmp");
        let bytes = serde_json::to_vec_pretty(state).map_err(|source| CoreError::Json {
            path: path.display().to_string(),
            source,
        })?;
        fs::write(&tmp_path, &bytes).map_err(|source| CoreError::Io {
            path: tmp_path.display().to_string(),
            source,
        })?;
        fs::rename(&tmp_path, &path).map_err(|source| CoreError::Io {
            path: path.display().to_string(),
            source,
        })?;
        if state_is_current_pr_control(state) {
            let control_path = self.control_path_for(&state.pr);
            let tmp_control_path = control_path.with_extension("json.tmp");
            fs::write(&tmp_control_path, &bytes).map_err(|source| CoreError::Io {
                path: tmp_control_path.display().to_string(),
                source,
            })?;
            fs::rename(&tmp_control_path, &control_path).map_err(|source| CoreError::Io {
                path: control_path.display().to_string(),
                source,
            })?;
        }
        Ok(())
    }
}

/// A configured rule whose criteria can fire one run independently of other runs.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct TriggerRule {
    pub id: String,
    pub run_kind: RunKind,
    pub criteria: Criteria,
    pub agent_plan: AgentPlan,
}

/// Adaptation-supplied criteria, deterministically evaluated by the core.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum Criteria {
    Event {
        event: EventKind,
    },
    State {
        state: StateCriterion,
    },
    /// Matches when the PR author is one of the listed forge logins.
    ///
    /// The author comes from the event's own facts for forge events, falling
    /// back to the facts recorded in run state for downstream events. Fails
    /// closed when no author evidence exists.
    PrAuthoredBy {
        any_of: Vec<String>,
    },
    /// Matches when the PR is not marked draft/work-in-progress by the forge.
    ///
    /// Forge events carry current readiness directly; downstream events use
    /// the most recent persisted forge facts. Older contract producers omit
    /// the fact, which deserialises as ready.
    PrReady,
    All {
        criteria: Vec<Self>,
    },
    Any {
        criteria: Vec<Self>,
    },
}

impl Criteria {
    #[must_use]
    pub fn matches(&self, event: &ContractEvent, state: Option<&PrRunState>) -> bool {
        match self {
            Self::Event { event: expected } => expected.matches(event),
            Self::State { state: expected } => expected.matches(state),
            Self::PrAuthoredBy { any_of } => {
                pr_author_login(event, state).is_some_and(|login| any_of.contains(&login))
            }
            Self::PrReady => pr_ready(event, state),
            Self::All { criteria } => criteria
                .iter()
                .all(|criterion| criterion.matches(event, state)),
            Self::Any { criteria } => criteria
                .iter()
                .any(|criterion| criterion.matches(event, state)),
        }
    }
}

/// Whether the PR is currently ready for review according to forge facts.
fn pr_ready(event: &ContractEvent, state: Option<&PrRunState>) -> bool {
    !pr_work_in_progress(event, state)
}

fn pr_work_in_progress(event: &ContractEvent, state: Option<&PrRunState>) -> bool {
    match &event.payload {
        EventPayload::PullRequestOpened { facts } | EventPayload::PullRequestUpdated { facts } => {
            facts.work_in_progress
        }
        _ => state
            .and_then(|state| state.extensions.get(EXT_FORGE_FACTS))
            .and_then(|value| serde_json::from_value::<ForgeFacts>(value.clone()).ok())
            .is_some_and(|facts| facts.work_in_progress),
    }
}

/// The PR author as the forge reported it, or `None` when no evidence exists.
fn pr_author_login(event: &ContractEvent, state: Option<&PrRunState>) -> Option<String> {
    match &event.payload {
        EventPayload::PullRequestOpened { facts } | EventPayload::PullRequestUpdated { facts } => {
            facts.author_login.clone()
        }
        _ => state
            .and_then(|state| state.extensions.get(EXT_FORGE_FACTS))
            .and_then(|value| serde_json::from_value::<ForgeFacts>(value.clone()).ok())
            .and_then(|facts| facts.author_login),
    }
}

/// Event predicates available to trigger rules.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "event")]
pub enum EventKind {
    PullRequestOpened,
    PullRequestUpdated,
    RunCompleted {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        run_kind: Option<RunKind>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        outcome: Option<RunOutcome>,
    },
    LabelApplied {
        name: Option<String>,
    },
}

impl EventKind {
    #[must_use]
    fn matches(&self, event: &ContractEvent) -> bool {
        match (self, &event.payload) {
            (Self::PullRequestOpened, EventPayload::PullRequestOpened { .. })
            | (Self::PullRequestUpdated, EventPayload::PullRequestUpdated { .. }) => true,
            (
                Self::RunCompleted {
                    run_kind: expected_kind,
                    outcome: expected_outcome,
                },
                EventPayload::RunCompleted {
                    run_kind: actual_kind,
                    outcome: actual_outcome,
                    ..
                },
            ) => {
                expected_kind.is_none_or(|expected| Some(expected) == *actual_kind)
                    && expected_outcome.is_none_or(|expected| expected == *actual_outcome)
            }
            (Self::LabelApplied { name: expected }, EventPayload::LabelApplied { label, .. }) => {
                expected
                    .as_ref()
                    .is_none_or(|expected| expected == &label.name)
            }
            _ => false,
        }
    }
}

/// State predicates available to trigger rules.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StateCriterion {
    HasMaterialDecision,
    HasConverged,
    CleanAndCurrent,
    CeilingAllowsPass,
}

impl StateCriterion {
    #[must_use]
    fn matches(self, state: Option<&PrRunState>) -> bool {
        let Some(state) = state else {
            return false;
        };
        match self {
            Self::HasMaterialDecision => state
                .decisions
                .iter()
                .filter(|decision| provenance_pass(&decision.provenance) == Some(state.pass_index))
                .any(|decision| decision.verdict == DecisionVerdict::Material),
            Self::HasConverged => state
                .decisions
                .iter()
                .filter(|decision| provenance_pass(&decision.provenance) == Some(state.pass_index))
                .any(|decision| decision.verdict == DecisionVerdict::Converged),
            Self::CleanAndCurrent => state
                .extensions
                .get(EXT_FORGE_FACTS)
                .and_then(|value| serde_json::from_value::<ForgeFacts>(value.clone()).ok())
                .is_some_and(|facts| merge_gate_clean_and_current(&facts)),
            Self::CeilingAllowsPass => !ceiling_refuses(state),
        }
    }
}

/// The set of agents whose sessions must be established before a run body starts.
#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct AgentPlan {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub reviewers: Vec<AgentLaunchTarget>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fixers: Vec<AgentLaunchTarget>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub judge: Option<AgentLaunchTarget>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub finishers: Vec<AgentLaunchTarget>,
}

impl AgentPlan {
    #[must_use]
    fn targets(&self) -> Vec<AgentLaunchTarget> {
        let mut targets =
            Vec::with_capacity(self.reviewers.len() + self.fixers.len() + self.finishers.len() + 1);
        targets.extend(self.reviewers.iter().cloned());
        targets.extend(self.fixers.iter().cloned());
        targets.extend(self.judge.iter().cloned());
        targets.extend(self.finishers.iter().cloned());
        targets
    }
}

/// A core-owned model target selected for one agent launch.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct AgentLaunchTarget {
    pub agent_id: AgentId,
    pub role: AgentRole,
    pub engine: AgentEngine,
    pub vendor: String,
    pub control_plane: String,
    pub lineage: ModelLineage,
}

/// The ensemble engine the core will use for an agent target.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AgentEngine {
    Codex,
    Claude,
    Opencode,
}

impl AgentEngine {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Codex => "codex",
            Self::Claude => "claude",
            Self::Opencode => "opencode",
        }
    }
}

/// Request to prepare an agent session without starting the run body.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AgentLaunchSpec {
    pub target: AgentLaunchTarget,
    pub pass_index: u32,
}

/// Prepared session evidence returned by the launcher seam.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PreparedAgent {
    pub agent_id: AgentId,
    pub role: AgentRole,
    pub session_id: SessionId,
    pub proof: LaunchProof,
}

/// Core-interpreted proof about a prepared session.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum LaunchProof {
    EstablishedFresh,
    Reused { original_session_id: SessionId },
    UnknownFreshness { reason: String },
    Unverified { reason: String },
}

/// Workspace request made after criteria match and before launch.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkspaceRequest {
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub pr: PullRequestRef,
    pub commit_sha: String,
}

/// Request made to trusted source preparation before untrusted code can execute.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SourcePreparationRequest {
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub event: ContractEvent,
    pub state: PrRunState,
    pub workspace: WorkspaceLease,
    pub pr: PullRequestRef,
    pub commit_sha: String,
}

/// A plain source tree prepared outside the credential-free workspace.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PreparedSource {
    pub tree: PathBuf,
    pub revision: String,
    pub cleanup_root: Option<PathBuf>,
}

/// A prepared workspace plus isolation assertion from the trusted provider seam.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkspaceLease {
    pub id: String,
    pub root: PathBuf,
    pub isolation: WorkspaceIsolation,
}

/// First-pass trusted-provider assertion about workspace isolation.
#[allow(
    clippy::struct_excessive_bools,
    reason = "the core must see each workspace trust assertion independently"
)]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct WorkspaceIsolation {
    pub isolated: bool,
    pub credential_free: bool,
    pub egress_bounded: bool,
    pub resource_bounded: bool,
    pub ephemeral: bool,
}

impl WorkspaceIsolation {
    #[must_use]
    pub const fn present(self) -> bool {
        self.isolated
            && self.credential_free
            && self.egress_bounded
            && self.resource_bounded
            && self.ephemeral
    }
}

/// Command execution request for an already-prepared isolated workspace.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkspaceExecRequest {
    pub program: String,
    pub args: Vec<String>,
    pub stdin: Vec<u8>,
    pub env_delta: BTreeMap<String, String>,
    pub cwd_inside_container: String,
}

/// Output from a command executed inside an isolated workspace.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkspaceExecOutput {
    pub exit_code: i32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

impl WorkspaceExecOutput {
    #[must_use]
    pub const fn success(&self) -> bool {
        self.exit_code == 0
    }
}

/// Request to start a run body after the enforcement gate passes.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RunLaunchRequest {
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub event: ContractEvent,
    pub state: PrRunState,
    pub workspace: WorkspaceLease,
    pub provenance: Vec<ModelProvenance>,
}

/// Artifacts produced by an authorised run body.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RunLaunchOutcome {
    pub outcome: RunOutcome,
    pub findings: Vec<Finding>,
    pub decisions: Vec<Decision>,
    pub patches: Vec<Patch>,
    pub token_usage: Option<u64>,
    pub ensemble_archive_path: Option<String>,
}

/// Observable result of evaluating one matching trigger rule.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DispatchOutcome {
    Launched {
        rule_id: String,
        run_id: RunId,
    },
    Superseded {
        rule_id: String,
        run_id: RunId,
        superseded_by: String,
    },
    Refused {
        rule_id: String,
        reason: LaunchRefusal,
    },
    Skipped {
        rule_id: String,
        reason: SkipReason,
    },
}

/// Reasons the gate refused to launch a run.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum LaunchRefusal {
    RunCeilingReached,
    RequiredFamilyUnavailable {
        agent_id: AgentId,
        family: ModelFamily,
        reason: String,
    },
    WorkspaceIsolationMissing,
    UnverifiedProvenance {
        agent_id: AgentId,
        reason: String,
    },
    InsufficientReviewerFamilies,
    ReviewerFixerOverlap,
    MissingIndependentJudge,
    NonFreshSession {
        agent_id: AgentId,
    },
}

/// Reasons a trigger match did not have enough contract context to launch.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum SkipReason {
    NoRunState,
    DuplicateDispatch,
    SerialisedByActiveRun,
    SupersededHead,
}

fn establish_provenance(
    target: &AgentLaunchTarget,
    prepared: PreparedAgent,
    pass_index: u32,
) -> ModelProvenance {
    let mismatch = prepared.agent_id != target.agent_id || prepared.role != target.role;
    let (freshness, verification) = if mismatch {
        (
            SessionFreshness::Unknown {
                reason: "prepared session did not match requested target".to_owned(),
            },
            ProvenanceVerification::Unverified {
                reason: "prepared session did not match requested target".to_owned(),
            },
        )
    } else {
        match prepared.proof {
            LaunchProof::EstablishedFresh => (
                SessionFreshness::FreshForPass { pass_index },
                verified_from_target(target),
            ),
            LaunchProof::Reused {
                original_session_id,
            } => (
                SessionFreshness::Reused {
                    original_session_id,
                },
                verified_from_target(target),
            ),
            LaunchProof::UnknownFreshness { reason } => (
                SessionFreshness::Unknown { reason },
                verified_from_target(target),
            ),
            LaunchProof::Unverified { reason } => (
                SessionFreshness::Unknown {
                    reason: reason.clone(),
                },
                ProvenanceVerification::Unverified { reason },
            ),
        }
    };

    let mut extensions = BTreeMap::new();
    extensions.insert(
        EXT_AGENT_ENGINE.to_owned(),
        Value::String(target.engine.as_str().to_owned()),
    );

    ModelProvenance {
        contract_version: ContractVersion::current(),
        agent_id: target.agent_id.clone(),
        role: target.role,
        session_id: prepared.session_id,
        freshness,
        verification,
        extensions,
    }
}

#[must_use]
fn verified_from_target(target: &AgentLaunchTarget) -> ProvenanceVerification {
    if let Err(reason) = verify_engine_lineage(target) {
        return ProvenanceVerification::Unverified { reason };
    }
    ProvenanceVerification::Verified {
        vendor: target.vendor.clone(),
        control_plane: target.control_plane.clone(),
        lineage: target.lineage.clone(),
    }
}

fn verify_engine_lineage(target: &AgentLaunchTarget) -> Result<(), String> {
    let Some(mapped_family) = mapped_family_for_engine_model(target.engine, &target.lineage.model)
    else {
        return Err(format!(
            "engine {} cannot map model {:?} to a trusted model family",
            target.engine.as_str(),
            target.lineage.model
        ));
    };
    if mapped_family == target.lineage.family {
        Ok(())
    } else {
        Err(format!(
            "engine {} with model {:?} maps to family {:?}, not declared family {:?}",
            target.engine.as_str(),
            target.lineage.model,
            mapped_family.0,
            target.lineage.family.0
        ))
    }
}

fn canonicalise_launch_target_model(target: &mut AgentLaunchTarget) {
    if let Some(canonical) =
        canonical_model_for_engine_alias(target.engine, target.lineage.model.as_str())
    {
        target.lineage.model = canonical.to_owned();
    }
}

fn canonical_model_for_engine_alias(engine: AgentEngine, model: &str) -> Option<&'static str> {
    let model = model.trim().to_ascii_lowercase();
    match engine {
        AgentEngine::Claude if model == "opus" => Some("claude-opus-4-8"),
        AgentEngine::Codex | AgentEngine::Claude | AgentEngine::Opencode => None,
    }
}

fn mapped_family_for_engine_model(engine: AgentEngine, model: &str) -> Option<ModelFamily> {
    let model = model.trim().to_ascii_lowercase();
    if model.is_empty() {
        return None;
    }
    match engine {
        AgentEngine::Codex => codex_engine_family(&model),
        AgentEngine::Claude => claude_engine_family(&model),
        AgentEngine::Opencode => opencode_engine_family(&model),
    }
    .map(|family| ModelFamily(family.to_owned()))
}

fn codex_engine_family(model: &str) -> Option<&'static str> {
    if model.starts_with("codex")
        || model.starts_with("gpt-")
        || model.starts_with("o1")
        || model.starts_with("o3")
        || model.starts_with("o4")
        || model.starts_with("o5")
        || model.starts_with("openai/")
    {
        Some("codex")
    } else {
        None
    }
}

fn claude_engine_family(model: &str) -> Option<&'static str> {
    if model.starts_with("claude") || model.starts_with("anthropic/claude") {
        Some("claude")
    } else {
        None
    }
}

fn opencode_engine_family(model: &str) -> Option<&'static str> {
    let model = model
        .strip_prefix("anthropic/")
        .or_else(|| model.strip_prefix("openai/"))
        .or_else(|| model.strip_prefix("google/"))
        .or_else(|| model.strip_prefix("mistral/"))
        .or_else(|| model.strip_prefix("openrouter/"))
        .or_else(|| model.strip_prefix("groq/"))
        .or_else(|| model.strip_prefix("xai/"))
        .or_else(|| model.strip_prefix("ollama/"))
        .unwrap_or(model);
    if model.starts_with("claude") {
        Some("claude")
    } else if model.starts_with("codex")
        || model.starts_with("gpt-")
        || model.starts_with("o1")
        || model.starts_with("o3")
        || model.starts_with("o4")
        || model.starts_with("o5")
    {
        Some("codex")
    } else if model.starts_with("glm") || model.starts_with("z-ai/glm") {
        Some("glm")
    } else if model.starts_with("gemini") || model.starts_with("palm") {
        Some("gemini")
    } else if model.starts_with("mistral") || model.starts_with("mixtral") {
        Some("mistral")
    } else if model.starts_with("llama") || model.starts_with("meta-llama") {
        Some("llama")
    } else if model.starts_with("qwen") {
        Some("qwen")
    } else if model.starts_with("deepseek") {
        Some("deepseek")
    } else if model.starts_with("grok") {
        Some("grok")
    } else {
        None
    }
}

fn launch_refusal_from_prepare_error(error: &CoreError) -> Option<LaunchRefusal> {
    match error {
        CoreError::RequiredFamilyUnavailable {
            agent_id,
            family,
            reason,
        } => Some(LaunchRefusal::RequiredFamilyUnavailable {
            agent_id: agent_id.clone(),
            family: family.clone(),
            reason: reason.clone(),
        }),
        CoreError::EventSource(_)
        | CoreError::Workspace(_)
        | CoreError::SourcePreparation(_)
        | CoreError::CommentFormatting(_)
        | CoreError::Launcher(_)
        | CoreError::StateStore(_)
        | CoreError::ForgeOperation(_)
        | CoreError::Io { .. }
        | CoreError::Json { .. } => None,
    }
}

#[must_use]
fn evaluate_gate(
    provenances: &[ModelProvenance],
    current_provenances: &[ModelProvenance],
    pass_index: u32,
    workspace: &WorkspaceLease,
) -> Option<LaunchRefusal> {
    if !workspace.isolation.present() {
        return Some(LaunchRefusal::WorkspaceIsolationMissing);
    }

    if let Some(unverified) =
        provenances
            .iter()
            .find_map(|provenance| match &provenance.verification {
                ProvenanceVerification::Verified { .. } => None,
                ProvenanceVerification::Unverified { reason } => Some((provenance, reason)),
            })
    {
        return Some(LaunchRefusal::UnverifiedProvenance {
            agent_id: unverified.0.agent_id.clone(),
            reason: unverified.1.clone(),
        });
    }

    if !has_two_verified_reviewer_families(provenances) {
        return Some(LaunchRefusal::InsufficientReviewerFamilies);
    }

    if !reviewers_disjoint_from_fixers(provenances) {
        return Some(LaunchRefusal::ReviewerFixerOverlap);
    }

    let all_reviewers = provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
        .cloned()
        .collect::<Vec<_>>();
    let current_reviewers = current_provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
        .cloned()
        .collect::<Vec<_>>();
    let current_judges = current_provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Judge)
        .collect::<Vec<_>>();
    let all_judges = provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Judge)
        .collect::<Vec<_>>();
    let judges = if current_judges.is_empty() {
        all_judges
    } else {
        current_judges
    };
    let reviewers = if current_reviewers.is_empty() {
        all_reviewers
    } else {
        current_reviewers
    };
    if judges.is_empty()
        || !judges
            .iter()
            .all(|judge| judge_independent_of_reviewers(judge, &reviewers))
    {
        return Some(LaunchRefusal::MissingIndependentJudge);
    }

    if !sessions_fresh_for_pass(current_provenances, pass_index) {
        let agent_id = current_provenances
            .iter()
            .find(|provenance| {
                !matches!(
                    provenance.freshness,
                    SessionFreshness::FreshForPass { pass_index: freshness_pass }
                        if freshness_pass == pass_index
                )
            })
            .map_or_else(
                || AgentId("unknown".to_owned()),
                |provenance| provenance.agent_id.clone(),
            );
        return Some(LaunchRefusal::NonFreshSession { agent_id });
    }

    None
}

#[must_use]
fn initial_state_from_event(event: &ContractEvent) -> Option<PrRunState> {
    let facts = match &event.payload {
        EventPayload::PullRequestOpened { facts } | EventPayload::PullRequestUpdated { facts } => {
            facts
        }
        EventPayload::LabelApplied { .. } | EventPayload::RunCompleted { .. } => return None,
    };
    Some(initial_state_from_facts(facts))
}

#[must_use]
fn initial_state_from_facts(facts: &ForgeFacts) -> PrRunState {
    let mut extensions = Extensions::new();
    if let Ok(value) = serde_json::to_value(facts) {
        extensions.insert(EXT_FORGE_FACTS.to_owned(), value);
    }
    PrRunState {
        contract_version: ContractVersion::current(),
        pr: facts.pr.clone(),
        commit_sha: facts.head.sha.clone(),
        current_head_sha: Some(facts.head.sha.clone()),
        pass_index: 1,
        status: RunStatus::Pending,
        active_run: None,
        run_history: Vec::new(),
        loop_history: Vec::new(),
        superseded_by: None,
        findings: Vec::new(),
        decisions: Vec::new(),
        patches: Vec::new(),
        publication: PublicationState::default(),
        ceiling: None,
        extensions,
    }
}

fn sync_state_with_forge_facts(
    state: &mut PrRunState,
    facts: &ForgeFacts,
) -> Result<(), CoreError> {
    state.current_head_sha = Some(facts.head.sha.clone());
    state.extensions.insert(
        EXT_FORGE_FACTS.to_owned(),
        serde_json::to_value(facts).map_err(|source| CoreError::Json {
            path: EXT_FORGE_FACTS.to_owned(),
            source,
        })?,
    );
    Ok(())
}

#[must_use]
fn collect_state_provenance(state: &PrRunState) -> Vec<ModelProvenance> {
    let archived_len = state
        .loop_history
        .iter()
        .map(|pass| pass.findings.len() + pass.decisions.len() + pass.patches.len())
        .sum::<usize>();
    let mut provenances = Vec::with_capacity(
        state.findings.len() + state.decisions.len() + state.patches.len() + archived_len,
    );
    // Run records carry the provenance their run launched with, so reviewers
    // stay visible to later gates (family spread, judge independence) even
    // when a clean review produced no findings to hang provenance on.
    provenances.extend(
        state
            .run_history
            .iter()
            .flat_map(|record| record.provenance.iter().cloned()),
    );
    provenances.extend(
        state
            .findings
            .iter()
            .map(|finding| finding.provenance.clone()),
    );
    provenances.extend(
        state
            .decisions
            .iter()
            .map(|decision| decision.provenance.clone()),
    );
    provenances.extend(state.patches.iter().map(|patch| patch.provenance.clone()));
    for pass in &state.loop_history {
        provenances.extend(
            pass.findings
                .iter()
                .map(|finding| finding.provenance.clone()),
        );
        provenances.extend(
            pass.decisions
                .iter()
                .map(|decision| decision.provenance.clone()),
        );
        provenances.extend(pass.patches.iter().map(|patch| patch.provenance.clone()));
    }
    provenances
}

const fn provenance_pass(provenance: &ModelProvenance) -> Option<u32> {
    match provenance.freshness {
        SessionFreshness::FreshForPass { pass_index } => Some(pass_index),
        SessionFreshness::Reused { .. } | SessionFreshness::Unknown { .. } => None,
    }
}

fn forge_facts_from_state(state: &PrRunState) -> Result<Option<ForgeFacts>, CoreError> {
    state
        .extensions
        .get(EXT_FORGE_FACTS)
        .map(|value| {
            serde_json::from_value::<ForgeFacts>(value.clone()).map_err(|source| CoreError::Json {
                path: EXT_FORGE_FACTS.to_owned(),
                source,
            })
        })
        .transpose()
}

fn decision_finding_ids(decision: &Decision) -> Vec<&pump19_contract::FindingId> {
    match &decision.subject {
        pump19_contract::DecisionSubject::Finding { finding_id } => vec![finding_id],
        pump19_contract::DecisionSubject::FindingSet { finding_ids } => {
            finding_ids.iter().collect()
        }
    }
}

fn material_findings_for_pass(state: &PrRunState) -> Vec<(Finding, Decision)> {
    state
        .decisions
        .iter()
        .filter(|decision| decision.verdict == DecisionVerdict::Material)
        .filter(|decision| provenance_pass(&decision.provenance) == Some(state.pass_index))
        .flat_map(|decision| {
            decision_finding_ids(decision)
                .into_iter()
                .filter_map(|finding_id| {
                    state
                        .findings
                        .iter()
                        .find(|finding| &finding.id == finding_id)
                        .map(|finding| (finding.clone(), decision.clone()))
                })
                .collect::<Vec<_>>()
        })
        .collect()
}

fn state_converged_for_current_pass(state: &PrRunState) -> bool {
    convergence_decision_id_for_current_pass(state).is_some()
}

fn convergence_decision_id_for_current_pass(state: &PrRunState) -> Option<String> {
    state
        .decisions
        .iter()
        .filter(|decision| provenance_pass(&decision.provenance) == Some(state.pass_index))
        .find(|decision| decision.verdict == DecisionVerdict::Converged)
        .map(|decision| decision.id.clone())
}

fn core_may_apply_finish_label(facts: &ForgeFacts, label: &str) -> bool {
    facts
        .finish_label
        .as_ref()
        .is_none_or(|finish_label| finish_label.name != label)
        && merge_gate_clean_and_current(facts)
}

fn finish_label_actor_may_merge(
    policy: &CorePolicy,
    facts: &ForgeFacts,
    label: &FinishLabel,
) -> bool {
    if is_core_actor(&label.applied_by) {
        core_policy_grants_finish_label(policy, &label.name)
    } else {
        actor_has_capability(facts, &label.applied_by, ActorCapability::Merge)
    }
}

fn core_policy_grants_finish_label(policy: &CorePolicy, label: &str) -> bool {
    matches!(
        &policy.finish_label_application,
        FinishLabelApplicationPolicy::CoreOnConvergence {
            label: granted_label
        } if granted_label == label
    )
}

fn finish_label_authorisation(
    state: &PrRunState,
    facts: &ForgeFacts,
    actor: &ActorRef,
    idempotency_key: String,
) -> AuthorisationContext {
    AuthorisationContext {
        pr: state.pr.clone(),
        observed_head_sha: facts.head.sha.clone(),
        idempotency_key,
        actor: actor.clone(),
        reason: "core authorised applying finish label after convergence".to_owned(),
        evidence: vec![
            AuthorisationEvidence::Decision {
                decision_id: convergence_decision_id_for_current_pass(state)
                    .unwrap_or_else(|| "converged".to_owned()),
                verdict: DecisionVerdict::Converged,
            },
            AuthorisationEvidence::ActorCapability {
                actor: actor.clone(),
                capability: ActorCapability::ApplyFinishLabel,
            },
            AuthorisationEvidence::MergeGateCleanAndCurrent {
                facts_head_sha: facts.head.sha.clone(),
            },
        ],
    }
}

fn record_finish_label_attempt(
    state: &mut PrRunState,
    run_id: &RunId,
    label: &str,
    idempotency_key: String,
    result: Result<ForgeOperationReceipt, ForgeOperationError>,
) -> Result<ForgeReceipt, CoreError> {
    let operation = PublicationOperation::ApplyFinishLabel {
        label: label.to_owned(),
    };
    match result {
        Ok(receipt) => {
            let receipt = receipt_to_contract(receipt);
            record_publication_attempt(
                state,
                run_id,
                operation,
                idempotency_key,
                None,
                Ok(receipt.clone()),
            );
            Ok(receipt)
        }
        Err(error) => {
            let message = error.to_string();
            record_publication_attempt(
                state,
                run_id,
                operation,
                idempotency_key,
                None,
                Err(message.clone()),
            );
            Err(CoreError::ForgeOperation(message))
        }
    }
}

fn finding_comment_authorisation(
    state: &PrRunState,
    facts: &ForgeFacts,
    idempotency_key: String,
    decision: &Decision,
    finding: &Finding,
    reason: &str,
) -> AuthorisationContext {
    AuthorisationContext {
        pr: state.pr.clone(),
        observed_head_sha: facts.head.sha.clone(),
        idempotency_key,
        actor: core_actor(),
        reason: reason.to_owned(),
        evidence: vec![
            AuthorisationEvidence::Decision {
                decision_id: decision.id.clone(),
                verdict: decision.verdict,
            },
            AuthorisationEvidence::Finding {
                finding_id: finding.id.clone(),
            },
        ],
    }
}

fn upsert_finding_comment_publication(
    state: &mut PrRunState,
    finding: &Finding,
    receipt: ForgeReceipt,
    status: FindingCommentStatus,
) {
    if let Some(existing) = state
        .publication
        .finding_comments
        .iter_mut()
        .find(|existing| existing.finding_dedup_key == finding.dedup_key)
    {
        existing.latest_finding_id = finding.id.clone();
        existing
            .comment_operation_id
            .clone_from(&receipt.operation_id);
        existing.status = status;
        existing.last_receipt = receipt;
        return;
    }
    state
        .publication
        .finding_comments
        .push(FindingCommentPublication {
            finding_dedup_key: finding.dedup_key.clone(),
            latest_finding_id: finding.id.clone(),
            comment_operation_id: receipt.operation_id.clone(),
            status,
            last_receipt: receipt,
        });
}

fn record_publication_attempt(
    state: &mut PrRunState,
    run_id: &RunId,
    operation: PublicationOperation,
    idempotency_key: String,
    expected_head_sha: Option<String>,
    result: Result<ForgeReceipt, String>,
) {
    let (status, receipt, error, refusal) = match result {
        Ok(receipt) => (
            PublicationAttemptStatus::Succeeded,
            Some(receipt),
            None,
            None,
        ),
        Err(error) => (PublicationAttemptStatus::Failed, None, Some(error), None),
    };
    state.publication.attempts.push(PublicationAttempt {
        contract_version: ContractVersion::current(),
        run_id: run_id.clone(),
        operation,
        idempotency_key,
        expected_head_sha,
        status,
        receipt,
        error,
        refusal,
    });
}

fn record_refused_publication_attempt(
    state: &mut PrRunState,
    run_id: &RunId,
    operation: PublicationOperation,
    idempotency_key: String,
    expected_head_sha: Option<String>,
    refusal: PublicationRefusal,
) {
    state.publication.attempts.push(PublicationAttempt {
        contract_version: ContractVersion::current(),
        run_id: run_id.clone(),
        operation,
        idempotency_key,
        expected_head_sha,
        status: PublicationAttemptStatus::Refused,
        receipt: None,
        error: Some(refusal.message.clone()),
        refusal: Some(refusal),
    });
}

fn record_publication_error_attempt(
    state: &mut PrRunState,
    run_id: &RunId,
    operation: PublicationOperation,
    idempotency_key: String,
    expected_head_sha: Option<String>,
    error: &ForgeOperationError,
    message: String,
) {
    if let Some(refusal) = publication_refusal_from_operation_error(error, message.clone()) {
        record_refused_publication_attempt(
            state,
            run_id,
            operation,
            idempotency_key,
            expected_head_sha,
            refusal,
        );
        return;
    }
    record_publication_attempt(
        state,
        run_id,
        operation,
        idempotency_key,
        expected_head_sha,
        Err(message),
    );
}

fn publication_refusal_from_operation_error(
    error: &ForgeOperationError,
    message: String,
) -> Option<PublicationRefusal> {
    match error {
        ForgeOperationError::HeadMoved {
            expected_head_sha,
            actual_head_sha,
        } => Some(PublicationRefusal {
            reason: PublicationRefusalReason::HeadMoved {
                expected_head_sha: expected_head_sha.clone(),
                actual_head_sha: actual_head_sha.clone(),
            },
            message,
        }),
        ForgeOperationError::InvalidRequest(_) | ForgeOperationError::Client(_) => None,
    }
}

fn mark_superseded_from_operation_error(state: &mut PrRunState, error: &ForgeOperationError) {
    if let ForgeOperationError::HeadMoved {
        actual_head_sha: Some(actual_head_sha),
        ..
    } = error
    {
        mark_superseded(state, actual_head_sha.clone(), None);
    }
}

fn has_recorded_ceiling_refusal(state: &PrRunState) -> bool {
    state.run_history.iter().any(|record| {
        matches!(
            record.refusal.as_ref().map(|refusal| &refusal.reason),
            Some(RunRefusalReason::RunCeilingReached)
        )
    })
}

fn operator_log_event(
    state: &PrRunState,
    run_id: &RunId,
    kind: OperatorLogEventKind,
    message: &str,
    refusal_reason: Option<RunRefusalReason>,
) -> Result<OperatorLogEvent, CoreError> {
    let record = run_record_for(state, run_id).ok_or_else(|| {
        CoreError::StateStore(format!(
            "cannot record operator event for unknown run {}",
            run_id.0
        ))
    })?;
    Ok(OperatorLogEvent {
        contract_version: ContractVersion::current(),
        kind,
        pr: state.pr.clone(),
        run_id: run_id.clone(),
        run_kind: record.run_kind,
        pass_index: record.pass_index,
        commit_sha: record.commit_sha.clone(),
        message: message.to_owned(),
        refusal_reason,
    })
}

fn run_record_for<'a>(state: &'a PrRunState, run_id: &RunId) -> Option<&'a RunRecord> {
    state
        .active_run
        .as_ref()
        .filter(|record| record.run_id == *run_id)
        .or_else(|| {
            state
                .run_history
                .iter()
                .rev()
                .find(|record| record.run_id == *run_id)
        })
}

fn receipt_to_contract(receipt: ForgeOperationReceipt) -> ForgeReceipt {
    ForgeReceipt {
        operation_id: receipt.operation_id,
        idempotency_key: receipt.idempotency_key,
        new_head_sha: receipt.new_head_sha,
    }
}

fn fix_patches_for_run(state: &PrRunState, run_id: &RunId) -> Vec<Patch> {
    state
        .loop_history
        .iter()
        .rev()
        .find(|pass| pass.fix_outcome == Some(RunOutcome::Succeeded))
        .map(|pass| {
            pass.patches
                .iter()
                .filter(|patch| patch.run_id == *run_id)
                .cloned()
                .collect()
        })
        .unwrap_or_default()
}

fn fix_commits_for_patches(patches: &[Patch]) -> Vec<AuthorisedFixCommit> {
    patches
        .iter()
        .map(|patch| AuthorisedFixCommit {
            patch: patch.clone(),
            message: fix_commit_message(patch),
            author_agent_id: patch.provenance.agent_id.clone(),
            provenance: patch.provenance.clone(),
        })
        .collect()
}

fn fix_push_authorisation(
    state: &PrRunState,
    facts: &ForgeFacts,
    idempotency_key: &str,
    patch_ids: &[pump19_contract::PatchId],
) -> AuthorisationContext {
    AuthorisationContext {
        pr: state.pr.clone(),
        observed_head_sha: facts.head.sha.clone(),
        idempotency_key: idempotency_key.to_owned(),
        actor: core_actor(),
        reason: "core authorised fix patches as attributed PR-head commits".to_owned(),
        evidence: patch_ids
            .iter()
            .cloned()
            .map(|patch_id| AuthorisationEvidence::Patch { patch_id })
            .collect(),
    }
}

fn published_fix_commits(commits: Vec<AuthorisedFixCommit>) -> Vec<PublishedFixCommit> {
    commits
        .into_iter()
        .map(|commit| PublishedFixCommit {
            patch_id: commit.patch.id,
            author_agent_id: commit.author_agent_id,
            provenance: commit.provenance,
        })
        .collect()
}

fn fix_commit_message(patch: &Patch) -> String {
    let subject = match &patch.change {
        pump19_contract::PatchChange::Description { summary } => {
            format!("fix: {summary}")
        }
        pump19_contract::PatchChange::UnifiedDiff { .. } => {
            format!("fix: apply Pump-19 patch {}", patch.id.0)
        }
    };
    let findings = patch
        .answers_findings
        .iter()
        .map(|finding_id| finding_id.0.as_str())
        .collect::<Vec<_>>()
        .join(", ");
    format!(
        "{subject}\n\nPump-19 patch: {}\nAnswers findings: {}",
        patch.id.0, findings
    )
}

fn actor_has_capability(facts: &ForgeFacts, actor: &ActorRef, capability: ActorCapability) -> bool {
    facts.actor_permissions.iter().any(|permission| {
        permission.actor == *actor && permission.capabilities.contains(&capability)
    })
}

fn core_actor() -> ActorRef {
    ActorRef {
        id: "pump19-core".to_owned(),
        display_name: "Pump-19 Core".to_owned(),
    }
}

fn is_core_actor(actor: &ActorRef) -> bool {
    actor.id == "pump19-core"
}

fn stable_id<'a>(prefix: &str, parts: impl IntoIterator<Item = &'a str>) -> String {
    let mut hash = 0xcbf2_9ce4_8422_2325_u64;
    for part in parts {
        for byte in part.bytes() {
            hash ^= u64::from(byte);
            hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
        }
        hash ^= u64::from(b':');
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    format!("{prefix}-{hash:016x}")
}

#[must_use]
fn ceiling_refuses(state: &PrRunState) -> bool {
    let Some(ceiling) = state.ceiling else {
        return false;
    };
    max_passes_refuses(ceiling, state.pass_index)
        || token_budget_refuses(ceiling, &state.extensions)
}

#[must_use]
fn max_passes_refuses(ceiling: RunCeiling, pass_index: u32) -> bool {
    ceiling
        .max_passes
        .is_some_and(|max_passes| pass_index >= max_passes)
}

#[must_use]
fn token_budget_refuses(ceiling: RunCeiling, extensions: &Extensions) -> bool {
    ceiling.token_budget.is_some_and(|budget| {
        extensions
            .get(EXT_TOKENS_USED)
            .and_then(Value::as_u64)
            .is_some_and(|used| used >= budget)
    })
}

fn dispatch_precheck(
    state: &PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
) -> Option<DispatchOutcome> {
    if state.status == RunStatus::Superseded {
        return Some(DispatchOutcome::Skipped {
            rule_id: rule.id.clone(),
            reason: SkipReason::SupersededHead,
        });
    }
    if state.status == RunStatus::Running || state.active_run.is_some() {
        return Some(DispatchOutcome::Skipped {
            rule_id: rule.id.clone(),
            reason: SkipReason::SerialisedByActiveRun,
        });
    }
    if state_already_dispatched(state, event, rule) {
        return Some(DispatchOutcome::Skipped {
            rule_id: rule.id.clone(),
            reason: SkipReason::DuplicateDispatch,
        });
    }
    if ceiling_refuses(state) {
        return Some(DispatchOutcome::Refused {
            rule_id: rule.id.clone(),
            reason: LaunchRefusal::RunCeilingReached,
        });
    }
    None
}

#[must_use]
fn mark_running(
    mut state: PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
    provenance: Vec<ModelProvenance>,
) -> PrRunState {
    state.status = RunStatus::Running;
    state.active_run = Some(RunRecord {
        run_id: run_id.clone(),
        run_kind: rule.run_kind,
        event_id: event.id.clone(),
        rule_id: rule.id.clone(),
        pass_index: state.pass_index,
        commit_sha: state.commit_sha.clone(),
        status: RunStatus::Running,
        outcome: None,
        refusal: None,
        ensemble_archive_path: None,
        provenance,
    });
    state.extensions.insert(
        EXT_RUNNING_RUN_ID.to_owned(),
        Value::String(run_id.0.clone()),
    );
    state.extensions.insert(
        EXT_LAST_EVENT_ID.to_owned(),
        Value::String(event.id.clone()),
    );
    state
        .extensions
        .insert(EXT_LAST_RULE_ID.to_owned(), Value::String(rule.id.clone()));
    state.extensions.insert(
        EXT_LAST_RUN_KIND.to_owned(),
        Value::String(format!("{:?}", rule.run_kind)),
    );
    state
}

fn mark_failed(
    state: &mut PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
    message: &str,
) {
    state.status = RunStatus::Failed;
    state.extensions.insert(
        EXT_LAST_FAILURE.to_owned(),
        Value::String(message.to_owned()),
    );
    record_terminal_run(
        state,
        fallback_run_record(state, event, rule, run_id),
        RunStatus::Failed,
        Some(RunOutcome::Failed),
    );
}

fn mark_failed_refusal(
    state: &mut PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
    refusal: &LaunchRefusal,
    message: &str,
) {
    state.status = RunStatus::Failed;
    state.extensions.insert(
        EXT_LAST_FAILURE.to_owned(),
        Value::String(message.to_owned()),
    );
    let mut record = fallback_run_record(state, event, rule, run_id);
    record.refusal = Some(run_refusal_from_launch_refusal(refusal, message));
    record_terminal_run(state, record, RunStatus::Failed, Some(RunOutcome::Failed));
}

fn mark_skipped_refusal(
    state: &mut PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
    refusal: &LaunchRefusal,
    message: &str,
) {
    state.status = RunStatus::Skipped;
    let mut record = fallback_run_record(state, event, rule, run_id);
    record.refusal = Some(run_refusal_from_launch_refusal(refusal, message));
    record_terminal_run(state, record, RunStatus::Skipped, None);
}

fn mark_superseded(state: &mut PrRunState, superseded_by: String, fallback: Option<RunRecord>) {
    state.status = RunStatus::Superseded;
    state.superseded_by = Some(superseded_by);
    let fallback = fallback.or_else(|| state.active_run.clone());
    if let Some(record) = fallback {
        record_terminal_run(
            state,
            record,
            RunStatus::Superseded,
            Some(RunOutcome::Cancelled),
        );
    } else {
        state.active_run = None;
    }
}

fn apply_run_outcome(state: &mut PrRunState, run_kind: RunKind, outcome: RunLaunchOutcome) {
    state.status = match outcome.outcome {
        RunOutcome::Succeeded | RunOutcome::NoOp => RunStatus::Completed,
        RunOutcome::Failed | RunOutcome::Cancelled => RunStatus::Failed,
    };
    if let Some(mut active_run) = state.active_run.take() {
        active_run.status = state.status;
        active_run.outcome = Some(outcome.outcome);
        active_run
            .ensemble_archive_path
            .clone_from(&outcome.ensemble_archive_path);
        record_terminal_run(state, active_run, state.status, Some(outcome.outcome));
    }
    match run_kind {
        RunKind::Review => {
            state.findings = outcome.findings;
            state.decisions.clear();
            state.patches.clear();
        }
        RunKind::Judge => {
            state.decisions = outcome.decisions;
        }
        RunKind::Fix => {
            let patches = outcome.patches;
            state.patches.clone_from(&patches);
            if matches!(outcome.outcome, RunOutcome::Succeeded | RunOutcome::NoOp) {
                state.loop_history.push(loop_record_from_state(
                    state,
                    patches,
                    Some(outcome.outcome),
                ));
                state.findings.clear();
                state.decisions.clear();
                state.patches.clear();
            }
        }
        RunKind::Finish => {}
    }
    if run_kind == RunKind::Fix && outcome.outcome == RunOutcome::Succeeded {
        state.pass_index = state.pass_index.saturating_add(1);
    }
    if let Some(tokens) = outcome.token_usage {
        let current = state
            .extensions
            .get(EXT_TOKENS_USED)
            .and_then(Value::as_u64)
            .unwrap_or(0);
        state.extensions.insert(
            EXT_TOKENS_USED.to_owned(),
            Value::Number(serde_json::Number::from(current.saturating_add(tokens))),
        );
    }
}

fn fallback_run_record(
    state: &PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
) -> RunRecord {
    RunRecord {
        run_id: run_id.clone(),
        run_kind: rule.run_kind,
        event_id: event.id.clone(),
        rule_id: rule.id.clone(),
        pass_index: state.pass_index,
        commit_sha: state.commit_sha.clone(),
        status: state.status,
        outcome: None,
        refusal: None,
        ensemble_archive_path: None,
        provenance: Vec::new(),
    }
}

fn record_terminal_run(
    state: &mut PrRunState,
    mut record: RunRecord,
    status: RunStatus,
    outcome: Option<RunOutcome>,
) {
    state.active_run = None;
    record.status = status;
    record.outcome = outcome;
    if let Some(existing) = state
        .run_history
        .iter_mut()
        .find(|candidate| candidate.run_id == record.run_id)
    {
        *existing = record;
    } else {
        state.run_history.push(record);
    }
}

fn run_refusal_from_launch_refusal(refusal: &LaunchRefusal, message: &str) -> RunRefusal {
    RunRefusal {
        reason: match refusal {
            LaunchRefusal::RunCeilingReached => RunRefusalReason::RunCeilingReached,
            LaunchRefusal::RequiredFamilyUnavailable { .. } => {
                RunRefusalReason::RequiredFamilyUnavailable
            }
            LaunchRefusal::WorkspaceIsolationMissing => RunRefusalReason::WorkspaceIsolationMissing,
            LaunchRefusal::UnverifiedProvenance { .. } => RunRefusalReason::UnverifiedProvenance,
            LaunchRefusal::InsufficientReviewerFamilies => {
                RunRefusalReason::InsufficientReviewerFamilies
            }
            LaunchRefusal::ReviewerFixerOverlap => RunRefusalReason::ReviewerFixerOverlap,
            LaunchRefusal::MissingIndependentJudge => RunRefusalReason::MissingIndependentJudge,
            LaunchRefusal::NonFreshSession { .. } => RunRefusalReason::NonFreshSession,
        },
        message: message.to_owned(),
    }
}

fn finish_before_cleanup(
    primary_result: Result<(), CoreError>,
    cleanup_result: Result<(), CoreError>,
) -> Result<(), CoreError> {
    match (primary_result, cleanup_result) {
        (Ok(()), Ok(())) => Ok(()),
        (Err(primary_error), Ok(())) => Err(primary_error),
        (Ok(()), Err(cleanup_error)) => Err(cleanup_error),
        (Err(primary_error), Err(cleanup_error)) => {
            eprintln!(
                "pump19_core_cleanup_error: primary_failure={primary_error}; cleanup_failure={cleanup_error}"
            );
            Err(combine_primary_and_cleanup_errors(
                &primary_error,
                &cleanup_error,
            ))
        }
    }
}

fn combine_primary_and_cleanup_errors(
    primary_error: &CoreError,
    cleanup_error: &CoreError,
) -> CoreError {
    let message = format!(
        "dispatch bookkeeping failed: {primary_error}; workspace cleanup also failed: {cleanup_error}"
    );
    match primary_error {
        CoreError::StateStore(_) | CoreError::Io { .. } | CoreError::Json { .. } => {
            CoreError::StateStore(message)
        }
        CoreError::EventSource(_)
        | CoreError::Workspace(_)
        | CoreError::SourcePreparation(_)
        | CoreError::CommentFormatting(_)
        | CoreError::Launcher(_)
        | CoreError::RequiredFamilyUnavailable { .. }
        | CoreError::ForgeOperation(_) => CoreError::Workspace(message),
    }
}

fn loop_record_from_state(
    state: &PrRunState,
    patches: Vec<Patch>,
    fix_outcome: Option<RunOutcome>,
) -> LoopPassRecord {
    LoopPassRecord {
        pass_index: state.pass_index,
        commit_sha: state.commit_sha.clone(),
        findings: state.findings.clone(),
        decisions: state.decisions.clone(),
        patches,
        judge_verdict: state.decisions.iter().rev().find_map(|decision| {
            (provenance_pass(&decision.provenance) == Some(state.pass_index))
                .then_some(decision.verdict)
        }),
        fix_outcome,
    }
}

fn state_records_run(state: &PrRunState, run_id: &RunId) -> bool {
    state
        .active_run
        .as_ref()
        .is_some_and(|record| record.run_id == *run_id)
        || state
            .run_history
            .iter()
            .any(|record| record.run_id == *run_id)
        || state
            .extensions
            .get(EXT_RUNNING_RUN_ID)
            .and_then(Value::as_str)
            == Some(run_id.0.as_str())
}

fn state_already_dispatched(state: &PrRunState, event: &ContractEvent, rule: &TriggerRule) -> bool {
    if matches!(
        event.payload,
        EventPayload::PullRequestOpened { .. } | EventPayload::PullRequestUpdated { .. }
    ) {
        return state.run_history.iter().any(|record| {
            record.status != RunStatus::Failed
                && record.run_kind == rule.run_kind
                && record.pass_index == state.pass_index
                && record.commit_sha == state.commit_sha
        });
    }

    state
        .run_history
        .iter()
        .any(|record| record.status != RunStatus::Failed && record.event_id == event.id)
}

fn completion_event_has_failed_dispatch(state: &PrRunState, event: &ContractEvent) -> bool {
    state
        .run_history
        .iter()
        .any(|record| record.status == RunStatus::Failed && record.event_id == event.id)
}

fn run_completed_event_from_state(state: &PrRunState, run_id: &RunId) -> Option<ContractEvent> {
    let record = state
        .run_history
        .iter()
        .rev()
        .find(|record| record.run_id == *run_id)?;
    run_completed_event_from_record(record)
}

fn run_completed_event_from_record(record: &RunRecord) -> Option<ContractEvent> {
    let outcome = record.outcome?;
    let mut extensions = Extensions::new();
    extensions.insert(EXT_SELF_EMITTED_EVENT.to_owned(), Value::Bool(true));
    Some(ContractEvent {
        contract_version: ContractVersion::current(),
        id: stable_id("run-completed", [record.run_id.0.as_str()]),
        payload: EventPayload::RunCompleted {
            run_id: record.run_id.clone(),
            run_kind: Some(record.run_kind),
            outcome,
        },
        extensions,
    })
}

const fn run_kind_requires_source(run_kind: RunKind) -> bool {
    matches!(run_kind, RunKind::Review | RunKind::Fix)
}

fn state_is_current_pr_control(state: &PrRunState) -> bool {
    state.status != RunStatus::Superseded
        && state
            .current_head_sha
            .as_deref()
            .is_none_or(|head| head == state.commit_sha)
}

#[must_use]
fn run_id_for(event: &ContractEvent, rule: &TriggerRule, pass_index: u32) -> RunId {
    RunId(format!(
        "{}:{}:{}",
        sanitise_path_component(&event.id),
        sanitise_path_component(&rule.id),
        pass_index
    ))
}

#[must_use]
fn sanitise_path_component(value: &str) -> String {
    value
        .chars()
        .map(|ch| {
            if ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | '.') {
                ch
            } else {
                '_'
            }
        })
        .collect()
}

fn validate_prepared_source(
    source: &PreparedSource,
    expected_revision: &str,
) -> Result<(), CoreError> {
    if source.revision != expected_revision {
        return Err(CoreError::SourcePreparation(format!(
            "prepared revision {} did not match recorded head {}",
            source.revision, expected_revision
        )));
    }
    if let Some(cleanup_root) = &source.cleanup_root {
        ensure_source_tree_under_cleanup_root(source, cleanup_root)?;
    }
    let metadata = fs::metadata(&source.tree).map_err(|error| {
        CoreError::SourcePreparation(format!(
            "prepared source tree {} is not readable: {error}",
            source.tree.display()
        ))
    })?;
    if !metadata.is_dir() {
        return Err(CoreError::SourcePreparation(format!(
            "prepared source tree {} is not a directory",
            source.tree.display()
        )));
    }
    reject_credential_residue(&source.tree)
}

fn ensure_source_tree_under_cleanup_root(
    source: &PreparedSource,
    cleanup_root: &Path,
) -> Result<(), CoreError> {
    let canonical_tree = source.tree.canonicalize().map_err(|error| {
        CoreError::SourcePreparation(format!(
            "inspect prepared source tree {}: {error}",
            source.tree.display()
        ))
    })?;
    let canonical_root = cleanup_root.canonicalize().map_err(|error| {
        CoreError::SourcePreparation(format!(
            "inspect trusted cleanup root {}: {error}",
            cleanup_root.display()
        ))
    })?;
    if canonical_tree.starts_with(&canonical_root) && canonical_tree != canonical_root {
        return Ok(());
    }
    Err(CoreError::SourcePreparation(format!(
        "prepared source tree {} is outside trusted cleanup root {}",
        source.tree.display(),
        cleanup_root.display()
    )))
}

fn reject_credential_residue(root: &Path) -> Result<(), CoreError> {
    reject_credential_residue_at(root, root)
}

fn cleanup_external_prepared_source(
    source: &PreparedSource,
    workspace: &WorkspaceLease,
) -> Result<(), CoreError> {
    if source.tree == workspace.root || !source.tree.exists() {
        return Ok(());
    }
    let Some(cleanup_root) = &source.cleanup_root else {
        return Err(CoreError::SourcePreparation(format!(
            "refusing to remove prepared source tree {} without trusted cleanup root",
            source.tree.display()
        )));
    };
    if let Err(error) = ensure_source_tree_under_cleanup_root(source, cleanup_root) {
        return Err(CoreError::SourcePreparation(format!(
            "refusing to remove prepared source tree: {error}"
        )));
    }
    fs::remove_dir_all(&source.tree).map_err(|error| {
        CoreError::SourcePreparation(format!(
            "remove trusted preparation tree {}: {error}",
            source.tree.display()
        ))
    })
}

fn reject_credential_residue_at(root: &Path, path: &Path) -> Result<(), CoreError> {
    for entry in fs::read_dir(path).map_err(|error| {
        CoreError::SourcePreparation(format!("read prepared tree {}: {error}", path.display()))
    })? {
        let entry = entry.map_err(|error| {
            CoreError::SourcePreparation(format!("read prepared tree {}: {error}", path.display()))
        })?;
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().into_owned();
        if credential_residue_name(name.as_str()) {
            return Err(CoreError::SourcePreparation(format!(
                "prepared source contains credential residue at {}",
                display_relative(root, &path)
            )));
        }
        let metadata = fs::symlink_metadata(&path).map_err(|error| {
            CoreError::SourcePreparation(format!(
                "inspect prepared tree {}: {error}",
                path.display()
            ))
        })?;
        if metadata.file_type().is_symlink() {
            reject_escaping_symlink(root, &path)?;
            continue;
        }
        if metadata.is_dir() {
            reject_credential_residue_at(root, &path)?;
        }
    }
    Ok(())
}

fn credential_residue_name(name: &str) -> bool {
    matches!(
        name,
        ".git" | ".gitconfig" | ".git-credentials" | ".netrc" | ".ssh"
    )
}

fn reject_escaping_symlink(root: &Path, path: &Path) -> Result<(), CoreError> {
    let target = fs::read_link(path).map_err(|error| {
        CoreError::SourcePreparation(format!("inspect symlink {}: {error}", path.display()))
    })?;
    if target.is_absolute() {
        return Err(CoreError::SourcePreparation(format!(
            "prepared source contains absolute symlink at {}",
            display_relative(root, path)
        )));
    }
    let parent = path.parent().unwrap_or(root);
    let mut depth = parent
        .strip_prefix(root)
        .map_or(0, |relative| relative.components().count());
    for component in target.components() {
        match component {
            std::path::Component::ParentDir if depth == 0 => {
                return Err(CoreError::SourcePreparation(format!(
                    "prepared source contains escaping symlink at {}",
                    display_relative(root, path)
                )));
            }
            std::path::Component::ParentDir => depth -= 1,
            std::path::Component::Normal(_) => depth += 1,
            std::path::Component::CurDir => {}
            std::path::Component::RootDir | std::path::Component::Prefix(_) => {
                return Err(CoreError::SourcePreparation(format!(
                    "prepared source contains escaping symlink at {}",
                    display_relative(root, path)
                )));
            }
        }
    }
    Ok(())
}

fn display_relative(root: &Path, path: &Path) -> String {
    path.strip_prefix(root)
        .unwrap_or(path)
        .display()
        .to_string()
}

#[cfg(test)]
mod tests {
    use std::{
        cell::RefCell,
        collections::{BTreeMap, VecDeque},
        path::PathBuf,
        rc::Rc,
    };

    use pump19_contract::{
        ActorCapability, ActorPermissions, ActorRef, BranchCurrency, DecisionSubject, FindingId,
        FinishLabel, Mergeability, ModelFamily, ReviewCleanliness, Revision,
    };

    use super::*;

    #[derive(Debug)]
    struct FakeEventSource {
        events: VecDeque<ContractEvent>,
    }

    impl FakeEventSource {
        fn empty() -> Self {
            Self {
                events: VecDeque::new(),
            }
        }

        fn from_events(events: Vec<ContractEvent>) -> Self {
            Self {
                events: VecDeque::from(events),
            }
        }
    }

    impl EventSource for FakeEventSource {
        fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError> {
            Ok(self.events.pop_front())
        }
    }

    #[derive(Debug)]
    struct FakeWorkspaceProvider {
        isolation: WorkspaceIsolation,
        cleaned: usize,
    }

    #[derive(Debug)]
    struct SourceWorkspaceProvider {
        root: PathBuf,
        cleaned: Rc<RefCell<usize>>,
        injections: Rc<RefCell<Vec<PreparedSource>>>,
    }

    #[derive(Debug)]
    struct FailingCleanupSourceWorkspaceProvider {
        inner: SourceWorkspaceProvider,
    }

    #[derive(Debug)]
    struct TreeSourcePreparer {
        tree: PathBuf,
    }

    #[derive(Debug)]
    struct OutsideCleanupRootSourcePreparer {
        tree: PathBuf,
        cleanup_root: PathBuf,
    }

    #[derive(Debug)]
    struct FailingSourcePreparer;

    impl WorkspaceProvider for FakeWorkspaceProvider {
        fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
            Ok(WorkspaceLease {
                id: request.run_id.0,
                root: PathBuf::from("/tmp/pump19-core-test"),
                isolation: self.isolation,
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
            self.cleaned += 1;
            Ok(())
        }
    }

    impl WorkspaceProvider for SourceWorkspaceProvider {
        fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
            fs::create_dir_all(&self.root).map_err(|source| CoreError::Io {
                path: self.root.display().to_string(),
                source,
            })?;
            Ok(WorkspaceLease {
                id: request.run_id.0,
                root: self.root.clone(),
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: true,
                    egress_bounded: true,
                    resource_bounded: true,
                    ephemeral: true,
                },
            })
        }

        fn inject_source(
            &mut self,
            _lease: &WorkspaceLease,
            source: &PreparedSource,
        ) -> Result<(), CoreError> {
            fs::create_dir_all(self.root.join("src")).map_err(|error| CoreError::Io {
                path: self.root.display().to_string(),
                source: error,
            })?;
            fs::copy(source.tree.join("src/lib.rs"), self.root.join("src/lib.rs")).map_err(
                |error| CoreError::Io {
                    path: self.root.join("src/lib.rs").display().to_string(),
                    source: error,
                },
            )?;
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

        fn cleanup(&mut self, _lease: &WorkspaceLease) -> Result<(), CoreError> {
            *self.cleaned.borrow_mut() += 1;
            Ok(())
        }
    }

    impl WorkspaceProvider for FailingCleanupSourceWorkspaceProvider {
        fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
            self.inner.prepare(request)
        }

        fn inject_source(
            &mut self,
            lease: &WorkspaceLease,
            source: &PreparedSource,
        ) -> Result<(), CoreError> {
            self.inner.inject_source(lease, source)
        }

        fn exec(
            &mut self,
            lease: &WorkspaceLease,
            request: WorkspaceExecRequest,
        ) -> Result<WorkspaceExecOutput, CoreError> {
            WorkspaceProvider::exec(&mut self.inner, lease, request)
        }

        fn cleanup(&mut self, lease: &WorkspaceLease) -> Result<(), CoreError> {
            self.inner.cleanup(lease)?;
            Err(CoreError::Workspace(format!(
                "cleanup failed for {}",
                lease.id
            )))
        }
    }

    impl SourcePreparer for TreeSourcePreparer {
        fn prepare_source(
            &mut self,
            request: SourcePreparationRequest,
        ) -> Result<PreparedSource, CoreError> {
            Ok(PreparedSource {
                tree: self.tree.clone(),
                revision: request.commit_sha,
                cleanup_root: self.tree.parent().map(Path::to_path_buf),
            })
        }
    }

    impl SourcePreparer for OutsideCleanupRootSourcePreparer {
        fn prepare_source(
            &mut self,
            request: SourcePreparationRequest,
        ) -> Result<PreparedSource, CoreError> {
            Ok(PreparedSource {
                tree: self.tree.clone(),
                revision: request.commit_sha,
                cleanup_root: Some(self.cleanup_root.clone()),
            })
        }
    }

    impl SourcePreparer for FailingSourcePreparer {
        fn prepare_source(
            &mut self,
            _request: SourcePreparationRequest,
        ) -> Result<PreparedSource, CoreError> {
            Err(CoreError::SourcePreparation(
                "checkout failed in test".to_owned(),
            ))
        }
    }

    #[derive(Debug)]
    struct FakeRunLauncher {
        proofs: VecDeque<LaunchProof>,
        launched: usize,
        outcome: RunLaunchOutcome,
        fail_launch: bool,
        fail_launches_remaining: usize,
        prepare_error: Option<CoreError>,
    }

    #[derive(Debug, Default)]
    struct RecordingForgeOperations {
        fail_comments: bool,
        comments: Vec<AuthorisedComment>,
        comment_updates: Vec<AuthorisedCommentUpdate>,
        comment_resolutions: Vec<AuthorisedCommentResolution>,
        labels: Vec<AuthorisedLabel>,
        merges: Vec<AuthorisedMerge>,
        fix_pushes: Vec<AuthorisedFixPush>,
    }

    #[derive(Debug, Default)]
    struct FailingForgeOperations {
        comments: Vec<AuthorisedComment>,
    }

    #[derive(Debug, Default)]
    struct RecordingCommentFormatter {
        fail: bool,
        requests: Rc<RefCell<Vec<FindingCommentFormatRequest>>>,
    }

    #[derive(Clone, Debug, Default)]
    struct RecordingOperatorLog {
        events: Rc<RefCell<Vec<OperatorLogEvent>>>,
    }

    impl RecordingCommentFormatter {
        fn failing() -> Self {
            Self {
                fail: true,
                requests: Rc::default(),
            }
        }
    }

    impl CommentFormatter for RecordingCommentFormatter {
        fn format_finding_comment(
            &mut self,
            request: FindingCommentFormatRequest,
        ) -> Result<String, CoreError> {
            self.requests.borrow_mut().push(request.clone());
            if self.fail {
                return Err(CoreError::CommentFormatting(
                    "formatter fixture failed".to_owned(),
                ));
            }
            Ok(format!(
                "formatted material finding {} via {}",
                request.finding.id.0, request.decision.id
            ))
        }
    }

    impl OperatorLog for RecordingOperatorLog {
        fn record(&mut self, event: OperatorLogEvent) -> Result<(), CoreError> {
            self.events.borrow_mut().push(event);
            Ok(())
        }
    }

    impl ForgeOperations for RecordingForgeOperations {
        fn post_comment(
            &mut self,
            request: AuthorisedComment,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            if self.fail_comments {
                return Err(ForgeOperationError::Client(
                    "comment channel unavailable".to_owned(),
                ));
            }
            let idempotency_key = request.authorisation.idempotency_key.clone();
            self.comments.push(request);
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
            self.comment_updates.push(request);
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
            self.comment_resolutions.push(request);
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
            self.labels.push(request);
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
            self.merges.push(request);
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
            self.fix_pushes.push(request);
            Ok(ForgeOperationReceipt {
                operation_id: "fix-push".to_owned(),
                idempotency_key,
                new_head_sha: Some("head-after-fix".to_owned()),
            })
        }
    }

    impl ForgeOperations for FailingForgeOperations {
        fn post_comment(
            &mut self,
            request: AuthorisedComment,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            self.comments.push(request);
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }

        fn update_comment(
            &mut self,
            _request: AuthorisedCommentUpdate,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }

        fn resolve_comment(
            &mut self,
            _request: AuthorisedCommentResolution,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }

        fn apply_label(
            &mut self,
            _request: AuthorisedLabel,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }

        fn merge(
            &mut self,
            _request: AuthorisedMerge,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }

        fn push_fix_commits(
            &mut self,
            _request: AuthorisedFixPush,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            Err(ForgeOperationError::Client("forge unavailable".to_owned()))
        }
    }

    #[derive(Debug, Default)]
    struct LoopLauncher;

    impl RunLauncher for LoopLauncher {
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
                RunKind::Review => {
                    let reviewers = request
                        .provenance
                        .iter()
                        .filter(|provenance| provenance.role == AgentRole::Reviewer)
                        .cloned()
                        .collect::<Vec<_>>();
                    if request.state.pass_index > 1 {
                        return Ok(RunLaunchOutcome {
                            outcome: RunOutcome::Succeeded,
                            findings: Vec::new(),
                            decisions: Vec::new(),
                            patches: Vec::new(),
                            token_usage: None,
                            ensemble_archive_path: None,
                        });
                    }
                    let primary_reviewer = reviewers.first().expect("primary reviewer").clone();
                    let secondary_reviewer = reviewers.get(1).expect("secondary reviewer").clone();
                    let primary_finding_id = "finding-supporting";
                    let judged_finding_id = "finding-material";
                    let finding_for = |finding_id: &str, provenance: ModelProvenance| Finding {
                        contract_version: ContractVersion::current(),
                        id: pump19_contract::FindingId(finding_id.to_owned()),
                        dedup_key: finding_id.to_owned(),
                        source_brief: "loop".to_owned(),
                        dimension: "correctness".to_owned(),
                        summary: format!("finding for pass {}", request.state.pass_index),
                        severity: pump19_contract::Severity::High,
                        confidence: pump19_contract::Confidence::High,
                        certainty: pump19_contract::CertaintyClass::Advisory,
                        provenance,
                        locations: vec![pump19_contract::FindingLocation::General {
                            description: "whole change".to_owned(),
                        }],
                        extensions: BTreeMap::new(),
                    };
                    Ok(RunLaunchOutcome {
                        outcome: RunOutcome::Succeeded,
                        findings: vec![
                            finding_for(primary_finding_id, primary_reviewer),
                            finding_for(judged_finding_id, secondary_reviewer),
                        ],
                        decisions: Vec::new(),
                        patches: Vec::new(),
                        token_usage: None,
                        ensemble_archive_path: None,
                    })
                }
                RunKind::Judge => {
                    let judge = request
                        .provenance
                        .iter()
                        .find(|provenance| provenance.role == AgentRole::Judge)
                        .expect("judge provenance")
                        .clone();
                    let (subject, verdict) = request.state.findings.last().map_or_else(
                        || {
                            (
                                DecisionSubject::FindingSet {
                                    finding_ids: Vec::new(),
                                },
                                DecisionVerdict::Converged,
                            )
                        },
                        |finding| {
                            (
                                DecisionSubject::Finding {
                                    finding_id: finding.id.clone(),
                                },
                                DecisionVerdict::Material,
                            )
                        },
                    );
                    Ok(RunLaunchOutcome {
                        outcome: RunOutcome::Succeeded,
                        findings: Vec::new(),
                        decisions: vec![Decision {
                            contract_version: ContractVersion::current(),
                            id: format!("decision-pass-{}", request.state.pass_index),
                            subject,
                            verdict,
                            rationale: "deterministic loop verdict".to_owned(),
                            provenance: judge,
                            extensions: BTreeMap::new(),
                        }],
                        patches: Vec::new(),
                        token_usage: None,
                        ensemble_archive_path: None,
                    })
                }
                RunKind::Fix => {
                    let fixer = request
                        .provenance
                        .iter()
                        .find(|provenance| provenance.role == AgentRole::Fixer)
                        .expect("fixer provenance")
                        .clone();
                    Ok(RunLaunchOutcome {
                        outcome: RunOutcome::Succeeded,
                        findings: Vec::new(),
                        decisions: Vec::new(),
                        patches: vec![Patch {
                            contract_version: ContractVersion::current(),
                            id: pump19_contract::PatchId("patch-material".to_owned()),
                            run_id: request.run_id.clone(),
                            commit_sha: request.state.commit_sha,
                            idempotency_key: "patch-material".to_owned(),
                            answers_findings: vec![pump19_contract::FindingId(
                                "finding-material".to_owned(),
                            )],
                            change: pump19_contract::PatchChange::Description {
                                summary: "fixed material finding".to_owned(),
                            },
                            provenance: fixer,
                            extensions: BTreeMap::new(),
                        }],
                        token_usage: None,
                        ensemble_archive_path: None,
                    })
                }
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

    impl FakeRunLauncher {
        fn new(proofs: Vec<LaunchProof>) -> Self {
            Self {
                proofs: proofs.into(),
                launched: 0,
                outcome: RunLaunchOutcome {
                    outcome: RunOutcome::Succeeded,
                    findings: Vec::new(),
                    decisions: Vec::new(),
                    patches: Vec::new(),
                    token_usage: None,
                    ensemble_archive_path: None,
                },
                fail_launch: false,
                fail_launches_remaining: 0,
                prepare_error: None,
            }
        }
    }

    impl RunLauncher for FakeRunLauncher {
        fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
            if let Some(error) = self.prepare_error.take() {
                return Err(error);
            }
            let proof = self
                .proofs
                .pop_front()
                .unwrap_or(LaunchProof::EstablishedFresh);
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
            self.launched += 1;
            if self.fail_launches_remaining > 0 {
                self.fail_launches_remaining -= 1;
                return Err(CoreError::Launcher("launch failed".to_owned()));
            }
            if self.fail_launch {
                return Err(CoreError::Launcher("launch failed".to_owned()));
            }
            Ok(self.outcome.clone())
        }
    }

    #[derive(Clone, Debug, Default)]
    struct FakeRunStateStore {
        states: Vec<PrRunState>,
    }

    #[derive(Clone, Debug, Default)]
    struct FailingSaveRunStateStore;

    #[derive(Clone, Debug, Default)]
    struct SupersedingRunStateStore {
        states: Vec<PrRunState>,
        superseding_head: Option<String>,
    }

    #[derive(Clone, Debug, Default)]
    struct FailingSupersededSaveRunStateStore {
        states: Vec<PrRunState>,
        superseding_head: Option<String>,
    }

    impl RunStateStore for FakeRunStateStore {
        fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
            Ok(self
                .states
                .iter()
                .find(|state| RunStateKey::from_state(state) == *key)
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
                .find(|state| state_records_run(state, run_id))
                .cloned())
        }

        fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
            Ok(self.states.clone())
        }

        fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
            let key = RunStateKey::from_state(state);
            if let Some(existing) = self
                .states
                .iter_mut()
                .find(|candidate| RunStateKey::from_state(candidate) == key)
            {
                *existing = state.clone();
            } else {
                self.states.push(state.clone());
            }
            Ok(())
        }
    }

    impl RunStateStore for FailingSaveRunStateStore {
        fn load(&self, _key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
            Ok(None)
        }

        fn load_latest_for_pr(
            &self,
            _pr: &PullRequestRef,
        ) -> Result<Option<PrRunState>, CoreError> {
            Ok(None)
        }

        fn load_by_run_id(&self, _run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
            Ok(None)
        }

        fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
            Ok(Vec::new())
        }

        fn save(&mut self, _state: &PrRunState) -> Result<(), CoreError> {
            Err(CoreError::StateStore("state store unavailable".to_owned()))
        }
    }

    impl RunStateStore for SupersedingRunStateStore {
        fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load(key)
        }

        fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load_latest_for_pr(pr)
        }

        fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load_by_run_id(run_id)
        }

        fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
            let key = RunStateKey::from_state(state);
            if let Some(existing) = self
                .states
                .iter_mut()
                .find(|candidate| RunStateKey::from_state(candidate) == key)
            {
                *existing = state.clone();
            } else {
                self.states.push(state.clone());
            }
            if state.status == RunStatus::Running
                && let Some(head) = &self.superseding_head
            {
                let mut superseding = initial_state_from_facts(&facts_with_head(head));
                superseding.pass_index = state.pass_index.saturating_add(1);
                self.states.push(superseding);
            }
            Ok(())
        }
    }

    impl RunStateStore for FailingSupersededSaveRunStateStore {
        fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load(key)
        }

        fn load_latest_for_pr(&self, pr: &PullRequestRef) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load_latest_for_pr(pr)
        }

        fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
            FakeRunStateStore {
                states: self.states.clone(),
            }
            .load_by_run_id(run_id)
        }

        fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
            Ok(self.states.clone())
        }

        fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
            if state.status == RunStatus::Superseded {
                return Err(CoreError::StateStore(
                    "superseded state save failed".to_owned(),
                ));
            }
            let key = RunStateKey::from_state(state);
            if let Some(existing) = self
                .states
                .iter_mut()
                .find(|candidate| RunStateKey::from_state(candidate) == key)
            {
                *existing = state.clone();
            } else {
                self.states.push(state.clone());
            }
            if state.status == RunStatus::Running
                && let Some(head) = &self.superseding_head
            {
                let mut superseding = initial_state_from_facts(&facts_with_head(head));
                superseding.pass_index = state.pass_index.saturating_add(1);
                self.states.push(superseding);
            }
            Ok(())
        }
    }

    fn pr() -> PullRequestRef {
        PullRequestRef {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
        }
    }

    fn facts() -> ForgeFacts {
        facts_with_head("abc123")
    }

    fn facts_with_head(head_sha: &str) -> ForgeFacts {
        ForgeFacts {
            contract_version: ContractVersion::current(),
            pr: pr(),
            head: Revision {
                sha: head_sha.to_owned(),
            },
            base: Revision {
                sha: "def456".to_owned(),
            },
            branch_currency: BranchCurrency::Current,
            cleanliness: ReviewCleanliness::Clean,
            mergeability: Mergeability::Mergeable,
            finish_label: Some(FinishLabel {
                name: "pump19-finish".to_owned(),
                applied_by: ActorRef {
                    id: "core".to_owned(),
                    display_name: "core".to_owned(),
                },
            }),
            actor_permissions: vec![ActorPermissions {
                actor: ActorRef {
                    id: "core".to_owned(),
                    display_name: "core".to_owned(),
                },
                capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
                    .into_iter()
                    .collect(),
            }],
            author_login: None,
            work_in_progress: false,
            extensions: BTreeMap::new(),
        }
    }

    fn core_authority_facts(head_sha: &str) -> ForgeFacts {
        ForgeFacts {
            finish_label: None,
            actor_permissions: Vec::new(),
            ..facts_with_head(head_sha)
        }
    }

    fn facts_with_spurious_core_actor_permissions(head_sha: &str) -> ForgeFacts {
        ForgeFacts {
            finish_label: Some(FinishLabel {
                name: "pump19-finish".to_owned(),
                applied_by: core_actor(),
            }),
            actor_permissions: vec![ActorPermissions {
                actor: core_actor(),
                capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
                    .into_iter()
                    .collect(),
            }],
            ..facts_with_head(head_sha)
        }
    }

    fn event() -> ContractEvent {
        event_with_facts(facts())
    }

    fn event_with_facts(facts: ForgeFacts) -> ContractEvent {
        ContractEvent {
            contract_version: ContractVersion::current(),
            id: "event-1".to_owned(),
            payload: EventPayload::PullRequestOpened { facts },
            extensions: BTreeMap::new(),
        }
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
            _ => format!("{family}-2026"),
        }
    }

    fn engine_for_family(family: &str) -> AgentEngine {
        match family {
            "claude" => AgentEngine::Claude,
            "codex" => AgentEngine::Codex,
            _ => AgentEngine::Opencode,
        }
    }

    fn verified_provenance(agent_id: &str, role: AgentRole, family: &str) -> ModelProvenance {
        establish_provenance(
            &target(agent_id, role, family),
            PreparedAgent {
                agent_id: AgentId(agent_id.to_owned()),
                role,
                session_id: SessionId(format!("{agent_id}-session")),
                proof: LaunchProof::EstablishedFresh,
            },
            1,
        )
    }

    fn finding_from(agent_id: &str, family: &str, id: &str) -> Finding {
        Finding {
            contract_version: ContractVersion::current(),
            id: pump19_contract::FindingId(id.to_owned()),
            dedup_key: format!("brief:{agent_id}:{id}"),
            source_brief: "review".to_owned(),
            dimension: "correctness".to_owned(),
            summary: "A material review finding.".to_owned(),
            severity: pump19_contract::Severity::High,
            confidence: pump19_contract::Confidence::High,
            certainty: pump19_contract::CertaintyClass::Advisory,
            provenance: verified_provenance(agent_id, AgentRole::Reviewer, family),
            locations: vec![pump19_contract::FindingLocation::General {
                description: "whole change".to_owned(),
            }],
            extensions: BTreeMap::new(),
        }
    }

    fn independent_rule() -> TriggerRule {
        TriggerRule {
            id: "review".to_owned(),
            run_kind: RunKind::Review,
            criteria: Criteria::Event {
                event: EventKind::PullRequestOpened,
            },
            agent_plan: AgentPlan {
                reviewers: vec![
                    target("reviewer-codex", AgentRole::Reviewer, "codex"),
                    target("reviewer-claude", AgentRole::Reviewer, "claude"),
                ],
                fixers: vec![target("fixer", AgentRole::Fixer, "codex")],
                judge: Some(target("judge", AgentRole::Judge, "glm")),
                finishers: Vec::new(),
            },
        }
    }

    fn update_review_rule() -> TriggerRule {
        TriggerRule {
            criteria: Criteria::Event {
                event: EventKind::PullRequestUpdated,
            },
            ..independent_rule()
        }
    }

    fn review_on_open_or_update_rule() -> TriggerRule {
        TriggerRule {
            id: "review-on-pr-change".to_owned(),
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
            ..independent_rule()
        }
    }

    fn review_on_fix_rule() -> TriggerRule {
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

    fn judge_after_noop_fix_rule() -> TriggerRule {
        TriggerRule {
            id: "judge-after-noop-fix".to_owned(),
            run_kind: RunKind::Judge,
            criteria: Criteria::Event {
                event: EventKind::RunCompleted {
                    run_kind: Some(RunKind::Fix),
                    outcome: Some(RunOutcome::NoOp),
                },
            },
            agent_plan: AgentPlan {
                reviewers: Vec::new(),
                fixers: Vec::new(),
                judge: Some(target("judge", AgentRole::Judge, "glm")),
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
                judge: Some(target("judge", AgentRole::Judge, "glm")),
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
                fixers: vec![target("fixer", AgentRole::Fixer, "codex")],
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

    fn run_completed_event(id: &str, run_id: RunId, run_kind: RunKind) -> ContractEvent {
        run_completed_event_with_outcome(id, run_id, run_kind, RunOutcome::Succeeded)
    }

    fn run_completed_event_with_outcome(
        id: &str,
        run_id: RunId,
        run_kind: RunKind,
        outcome: RunOutcome,
    ) -> ContractEvent {
        ContractEvent {
            contract_version: ContractVersion::current(),
            id: id.to_owned(),
            payload: EventPayload::RunCompleted {
                run_id,
                run_kind: Some(run_kind),
                outcome,
            },
            extensions: BTreeMap::new(),
        }
    }

    fn launched_run_id(outcomes: &[DispatchOutcome]) -> Option<RunId> {
        let [DispatchOutcome::Launched { run_id, .. }] = outcomes else {
            return None;
        };
        Some(run_id.clone())
    }

    fn isolated_workspace() -> WorkspaceIsolation {
        WorkspaceIsolation {
            isolated: true,
            credential_free: true,
            egress_bounded: true,
            resource_bounded: true,
            ephemeral: true,
        }
    }

    fn run(rule: TriggerRule, proofs: Vec<LaunchProof>) -> (Vec<DispatchOutcome>, usize) {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(proofs),
            FakeRunStateStore::default(),
        );
        let outcomes = core
            .process_event(&event(), &[rule])
            .expect("process event");
        (outcomes, core.launcher.launched)
    }

    #[test]
    fn trusted_source_preparation_injects_tree_before_launch_and_cleans_prepared_tree() {
        let temp = tempfile::tempdir().expect("temp dir");
        let prepared_tree = temp.path().join("prepared");
        fs::create_dir_all(prepared_tree.join("src")).expect("create prepared tree");
        fs::write(prepared_tree.join("src/lib.rs"), "pub fn prepared() {}\n")
            .expect("write source");
        let workspace_root = temp.path().join("workspace");
        let cleaned = Rc::new(RefCell::new(0));
        let injections = Rc::new(RefCell::new(Vec::new()));
        let mut core = Core::with_forge_operations_and_source_preparer(
            FakeEventSource::empty(),
            SourceWorkspaceProvider {
                root: workspace_root.clone(),
                cleaned: Rc::clone(&cleaned),
                injections: Rc::clone(&injections),
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
            NoopForgeOperations,
            TreeSourcePreparer {
                tree: prepared_tree.clone(),
            },
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert!(matches!(outcomes[0], DispatchOutcome::Launched { .. }));
        assert_eq!(
            fs::read_to_string(workspace_root.join("src/lib.rs")).expect("read injected source"),
            "pub fn prepared() {}\n"
        );
        assert_eq!(injections.borrow().len(), 1);
        assert!(!prepared_tree.exists());
        assert_eq!(*cleaned.borrow(), 1);
    }

    #[test]
    fn source_cleanup_refuses_tree_outside_trusted_preparation_root() {
        let temp = tempfile::tempdir().expect("temp dir");
        let prepared_tree = temp.path().join("outside-prepared");
        let trusted_root = temp.path().join("trusted-preparation-root");
        fs::create_dir_all(prepared_tree.join("src")).expect("create prepared tree");
        fs::create_dir_all(&trusted_root).expect("create trusted root");
        fs::write(
            prepared_tree.join("src/lib.rs"),
            "pub fn unsafe_tree() {}\n",
        )
        .expect("write source");
        let workspace_root = temp.path().join("workspace");
        let cleaned = Rc::new(RefCell::new(0));
        let mut core = Core::with_forge_operations_and_source_preparer(
            FakeEventSource::empty(),
            SourceWorkspaceProvider {
                root: workspace_root,
                cleaned: Rc::clone(&cleaned),
                injections: Rc::new(RefCell::new(Vec::new())),
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            OutsideCleanupRootSourcePreparer {
                tree: prepared_tree.clone(),
                cleanup_root: trusted_root,
            },
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("outside cleanup root should fail dispatch");

        assert!(matches!(error, CoreError::SourcePreparation(_)));
        assert!(prepared_tree.exists());
        assert_eq!(*cleaned.borrow(), 1);
        let saved = core
            .state_store
            .load(&RunStateKey::from_facts(&facts()))
            .expect("load state")
            .expect("saved state");
        assert_eq!(saved.status, RunStatus::Failed);
        assert!(
            saved
                .extensions
                .get(EXT_LAST_FAILURE)
                .and_then(Value::as_str)
                .expect("last failure")
                .contains("outside trusted cleanup root")
        );
    }

    #[test]
    fn source_preparation_failure_records_surfaces_and_cleans_workspace() {
        let temp = tempfile::tempdir().expect("temp dir");
        let cleaned = Rc::new(RefCell::new(0));
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_source_preparer_comment_formatter_and_policy(
            FakeEventSource::empty(),
            SourceWorkspaceProvider {
                root: temp.path().join("workspace"),
                cleaned: Rc::clone(&cleaned),
                injections: Rc::new(RefCell::new(Vec::new())),
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            FailingSourcePreparer,
            MissingCommentFormatter,
            operator_log,
            CorePolicy::human_gate(),
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("source preparation failure should fail dispatch");

        assert!(matches!(error, CoreError::SourcePreparation(_)));
        assert_eq!(*cleaned.borrow(), 1);
        let saved = core
            .state_store
            .load(&RunStateKey::from_facts(&facts()))
            .expect("load state")
            .expect("saved state");
        assert_eq!(saved.status, RunStatus::Failed);
        assert_eq!(saved.run_history.len(), 1);
        assert_eq!(saved.run_history[0].status, RunStatus::Failed);
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
        assert_eq!(
            operator_events.borrow()[0].kind,
            OperatorLogEventKind::RunFailure
        );
        assert!(
            operator_events.borrow()[0]
                .message
                .contains("checkout failed in test")
        );
        assert_eq!(
            saved
                .extensions
                .get(EXT_LAST_FAILURE)
                .and_then(Value::as_str),
            Some("source preparation failed: checkout failed in test")
        );
    }

    #[test]
    fn failed_dispatch_recording_does_not_skip_workspace_cleanup() {
        let temp = tempfile::tempdir().expect("temp dir");
        let cleaned = Rc::new(RefCell::new(0));
        let mut core = Core::with_forge_operations_and_source_preparer(
            FakeEventSource::empty(),
            SourceWorkspaceProvider {
                root: temp.path().join("workspace"),
                cleaned: Rc::clone(&cleaned),
                injections: Rc::new(RefCell::new(Vec::new())),
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FailingSaveRunStateStore,
            RecordingForgeOperations::default(),
            FailingSourcePreparer,
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("recording the failure should fail dispatch");

        assert!(
            matches!(error, CoreError::StateStore(message) if message == "state store unavailable")
        );
        assert_eq!(*cleaned.borrow(), 1);
    }

    #[test]
    fn record_and_cleanup_failures_are_reported_together() {
        let temp = tempfile::tempdir().expect("temp dir");
        let cleaned = Rc::new(RefCell::new(0));
        let mut core = Core::with_forge_operations_and_source_preparer(
            FakeEventSource::empty(),
            FailingCleanupSourceWorkspaceProvider {
                inner: SourceWorkspaceProvider {
                    root: temp.path().join("workspace"),
                    cleaned: Rc::clone(&cleaned),
                    injections: Rc::new(RefCell::new(Vec::new())),
                },
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FailingSaveRunStateStore,
            RecordingForgeOperations::default(),
            FailingSourcePreparer,
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("recording and cleanup failures should be combined");

        assert!(matches!(error, CoreError::StateStore(message)
            if message.contains("state store unavailable")
                && message.contains("workspace cleanup also failed")
                && message.contains("cleanup failed")));
        assert_eq!(*cleaned.borrow(), 1);
    }

    #[test]
    fn judge_dispatch_does_not_prepare_source_by_default() {
        let review_run_id = RunId("review-completed".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Completed;
        state
            .findings
            .push(finding_from("reviewer-codex", "codex", "finding-1"));
        state.run_history.push(RunRecord {
            run_id: review_run_id.clone(),
            run_kind: RunKind::Review,
            event_id: "forgejo-pr-opened".to_owned(),
            rule_id: "review".to_owned(),
            pass_index: 1,
            commit_sha: state.commit_sha.clone(),
            status: RunStatus::Completed,
            outcome: Some(RunOutcome::Succeeded),
            refusal: None,
            ensemble_archive_path: None,
            provenance: vec![
                verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
                verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            ],
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save review state");
        let temp = tempfile::tempdir().expect("temp dir");
        let mut core = Core::with_forge_operations_and_source_preparer(
            FakeEventSource::empty(),
            SourceWorkspaceProvider {
                root: temp.path().join("workspace"),
                cleaned: Rc::new(RefCell::new(0)),
                injections: Rc::new(RefCell::new(Vec::new())),
            },
            FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]),
            store,
            RecordingForgeOperations::default(),
            FailingSourcePreparer,
        );

        let outcomes = core
            .process_event(
                &run_completed_event("review-done", review_run_id, RunKind::Review),
                &[judge_after_review_rule()],
            )
            .expect("judge dispatch should not require source preparation");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "judge-after-review"
        ));
        assert_eq!(core.launcher.launched, 1);
    }

    #[test]
    fn judge_run_uses_historical_reviewers_for_independence_gate() {
        let review_run_id = RunId("event-1:review:1".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Completed;
        state.extensions.insert(
            EXT_RUNNING_RUN_ID.to_owned(),
            Value::String(review_run_id.0.clone()),
        );
        state
            .findings
            .push(finding_from("reviewer-codex", "codex", "finding-1"));
        state
            .findings
            .push(finding_from("reviewer-claude", "claude", "finding-2"));

        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]),
            store,
        );
        let event = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "event-judge".to_owned(),
            payload: EventPayload::RunCompleted {
                run_id: review_run_id,
                run_kind: Some(RunKind::Review),
                outcome: RunOutcome::Succeeded,
            },
            extensions: BTreeMap::new(),
        };
        let rule = TriggerRule {
            id: "judge".to_owned(),
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
                judge: Some(target("judge", AgentRole::Judge, "glm")),
                finishers: Vec::new(),
            },
        };

        let outcomes = core.process_event(&event, &[rule]).expect("process event");

        assert_eq!(core.launcher.launched, 1);
        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "judge"
        ));
    }

    #[test]
    fn run_completed_criteria_match_run_kind_and_outcome() {
        let event = run_completed_event("fix-completed", RunId("run-fix".to_owned()), RunKind::Fix);

        assert!(
            EventKind::RunCompleted {
                run_kind: Some(RunKind::Fix),
                outcome: Some(RunOutcome::Succeeded),
            }
            .matches(&event)
        );
        assert!(
            !EventKind::RunCompleted {
                run_kind: Some(RunKind::Review),
                outcome: Some(RunOutcome::Succeeded),
            }
            .matches(&event)
        );
        assert!(
            !EventKind::RunCompleted {
                run_kind: Some(RunKind::Fix),
                outcome: Some(RunOutcome::Failed),
            }
            .matches(&event)
        );
    }

    fn material_decision_for(finding: &Finding, rationale: &str) -> Decision {
        Decision {
            contract_version: ContractVersion::current(),
            id: "decision-1".to_owned(),
            subject: pump19_contract::DecisionSubject::Finding {
                finding_id: finding.id.clone(),
            },
            verdict: DecisionVerdict::Material,
            rationale: rationale.to_owned(),
            provenance: verified_provenance("judge", AgentRole::Judge, "glm"),
            extensions: BTreeMap::new(),
        }
    }

    #[test]
    fn material_finding_comment_posts_formatter_body() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        let finding = finding_from("reviewer-codex", "codex", "finding-1");
        let decision = material_decision_for(&finding, "Material because the loop can wedge.");
        let run_id = RunId("run-judge-1".to_owned());
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );

        core.post_finding_comment(&run_id, &mut state, &facts(), &finding, &decision)
            .expect("post formatted finding");

        assert_eq!(core.comment_formatter.requests.borrow().len(), 1);
        assert_eq!(core.forge_operations.comments.len(), 1);
        assert_eq!(
            core.forge_operations.comments[0].body,
            "formatted material finding finding-1 via decision-1"
        );
        assert_eq!(state.publication.finding_comments.len(), 1);
    }

    #[test]
    fn material_finding_comment_formatter_failure_posts_nothing_and_records_attempt() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        let finding = finding_from("reviewer-codex", "codex", "finding-1");
        let decision = material_decision_for(&finding, "Material.");
        let run_id = RunId("run-judge-1".to_owned());
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::failing(),
        );

        let error = core
            .post_finding_comment(&run_id, &mut state, &facts(), &finding, &decision)
            .expect_err("formatter failure is returned");

        assert!(
            matches!(error, CoreError::CommentFormatting(message) if message.contains("formatter fixture failed"))
        );
        assert!(core.forge_operations.comments.is_empty());
        assert_eq!(state.publication.attempts.len(), 1);
        assert_eq!(
            state.publication.attempts[0].status,
            PublicationAttemptStatus::Failed
        );
        assert!(
            state.publication.attempts[0]
                .error
                .as_deref()
                .is_some_and(|error| error.contains("formatter fixture failed"))
        );
    }

    #[test]
    fn pr_authored_by_matches_the_event_facts_author() {
        let mut with_author = facts();
        with_author.author_login = Some("example".to_owned());
        let event = event_with_facts(with_author);
        let allowed = Criteria::PrAuthoredBy {
            any_of: vec!["example".to_owned(), "BFJ-Concerns".to_owned()],
        };
        let someone_else = Criteria::PrAuthoredBy {
            any_of: vec!["mallory".to_owned()],
        };

        assert!(allowed.matches(&event, None));
        assert!(!someone_else.matches(&event, None));
    }

    #[test]
    fn pr_authored_by_fails_closed_when_the_forge_reports_no_author() {
        let event = event_with_facts(facts());
        let criteria = Criteria::PrAuthoredBy {
            any_of: vec!["example".to_owned()],
        };

        assert!(!criteria.matches(&event, None));
    }

    #[test]
    fn pr_authored_by_reads_state_facts_for_non_forge_events() {
        let mut with_author = facts();
        with_author.author_login = Some("example".to_owned());
        let state = initial_state_from_event(&event_with_facts(with_author)).expect("state");
        let completed =
            run_completed_event("fix-completed", RunId("run-fix".to_owned()), RunKind::Fix);
        let criteria = Criteria::PrAuthoredBy {
            any_of: vec!["example".to_owned()],
        };

        assert!(criteria.matches(&completed, Some(&state)));
        assert!(!criteria.matches(&completed, None));
    }

    #[test]
    fn verified_independent_fresh_set_launches() {
        let (outcomes, launched) = run(
            independent_rule(),
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        );

        assert_eq!(launched, 1);
        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review"
        ));
    }

    #[test]
    fn workspace_is_cleaned_after_successful_launch() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));
        assert_eq!(core.workspace_provider.cleaned, 1);
    }

    #[test]
    fn duplicate_forge_event_dispatches_nothing_twice() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );

        let first = core
            .process_event(&event(), &[independent_rule()])
            .expect("first event");
        let second = core
            .process_event(&event(), &[independent_rule()])
            .expect("duplicate event");

        assert!(matches!(
            first.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));
        assert_eq!(core.launcher.launched, 1);
        assert_eq!(
            second,
            vec![DispatchOutcome::Skipped {
                rule_id: "review".to_owned(),
                reason: SkipReason::DuplicateDispatch,
            }]
        );
    }

    #[test]
    fn opened_and_updated_for_same_unseen_head_dispatch_one_review() {
        let rule = review_on_open_or_update_rule();
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );
        let updated_same_head = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "forgejo-poll-updated-same-head".to_owned(),
            payload: EventPayload::PullRequestUpdated { facts: facts() },
            extensions: BTreeMap::new(),
        };

        let opened = core
            .process_event(&event(), std::slice::from_ref(&rule))
            .expect("opened event");
        let updated = core
            .process_event(&updated_same_head, &[rule])
            .expect("updated event for same head");

        assert!(matches!(
            opened.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review-on-pr-change"
        ));
        assert_eq!(
            updated,
            vec![DispatchOutcome::Skipped {
                rule_id: "review-on-pr-change".to_owned(),
                reason: SkipReason::DuplicateDispatch,
            }]
        );
        assert_eq!(core.launcher.launched, 1);
    }

    #[test]
    fn review_fires_on_pr_open_or_update() {
        let rule = review_on_open_or_update_rule();
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );
        let updated = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "human-pushed-update".to_owned(),
            payload: EventPayload::PullRequestUpdated {
                facts: facts_with_head("human-push-sha"),
            },
            extensions: BTreeMap::new(),
        };

        let opened = core
            .process_event(&event(), std::slice::from_ref(&rule))
            .expect("opened event");
        let update = core
            .process_event(&updated, &[rule])
            .expect("updated event");

        assert!(matches!(
            opened.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review-on-pr-change"
        ));
        assert!(matches!(
            update.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review-on-pr-change"
        ));
        assert_eq!(core.launcher.launched, 2);
    }

    #[test]
    fn moved_head_supersedes_running_state_and_stale_completion_posts_nothing() {
        let old_run_id = RunId("event-1:review:1".to_owned());
        let mut old_state = initial_state_from_event(&event()).expect("initial state");
        old_state = mark_running(
            old_state,
            &event(),
            &independent_rule(),
            &old_run_id,
            Vec::new(),
        );
        let mut store = FakeRunStateStore::default();
        store.save(&old_state).expect("save old running state");
        let mut core = Core::with_forge_operations(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            store,
            RecordingForgeOperations::default(),
        );
        let moved = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "event-head-moved".to_owned(),
            payload: EventPayload::PullRequestUpdated {
                facts: facts_with_head("new-head-sha"),
            },
            extensions: BTreeMap::new(),
        };

        let moved_outcome = core
            .process_event(&moved, &[update_review_rule()])
            .expect("moved head event");
        let stale_completion = core
            .process_event(
                &run_completed_event("old-review-completed", old_run_id, RunKind::Review),
                &[judge_after_review_rule()],
            )
            .expect("stale completion");

        assert!(matches!(
            moved_outcome.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));
        let old_saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load old")
            .expect("old state");
        assert_eq!(old_saved.status, RunStatus::Superseded);
        assert_eq!(old_saved.superseded_by, Some("new-head-sha".to_owned()));
        assert_eq!(
            stale_completion,
            vec![DispatchOutcome::Skipped {
                rule_id: "judge-after-review".to_owned(),
                reason: SkipReason::SupersededHead,
            }]
        );
        assert!(core.forge_operations.comments.is_empty());
    }

    #[test]
    fn launch_failure_persists_failed_state_and_operator_log() {
        let mut launcher = FakeRunLauncher::new(vec![
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
        ]);
        launcher.fail_launch = true;
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            operator_log,
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("launcher failure is returned");

        assert!(matches!(error, CoreError::Launcher(message) if message == "launch failed"));
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("failed state");
        assert_eq!(saved.status, RunStatus::Failed);
        assert!(core.forge_operations.comments.is_empty());
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
        let event = &operator_events.borrow()[0];
        assert_eq!(event.kind, OperatorLogEventKind::RunFailure);
        assert_eq!(event.run_kind, RunKind::Review);
        assert_eq!(event.pass_index, 1);
        assert_eq!(event.message, "run launcher failed: launch failed");
    }

    #[test]
    fn failed_completion_triggered_run_rearms_from_durable_completion() {
        let mut review_core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );
        review_core
            .process_event(&event(), &[independent_rule()])
            .expect("review dispatch");

        let mut judge_launcher = FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]);
        judge_launcher.fail_launches_remaining = 1;
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            judge_launcher,
            review_core.state_store,
        );

        let first_recovery = core
            .rederive_pending_completions()
            .expect("rederive review completion");
        assert_eq!(first_recovery.queued, 1);
        let first_error = core
            .process_next(&[judge_after_review_rule()])
            .expect_err("transient judge failure is returned");
        assert!(matches!(first_error, CoreError::Launcher(message) if message == "launch failed"));

        let second_recovery = core
            .rederive_pending_completions()
            .expect("rederive failed judge trigger");
        assert_eq!(second_recovery.queued, 1);
        let retry = core
            .process_next(&[judge_after_review_rule()])
            .expect("retry dispatch")
            .expect("retried completion event");

        assert!(matches!(
            retry.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "judge-after-review"
        ));
        assert_eq!(core.launcher.launched, 2);
        let saved = core
            .state_store
            .load(&RunStateKey::from_facts(&facts()))
            .expect("load state")
            .expect("retried state");
        assert_eq!(saved.status, RunStatus::Completed);
        assert!(saved.run_history.iter().any(
            |record| record.run_kind == RunKind::Judge && record.status == RunStatus::Completed
        ));
    }

    #[test]
    fn launch_failure_does_not_try_pr_comment_when_comment_channel_fails() {
        let mut launcher = FakeRunLauncher::new(vec![
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
        ]);
        launcher.fail_launch = true;
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            FakeRunStateStore::default(),
            RecordingForgeOperations {
                fail_comments: true,
                ..Default::default()
            },
            operator_log,
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("launcher failure is returned");

        assert!(matches!(error, CoreError::Launcher(message) if message == "launch failed"));
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("failed state");
        assert_eq!(saved.status, RunStatus::Failed);
        assert!(core.forge_operations.comments.is_empty());
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
    }

    #[test]
    fn forge_operation_failure_amends_run_history_without_contradiction() {
        let review_run_id = RunId("event-1:review:1".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Completed;
        state.extensions.insert(
            EXT_RUNNING_RUN_ID.to_owned(),
            Value::String(review_run_id.0.clone()),
        );
        let finding = finding_from("reviewer-codex", "codex", "finding-1");
        state.findings.push(finding.clone());
        state
            .findings
            .push(finding_from("reviewer-claude", "claude", "finding-2"));
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let mut launcher = FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]);
        launcher.outcome = RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: vec![Decision {
                contract_version: ContractVersion::current(),
                id: "decision-material".to_owned(),
                subject: DecisionSubject::Finding {
                    finding_id: finding.id,
                },
                verdict: DecisionVerdict::Material,
                rationale: "worth another pass".to_owned(),
                provenance: verified_provenance("judge", AgentRole::Judge, "glm"),
                extensions: BTreeMap::new(),
            }],
            patches: Vec::new(),
            token_usage: None,
            ensemble_archive_path: None,
        };
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            store,
            FailingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );
        let event = run_completed_event("review-one-done", review_run_id, RunKind::Review);

        let error = core
            .process_event(&event, &[judge_after_review_rule()])
            .expect_err("forge operation failure is returned");

        assert!(
            matches!(error, CoreError::ForgeOperation(message) if message.contains("forge unavailable"))
        );
        let run_id = RunId("review-one-done:judge-after-review:1".to_owned());
        let saved = core
            .state_store
            .load_by_run_id(&run_id)
            .expect("load by run")
            .expect("failed judge state");
        let records = saved
            .run_history
            .iter()
            .filter(|record| record.run_id == run_id)
            .collect::<Vec<_>>();
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].status, RunStatus::Failed);
        assert_eq!(records[0].outcome, Some(RunOutcome::Failed));
        assert_eq!(saved.status, RunStatus::Failed);
        assert!(
            saved
                .publication
                .attempts
                .iter()
                .any(|attempt| attempt.status == PublicationAttemptStatus::Failed)
        );
    }

    #[test]
    fn known_moved_head_refuses_comment_publication() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.current_head_sha = Some("new-head-sha".to_owned());
        let core = Core::with_forge_operations(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
        );
        let run_id = RunId("run-judge-1".to_owned());

        let refused = core
            .refuse_comment_if_head_moved(
                &mut state,
                &run_id,
                PublicationOperation::PostFindingComment {
                    finding_id: FindingId("finding-1".to_owned()),
                    finding_dedup_key: "brief:correctness:path:src/lib.rs:12".to_owned(),
                },
                "comment-finding-1".to_owned(),
                "abc123".to_owned(),
            )
            .expect("head-moved guard");

        assert!(refused);
        assert_eq!(state.status, RunStatus::Superseded);
        assert_eq!(state.superseded_by.as_deref(), Some("new-head-sha"));
        assert_eq!(state.publication.attempts.len(), 1);
        let attempt = &state.publication.attempts[0];
        assert_eq!(attempt.status, PublicationAttemptStatus::Refused);
        assert_eq!(attempt.expected_head_sha.as_deref(), Some("abc123"));
        assert!(matches!(
            attempt.refusal.as_ref().map(|refusal| &refusal.reason),
            Some(PublicationRefusalReason::HeadMoved {
                expected_head_sha,
                actual_head_sha
            }) if expected_head_sha == "abc123"
                && actual_head_sha.as_deref() == Some("new-head-sha")
        ));
    }

    #[test]
    fn run_ceiling_refusal_is_skipped_in_state_and_visible_to_operator() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.pass_index = 2;
        state.ceiling = Some(RunCeiling {
            max_passes: Some(2),
            token_budget: None,
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            store,
            RecordingForgeOperations::default(),
            operator_log,
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("ceiling refusal is handled");

        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::RunCeilingReached,
            }]
        );
        assert_eq!(core.launcher.launched, 0);
        assert!(core.forge_operations.comments.is_empty());
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("state");
        assert_eq!(saved.status, RunStatus::Skipped);
        assert_eq!(saved.run_history.len(), 1);
        assert_eq!(saved.run_history[0].status, RunStatus::Skipped);
        assert_eq!(
            saved.run_history[0]
                .refusal
                .as_ref()
                .map(|refusal| &refusal.reason),
            Some(&RunRefusalReason::RunCeilingReached)
        );
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
        let event = &operator_events.borrow()[0];
        assert_eq!(event.kind, OperatorLogEventKind::LaunchRefusal);
        assert_eq!(
            event.refusal_reason,
            Some(RunRefusalReason::RunCeilingReached)
        );
        assert_eq!(event.run_kind, RunKind::Review);
        assert!(event.message.contains("RunCeilingReached"));
    }

    #[test]
    fn run_ceiling_refusal_visibility_is_not_relogged_for_same_pr() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.pass_index = 2;
        state.ceiling = Some(RunCeiling {
            max_passes: Some(2),
            token_budget: None,
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            store,
            RecordingForgeOperations::default(),
            operator_log,
        );
        let first_event = event();
        let mut second_event = event();
        second_event.id = "event-2".to_owned();

        let first_outcomes = core
            .process_event(&first_event, &[independent_rule()])
            .expect("first ceiling refusal");
        let second_outcomes = core
            .process_event(&second_event, &[independent_rule()])
            .expect("second ceiling refusal");

        assert_eq!(
            first_outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::RunCeilingReached,
            }]
        );
        assert_eq!(
            second_outcomes,
            vec![DispatchOutcome::Skipped {
                rule_id: "review".to_owned(),
                reason: SkipReason::DuplicateDispatch,
            }]
        );
        assert_eq!(core.launcher.launched, 0);
        assert!(core.forge_operations.comments.is_empty());
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("state");
        assert_eq!(saved.run_history.len(), 1);
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
    }

    #[test]
    fn required_family_unavailable_is_typed_launch_refusal() {
        let mut launcher = FakeRunLauncher::new(Vec::new());
        launcher.prepare_error = Some(CoreError::RequiredFamilyUnavailable {
            agent_id: AgentId("reviewer-codex".to_owned()),
            family: ModelFamily("codex".to_owned()),
            reason: "codex CLI unavailable".to_owned(),
        });
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            operator_log,
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("typed refusal is handled");

        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::RequiredFamilyUnavailable {
                    agent_id: AgentId("reviewer-codex".to_owned()),
                    family: ModelFamily("codex".to_owned()),
                    reason: "codex CLI unavailable".to_owned(),
                },
            }]
        );
        assert_eq!(core.launcher.launched, 0);
        assert!(core.forge_operations.comments.is_empty());
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("state");
        assert_eq!(saved.status, RunStatus::Failed);
        assert_eq!(
            saved.run_history[0]
                .refusal
                .as_ref()
                .map(|refusal| &refusal.reason),
            Some(&RunRefusalReason::RequiredFamilyUnavailable)
        );
        assert!(saved.publication.attempts.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
        let event = &operator_events.borrow()[0];
        assert_eq!(event.kind, OperatorLogEventKind::LaunchRefusal);
        assert_eq!(
            event.refusal_reason,
            Some(RunRefusalReason::RequiredFamilyUnavailable)
        );
    }

    #[test]
    fn post_launch_head_race_records_actual_superseding_head() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            SupersedingRunStateStore {
                states: Vec::new(),
                superseding_head: Some("new-head-sha".to_owned()),
            },
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Superseded {
                rule_id: "review".to_owned(),
                run_id: RunId("event-1:review:1".to_owned()),
                superseded_by: "new-head-sha".to_owned(),
            }]
        );
        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load")
            .expect("old state");
        assert_eq!(saved.status, RunStatus::Superseded);
        assert_eq!(saved.superseded_by, Some("new-head-sha".to_owned()));
        assert_eq!(saved.active_run, None);
        assert_eq!(saved.run_history.len(), 1);
        assert_eq!(saved.run_history[0].status, RunStatus::Superseded);
    }

    #[test]
    fn superseded_save_failure_still_cleans_workspace() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FailingSupersededSaveRunStateStore {
                states: Vec::new(),
                superseding_head: Some("new-head-sha".to_owned()),
            },
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("superseded save failure should be returned");

        assert!(matches!(error, CoreError::StateStore(message)
            if message == "superseded state save failed"));
        assert_eq!(core.workspace_provider.cleaned, 1);
    }

    #[test]
    fn stale_exact_key_forge_event_cannot_bypass_current_head() {
        let mut stale = initial_state_from_facts(&facts_with_head("old-head-sha"));
        stale.status = RunStatus::Completed;
        let mut current = initial_state_from_facts(&facts_with_head("new-head-sha"));
        current.pass_index = 2;
        let mut store = FakeRunStateStore::default();
        store.save(&stale).expect("save stale");
        store.save(&current).expect("save current");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            store,
        );
        let stale_event = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "stale-update".to_owned(),
            payload: EventPayload::PullRequestUpdated {
                facts: facts_with_head("old-head-sha"),
            },
            extensions: BTreeMap::new(),
        };

        let outcomes = core
            .process_event(&stale_event, &[update_review_rule()])
            .expect("process stale event");

        assert_eq!(core.launcher.launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Skipped {
                rule_id: "review".to_owned(),
                reason: SkipReason::SupersededHead,
            }]
        );
    }

    #[test]
    fn process_event_refreshes_state_between_matching_rules() {
        let mut second_rule = independent_rule();
        second_rule.id = "review-again".to_owned();
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule(), second_rule])
            .expect("process event");

        assert_eq!(core.launcher.launched, 1);
        assert!(matches!(
            outcomes.as_slice(),
            [
                DispatchOutcome::Launched { rule_id, .. },
                DispatchOutcome::Skipped {
                    rule_id: skipped_rule,
                    reason: SkipReason::DuplicateDispatch,
                },
            ] if rule_id == "review" && skipped_rule == "review-again"
        ));
    }

    #[test]
    fn workspace_is_cleaned_after_gate_refusal() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: false,
                    egress_bounded: true,
                    resource_bounded: true,
                    ephemeral: true,
                },
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Refused { .. }]
        ));
        assert_eq!(core.workspace_provider.cleaned, 1);
    }

    #[test]
    fn workspace_is_cleaned_after_launcher_failure() {
        let mut launcher = FakeRunLauncher::new(vec![
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
            LaunchProof::EstablishedFresh,
        ]);
        launcher.fail_launch = true;
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            FakeRunStateStore::default(),
        );

        let error = core
            .process_event(&event(), &[independent_rule()])
            .expect_err("launcher failure is returned");

        assert!(matches!(error, CoreError::Launcher(message) if message == "launch failed"));
        assert_eq!(core.workspace_provider.cleaned, 1);
    }

    #[test]
    fn process_event_drives_review_fix_rereview_finish_loop() {
        let rules = vec![
            review_on_open_or_update_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
            finish_on_label_rule(),
        ];
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );

        let outcomes = core
            .process_event(&event(), &rules)
            .expect("PR open launches review");
        let review_one = launched_run_id(&outcomes).expect("review launched");
        let outcomes = core
            .process_event(
                &run_completed_event("review-one-done", review_one, RunKind::Review),
                &rules,
            )
            .expect("review completion launches judge");
        let judge_one = launched_run_id(&outcomes).expect("judge launched");
        assert_eq!(core.forge_operations.comments.len(), 1);
        assert!(matches!(
            core.forge_operations.comments[0]
                .authorisation
                .evidence
                .as_slice(),
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
            .expect("fix completion publishes patches");
        assert!(outcomes.is_empty());
        assert_eq!(core.forge_operations.fix_pushes.len(), 1);
        assert!(
            core.forge_operations.fix_pushes[0].commits[0]
                .message
                .contains("fix: fixed material finding")
        );
        assert!(
            core.forge_operations.fix_pushes[0].commits[0]
                .message
                .contains("Answers findings: finding-material")
        );

        let update_event = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "fix-push-updated-pr".to_owned(),
            payload: EventPayload::PullRequestUpdated {
                facts: facts_with_head("head-after-fix"),
            },
            extensions: BTreeMap::new(),
        };
        let outcomes = core
            .process_event(&update_event, &rules)
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
                &run_completed_event("judge-two-done", judge_two.clone(), RunKind::Judge),
                &rules,
            )
            .expect("minor judge completion is processed");
        assert!(no_fix.is_empty());
        assert_eq!(
            core.forge_operations.comments.len(),
            1,
            "pass-one material finding is not reposted after convergence"
        );
        let converged = core
            .state_store
            .load_by_run_id(&judge_two)
            .expect("load judge-two state")
            .expect("judge-two state");
        assert!(
            Criteria::State {
                state: StateCriterion::HasConverged
            }
            .matches(&event(), Some(&converged))
        );

        let finish_label = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "finish-label".to_owned(),
            payload: EventPayload::LabelApplied {
                pr: pr(),
                label: facts().finish_label.expect("finish label"),
            },
            extensions: BTreeMap::new(),
        };
        let outcomes = core
            .process_event(&finish_label, &rules)
            .expect("finish label launches finish");
        let finish = launched_run_id(&outcomes).expect("finish launched");
        assert!(finish.0.contains("finish-on-label"));
        assert_eq!(core.forge_operations.merges.len(), 1);
        let finished = core
            .state_store
            .load_by_run_id(&finish)
            .expect("load finish state")
            .expect("finish state");
        assert_eq!(finished.publication.merges.len(), 1);
        assert!(
            core.forge_operations.merges[0]
                .authorisation
                .evidence
                .iter()
                .any(|evidence| matches!(
                    evidence,
                    AuthorisationEvidence::MergeGateCleanAndCurrent { .. }
                ))
        );
        assert_eq!(core.workspace_provider.cleaned, 6);
    }

    #[test]
    fn judge_launches_after_clean_review_via_recorded_run_provenance() {
        let review_run_id = RunId("review-clean".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Completed;
        // A clean review: no findings, so the only reviewer evidence is the
        // provenance recorded on the run itself.
        state.run_history.push(RunRecord {
            run_id: review_run_id.clone(),
            run_kind: RunKind::Review,
            event_id: "review-trigger".to_owned(),
            rule_id: "review-on-pr-change".to_owned(),
            pass_index: 1,
            commit_sha: state.commit_sha.clone(),
            status: RunStatus::Completed,
            outcome: Some(RunOutcome::Succeeded),
            refusal: None,
            ensemble_archive_path: None,
            provenance: vec![
                verified_provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
                verified_provenance("reviewer-claude", AgentRole::Reviewer, "claude"),
            ],
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save clean review state");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]),
            store,
        );

        let outcomes = core
            .process_event(
                &run_completed_event_with_outcome(
                    "review-clean-done",
                    review_run_id,
                    RunKind::Review,
                    RunOutcome::Succeeded,
                ),
                &[judge_after_review_rule()],
            )
            .expect("clean review completion");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "judge-after-review"
        ));
        assert_eq!(core.launcher.launched, 1);
    }

    #[test]
    fn noop_fix_completion_routes_to_judge_with_standing_findings() {
        let fix_run_id = RunId("fix-noop".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        let first = finding_from("reviewer-codex", "codex", "finding-1");
        let second = finding_from("reviewer-claude", "claude", "finding-2");
        state.status = RunStatus::Completed;
        state.findings = vec![first.clone(), second];
        state.decisions.push(Decision {
            contract_version: ContractVersion::current(),
            id: "decision-material".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: first.id,
            },
            verdict: DecisionVerdict::Material,
            rationale: "worth another pass".to_owned(),
            provenance: verified_provenance("judge-old", AgentRole::Judge, "glm"),
            extensions: BTreeMap::new(),
        });
        state.run_history.push(RunRecord {
            run_id: fix_run_id.clone(),
            run_kind: RunKind::Fix,
            event_id: "fix-trigger".to_owned(),
            rule_id: "fix-after-material-judge".to_owned(),
            pass_index: 1,
            commit_sha: state.commit_sha.clone(),
            status: RunStatus::Completed,
            outcome: Some(RunOutcome::NoOp),
            refusal: None,
            ensemble_archive_path: None,
            provenance: Vec::new(),
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save NoOp state");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![LaunchProof::EstablishedFresh]),
            store,
        );

        let outcomes = core
            .process_event(
                &run_completed_event_with_outcome(
                    "fix-noop-done",
                    fix_run_id,
                    RunKind::Fix,
                    RunOutcome::NoOp,
                ),
                &[judge_after_noop_fix_rule()],
            )
            .expect("NoOp fix completion");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "judge-after-noop-fix"
        ));
        assert_eq!(core.launcher.launched, 1);
    }

    #[test]
    fn noop_fix_outcome_archives_standing_findings_for_judge() {
        let run_id = RunId("fix-noop".to_owned());
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Running;
        state.active_run = Some(RunRecord {
            run_id,
            run_kind: RunKind::Fix,
            event_id: "judge-done".to_owned(),
            rule_id: "fix-after-material-judge".to_owned(),
            pass_index: 1,
            commit_sha: state.commit_sha.clone(),
            status: RunStatus::Running,
            outcome: None,
            refusal: None,
            ensemble_archive_path: None,
            provenance: Vec::new(),
        });
        let finding = finding_from("reviewer-codex", "codex", "finding-1");
        state.findings.push(finding.clone());
        state.decisions.push(Decision {
            contract_version: ContractVersion::current(),
            id: "decision-material".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: finding.id,
            },
            verdict: DecisionVerdict::Material,
            rationale: "worth another pass".to_owned(),
            provenance: verified_provenance("judge", AgentRole::Judge, "glm"),
            extensions: BTreeMap::new(),
        });

        apply_run_outcome(
            &mut state,
            RunKind::Fix,
            RunLaunchOutcome {
                outcome: RunOutcome::NoOp,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
                ensemble_archive_path: None,
            },
        );

        assert_eq!(state.status, RunStatus::Completed);
        assert!(state.findings.is_empty());
        assert!(state.decisions.is_empty());
        assert_eq!(state.loop_history.len(), 1);
        assert_eq!(state.loop_history[0].findings.len(), 1);
        assert_eq!(state.loop_history[0].decisions.len(), 1);
        assert_eq!(state.loop_history[0].fix_outcome, Some(RunOutcome::NoOp));
        assert_eq!(state.run_history[0].outcome, Some(RunOutcome::NoOp));
    }

    #[test]
    fn stale_running_recovery_logs_failure_and_unblocks_pr() {
        let run_id = RunId("event-1:review:1".to_owned());
        let mut running = mark_running(
            initial_state_from_event(&event()).expect("initial state"),
            &event(),
            &independent_rule(),
            &run_id,
            Vec::new(),
        );
        running.status = RunStatus::Running;
        let mut store = FakeRunStateStore::default();
        store.save(&running).expect("save running state");
        let operator_log = RecordingOperatorLog::default();
        let operator_events = Rc::clone(&operator_log.events);
        let mut core = Core::with_forge_operations_and_operator_log(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ]),
            store,
            RecordingForgeOperations::default(),
            operator_log,
        );

        let recovered = core
            .rederive_pending_completions()
            .expect("recover stale running");

        assert_eq!(recovered.queued, 1);
        assert_eq!(recovered.terminal_replays, 0);
        assert_eq!(recovered.stale_running_failures, 1);
        assert_eq!(core.pending_event_count(), 1);
        assert!(core.forge_operations.comments.is_empty());
        assert_eq!(operator_events.borrow().len(), 1);
        assert!(
            operator_events.borrow()[0]
                .message
                .contains("daemon restarted")
        );
        let failed = core
            .state_store
            .load_by_run_id(&run_id)
            .expect("load failed run")
            .expect("failed state");
        assert_eq!(failed.status, RunStatus::Failed);
        assert_eq!(failed.run_history[0].status, RunStatus::Failed);
        assert_eq!(failed.run_history[0].outcome, Some(RunOutcome::Failed));

        let updated = ContractEvent {
            contract_version: ContractVersion::current(),
            id: "retry-after-restart".to_owned(),
            payload: EventPayload::PullRequestUpdated { facts: facts() },
            extensions: BTreeMap::new(),
        };
        let outcomes = core
            .process_event(&updated, &[update_review_rule()])
            .expect("updated event after recovery");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review"
        ));
    }

    #[test]
    fn convergence_applies_finish_label_and_reaches_merge_when_policy_grants_authority() {
        let rules = vec![
            independent_rule(),
            update_review_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
            finish_on_label_rule(),
        ];
        let mut core = Core::with_forge_operations_source_preparer_comment_formatter_and_policy(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            NoopSourcePreparer,
            RecordingCommentFormatter::default(),
            NoopOperatorLog,
            CorePolicy {
                finish_label_application: FinishLabelApplicationPolicy::CoreOnConvergence {
                    label: "pump19-finish".to_owned(),
                },
            },
        );

        let opened_facts = core_authority_facts("abc123");
        assert!(
            !opened_facts
                .actor_permissions
                .iter()
                .any(|permission| permission.actor == core_actor()),
            "core authority must derive from local policy, not forge-supplied sentinel permissions"
        );
        let outcomes = core
            .process_event(&event_with_facts(opened_facts), &rules)
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
            .expect("material judge completion launches fix");
        let fix_one = launched_run_id(&outcomes).expect("fix launched");
        core.process_event(
            &run_completed_event("fix-one-done", fix_one, RunKind::Fix),
            &rules,
        )
        .expect("fix completion publishes patches");

        let outcomes = core
            .process_event(
                &ContractEvent {
                    contract_version: ContractVersion::current(),
                    id: "fix-push-updated-pr".to_owned(),
                    payload: EventPayload::PullRequestUpdated {
                        facts: core_authority_facts("head-after-fix"),
                    },
                    extensions: BTreeMap::new(),
                },
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

        core.process_event(
            &run_completed_event("judge-two-done", judge_two, RunKind::Judge),
            &rules,
        )
        .expect("converged judge applies finish label");
        let drained = core
            .drain_available(&rules)
            .expect("queued label event launches finish");

        assert_eq!(core.forge_operations.labels.len(), 1);
        assert_eq!(core.forge_operations.labels[0].label, "pump19-finish");
        assert_eq!(
            core.forge_operations.labels[0].authorisation.actor,
            core_actor()
        );
        assert_eq!(core.forge_operations.merges.len(), 1);
        assert_eq!(
            core.forge_operations.merges[0].authorisation.actor,
            core_actor()
        );
        assert!(
            drained
                .iter()
                .flatten()
                .any(|outcome| matches!(outcome, DispatchOutcome::Launched { rule_id, .. } if rule_id == "finish-on-label"))
        );
    }

    #[test]
    fn convergence_does_not_apply_finish_label_without_policy_grant() {
        let rules = vec![
            independent_rule(),
            update_review_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
            finish_on_label_rule(),
        ];
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );

        let outcomes = core
            .process_event(&event_with_facts(core_authority_facts("abc123")), &rules)
            .expect("PR open launches review");
        let review = launched_run_id(&outcomes).expect("review launched");
        let outcomes = core
            .process_event(
                &run_completed_event("review-one-done", review, RunKind::Review),
                &rules,
            )
            .expect("review completion launches judge");
        let judge_one = launched_run_id(&outcomes).expect("judge launched");
        let outcomes = core
            .process_event(
                &run_completed_event("judge-one-done", judge_one, RunKind::Judge),
                &rules,
            )
            .expect("material judge completion launches fix");
        let fix_one = launched_run_id(&outcomes).expect("fix launched");
        core.process_event(
            &run_completed_event("fix-one-done", fix_one, RunKind::Fix),
            &rules,
        )
        .expect("fix completion publishes patches");
        let outcomes = core
            .process_event(
                &ContractEvent {
                    contract_version: ContractVersion::current(),
                    id: "fix-push-updated-pr".to_owned(),
                    payload: EventPayload::PullRequestUpdated {
                        facts: core_authority_facts("head-after-fix"),
                    },
                    extensions: BTreeMap::new(),
                },
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
        core.process_event(
            &run_completed_event("judge-two-done", judge_two.clone(), RunKind::Judge),
            &rules,
        )
        .expect("converged judge does not apply finish label");

        assert!(core.forge_operations.labels.is_empty());
        assert!(core.forge_operations.merges.is_empty());

        let spoofed_facts = facts_with_spurious_core_actor_permissions("head-after-fix");
        let spoofed_label = spoofed_facts.finish_label.clone().expect("finish label");
        let mut spoofed_state = core
            .state_store
            .load_by_run_id(&judge_two)
            .expect("load converged state")
            .expect("converged state");
        spoofed_state.extensions.insert(
            EXT_FORGE_FACTS.to_owned(),
            serde_json::to_value(&spoofed_facts).expect("serialise spoofed facts"),
        );
        core.state_store
            .save(&spoofed_state)
            .expect("save spoofed facts");

        let error = core
            .process_event(
                &ContractEvent {
                    contract_version: ContractVersion::current(),
                    id: "spurious-core-finish-label".to_owned(),
                    payload: EventPayload::LabelApplied {
                        pr: pr(),
                        label: spoofed_label,
                    },
                    extensions: BTreeMap::new(),
                },
                &[finish_on_label_rule()],
            )
            .expect_err("forge-supplied core actor is not trusted without policy grant");

        assert!(matches!(
            error,
            CoreError::ForgeOperation(message)
                if message == "finish label actor lacks merge capability"
        ));
        assert!(core.forge_operations.merges.is_empty());
    }

    #[test]
    fn core_does_not_apply_finish_label_when_facts_are_stale() {
        let facts = ForgeFacts {
            branch_currency: BranchCurrency::Stale,
            ..core_authority_facts("abc123")
        };

        assert!(!core_may_apply_finish_label(&facts, "pump19-finish"));
    }

    #[test]
    fn drain_available_self_emits_run_completions_without_external_echo() {
        let rules = vec![
            independent_rule(),
            review_on_fix_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
        ];
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::from_events(vec![event()]),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );

        let batches = core.drain_available(&rules).expect("drain event queue");

        assert_eq!(
            core.workspace_provider.cleaned, 5,
            "review, judge, fix, re-review, and final judge all launch from one ingress event"
        );
        assert_eq!(
            core.forge_operations.comments.len(),
            1,
            "material findings are still posted exactly once"
        );
        assert_eq!(batches.len(), 6);
        assert!(
            batches
                .iter()
                .filter(|batch| matches!(batch.as_slice(), [DispatchOutcome::Launched { .. }]))
                .count()
                == 5
        );
        let converged = core
            .state_store
            .load_latest_for_pr(&pr())
            .expect("load latest")
            .expect("converged state");
        assert!(
            Criteria::State {
                state: StateCriterion::HasConverged
            }
            .matches(&event(), Some(&converged))
        );

        let duplicate = core
            .process_event(
                &run_completed_event(
                    stable_id("run-completed", ["event-1:review:1"]).as_str(),
                    RunId("event-1:review:1".to_owned()),
                    RunKind::Review,
                ),
                &rules,
            )
            .expect("duplicate completion replay");
        assert_eq!(
            duplicate,
            vec![DispatchOutcome::Skipped {
                rule_id: "judge-after-review".to_owned(),
                reason: SkipReason::DuplicateDispatch,
            }]
        );
    }

    #[test]
    fn rederived_completions_resume_loop_after_core_restart() {
        let rules = vec![
            independent_rule(),
            review_on_fix_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
        ];
        let mut core = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::from_events(vec![event()]),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );

        let opened = core
            .process_next(&rules)
            .expect("process opened event")
            .expect("opened event available");
        assert!(matches!(
            opened.as_slice(),
            [DispatchOutcome::Launched {
                rule_id,
                run_id: _
            }] if rule_id == "review"
        ));
        assert_eq!(
            core.pending_event_count(),
            1,
            "the first completion is deliberately still only queued in memory"
        );
        let persisted_store = core.state_store.clone();
        drop(core);

        let mut restarted = Core::with_forge_operations_and_comment_formatter(
            FakeEventSource::from_events(Vec::new()),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            persisted_store,
            RecordingForgeOperations::default(),
            RecordingCommentFormatter::default(),
        );
        let recovered = restarted
            .rederive_pending_completions()
            .expect("rederive completions from stored run history");
        assert_eq!(recovered.queued, 1);
        assert_eq!(recovered.terminal_replays, 1);
        assert_eq!(recovered.stale_running_failures, 0);

        let batches = restarted.drain_available(&rules).expect("resume loop");

        assert_eq!(
            restarted.workspace_provider.cleaned, 4,
            "judge, fix, re-review, and final judge resume after restart"
        );
        assert_eq!(
            restarted.forge_operations.comments.len(),
            1,
            "the material finding still surfaces once after restart"
        );
        assert_eq!(batches.len(), 5);
        let converged = restarted
            .state_store
            .load_latest_for_pr(&pr())
            .expect("load latest")
            .expect("converged state");
        assert!(
            Criteria::State {
                state: StateCriterion::HasConverged
            }
            .matches(&event(), Some(&converged))
        );
    }

    #[test]
    fn single_reviewer_family_is_refused_before_launch() {
        let mut rule = independent_rule();
        rule.agent_plan.reviewers[1] = target("reviewer-two", AgentRole::Reviewer, "codex");
        let (outcomes, launched) = run(rule, Vec::new());

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::InsufficientReviewerFamilies,
            }]
        );
    }

    #[test]
    fn reviewer_fixer_overlap_is_refused_before_launch() {
        let mut rule = independent_rule();
        rule.agent_plan.fixers = vec![target("reviewer-codex", AgentRole::Fixer, "codex")];
        let (outcomes, launched) = run(rule, Vec::new());

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::ReviewerFixerOverlap,
            }]
        );
    }

    #[test]
    fn judge_sharing_reviewer_family_is_refused_before_launch() {
        let mut rule = independent_rule();
        rule.agent_plan.judge = Some(target("judge", AgentRole::Judge, "codex"));
        let (outcomes, launched) = run(rule, Vec::new());

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::MissingIndependentJudge,
            }]
        );
    }

    #[test]
    fn engine_model_family_mismatch_is_refused_before_launch() {
        let mut rule = independent_rule();
        rule.agent_plan.reviewers[1].engine = AgentEngine::Codex;
        let (outcomes, launched) = run(rule, Vec::new());

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::UnverifiedProvenance {
                    agent_id: AgentId("reviewer-claude".to_owned()),
                    reason:
                        "engine codex cannot map model \"claude-2026\" to a trusted model family"
                            .to_owned(),
                },
            }]
        );
    }

    #[test]
    fn reused_session_is_refused_before_launch() {
        let (outcomes, launched) = run(
            independent_rule(),
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::Reused {
                    original_session_id: SessionId("old-session".to_owned()),
                },
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        );

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::NonFreshSession {
                    agent_id: AgentId("reviewer-claude".to_owned()),
                },
            }]
        );
    }

    #[test]
    fn unverified_provenance_is_refused_before_launch() {
        let (outcomes, launched) = run(
            independent_rule(),
            vec![
                LaunchProof::EstablishedFresh,
                LaunchProof::Unverified {
                    reason: "control plane could not bind session".to_owned(),
                },
                LaunchProof::EstablishedFresh,
                LaunchProof::EstablishedFresh,
            ],
        );

        assert_eq!(launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::UnverifiedProvenance {
                    agent_id: AgentId("reviewer-claude".to_owned()),
                    reason: "control plane could not bind session".to_owned(),
                },
            }]
        );
    }

    #[test]
    fn claude_opus_alias_is_canonicalised_before_provenance_gate() {
        let mut rule = independent_rule();
        rule.agent_plan.reviewers[1].lineage.model = "opus".to_owned();
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[rule])
            .expect("process event");

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));
        let state = core
            .state_store
            .load_latest_for_pr(&pr())
            .expect("load state")
            .expect("state saved");
        let claude = state.run_history[0]
            .provenance
            .iter()
            .find(|provenance| provenance.agent_id == AgentId("reviewer-claude".to_owned()))
            .expect("claude reviewer provenance");
        assert!(matches!(
            claude.verification,
            ProvenanceVerification::Verified { .. }
        ));
        let ProvenanceVerification::Verified { lineage, .. } = &claude.verification else {
            return;
        };
        assert_eq!(lineage.model, "claude-opus-4-8");
        assert_ne!(lineage.model, "opus");
        assert_eq!(core.launcher.launched, 1);
    }

    #[test]
    fn missing_workspace_isolation_is_refused_before_launch() {
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: false,
                    egress_bounded: true,
                    resource_bounded: true,
                    ephemeral: true,
                },
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert_eq!(core.launcher.launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::WorkspaceIsolationMissing,
            }]
        );
    }

    #[test]
    fn run_ceiling_refuses_before_prepare_or_launch() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.ceiling = Some(RunCeiling {
            max_passes: Some(1),
            token_budget: None,
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            store,
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert_eq!(core.launcher.launched, 0);
        assert_eq!(
            outcomes,
            vec![DispatchOutcome::Refused {
                rule_id: "review".to_owned(),
                reason: LaunchRefusal::RunCeilingReached,
            }]
        );
    }

    #[test]
    fn running_state_is_durable_and_not_success() {
        let mut launcher = FakeRunLauncher::new(Vec::new());
        launcher.outcome = RunLaunchOutcome {
            outcome: RunOutcome::Failed,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            token_usage: Some(7),
            ensemble_archive_path: None,
        };
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            launcher,
            FakeRunStateStore::default(),
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");
        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));

        let saved = core
            .state_store
            .load(&RunStateKey {
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("load state")
            .expect("state exists");
        assert_eq!(saved.status, RunStatus::Failed);
        assert_eq!(
            saved
                .extensions
                .get(EXT_TOKENS_USED)
                .and_then(Value::as_u64),
            Some(7)
        );
    }

    #[test]
    fn json_store_round_trips_and_finds_running_state_by_run_id() {
        let dir = tempfile::tempdir().expect("tempdir");
        let mut store = JsonRunStateStore::new(dir.path()).expect("store");
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.status = RunStatus::Running;
        state.extensions.insert(
            EXT_RUNNING_RUN_ID.to_owned(),
            Value::String("event-1:review:1".to_owned()),
        );

        store.save(&state).expect("save");

        let key = RunStateKey {
            pr: pr(),
            commit_sha: "abc123".to_owned(),
        };
        assert_eq!(store.load(&key).expect("load"), Some(state.clone()));
        assert_eq!(
            store
                .load_by_run_id(&RunId("event-1:review:1".to_owned()))
                .expect("load by run"),
            Some(state)
        );
    }

    #[test]
    fn json_store_all_states_ignores_control_mirror_duplicates() {
        let dir = tempfile::tempdir().expect("tempdir");
        let mut store = JsonRunStateStore::new(dir.path()).expect("store");
        let state = initial_state_from_event(&event()).expect("initial state");

        store.save(&state).expect("save");

        assert_eq!(store.all_states().expect("all states"), vec![state]);
    }

    #[test]
    fn state_criteria_match_material_decision() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.decisions.push(Decision {
            contract_version: ContractVersion::current(),
            id: "decision-1".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: pump19_contract::FindingId("finding-1".to_owned()),
            },
            verdict: DecisionVerdict::Material,
            rationale: "worth another pass".to_owned(),
            provenance: establish_provenance(
                &target("judge", AgentRole::Judge, "glm"),
                PreparedAgent {
                    agent_id: AgentId("judge".to_owned()),
                    role: AgentRole::Judge,
                    session_id: SessionId("judge-session".to_owned()),
                    proof: LaunchProof::EstablishedFresh,
                },
                1,
            ),
            extensions: BTreeMap::new(),
        });

        assert!(
            Criteria::State {
                state: StateCriterion::HasMaterialDecision
            }
            .matches(&event(), Some(&state))
        );
    }

    #[test]
    fn state_criteria_match_recorded_convergence_verdict() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.decisions.push(Decision {
            contract_version: ContractVersion::current(),
            id: "decision-converged".to_owned(),
            subject: DecisionSubject::FindingSet {
                finding_ids: Vec::new(),
            },
            verdict: DecisionVerdict::Converged,
            rationale: "nothing material stands".to_owned(),
            provenance: establish_provenance(
                &target("judge", AgentRole::Judge, "glm"),
                PreparedAgent {
                    agent_id: AgentId("judge".to_owned()),
                    role: AgentRole::Judge,
                    session_id: SessionId("judge-session".to_owned()),
                    proof: LaunchProof::EstablishedFresh,
                },
                1,
            ),
            extensions: BTreeMap::new(),
        });

        assert!(
            Criteria::State {
                state: StateCriterion::HasConverged
            }
            .matches(&event(), Some(&state))
        );
    }

    #[test]
    fn prior_pass_provenance_does_not_make_current_sessions_stale() {
        let mut state = initial_state_from_event(&event()).expect("initial state");
        state.pass_index = 2;
        state.patches.push(Patch {
            contract_version: ContractVersion::current(),
            id: pump19_contract::PatchId("patch-1".to_owned()),
            run_id: RunId("fix-pass-1".to_owned()),
            commit_sha: "abc123".to_owned(),
            idempotency_key: "fix-pass-1:abc123".to_owned(),
            answers_findings: vec![pump19_contract::FindingId("finding-1".to_owned())],
            change: pump19_contract::PatchChange::Description {
                summary: "previous pass fix".to_owned(),
            },
            provenance: establish_provenance(
                &target("fixer-pass-1", AgentRole::Fixer, "codex"),
                PreparedAgent {
                    agent_id: AgentId("fixer-pass-1".to_owned()),
                    role: AgentRole::Fixer,
                    session_id: SessionId("fixer-pass-1-session".to_owned()),
                    proof: LaunchProof::EstablishedFresh,
                },
                1,
            ),
            extensions: BTreeMap::new(),
        });
        let mut store = FakeRunStateStore::default();
        store.save(&state).expect("save state");
        let mut core = Core::new(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            FakeRunLauncher::new(Vec::new()),
            store,
        );

        let outcomes = core
            .process_event(&event(), &[independent_rule()])
            .expect("process event");

        assert_eq!(core.launcher.launched, 1);
        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { .. }]
        ));
    }
}
