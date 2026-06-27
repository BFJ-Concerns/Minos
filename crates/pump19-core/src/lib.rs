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

use std::{collections::BTreeMap, fs, path::PathBuf};

use pump19_contract::{
    ActorCapability, ActorRef, AgentId, AgentRole, ContractEvent, ContractVersion, Decision,
    DecisionVerdict, EventPayload, Extensions, Finding, FinishLabel, ForgeFacts, ModelLineage,
    ModelProvenance, Patch, PrRunState, ProvenanceVerification, PullRequestRef, RunCeiling, RunId,
    RunKind, RunOutcome, RunStatus, SessionFreshness, SessionId,
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

/// Core errors raised before a launch decision can be made.
#[derive(Debug, Error)]
pub enum CoreError {
    #[error("event source failed: {0}")]
    EventSource(String),
    #[error("workspace provider failed: {0}")]
    Workspace(String),
    #[error("run launcher failed: {0}")]
    Launcher(String),
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
        })
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-label".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
        })
    }

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        Ok(ForgeOperationReceipt {
            operation_id: "noop-merge".to_owned(),
            idempotency_key: request.authorisation.idempotency_key,
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
    pub body: String,
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

/// Merge method requested from a forge adapter.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum MergeMethod {
    Merge,
    Squash,
    Rebase,
}

/// Receipt returned by an outbound forge operation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgeOperationReceipt {
    pub operation_id: String,
    pub idempotency_key: String,
}

/// Errors raised by the outbound operation seam.
#[derive(Debug, Error)]
pub enum ForgeOperationError {
    #[error("authorised forge operation has invalid shape: {0}")]
    InvalidRequest(&'static str),
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

    /// Saves state durably enough that a crash cannot be mistaken for success.
    ///
    /// # Errors
    ///
    /// Returns an error when the store cannot encode or persist the state.
    fn save(&mut self, state: &PrRunState) -> Result<(), CoreError>;
}

/// Deterministic dispatch-and-enforce core.
#[derive(Debug)]
pub struct Core<E, W, L, S, F = NoopForgeOperations> {
    event_source: E,
    workspace_provider: W,
    launcher: L,
    state_store: S,
    forge_operations: F,
}

impl<E, W, L, S> Core<E, W, L, S, NoopForgeOperations>
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
        Self {
            event_source,
            workspace_provider,
            launcher,
            state_store,
            forge_operations,
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
        let Some(event) = self.event_source.next_event()? else {
            return Ok(None);
        };
        self.process_event(&event, rules).map(Some)
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
        let state = self.load_state_for_event(event)?;
        let mut outcomes = Vec::new();
        for rule in rules {
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

    fn load_state_for_event(&self, event: &ContractEvent) -> Result<Option<PrRunState>, CoreError> {
        match &event.payload {
            EventPayload::PullRequestOpened { facts }
            | EventPayload::PullRequestUpdated { facts } => {
                self.state_store.load(&RunStateKey::from_facts(facts))
            }
            EventPayload::LabelApplied { pr, .. } => self.state_store.load_latest_for_pr(pr),
            EventPayload::RunCompleted { run_id, .. } => self.state_store.load_by_run_id(run_id),
        }
    }

    fn dispatch_rule(
        &mut self,
        event: &ContractEvent,
        rule: &TriggerRule,
        state: PrRunState,
    ) -> Result<DispatchOutcome, CoreError> {
        if ceiling_refuses(&state) {
            return Ok(DispatchOutcome::Refused {
                rule_id: rule.id.clone(),
                reason: LaunchRefusal::RunCeilingReached,
            });
        }

        let run_id = run_id_for(event, rule, state.pass_index);
        let prepared = self.prepare_provenance(&rule.agent_plan, state.pass_index)?;
        let workspace = self.workspace_provider.prepare(WorkspaceRequest {
            run_id: run_id.clone(),
            run_kind: rule.run_kind,
            pr: state.pr.clone(),
            commit_sha: state.commit_sha.clone(),
        })?;
        let mut gate_provenance = collect_state_provenance(&state);
        gate_provenance.extend(prepared.iter().cloned());

        if let Some(reason) =
            evaluate_gate(&gate_provenance, &prepared, state.pass_index, &workspace)
        {
            self.workspace_provider.cleanup(&workspace)?;
            return Ok(DispatchOutcome::Refused {
                rule_id: rule.id.clone(),
                reason,
            });
        }

        let mut running_state = mark_running(state, event, rule, &run_id);
        if let Err(error) = self.state_store.save(&running_state) {
            self.workspace_provider.cleanup(&workspace)?;
            return Err(error);
        }

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
                self.workspace_provider.cleanup(&workspace)?;
                return Err(error);
            }
        };

        apply_run_outcome(&mut running_state, rule.run_kind, outcome);
        if let Err(error) =
            self.apply_authorised_forge_operations(rule.run_kind, &run_id, &running_state)
        {
            self.workspace_provider.cleanup(&workspace)?;
            return Err(error);
        }
        let save_result = self.state_store.save(&running_state);
        let cleanup_result = self.workspace_provider.cleanup(&workspace);
        save_result?;
        cleanup_result?;

        Ok(DispatchOutcome::Launched {
            rule_id: rule.id.clone(),
            run_id,
        })
    }

