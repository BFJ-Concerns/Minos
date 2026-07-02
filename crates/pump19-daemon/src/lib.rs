#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::too_many_lines,
        clippy::unwrap_used,
        reason = "daemon tests use local fixtures and direct state assertions"
    )
)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    thread,
    time::Duration,
};

use pump19_adaptations::{PromptPack, WorkflowScript, load_prompt_pack, load_trigger_rules};
use pump19_contract::{PullRequestRef, RunKind, SessionId};
use pump19_core::{
    AgentLaunchSpec, Core, CoreError, DispatchOutcome, JsonRunStateStore, LaunchProof,
    PreparedAgent, TriggerRule,
};
use pump19_forge_forgejo::{
    ForgejoActivityError, ForgejoCommandClient, ForgejoCommandMetadata, ForgejoCommandReceipt,
    ForgejoEventSource, ForgejoFixCommit, ForgejoForgeOperations, ForgejoNormalisationConfig,
    ForgejoPollingClient, ForgejoPollingConfig, ForgejoPullRequestSnapshot,
    PollingForgejoActivitySource,
};
use pump19_runs::{
    AgentSessionPreparer, EnsembleFixBody, EnsembleJudgeBody, EnsembleReviewBody,
    EnsembleWorkflowConfig, HostEnsembleWorkflowRunner, MergeGateFinishBody, Pump19RunLauncher,
    RunBodyError,
};
use pump19_workspace::{CommandRuntime, ContainerWorkspaceProvider, WorkspaceConfig};
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors raised while loading, composing, or running the daemon.
#[derive(Debug, Error)]
pub enum DaemonError {
    #[error("daemon config path is required")]
    MissingConfigPath,
    #[error("I/O error at {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("TOML parse error at {path}: {source}")]
    Toml {
        path: String,
        #[source]
        source: toml::de::Error,
    },
    #[error("adaptation loading failed: {0}")]
    Adaptation(String),
    #[error("core failed: {0}")]
    Core(#[from] CoreError),
    #[error("workflow script for {0:?} is missing from the prompt pack")]
    MissingWorkflowScript(RunKind),
}

/// Deployment configuration for one Pump-19 daemon process.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct DaemonConfig {
    pub trigger_pack: PathBuf,
    pub prompt_pack: PathBuf,
    pub state_root: PathBuf,
    pub workspace: WorkspaceDaemonConfig,
    pub ensemble: EnsembleDaemonConfig,
    pub forgejo: ForgejoDaemonConfig,
    #[serde(default)]
    pub loop_control: LoopControlConfig,
}