    fn apply_authorised_forge_operations(
        &mut self,
        run_kind: RunKind,
        run_id: &RunId,
        state: &PrRunState,
    ) -> Result<(), CoreError> {
        match run_kind {
            RunKind::Judge => self.post_material_findings(run_id, state),
            RunKind::Finish if state.status == RunStatus::Completed => {
                self.merge_finished_pr(run_id, state)
            }
            RunKind::Review | RunKind::Fix | RunKind::Finish => Ok(()),
        }
    }

    fn post_material_findings(
        &mut self,
        run_id: &RunId,
        state: &PrRunState,
    ) -> Result<(), CoreError> {
        let Some(facts) = forge_facts_from_state(state)? else {
            return Ok(());
        };
        for decision in state
            .decisions
            .iter()
            .filter(|decision| decision.verdict == DecisionVerdict::Material)
            .filter(|decision| provenance_pass(&decision.provenance) == Some(state.pass_index))
        {
            let finding_ids = decision_finding_ids(decision);
            for finding_id in finding_ids {
                let Some(finding) = state
                    .findings
                    .iter()
                    .find(|finding| &finding.id == finding_id)
                else {
                    continue;
                };
                let authorisation = AuthorisationContext {
                    pr: state.pr.clone(),
                    observed_head_sha: facts.head.sha.clone(),
                    idempotency_key: stable_id(
                        "forge-comment",
                        [
                            run_id.0.as_str(),
                            decision.id.as_str(),
                            finding.id.0.as_str(),
                        ],
                    ),
                    actor: core_actor(),
                    reason: "core authorised material finding comment from judge decision"
                        .to_owned(),
                    evidence: vec![
                        AuthorisationEvidence::Decision {
                            decision_id: decision.id.clone(),
                            verdict: decision.verdict,
                        },
                        AuthorisationEvidence::Finding {
                            finding_id: finding.id.clone(),
                        },
                    ],
                };
                self.forge_operations
                    .post_comment(AuthorisedComment {
                        authorisation,
                        body: material_finding_comment(finding, decision),
                    })
                    .map_err(|error| CoreError::ForgeOperation(error.to_string()))?;
            }
        }
        Ok(())
    }

    fn merge_finished_pr(&mut self, run_id: &RunId, state: &PrRunState) -> Result<(), CoreError> {
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
        if !actor_has_capability(&facts, &label.applied_by, ActorCapability::Merge) {
            return Err(CoreError::ForgeOperation(
                "finish label actor lacks merge capability".to_owned(),
            ));
        }
        let authorisation = AuthorisationContext {
            pr: state.pr.clone(),
            observed_head_sha: facts.head.sha.clone(),
            idempotency_key: stable_id("forge-merge", [run_id.0.as_str(), facts.head.sha.as_str()]),
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
                    facts_head_sha: facts.head.sha,
                },
            ],
        };
        self.forge_operations
            .merge(AuthorisedMerge {
                authorisation,
                method: MergeMethod::Squash,
            })
            .map_err(|error| CoreError::ForgeOperation(error.to_string()))?;
        Ok(())
    }

    fn prepare_provenance(
        &mut self,
        plan: &AgentPlan,
        pass_index: u32,
    ) -> Result<Vec<ModelProvenance>, CoreError> {
        let mut provenances = Vec::new();
        for target in plan.targets() {
            let spec = AgentLaunchSpec {
                target: target.clone(),
                pass_index,
            };
            let prepared = self.launcher.prepare_agent(spec)?;
            provenances.push(establish_provenance(&target, prepared, pass_index));
        }
        Ok(provenances)
    }
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
            states.push(
                serde_json::from_slice(&bytes).map_err(|source| CoreError::Json {
                    path: path.display().to_string(),
                    source,
                })?,
            );
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
        Ok(self
            .all_states()?
            .into_iter()
            .filter(|state| state.pr == *pr)
            .max_by_key(|state| state.pass_index))
    }

    fn load_by_run_id(&self, run_id: &RunId) -> Result<Option<PrRunState>, CoreError> {
        Ok(self.all_states()?.into_iter().find(|state| {
            state
                .extensions
                .get(EXT_RUNNING_RUN_ID)
                .and_then(Value::as_str)
                == Some(run_id.0.as_str())
        }))
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
        fs::write(&tmp_path, bytes).map_err(|source| CoreError::Io {
            path: tmp_path.display().to_string(),
            source,
        })?;
        fs::rename(&tmp_path, &path).map_err(|source| CoreError::Io {
            path: path.display().to_string(),
            source,
        })?;
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
    Event { event: EventKind },
    State { state: StateCriterion },
    All { criteria: Vec<Self> },
    Any { criteria: Vec<Self> },
}

impl Criteria {
    #[must_use]
    pub fn matches(&self, event: &ContractEvent, state: Option<&PrRunState>) -> bool {
        match self {
            Self::Event { event: expected } => expected.matches(event),
            Self::State { state: expected } => expected.matches(state),
            Self::All { criteria } => criteria
                .iter()
                .all(|criterion| criterion.matches(event, state)),
            Self::Any { criteria } => criteria
                .iter()
                .any(|criterion| criterion.matches(event, state)),
        }
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
    pub vendor: String,
    pub control_plane: String,
    pub lineage: ModelLineage,
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
}

/// Observable result of evaluating one matching trigger rule.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DispatchOutcome {
    Launched {
        rule_id: String,
        run_id: RunId,
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
    WorkspaceIsolationMissing,
    UnverifiedProvenance { agent_id: AgentId, reason: String },
    InsufficientReviewerFamilies,
    ReviewerFixerOverlap,
    MissingIndependentJudge,
    NonFreshSession { agent_id: AgentId },
}

/// Reasons a trigger match did not have enough contract context to launch.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum SkipReason {
    NoRunState,
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

    ModelProvenance {
        contract_version: ContractVersion::current(),
        agent_id: target.agent_id.clone(),
        role: target.role,
        session_id: prepared.session_id,
        freshness,
        verification,
        extensions: BTreeMap::new(),
    }
}