impl DaemonConfig {
    /// Loads deployment configuration from TOML.
    ///
    /// # Errors
    ///
    /// Returns an error when the file cannot be read or parsed.
    pub fn load(path: &Path) -> Result<Self, DaemonError> {
        let text = fs::read_to_string(path).map_err(|source| DaemonError::Io {
            path: path.display().to_string(),
            source,
        })?;
        toml::from_str(&text).map_err(|source| DaemonError::Toml {
            path: path.display().to_string(),
            source,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct WorkspaceDaemonConfig {
    pub root: PathBuf,
    pub image: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct EnsembleDaemonConfig {
    pub node_program: PathBuf,
    pub launcher_path: PathBuf,
    pub archive_root: PathBuf,
    pub timeout_ms: u64,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgejoDaemonConfig {
    pub repositories: Vec<String>,
    pub finish_label: String,
    pub poll_command: CommandConfig,
    pub operation_command: CommandConfig,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct CommandConfig {
    pub program: PathBuf,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct LoopControlConfig {
    #[serde(default = "default_poll_interval_ms")]
    pub poll_interval_ms: u64,
    #[serde(default)]
    pub stop_after_quiet_polls: Option<u32>,
}

impl Default for LoopControlConfig {
    fn default() -> Self {
        Self {
            poll_interval_ms: default_poll_interval_ms(),
            stop_after_quiet_polls: None,
        }
    }
}

const fn default_poll_interval_ms() -> u64 {
    5_000
}

/// Shutdown check used between event dispatches.
pub trait Shutdown {
    fn should_shutdown(&self) -> bool;
}

/// Shutdown implementation that never requests shutdown.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub struct NeverShutdown;

impl Shutdown for NeverShutdown {
    fn should_shutdown(&self) -> bool {
        false
    }
}

/// Summary of a daemon run.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct DaemonRunSummary {
    pub events_processed: usize,
    pub launches: usize,
    pub quiet_polls: u32,
    pub stopped_by_shutdown: bool,
}

/// Runs the daemon from deployment configuration.
///
/// # Errors
///
/// Returns an error when configuration loading, composition, or core dispatch fails.
pub fn run_from_config(config: DaemonConfig) -> Result<(), DaemonError> {
    let rules = load_rules(&config)?;
    let mut daemon = build_daemon(config)?;
    let _summary = daemon.run(&rules, &NeverShutdown)?;
    Ok(())
}

type RuntimeCore = Core<
    ForgejoEventSource<PollingForgejoActivitySource<CommandPollingClient>>,
    ContainerWorkspaceProvider<CommandRuntime>,
    RuntimeLauncher,
    JsonRunStateStore,
    ForgejoForgeOperations<CommandForgejoClient>,
>;

type RuntimeLauncher = Pump19RunLauncher<
    DaemonSessionPreparer,
    EnsembleReviewBody<HostEnsembleWorkflowRunner>,
    EnsembleJudgeBody<HostEnsembleWorkflowRunner>,
    EnsembleFixBody<HostEnsembleWorkflowRunner>,
    MergeGateFinishBody,
>;

#[derive(Debug)]
struct Pump19Daemon<C> {
    core: C,
    poll_interval: Duration,
    stop_after_quiet_polls: Option<u32>,
}

impl<C> Pump19Daemon<C>
where
    C: CoreRunner,
{
    const fn new(core: C, loop_control: LoopControlConfig) -> Self {
        Self {
            core,
            poll_interval: Duration::from_millis(loop_control.poll_interval_ms),
            stop_after_quiet_polls: loop_control.stop_after_quiet_polls,
        }
    }

    fn run(
        &mut self,
        rules: &[TriggerRule],
        shutdown: &dyn Shutdown,
    ) -> Result<DaemonRunSummary, DaemonError> {
        let mut summary = DaemonRunSummary::default();
        loop {
            if shutdown.should_shutdown() {
                summary.stopped_by_shutdown = true;
                return Ok(summary);
            }
            if let Some(outcomes) = self.core.run_next(rules)? {
                summary.events_processed += 1;
                summary.launches += outcomes
                    .iter()
                    .filter(|outcome| matches!(outcome, DispatchOutcome::Launched { .. }))
                    .count();
                summary.quiet_polls = 0;
            } else {
                summary.quiet_polls = summary.quiet_polls.saturating_add(1);
                if self
                    .stop_after_quiet_polls
                    .is_some_and(|limit| summary.quiet_polls >= limit)
                {
                    return Ok(summary);
                }
                thread::sleep(self.poll_interval);
            }
        }
    }
}

trait CoreRunner {
    fn run_next(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Option<Vec<DispatchOutcome>>, CoreError>;
}

impl<E, W, L, S, F> CoreRunner for Core<E, W, L, S, F>
where
    E: pump19_core::EventSource,
    W: pump19_core::WorkspaceProvider,
    L: pump19_core::RunLauncher,
    S: pump19_core::RunStateStore,
    F: pump19_core::ForgeOperations,
{
    fn run_next(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Option<Vec<DispatchOutcome>>, CoreError> {
        self.process_next(rules)
    }
}

fn load_rules(config: &DaemonConfig) -> Result<Vec<TriggerRule>, DaemonError> {
    load_trigger_rules(&config.trigger_pack)
        .map_err(|error| DaemonError::Adaptation(error.to_string()))
}

fn build_daemon(config: DaemonConfig) -> Result<Pump19Daemon<RuntimeCore>, DaemonError> {
    let prompt_pack = load_prompt_pack(&config.prompt_pack)
        .map_err(|error| DaemonError::Adaptation(error.to_string()))?;
    let prompt_root = config
        .prompt_pack
        .parent()
        .map_or_else(|| PathBuf::from("."), Path::to_path_buf);
    let launcher = runtime_launcher(&config.ensemble, &prompt_pack, &prompt_root)?;
    let polling = PollingForgejoActivitySource::new(
        CommandPollingClient::new(config.forgejo.poll_command.clone()),
        ForgejoPollingConfig::new(config.forgejo.repositories, &config.forgejo.finish_label),
    );
    let event_source = ForgejoEventSource::new(
        polling,
        ForgejoNormalisationConfig::new(config.forgejo.finish_label),
    );
    let workspace = ContainerWorkspaceProvider::new(WorkspaceConfig::new(
        config.workspace.root,
        config.workspace.image,
    ));
    let state_store = JsonRunStateStore::new(config.state_root)?;
    let forge_operations =
        ForgejoForgeOperations::new(CommandForgejoClient::new(config.forgejo.operation_command));
    let core = Core::with_forge_operations(
        event_source,
        workspace,
        launcher,
        state_store,
        forge_operations,
    );
    Ok(Pump19Daemon::new(core, config.loop_control))
}

fn runtime_launcher(
    ensemble: &EnsembleDaemonConfig,
    prompt_pack: &PromptPack,
    prompt_root: &Path,
) -> Result<RuntimeLauncher, DaemonError> {
    let runner = HostEnsembleWorkflowRunner::new(&ensemble.node_program, &ensemble.launcher_path);
    Ok(Pump19RunLauncher::new(
        DaemonSessionPreparer,
        EnsembleReviewBody::new(
            runner.clone(),
            workflow_config(ensemble, prompt_pack, prompt_root, RunKind::Review)?,
        ),
        EnsembleJudgeBody::new(
            runner.clone(),
            workflow_config(ensemble, prompt_pack, prompt_root, RunKind::Judge)?,
        ),
        EnsembleFixBody::new(
            runner,
            workflow_config(ensemble, prompt_pack, prompt_root, RunKind::Fix)?,
        ),
        MergeGateFinishBody,
    ))
}

fn workflow_config(
    ensemble: &EnsembleDaemonConfig,
    prompt_pack: &PromptPack,
    prompt_root: &Path,
    run_kind: RunKind,
) -> Result<EnsembleWorkflowConfig, DaemonError> {
    let script = workflow_script(prompt_pack, run_kind)
        .ok_or(DaemonError::MissingWorkflowScript(run_kind))?;
    Ok(EnsembleWorkflowConfig {
        script: prompt_root.join(&script.path),
        archive_root: ensemble.archive_root.clone(),
        timeout_ms: ensemble.timeout_ms,
    })
}

fn workflow_script(prompt_pack: &PromptPack, run_kind: RunKind) -> Option<&WorkflowScript> {
    prompt_pack
        .manifest
        .workflow_scripts
        .iter()
        .find(|script| script.run_kind == run_kind)
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
struct DaemonSessionPreparer;

impl AgentSessionPreparer for DaemonSessionPreparer {
    fn prepare(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, RunBodyError> {
        Ok(PreparedAgent {
            agent_id: spec.target.agent_id,
            role: spec.target.role,
            session_id: SessionId(format!(
                "daemon:{}:{}",
                spec.pass_index, spec.target.lineage.model
            )),
            proof: LaunchProof::EstablishedFresh,
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct CommandPollingClient {
    command: CommandConfig,
}

impl CommandPollingClient {
    const fn new(command: CommandConfig) -> Self {
        Self { command }
    }
}

impl ForgejoPollingClient for CommandPollingClient {
    fn open_pull_requests(
        &mut self,
        repository: &str,
    ) -> Result<Vec<ForgejoPullRequestSnapshot>, ForgejoActivityError> {
        let output = run_json_command(&self.command, [repository])
            .map_err(|error| ForgejoActivityError::Source(error.to_string()))?;
        serde_json::from_slice(&output)
            .map_err(|error| ForgejoActivityError::Source(error.to_string()))
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct CommandForgejoClient {
    command: CommandConfig,
}

impl CommandForgejoClient {
    const fn new(command: CommandConfig) -> Self {
        Self { command }
    }
}

impl ForgejoCommandClient for CommandForgejoClient {
    fn post_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        body: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::PostComment { pr, body, metadata })
    }

    fn update_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        comment_operation_id: &str,
        body: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::UpdateComment {
            pr,
            comment_operation_id,
            body,
            metadata,
        })
    }

    fn resolve_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        comment_operation_id: &str,
        reason: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::ResolveComment {
            pr,
            comment_operation_id,
            reason,
            metadata,
        })
    }

    fn apply_pr_label(
        &mut self,
        pr: &PullRequestRef,
        label: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::ApplyLabel {
            pr,
            label,
            metadata,
        })
    }

    fn merge_pr(
        &mut self,
        pr: &PullRequestRef,
        method: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::Merge {
            pr,
            method,
            metadata,
        })
    }

    fn push_fix_commits_to_pr_head(
        &mut self,
        pr: &PullRequestRef,
        expected_head_sha: &str,
        commits: &[ForgejoFixCommit],
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        self.run_operation(&CommandOperation::PushFixCommits {
            pr,
            expected_head_sha,
            commits,
            metadata,
        })
    }
}

impl CommandForgejoClient {
    fn run_operation(
        &self,
        operation: &CommandOperation<'_>,
    ) -> Result<ForgejoCommandReceipt, pump19_forge_forgejo::ForgejoClientError> {
        let input = serde_json::to_vec(&operation).map_err(|error| {
            pump19_forge_forgejo::ForgejoClientError::Transport(error.to_string())
        })?;
        let output = run_json_command_with_stdin(&self.command, std::iter::empty::<&str>(), &input)
            .map_err(|error| {
                pump19_forge_forgejo::ForgejoClientError::Transport(error.to_string())
            })?;
        serde_json::from_slice::<CommandReceipt>(&output)
            .map(|receipt| ForgejoCommandReceipt {
                operation_id: receipt.operation_id,
                new_head_sha: receipt.new_head_sha,
            })
            .map_err(|error| pump19_forge_forgejo::ForgejoClientError::Rejected(error.to_string()))
    }
}

#[derive(Serialize)]
#[serde(rename_all = "snake_case", tag = "operation")]
enum CommandOperation<'a> {
    PostComment {
        pr: &'a PullRequestRef,
        body: &'a str,
        metadata: &'a ForgejoCommandMetadata,
    },
    UpdateComment {
        pr: &'a PullRequestRef,
        comment_operation_id: &'a str,
        body: &'a str,
        metadata: &'a ForgejoCommandMetadata,
    },
    ResolveComment {
        pr: &'a PullRequestRef,
        comment_operation_id: &'a str,
        reason: &'a str,
        metadata: &'a ForgejoCommandMetadata,
    },
    ApplyLabel {
        pr: &'a PullRequestRef,
        label: &'a str,
        metadata: &'a ForgejoCommandMetadata,
    },
    Merge {
        pr: &'a PullRequestRef,
        method: &'a str,
        metadata: &'a ForgejoCommandMetadata,
    },
    PushFixCommits {
        pr: &'a PullRequestRef,
        expected_head_sha: &'a str,
        commits: &'a [ForgejoFixCommit],
        metadata: &'a ForgejoCommandMetadata,
    },
}

#[derive(Deserialize)]
struct CommandReceipt {
    operation_id: String,
    #[serde(default)]
    new_head_sha: Option<String>,
}

fn run_json_command<'a>(
    config: &CommandConfig,
    args: impl IntoIterator<Item = &'a str>,
) -> Result<Vec<u8>, std::io::Error> {
    run_json_command_with_stdin(config, args, &[])
}

fn run_json_command_with_stdin<'a>(
    config: &CommandConfig,
    args: impl IntoIterator<Item = &'a str>,
    stdin: &[u8],
) -> Result<Vec<u8>, std::io::Error> {
    use std::io::Write as _;

    let mut command = Command::new(&config.program);
    command.args(&config.args).args(args).envs(&config.env);
    if stdin.is_empty() {
        let output = command.output()?;
        return command_output(output);
    }
    command.stdin(std::process::Stdio::piped());
    command.stdout(std::process::Stdio::piped());
    command.stderr(std::process::Stdio::piped());
    let mut child = command.spawn()?;
    if let Some(child_stdin) = child.stdin.as_mut() {
        child_stdin.write_all(stdin)?;
    }
    command_output(child.wait_with_output()?)
}

fn command_output(output: std::process::Output) -> Result<Vec<u8>, std::io::Error> {
    if output.status.success() {
        return Ok(output.stdout);
    }
    Err(std::io::Error::other(format!(
        "command exited with {:?}: {}",
        output.status.code(),
        String::from_utf8_lossy(&output.stderr)
    )))
}

#[cfg(test)]
mod tests {
    use std::{cell::RefCell, collections::VecDeque, path::PathBuf, rc::Rc};

    use pump19_contract::{
        ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
        CertaintyClass, Confidence, ContractVersion, Decision, DecisionSubject, DecisionVerdict,
        Extensions, Finding, FindingId, FindingLocation, ForgeFacts, Mergeability, ModelFamily,
        ModelLineage, Patch, PatchChange, PatchId, PrRunState, ReviewCleanliness, Revision, RunId,
        RunOutcome, SessionId, Severity,
    };
    use pump19_core::{
        AgentEngine, AgentLaunchTarget, AgentPlan, AuthorisationEvidence, AuthorisedComment,
        AuthorisedCommentResolution, AuthorisedCommentUpdate, AuthorisedFixPush, AuthorisedLabel,
        AuthorisedMerge, Criteria, EventKind, ForgeOperationError, ForgeOperationReceipt,
        ForgeOperations, RunLaunchOutcome, RunLaunchRequest, RunLauncher, RunStateKey,
        RunStateStore, StateCriterion, WorkspaceExecOutput, WorkspaceExecRequest,
        WorkspaceExecutor, WorkspaceIsolation, WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
    };
    use pump19_forge_forgejo::{
        ForgejoBranchCurrency, ForgejoMergeability, ForgejoReviewCleanliness,
    };
    use tempfile::tempdir;

    use super::*;

    #[derive(Clone, Debug)]
    struct StubCore {
        outcomes: Rc<RefCell<Vec<Option<Vec<DispatchOutcome>>>>>,
    }

    impl CoreRunner for StubCore {
        fn run_next(
            &mut self,
            _rules: &[TriggerRule],
        ) -> Result<Option<Vec<DispatchOutcome>>, CoreError> {
            Ok(self.outcomes.borrow_mut().remove(0))
        }
    }

    #[derive(Clone, Debug)]
    struct StopAfter {
        calls: Rc<RefCell<u32>>,
        limit: u32,
    }

    impl Shutdown for StopAfter {
        fn should_shutdown(&self) -> bool {
            let mut calls = self.calls.borrow_mut();
            *calls = calls.saturating_add(1);
            *calls > self.limit
        }
    }

    #[derive(Debug)]
    struct FakePollingClient {
        polls: VecDeque<Vec<ForgejoPullRequestSnapshot>>,
    }

    impl ForgejoPollingClient for FakePollingClient {
        fn open_pull_requests(
            &mut self,
            _repository: &str,
        ) -> Result<Vec<ForgejoPullRequestSnapshot>, ForgejoActivityError> {
            Ok(self.polls.pop_front().unwrap_or_default())
        }
    }

    #[derive(Clone, Debug)]
    struct FakeWorkspaceProvider {
        cleaned: Rc<RefCell<usize>>,
    }

    impl WorkspaceProvider for FakeWorkspaceProvider {
        fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
            Ok(WorkspaceLease {
                id: request.run_id.0,
                root: PathBuf::from("/tmp/pump19-daemon-test"),
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: true,
                    egress_bounded: true,
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
            *self.cleaned.borrow_mut() += 1;
            Ok(())
        }
    }

    #[derive(Clone, Debug, Default)]
    struct RecordingForgeOperations {
        comments: Rc<RefCell<Vec<AuthorisedComment>>>,
        comment_updates: Rc<RefCell<Vec<AuthorisedCommentUpdate>>>,
        comment_resolutions: Rc<RefCell<Vec<AuthorisedCommentResolution>>>,
        labels: Rc<RefCell<Vec<AuthorisedLabel>>>,
        merges: Rc<RefCell<Vec<AuthorisedMerge>>>,
        fix_pushes: Rc<RefCell<Vec<AuthorisedFixPush>>>,
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
            let new_head_sha = Some(request.expected_head_sha.clone());
            self.fix_pushes.borrow_mut().push(request);
            Ok(ForgeOperationReceipt {
                operation_id: "fix-push".to_owned(),
                idempotency_key,
                new_head_sha,
            })
        }
    }

    #[derive(Clone, Debug, Default)]
    struct SharedStore {
        states: Rc<RefCell<Vec<PrRunState>>>,
    }

    impl RunStateStore for SharedStore {
        fn load(&self, key: &RunStateKey) -> Result<Option<PrRunState>, CoreError> {
            Ok(self
                .states
                .borrow()
                .iter()
                .find(|state| state.pr == key.pr && state.commit_sha == key.commit_sha)
                .cloned())
        }

        fn load_latest_for_pr(
            &self,
            pr: &pump19_contract::PullRequestRef,
        ) -> Result<Option<PrRunState>, CoreError> {
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
                        .active_run
                        .as_ref()
                        .is_some_and(|record| record.run_id == *run_id)
                        || state
                            .run_history
                            .iter()
                            .any(|record| record.run_id == *run_id)
                })
                .cloned())
        }

        fn save(&mut self, state: &PrRunState) -> Result<(), CoreError> {
            let mut states = self.states.borrow_mut();
            if let Some(existing) = states.iter_mut().find(|candidate| {
                candidate.pr == state.pr && candidate.commit_sha == state.commit_sha
            }) {
                *existing = state.clone();
            } else {
                states.push(state.clone());
            }
            Ok(())
        }
    }

    #[derive(Debug, Default)]
    struct EstateLoopLauncher;

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

    #[test]
    fn run_loop_stops_cleanly_when_quiet() {
        let mut daemon = Pump19Daemon::new(
            StubCore {
                outcomes: Rc::new(RefCell::new(vec![None])),
            },
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let summary = daemon.run(&[], &NeverShutdown).expect("run daemon");

        assert_eq!(summary.quiet_polls, 1);
        assert_eq!(summary.events_processed, 0);
        assert!(!summary.stopped_by_shutdown);
    }

    #[test]
    fn run_loop_checks_shutdown_between_events() {
        let mut daemon = Pump19Daemon::new(
            StubCore {
                outcomes: Rc::new(RefCell::new(vec![
                    Some(vec![DispatchOutcome::Launched {
                        rule_id: "review".to_owned(),
                        run_id: pump19_contract::RunId("run-review".to_owned()),
                    }]),
                    Some(vec![DispatchOutcome::Launched {
                        rule_id: "judge".to_owned(),
                        run_id: pump19_contract::RunId("run-judge".to_owned()),
                    }]),
                ])),
            },
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: None,
            },
        );
        let shutdown = StopAfter {
            calls: Rc::new(RefCell::new(0)),
            limit: 2,
        };

        let summary = daemon.run(&[], &shutdown).expect("run daemon");

        assert_eq!(summary.events_processed, 2);
        assert_eq!(summary.launches, 2);
        assert!(summary.stopped_by_shutdown);
    }

    #[test]
    fn daemon_drives_loop_from_polled_pr_without_completion_echo() {
        let cleaned = Rc::new(RefCell::new(0));
        let forge_operations = RecordingForgeOperations::default();
        let comments = Rc::clone(&forge_operations.comments);
        let store = SharedStore::default();
        let state_observer = store.clone();
        let polling = PollingForgejoActivitySource::new(
            FakePollingClient {
                polls: VecDeque::from([vec![forgejo_snapshot()], vec![forgejo_snapshot()]]),
            },
            ForgejoPollingConfig::new(vec!["acme/widgets".to_owned()], "pump19-finish"),
        );
        let source =
            ForgejoEventSource::new(polling, ForgejoNormalisationConfig::new("pump19-finish"));
        let core = Core::with_forge_operations(
            source,
            FakeWorkspaceProvider {
                cleaned: Rc::clone(&cleaned),
            },
            EstateLoopLauncher,
            store,
            forge_operations,
        );
        let mut daemon = Pump19Daemon::new(
            core,
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let summary = daemon
            .run(&loop_rules(), &NeverShutdown)
            .expect("run daemon");

        assert_eq!(summary.launches, 5);
        assert_eq!(*cleaned.borrow(), 5);
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
        let latest = state_observer
            .load_latest_for_pr(&pr())
            .expect("load latest")
            .expect("latest state");
        assert!(
            Criteria::State {
                state: StateCriterion::HasConverged
            }
            .matches(&probe_event(), Some(&latest))
        );
    }

    #[test]
    fn config_loads_from_external_toml() {
        let dir = tempdir().expect("temp dir");
        let path = dir.path().join("pump19.toml");
        fs::write(
            &path,
            r#"
trigger_pack = "triggers.toml"
prompt_pack = "prompt-pack.toml"
state_root = "state"

[workspace]
root = "workspaces"
image = "localhost/pump19-workspace:stable"

[ensemble]
node_program = "node"
launcher_path = "launcher.js"
archive_root = "archives"
timeout_ms = 5000

[forgejo]
repositories = ["acme/widgets"]
finish_label = "pump19-finish"

[forgejo.poll_command]
program = "poll-forgejo"
args = ["--json"]

[forgejo.operation_command]
program = "write-forgejo"

[loop_control]
poll_interval_ms = 0
stop_after_quiet_polls = 1
"#,
        )
        .expect("write config");

        let config = DaemonConfig::load(&path).expect("load config");

        assert_eq!(config.forgejo.repositories, vec!["acme/widgets"]);
        assert_eq!(config.loop_control.stop_after_quiet_polls, Some(1));
    }

    fn assert_core_runner<T: CoreRunner>() {}

    #[test]
    fn runtime_core_type_satisfies_daemon_runner_contract() {
        assert_core_runner::<
            Core<
                ForgejoEventSource<PollingForgejoActivitySource<CommandPollingClient>>,
                ContainerWorkspaceProvider<CommandRuntime>,
                RuntimeLauncher,
                JsonRunStateStore,
                ForgejoForgeOperations<CommandForgejoClient>,
            >,
        >();
    }

    fn forgejo_snapshot() -> ForgejoPullRequestSnapshot {
        ForgejoPullRequestSnapshot {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
            head_sha: "abc123".to_owned(),
            base_sha: "def456".to_owned(),
            branch_currency: ForgejoBranchCurrency::Current,
            cleanliness: ForgejoReviewCleanliness::Clean,
            mergeability: ForgejoMergeability::Mergeable,
            labels: Vec::new(),
            actor_permissions: Vec::new(),
        }
    }

    fn pr() -> pump19_contract::PullRequestRef {
        pump19_contract::PullRequestRef {
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

    fn contract_facts() -> ForgeFacts {
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
            finish_label: None,
            actor_permissions: vec![ActorPermissions {
                actor: actor("pump19-core"),
                capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
                    .into_iter()
                    .collect(),
            }],
            extensions: Extensions::new(),
        }
    }

    fn probe_event() -> pump19_contract::ContractEvent {
        pump19_contract::ContractEvent {
            contract_version: ContractVersion::current(),
            id: "probe".to_owned(),
            payload: pump19_contract::EventPayload::PullRequestUpdated {
                facts: contract_facts(),
            },
            extensions: Extensions::new(),
        }
    }

    fn target(agent_id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
        AgentLaunchTarget {
            agent_id: AgentId(agent_id.to_owned()),
            role,
            engine: match family {
                "claude" => AgentEngine::Claude,
                "codex" => AgentEngine::Codex,
                _ => AgentEngine::Opencode,
            },
            vendor: "local".to_owned(),
            control_plane: "pump19-core".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: format!("{family}-2026-06"),
            },
        }
    }

    fn loop_rules() -> Vec<TriggerRule> {
        vec![
            TriggerRule {
                id: "review-on-pr-opened".to_owned(),
                run_kind: RunKind::Review,
                criteria: Criteria::Event {
                    event: EventKind::PullRequestOpened,
                },
                agent_plan: AgentPlan {
                    reviewers: vec![
                        target("reviewer-codex", AgentRole::Reviewer, "codex"),
                        target("reviewer-claude", AgentRole::Reviewer, "claude"),
                    ],
                    fixers: Vec::new(),
                    judge: Some(target("judge-gemini", AgentRole::Judge, "gemini")),
                    finishers: Vec::new(),
                },
            },
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
            },
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
            },
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
            },
        ]
    }

    fn review_outcome(request: &RunLaunchRequest) -> RunLaunchOutcome {
        if request.state.pass_index > 1 {
            return RunLaunchOutcome {
                outcome: RunOutcome::Succeeded,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
            };
        }
        RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: request
                .provenance
                .iter()
                .filter(|provenance| provenance.role == AgentRole::Reviewer)
                .cloned()
                .enumerate()
                .map(|(index, reviewer)| Finding {
                    contract_version: ContractVersion::current(),
                    id: FindingId(format!("finding-material-{index}")),
                    dedup_key: format!("daemon-loop:finding-material-{index}"),
                    source_brief: "daemon-loop".to_owned(),
                    dimension: "correctness".to_owned(),
                    summary: "A material review finding.".to_owned(),
                    severity: Severity::High,
                    confidence: Confidence::High,
                    certainty: CertaintyClass::Advisory,
                    provenance: reviewer,
                    locations: vec![FindingLocation::General {
                        description: "whole change".to_owned(),
                    }],
                    extensions: Extensions::new(),
                })
                .collect(),
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
        let (subject, verdict) = request.state.findings.first().map_or_else(
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
        RunLaunchOutcome {
            outcome: RunOutcome::Succeeded,
            findings: Vec::new(),
            decisions: vec![Decision {
                contract_version: ContractVersion::current(),
                id: format!("decision-pass-{}", request.state.pass_index),
                subject,
                verdict,
                rationale: "deterministic test verdict".to_owned(),
                provenance: judge,
                extensions: Extensions::new(),
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
            patches: vec![Patch {
                contract_version: ContractVersion::current(),
                id: PatchId("patch-material".to_owned()),
                run_id: request.run_id.clone(),
                commit_sha: request.state.commit_sha.clone(),
                idempotency_key: "patch-material".to_owned(),
                answers_findings: vec![FindingId("finding-material".to_owned())],
                change: PatchChange::Description {
                    summary: "fixed material finding".to_owned(),
                },
                provenance: fixer,
                extensions: Extensions::new(),
            }],
            token_usage: None,
        }
    }
}