#[must_use]
fn verified_from_target(target: &AgentLaunchTarget) -> ProvenanceVerification {
    ProvenanceVerification::Verified {
        vendor: target.vendor.clone(),
        control_plane: target.control_plane.clone(),
        lineage: target.lineage.clone(),
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
    let mut extensions = Extensions::new();
    if let Ok(value) = serde_json::to_value(facts) {
        extensions.insert(EXT_FORGE_FACTS.to_owned(), value);
    }
    Some(PrRunState {
        contract_version: ContractVersion::current(),
        pr: facts.pr.clone(),
        commit_sha: facts.head.sha.clone(),
        pass_index: 1,
        status: RunStatus::Pending,
        findings: Vec::new(),
        decisions: Vec::new(),
        patches: Vec::new(),
        ceiling: None,
        extensions,
    })
}

#[must_use]
fn collect_state_provenance(state: &PrRunState) -> Vec<ModelProvenance> {
    let mut provenances =
        Vec::with_capacity(state.findings.len() + state.decisions.len() + state.patches.len());
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

fn material_finding_comment(finding: &Finding, decision: &Decision) -> String {
    format!(
        "Pump-19 material finding: {}\n\nDecision {}: {}",
        finding.summary, decision.id, decision.rationale
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

#[must_use]
fn mark_running(
    mut state: PrRunState,
    event: &ContractEvent,
    rule: &TriggerRule,
    run_id: &RunId,
) -> PrRunState {
    state.status = RunStatus::Running;
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

fn apply_run_outcome(state: &mut PrRunState, run_kind: RunKind, outcome: RunLaunchOutcome) {
    state.status = match outcome.outcome {
        RunOutcome::Succeeded => RunStatus::Completed,
        RunOutcome::Failed | RunOutcome::Cancelled => RunStatus::Failed,
    };
    state.findings.extend(outcome.findings);
    state.decisions.extend(outcome.decisions);
    state.patches.extend(outcome.patches);
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

#[cfg(test)]
mod tests {
    use std::{collections::VecDeque, path::PathBuf};

    use pump19_contract::{
        ActorCapability, ActorPermissions, ActorRef, BranchCurrency, DecisionSubject, FinishLabel,
        Mergeability, ModelFamily, ReviewCleanliness, Revision,
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

    #[derive(Debug)]
    struct FakeRunLauncher {
        proofs: VecDeque<LaunchProof>,
        launched: usize,
        outcome: RunLaunchOutcome,
        fail_launch: bool,
    }

    #[derive(Debug, Default)]
    struct RecordingForgeOperations {
        comments: Vec<AuthorisedComment>,
        labels: Vec<AuthorisedLabel>,
        merges: Vec<AuthorisedMerge>,
    }

    impl ForgeOperations for RecordingForgeOperations {
        fn post_comment(
            &mut self,
            request: AuthorisedComment,
        ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
            let idempotency_key = request.authorisation.idempotency_key.clone();
            self.comments.push(request);
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
            self.labels.push(request);
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
            self.merges.push(request);
            Ok(ForgeOperationReceipt {
                operation_id: "merge".to_owned(),
                idempotency_key,
            })
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
                    let primary_reviewer = reviewers.first().expect("primary reviewer").clone();
                    let secondary_reviewer = reviewers.get(1).expect("secondary reviewer").clone();
                    let primary_finding_id = if request.state.pass_index == 1 {
                        "finding-supporting"
                    } else {
                        "finding-minor-supporting"
                    };
                    let judged_finding_id = if request.state.pass_index == 1 {
                        "finding-material"
                    } else {
                        "finding-minor"
                    };
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
                    })
                }
                RunKind::Judge => {
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
                    Ok(RunLaunchOutcome {
                        outcome: RunOutcome::Succeeded,
                        findings: Vec::new(),
                        decisions: vec![Decision {
                            contract_version: ContractVersion::current(),
                            id: format!("decision-pass-{}", request.state.pass_index),
                            subject: DecisionSubject::Finding {
                                finding_id: finding.id.clone(),
                            },
                            verdict,
                            rationale: "deterministic loop verdict".to_owned(),
                            provenance: judge,
                            extensions: BTreeMap::new(),
                        }],
                        patches: Vec::new(),
                        token_usage: None,
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
                    })
                }
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
                },
                fail_launch: false,
            }
        }
    }

    impl RunLauncher for FakeRunLauncher {
        fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
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
                .find(|state| {
                    state
                        .extensions
                        .get(EXT_RUNNING_RUN_ID)
                        .and_then(Value::as_str)
                        == Some(run_id.0.as_str())
                })
                .cloned())
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

    fn pr() -> PullRequestRef {
        PullRequestRef {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
        }
    }

    fn facts() -> ForgeFacts {
        ForgeFacts {
            contract_version: ContractVersion::current(),
            pr: pr(),
            head: Revision {
                sha: "abc123".to_owned(),
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
            extensions: BTreeMap::new(),
        }
    }

    fn event() -> ContractEvent {
        ContractEvent {
            contract_version: ContractVersion::current(),
            id: "event-1".to_owned(),
            payload: EventPayload::PullRequestOpened { facts: facts() },
            extensions: BTreeMap::new(),
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
                model: format!("{family}-2026"),
            },
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
                judge: Some(target("judge", AgentRole::Judge, "gemini")),
                finishers: Vec::new(),
            },
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
                judge: Some(target("judge", AgentRole::Judge, "gemini")),
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
        ContractEvent {
            contract_version: ContractVersion::current(),
            id: id.to_owned(),
            payload: EventPayload::RunCompleted {
                run_id,
                run_kind: Some(run_kind),
                outcome: RunOutcome::Succeeded,
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
                judge: Some(target("judge", AgentRole::Judge, "gemini")),
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
            independent_rule(),
            review_on_fix_rule(),
            judge_after_review_rule(),
            fix_after_material_judge_rule(),
            finish_on_label_rule(),
        ];
        let mut core = Core::with_forge_operations(
            FakeEventSource::empty(),
            FakeWorkspaceProvider {
                isolation: isolated_workspace(),
                cleaned: 0,
            },
            LoopLauncher,
            FakeRunStateStore::default(),
            RecordingForgeOperations::default(),
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
        assert_eq!(
            core.forge_operations.comments.len(),
            1,
            "pass-one material finding is not reposted after minor convergence"
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
                &target("judge", AgentRole::Judge, "gemini"),
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
