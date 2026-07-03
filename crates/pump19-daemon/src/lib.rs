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
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use pump19_adaptations::{
    MechanicalExecution, MechanicalPack, MechanicalStep, MechanicalStepKind, PromptPack,
    PromptTemplate, WorkflowScript, load_mechanical_pack, load_prompt_pack, load_trigger_rules,
};
use pump19_contract::{PullRequestRef, RunKind, SessionId};
use pump19_core::{
    AgentLaunchSpec, CompletionRecoverySummary, Core, CoreError, CorePolicy, DispatchOutcome,
    FinishLabelApplicationPolicy, JsonRunStateStore, LaunchProof, PreparedAgent, PreparedSource,
    SourcePreparationRequest, SourcePreparer, TriggerRule,
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
use serde_json::{Value, json};
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
    #[error("prompt template for {0:?} is missing from the prompt pack")]
    MissingPromptTemplate(RunKind),
    #[error("mechanical source preparation step is missing from the mechanical pack")]
    MissingSourcePreparationStep,
    #[error("source preparation step {0:?} uses unsupported container execution")]
    UnsupportedSourcePreparationContainer(String),
    #[error("signal handler setup failed: {0}")]
    Signal(String),
}

/// Deployment configuration for one Pump-19 daemon process.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct DaemonConfig {
    pub trigger_pack: PathBuf,
    pub prompt_pack: PathBuf,
    pub mechanical_pack: PathBuf,
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
    #[serde(default)]
    pub core_applies_finish_label_on_convergence: bool,
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

/// Shutdown check backed by SIGINT/SIGTERM.
#[derive(Clone, Debug)]
pub struct SignalShutdown {
    requested: Arc<AtomicBool>,
}

impl SignalShutdown {
    /// Installs a process signal handler that flips the daemon shutdown flag.
    ///
    /// # Errors
    ///
    /// Returns an error when the process already has an incompatible ctrl-c
    /// handler or the platform cannot install the handler.
    pub fn install() -> Result<Self, DaemonError> {
        let requested = Arc::new(AtomicBool::new(false));
        let handler_requested = Arc::clone(&requested);
        ctrlc::set_handler(move || {
            handler_requested.store(true, Ordering::SeqCst);
        })
        .map_err(|error| DaemonError::Signal(error.to_string()))?;
        Ok(Self { requested })
    }
}

impl Shutdown for SignalShutdown {
    fn should_shutdown(&self) -> bool {
        self.requested.load(Ordering::SeqCst)
    }
}

/// Summary of a daemon run.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct DaemonRunSummary {
    pub events_processed: usize,
    pub launches: usize,
    pub quiet_polls: u32,
    pub recovered_completion_events: usize,
    pub recovered_terminal_events: usize,
    pub recovered_stale_running_events: usize,
    pub recovery_errors_continued: usize,
    pub dispatch_errors_continued: usize,
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
    let shutdown = SignalShutdown::install()?;
    let summary = daemon.run(&rules, &shutdown)?;
    log_daemon_event(
        "info",
        "daemon_summary",
        &json!({
            "events_processed": summary.events_processed,
            "launches": summary.launches,
            "quiet_polls": summary.quiet_polls,
            "recovered_completion_events": summary.recovered_completion_events,
            "recovered_terminal_events": summary.recovered_terminal_events,
            "recovered_stale_running_events": summary.recovered_stale_running_events,
            "recovery_errors_continued": summary.recovery_errors_continued,
            "dispatch_errors_continued": summary.dispatch_errors_continued,
            "stopped_by_shutdown": summary.stopped_by_shutdown,
        }),
    );
    Ok(())
}

type RuntimeCore = Core<
    ForgejoEventSource<PollingForgejoActivitySource<CommandPollingClient>>,
    ContainerWorkspaceProvider<CommandRuntime>,
    RuntimeLauncher,
    JsonRunStateStore,
    ForgejoForgeOperations<CommandForgejoClient>,
    RuntimeSourcePreparer,
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
        self.rederive_pending_completions(&mut summary)?;
        loop {
            if shutdown.should_shutdown() {
                summary.stopped_by_shutdown = true;
                log_daemon_event(
                    "info",
                    "shutdown_requested",
                    &json!({
                        "events_processed": summary.events_processed,
                        "pending_events": self.core.pending_event_count(),
                    }),
                );
                return Ok(summary);
            }
            match self.core.run_next(rules) {
                Ok(Some(outcomes)) => {
                    let launched = outcomes
                        .iter()
                        .filter(|outcome| matches!(outcome, DispatchOutcome::Launched { .. }))
                        .count();
                    summary.events_processed += 1;
                    summary.launches += launched;
                    summary.quiet_polls = 0;
                    log_daemon_event(
                        "info",
                        "dispatch_outcomes",
                        &json!({
                            "event_index": summary.events_processed,
                            "launches": launched,
                            "outcomes": outcomes.iter().map(dispatch_outcome_log).collect::<Vec<_>>(),
                            "pending_events": self.core.pending_event_count(),
                        }),
                    );
                }
                Ok(None) => {
                    if self.rederive_pending_completions(&mut summary)? {
                        continue;
                    }
                    summary.quiet_polls = summary.quiet_polls.saturating_add(1);
                    log_daemon_event(
                        "info",
                        "quiet_poll",
                        &json!({
                            "quiet_polls": summary.quiet_polls,
                            "stop_after_quiet_polls": self.stop_after_quiet_polls,
                        }),
                    );
                    if self
                        .stop_after_quiet_polls
                        .is_some_and(|limit| summary.quiet_polls >= limit)
                    {
                        return Ok(summary);
                    }
                    thread::sleep(self.poll_interval);
                }
                Err(error) if core_error_is_daemon_fatal(&error) => {
                    log_daemon_event(
                        "error",
                        "daemon_fatal_core_error",
                        &json!({
                            "class": core_error_class(&error),
                            "error": error.to_string(),
                        }),
                    );
                    return Err(DaemonError::Core(error));
                }
                Err(error) => {
                    summary.dispatch_errors_continued += 1;
                    let retry_after_poll_interval = matches!(error, CoreError::EventSource(_));
                    if retry_after_poll_interval {
                        summary.quiet_polls = summary.quiet_polls.saturating_add(1);
                    } else {
                        summary.quiet_polls = 0;
                    }
                    log_daemon_event(
                        "error",
                        "dispatch_error_continued",
                        &json!({
                            "class": core_error_class(&error),
                            "error": error.to_string(),
                            "continued_errors": summary.dispatch_errors_continued,
                            "retry_after_poll_interval": retry_after_poll_interval,
                        }),
                    );
                    if retry_after_poll_interval {
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
    }

    fn rederive_pending_completions(
        &mut self,
        summary: &mut DaemonRunSummary,
    ) -> Result<bool, DaemonError> {
        let recovery = match self.core.recover_pending_completions() {
            Ok(recovery) => recovery,
            Err(error) if core_error_is_daemon_fatal(&error) => {
                return Err(DaemonError::Core(error));
            }
            Err(error) => {
                summary.recovery_errors_continued += 1;
                log_daemon_event(
                    "error",
                    "completion_recovery_error_continued",
                    &json!({
                        "class": core_error_class(&error),
                        "error": error.to_string(),
                        "continued_errors": summary.recovery_errors_continued,
                    }),
                );
                return Ok(true);
            }
        };
        if recovery == CompletionRecoverySummary::default() {
            return Ok(false);
        }
        summary.recovered_completion_events += recovery.queued;
        summary.recovered_terminal_events += recovery.terminal_replays;
        summary.recovered_stale_running_events += recovery.stale_running_failures;
        summary.recovery_errors_continued += recovery.errors_continued;
        summary.quiet_polls = 0;
        log_daemon_event(
            "info",
            if recovery.stale_running_failures == 0 {
                "completion_recovery"
            } else {
                "stale_running_recovery"
            },
            &json!({
                "recovered": recovery.queued,
                "terminal_replays": recovery.terminal_replays,
                "stale_running_failures": recovery.stale_running_failures,
                "errors_continued": recovery.errors_continued,
                "total_recovered": summary.recovered_completion_events,
                "total_terminal_replays": summary.recovered_terminal_events,
                "total_stale_running_failures": summary.recovered_stale_running_events,
                "total_recovery_errors_continued": summary.recovery_errors_continued,
                "pending_events": self.core.pending_event_count(),
            }),
        );
        Ok(true)
    }
}

trait CoreRunner {
    fn run_next(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Option<Vec<DispatchOutcome>>, CoreError>;

    fn recover_pending_completions(&mut self) -> Result<CompletionRecoverySummary, CoreError>;

    fn pending_event_count(&self) -> usize;
}

impl<E, W, L, S, F, P> CoreRunner for Core<E, W, L, S, F, P>
where
    E: pump19_core::EventSource,
    W: pump19_core::WorkspaceProvider,
    L: pump19_core::RunLauncher,
    S: pump19_core::RunStateStore,
    F: pump19_core::ForgeOperations,
    P: pump19_core::SourcePreparer,
{
    fn run_next(
        &mut self,
        rules: &[TriggerRule],
    ) -> Result<Option<Vec<DispatchOutcome>>, CoreError> {
        self.process_next(rules)
    }

    fn recover_pending_completions(&mut self) -> Result<CompletionRecoverySummary, CoreError> {
        self.rederive_pending_completions()
    }

    fn pending_event_count(&self) -> usize {
        Self::pending_event_count(self)
    }
}

const fn core_error_is_daemon_fatal(error: &CoreError) -> bool {
    // Per-event launch/forge/adapter failures are recorded against the PR by the
    // core before they reach the daemon. Store and serialisation errors are
    // fatal because continuing would make the persisted run ledger untrustworthy.
    matches!(
        error,
        CoreError::StateStore(_) | CoreError::Io { .. } | CoreError::Json { .. }
    )
}

const fn core_error_class(error: &CoreError) -> &'static str {
    match error {
        CoreError::EventSource(_) => "event_source",
        CoreError::Workspace(_) => "workspace",
        CoreError::SourcePreparation(_) => "source_preparation",
        CoreError::Launcher(_) => "launcher",
        CoreError::RequiredFamilyUnavailable { .. } => "required_family_unavailable",
        CoreError::StateStore(_) => "state_store",
        CoreError::ForgeOperation(_) => "forge_operation",
        CoreError::Io { .. } => "io",
        CoreError::Json { .. } => "json",
    }
}

fn dispatch_outcome_log(outcome: &DispatchOutcome) -> Value {
    match outcome {
        DispatchOutcome::Launched { rule_id, run_id } => json!({
            "outcome": "launched",
            "rule_id": rule_id,
            "run_id": run_id.0,
        }),
        DispatchOutcome::Superseded {
            rule_id,
            run_id,
            superseded_by,
        } => json!({
            "outcome": "superseded",
            "rule_id": rule_id,
            "run_id": run_id.0,
            "superseded_by": superseded_by,
        }),
        DispatchOutcome::Refused { rule_id, reason } => json!({
            "outcome": "refused",
            "rule_id": rule_id,
            "reason": format!("{reason:?}"),
        }),
        DispatchOutcome::Skipped { rule_id, reason } => json!({
            "outcome": "skipped",
            "rule_id": rule_id,
            "reason": format!("{reason:?}"),
        }),
    }
}

fn log_daemon_event(level: &str, event: &str, fields: &Value) {
    eprintln!(
        "{}",
        json!({
            "level": level,
            "event": event,
            "fields": fields,
        })
    );
}

fn load_rules(config: &DaemonConfig) -> Result<Vec<TriggerRule>, DaemonError> {
    load_trigger_rules(&config.trigger_pack)
        .map_err(|error| DaemonError::Adaptation(error.to_string()))
}

fn build_daemon(config: DaemonConfig) -> Result<Pump19Daemon<RuntimeCore>, DaemonError> {
    let prompt_pack = load_prompt_pack(&config.prompt_pack)
        .map_err(|error| DaemonError::Adaptation(error.to_string()))?;
    let mechanical_pack = load_mechanical_pack(&config.mechanical_pack)
        .map_err(|error| DaemonError::Adaptation(error.to_string()))?;
    let prompt_root = config
        .prompt_pack
        .parent()
        .map_or_else(|| PathBuf::from("."), Path::to_path_buf);
    let launcher = runtime_launcher(&config.ensemble, &prompt_pack, &prompt_root)?;
    let policy = core_policy(&config.forgejo);
    let source_preparer = runtime_source_preparer(&mechanical_pack)?;
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
    let core = Core::with_forge_operations_source_preparer_and_policy(
        event_source,
        workspace,
        launcher,
        state_store,
        forge_operations,
        source_preparer,
        policy,
    );
    Ok(Pump19Daemon::new(core, config.loop_control))
}

fn core_policy(config: &ForgejoDaemonConfig) -> CorePolicy {
    if config.core_applies_finish_label_on_convergence {
        CorePolicy {
            finish_label_application: FinishLabelApplicationPolicy::CoreOnConvergence {
                label: config.finish_label.clone(),
            },
        }
    } else {
        CorePolicy::human_gate()
    }
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
    let template = prompt_template(prompt_pack, run_kind)
        .ok_or(DaemonError::MissingPromptTemplate(run_kind))?;
    Ok(EnsembleWorkflowConfig {
        script: prompt_root.join(&script.path),
        archive_root: ensemble.archive_root.clone(),
        timeout_ms: ensemble.timeout_ms,
        prompt_template: template.template.clone(),
        briefs: prompt_pack.briefs.clone(),
    })
}

fn workflow_script(prompt_pack: &PromptPack, run_kind: RunKind) -> Option<&WorkflowScript> {
    prompt_pack
        .manifest
        .workflow_scripts
        .iter()
        .find(|script| script.run_kind == run_kind)
}

fn prompt_template(prompt_pack: &PromptPack, run_kind: RunKind) -> Option<&PromptTemplate> {
    prompt_pack
        .manifest
        .prompt_templates
        .iter()
        .find(|template| template.run_kind == run_kind)
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct RuntimeSourcePreparer {
    steps: Vec<MechanicalStep>,
}

impl RuntimeSourcePreparer {
    const fn new(steps: Vec<MechanicalStep>) -> Self {
        Self { steps }
    }
}

impl SourcePreparer for RuntimeSourcePreparer {
    fn prepare_source(
        &mut self,
        request: SourcePreparationRequest,
    ) -> Result<PreparedSource, CoreError> {
        let started_at = Instant::now();
        let preparation_root = source_preparation_root(&request);
        if preparation_root.exists() {
            fs::remove_dir_all(&preparation_root).map_err(|source| {
                CoreError::SourcePreparation(format!(
                    "clear source preparation root {}: {source}",
                    preparation_root.display()
                ))
            })?;
        }
        fs::create_dir_all(&preparation_root).map_err(|source| {
            CoreError::SourcePreparation(format!(
                "create source preparation root {}: {source}",
                preparation_root.display()
            ))
        })?;

        let mut prepared = None;
        let mut steps_run = Vec::with_capacity(self.steps.len());
        for step in &self.steps {
            prepared = Some(run_source_preparation_step(
                step,
                &request,
                &preparation_root,
                prepared.as_ref(),
            )?);
            steps_run.push(step.id.clone());
        }
        let mut prepared = prepared.ok_or_else(|| {
            CoreError::SourcePreparation("no source preparation steps configured".to_owned())
        })?;
        prepared.cleanup_root = Some(preparation_root.clone());
        log_daemon_event(
            "info",
            "source_preparation_succeeded",
            &json!({
                "run_id": request.run_id.0,
                "run_kind": request.run_kind,
                "repository": request.pr.repository,
                "pull_request": request.pr.id,
                "steps_run": steps_run,
                "prepared_revision": prepared.revision,
                "duration_ms": started_at.elapsed().as_millis(),
                "preparation_root": preparation_root,
            }),
        );
        Ok(prepared)
    }
}

#[derive(Serialize)]
struct SourcePreparationCommandInput<'a> {
    step_id: &'a str,
    run_id: &'a str,
    run_kind: RunKind,
    repository: &'a str,
    pull_request: &'a str,
    commit_sha: &'a str,
    workspace_root: &'a Path,
    preparation_root: &'a Path,
    previous_tree: Option<&'a Path>,
    event: &'a pump19_contract::ContractEvent,
    state: &'a pump19_contract::PrRunState,
}

#[derive(Deserialize)]
struct SourcePreparationCommandOutput {
    tree: PathBuf,
    revision: String,
}

fn runtime_source_preparer(pack: &MechanicalPack) -> Result<RuntimeSourcePreparer, DaemonError> {
    let steps = pack
        .steps
        .iter()
        .filter(|step| {
            matches!(
                step.kind,
                MechanicalStepKind::Checkout | MechanicalStepKind::Prepare
            )
        })
        .cloned()
        .collect::<Vec<_>>();
    if steps.is_empty() {
        return Err(DaemonError::MissingSourcePreparationStep);
    }
    Ok(RuntimeSourcePreparer::new(steps))
}

fn source_preparation_root(request: &SourcePreparationRequest) -> PathBuf {
    let parent = request
        .workspace
        .root
        .parent()
        .map_or_else(|| PathBuf::from("."), Path::to_path_buf);
    parent.join(format!("{}-source-prep", request.workspace.id))
}

fn run_source_preparation_step(
    step: &MechanicalStep,
    request: &SourcePreparationRequest,
    preparation_root: &Path,
    previous: Option<&PreparedSource>,
) -> Result<PreparedSource, CoreError> {
    let input = SourcePreparationCommandInput {
        step_id: &step.id,
        run_id: &request.run_id.0,
        run_kind: request.run_kind,
        repository: &request.pr.repository,
        pull_request: &request.pr.id,
        commit_sha: &request.commit_sha,
        workspace_root: &request.workspace.root,
        preparation_root,
        previous_tree: previous.map(|source| source.tree.as_path()),
        event: &request.event,
        state: &request.state,
    };
    let stdin = serde_json::to_vec(&input).map_err(|error| {
        CoreError::SourcePreparation(format!(
            "serialise source preparation input for step {:?}: {error}",
            step.id
        ))
    })?;
    let output = match &step.execution {
        MechanicalExecution::Command { program, args } => {
            run_json_program(program, args, &stdin)
                .map_err(|error| source_step_command_error(&step.id, &error))?
        }
        MechanicalExecution::Container { .. } => {
            return Err(CoreError::SourcePreparation(
                DaemonError::UnsupportedSourcePreparationContainer(step.id.clone()).to_string(),
            ));
        }
    };
    let output =
        serde_json::from_slice::<SourcePreparationCommandOutput>(&output).map_err(|error| {
            CoreError::SourcePreparation(format!(
                "source preparation step {:?} returned invalid JSON: {error}",
                step.id
            ))
        })?;
    Ok(PreparedSource {
        tree: output.tree,
        revision: output.revision,
        cleanup_root: None,
    })
}

fn source_step_command_error(step_id: &str, error: &std::io::Error) -> CoreError {
    CoreError::SourcePreparation(format!(
        "source preparation step {step_id:?} failed: {error}"
    ))
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

fn run_json_program(
    program: &str,
    args: &[String],
    stdin: &[u8],
) -> Result<Vec<u8>, std::io::Error> {
    use std::io::Write as _;

    let mut command = Command::new(program);
    command.args(args);
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
    use std::{
        cell::RefCell,
        collections::VecDeque,
        path::{Path, PathBuf},
        rc::Rc,
    };

    use pump19_contract::{
        ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
        CertaintyClass, Confidence, ContractVersion, Decision, DecisionSubject, DecisionVerdict,
        Extensions, Finding, FindingId, FindingLocation, ForgeFacts, Mergeability, ModelFamily,
        ModelLineage, Patch, PatchChange, PatchId, PrRunState, PublicationState, ReviewCleanliness,
        Revision, RunId, RunOutcome, SessionId, Severity,
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
        ForgejoActor, ForgejoActorPermission, ForgejoBranchCurrency, ForgejoLabelApplication,
        ForgejoMergeability, ForgejoReviewCleanliness,
    };
    use tempfile::tempdir;

    use super::*;

    type StubOutcome = Result<Option<Vec<DispatchOutcome>>, CoreError>;

    #[derive(Clone, Debug)]
    struct StubCore {
        outcomes: Rc<RefCell<Vec<StubOutcome>>>,
        recovered: Rc<RefCell<Vec<Result<CompletionRecoverySummary, CoreError>>>>,
    }

    impl CoreRunner for StubCore {
        fn run_next(
            &mut self,
            _rules: &[TriggerRule],
        ) -> Result<Option<Vec<DispatchOutcome>>, CoreError> {
            self.outcomes.borrow_mut().remove(0)
        }

        fn recover_pending_completions(&mut self) -> Result<CompletionRecoverySummary, CoreError> {
            self.recovered
                .borrow_mut()
                .pop()
                .unwrap_or_else(|| Ok(CompletionRecoverySummary::default()))
        }

        fn pending_event_count(&self) -> usize {
            0
        }
    }

    fn stub_core(outcomes: Vec<StubOutcome>) -> StubCore {
        StubCore {
            outcomes: Rc::new(RefCell::new(outcomes)),
            recovered: Rc::new(RefCell::new(Vec::new())),
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

        fn completion_recovery_states(&self) -> Result<Vec<PrRunState>, CoreError> {
            Ok(self.states.borrow().clone())
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
                    ensemble_archive_path: None,
                }),
            }
        }
    }

    #[test]
    fn run_loop_stops_cleanly_when_quiet() {
        let mut daemon = Pump19Daemon::new(
            stub_core(vec![Ok(None)]),
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
            stub_core(vec![
                Ok(Some(vec![DispatchOutcome::Launched {
                    rule_id: "review".to_owned(),
                    run_id: pump19_contract::RunId("run-review".to_owned()),
                }])),
                Ok(Some(vec![DispatchOutcome::Launched {
                    rule_id: "judge".to_owned(),
                    run_id: pump19_contract::RunId("run-judge".to_owned()),
                }])),
            ]),
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
    fn run_loop_logs_per_event_errors_and_continues() {
        let mut daemon = Pump19Daemon::new(
            stub_core(vec![
                Err(CoreError::Launcher("agent launch failed".to_owned())),
                Ok(Some(vec![DispatchOutcome::Launched {
                    rule_id: "review".to_owned(),
                    run_id: pump19_contract::RunId("run-review".to_owned()),
                }])),
                Ok(None),
            ]),
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let summary = daemon.run(&[], &NeverShutdown).expect("run daemon");

        assert_eq!(summary.dispatch_errors_continued, 1);
        assert_eq!(summary.events_processed, 1);
        assert_eq!(summary.launches, 1);
        assert_eq!(summary.quiet_polls, 1);
    }

    #[test]
    fn run_loop_treats_store_errors_as_fatal() {
        let mut daemon = Pump19Daemon::new(
            stub_core(vec![Err(CoreError::StateStore(
                "state ledger cannot be trusted".to_owned(),
            ))]),
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let error = daemon
            .run(&[], &NeverShutdown)
            .expect_err("store error should stop daemon");

        assert!(matches!(error, DaemonError::Core(CoreError::StateStore(_))));
    }

    #[test]
    fn run_loop_rederives_completions_before_quiet_polling() {
        let mut daemon = Pump19Daemon::new(
            StubCore {
                outcomes: Rc::new(RefCell::new(vec![
                    Ok(Some(vec![DispatchOutcome::Launched {
                        rule_id: "review".to_owned(),
                        run_id: pump19_contract::RunId("run-review".to_owned()),
                    }])),
                    Ok(None),
                ])),
                recovered: Rc::new(RefCell::new(vec![
                    Ok(CompletionRecoverySummary::default()),
                    Ok(CompletionRecoverySummary {
                        queued: 1,
                        terminal_replays: 1,
                        stale_running_failures: 0,
                        errors_continued: 0,
                    }),
                ])),
            },
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let summary = daemon.run(&[], &NeverShutdown).expect("run daemon");

        assert_eq!(summary.recovered_completion_events, 1);
        assert_eq!(summary.recovered_terminal_events, 1);
        assert_eq!(summary.recovered_stale_running_events, 0);
        assert_eq!(summary.events_processed, 1);
        assert_eq!(summary.quiet_polls, 1);
    }

    #[test]
    fn run_loop_contains_nonfatal_completion_recovery_errors() {
        let mut daemon = Pump19Daemon::new(
            StubCore {
                outcomes: Rc::new(RefCell::new(vec![Ok(None)])),
                recovered: Rc::new(RefCell::new(vec![
                    Ok(CompletionRecoverySummary::default()),
                    Err(CoreError::Launcher("recovery hook failed".to_owned())),
                ])),
            },
            LoopControlConfig {
                poll_interval_ms: 0,
                stop_after_quiet_polls: Some(1),
            },
        );

        let summary = daemon.run(&[], &NeverShutdown).expect("run daemon");

        assert_eq!(summary.recovery_errors_continued, 1);
        assert_eq!(summary.quiet_polls, 1);
    }

    #[test]
    fn daemon_drives_loop_from_polled_pr_without_completion_echo() {
        let cleaned = Rc::new(RefCell::new(0));
        let forge_operations = RecordingForgeOperations::default();
        let comments = Rc::clone(&forge_operations.comments);
        let merges = Rc::clone(&forge_operations.merges);
        let store = SharedStore::default();
        let state_observer = store.clone();
        let polling = PollingForgejoActivitySource::new(
            FakePollingClient {
                polls: VecDeque::from([
                    vec![forgejo_snapshot_without_finish_label()],
                    vec![forgejo_snapshot()],
                ]),
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

        assert_eq!(summary.launches, 6);
        assert_eq!(*cleaned.borrow(), 6);
        assert_eq!(comments.borrow().len(), 1);
        assert_eq!(merges.borrow().len(), 1);
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
    fn runtime_source_preparer_exports_recorded_revision_from_local_bare_repo() {
        let dir = tempdir().expect("temp dir");
        let source = dir.path().join("repo");
        let bare = dir.path().join("repo.git");
        run_git(["init", source.to_str().expect("source path")]);
        fs::write(source.join("README.md"), "first\n").expect("write first file");
        run_git(["-C", source.to_str().expect("source path"), "add", "."]);
        run_git([
            "-C",
            source.to_str().expect("source path"),
            "-c",
            "user.name=Pump 19",
            "-c",
            "user.email=pump19@example.invalid",
            "commit",
            "-m",
            "initial",
        ]);
        fs::write(source.join("README.md"), "second\n").expect("write second file");
        run_git(["-C", source.to_str().expect("source path"), "add", "."]);
        run_git([
            "-C",
            source.to_str().expect("source path"),
            "-c",
            "user.name=Pump 19",
            "-c",
            "user.email=pump19@example.invalid",
            "commit",
            "-m",
            "second",
        ]);
        let head = git_stdout([
            "-C",
            source.to_str().expect("source path"),
            "rev-parse",
            "HEAD",
        ]);
        run_git([
            "clone",
            "--bare",
            source.to_str().expect("source path"),
            bare.to_str().expect("bare path"),
        ]);
        let output_tree = dir
            .path()
            .join("workspace-1-source-prep")
            .join("prepared-tree");
        let script = dir.path().join("prepare-source.sh");
        fs::write(
            &script,
            format!(
                r#"#!/bin/sh
set -eu
cat >/dev/null
mkdir -p "{output_tree}"
git --git-dir="{bare}" archive "{head}" | tar -x -C "{output_tree}"
printf '{{"tree":"{output_tree}","revision":"{head}"}}'
"#,
                output_tree = output_tree.display(),
                bare = bare.display(),
                head = head,
            ),
        )
        .expect("write source prep script");
        make_executable(&script);
        let pack = MechanicalPack {
            schema_version: pump19_adaptations::AdaptationSchemaVersion::current(),
            contract_version: ContractVersion::current(),
            id: "test-mechanics".to_owned(),
            steps: vec![MechanicalStep {
                id: "checkout-source".to_owned(),
                kind: MechanicalStepKind::Checkout,
                execution: MechanicalExecution::Command {
                    program: script.display().to_string(),
                    args: Vec::new(),
                },
                inputs: Vec::new(),
                outputs: Vec::new(),
                extensions: Extensions::new(),
            }],
            extensions: Extensions::new(),
        };
        let mut preparer = runtime_source_preparer(&pack).expect("source preparer");
        let workspace = WorkspaceLease {
            id: "workspace-1".to_owned(),
            root: dir.path().join("workspace"),
            isolation: WorkspaceIsolation {
                isolated: true,
                credential_free: true,
                egress_bounded: true,
                resource_bounded: true,
                ephemeral: true,
            },
        };
        let mut state_extensions = Extensions::new();
        state_extensions.insert(
            "pump19.core.forge_facts".to_owned(),
            serde_json::to_value(contract_facts()).expect("forge facts JSON"),
        );
        let state = PrRunState {
            contract_version: ContractVersion::current(),
            pr: pr(),
            commit_sha: head.clone(),
            current_head_sha: Some(head.clone()),
            pass_index: 1,
            status: pump19_contract::RunStatus::Pending,
            active_run: None,
            run_history: Vec::new(),
            loop_history: Vec::new(),
            superseded_by: None,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            publication: PublicationState::default(),
            ceiling: None,
            extensions: state_extensions,
        };

        let checkout = preparer
            .prepare_source(SourcePreparationRequest {
                run_id: RunId("run-1".to_owned()),
                run_kind: RunKind::Review,
                event: probe_event(),
                state,
                workspace,
                pr: pr(),
                commit_sha: head.clone(),
            })
            .expect("prepare source");

        assert_eq!(checkout.revision, head);
        assert_eq!(
            fs::read_to_string(checkout.tree.join("README.md")).expect("read prepared source"),
            "second\n"
        );
        assert!(!checkout.tree.join(".git").exists());
    }

    #[test]
    fn runtime_source_preparer_threads_previous_tree_through_chained_steps() {
        let dir = tempdir().expect("temp dir");
        let checkout_tree = dir
            .path()
            .join("workspace-1-source-prep")
            .join("checkout-tree");
        let prepared_tree = dir
            .path()
            .join("workspace-1-source-prep")
            .join("prepared-tree");
        let checkout_script = dir.path().join("checkout.sh");
        let prepare_script = dir.path().join("prepare.sh");
        fs::write(
            &checkout_script,
            format!(
                r#"#!/bin/sh
set -eu
cat >/dev/null
mkdir -p "{checkout_tree}"
printf 'checked out\n' > "{checkout_tree}/source.txt"
printf '{{"tree":"{checkout_tree}","revision":"abc123"}}'
"#,
                checkout_tree = checkout_tree.display(),
            ),
        )
        .expect("write checkout script");
        fs::write(
            &prepare_script,
            format!(
                r#"#!/bin/sh
set -eu
input="$(cat)"
case "$input" in
  *'"previous_tree":"{checkout_tree}"'*) ;;
  *) echo "$input" >&2; exit 23 ;;
esac
mkdir -p "{prepared_tree}"
cp "{checkout_tree}/source.txt" "{prepared_tree}/source.txt"
printf 'prepared\n' >> "{prepared_tree}/source.txt"
printf '{{"tree":"{prepared_tree}","revision":"abc123"}}'
"#,
                checkout_tree = checkout_tree.display(),
                prepared_tree = prepared_tree.display(),
            ),
        )
        .expect("write prepare script");
        make_executable(&checkout_script);
        make_executable(&prepare_script);
        let pack = MechanicalPack {
            schema_version: pump19_adaptations::AdaptationSchemaVersion::current(),
            contract_version: ContractVersion::current(),
            id: "test-mechanics".to_owned(),
            steps: vec![
                MechanicalStep {
                    id: "checkout-source".to_owned(),
                    kind: MechanicalStepKind::Checkout,
                    execution: MechanicalExecution::Command {
                        program: checkout_script.display().to_string(),
                        args: Vec::new(),
                    },
                    inputs: Vec::new(),
                    outputs: Vec::new(),
                    extensions: Extensions::new(),
                },
                MechanicalStep {
                    id: "prepare-source".to_owned(),
                    kind: MechanicalStepKind::Prepare,
                    execution: MechanicalExecution::Command {
                        program: prepare_script.display().to_string(),
                        args: Vec::new(),
                    },
                    inputs: Vec::new(),
                    outputs: Vec::new(),
                    extensions: Extensions::new(),
                },
            ],
            extensions: Extensions::new(),
        };
        let mut preparer = runtime_source_preparer(&pack).expect("source preparer");
        let workspace = WorkspaceLease {
            id: "workspace-1".to_owned(),
            root: dir.path().join("workspace"),
            isolation: WorkspaceIsolation {
                isolated: true,
                credential_free: true,
                egress_bounded: true,
                resource_bounded: true,
                ephemeral: true,
            },
        };

        let checkout = preparer
            .prepare_source(SourcePreparationRequest {
                run_id: RunId("run-1".to_owned()),
                run_kind: RunKind::Review,
                event: probe_event(),
                state: pending_state("abc123"),
                workspace: workspace.clone(),
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            })
            .expect("prepare source");

        assert_eq!(checkout.tree, prepared_tree);
        assert_eq!(checkout.revision, "abc123");
        assert_eq!(
            fs::read_to_string(checkout.tree.join("source.txt")).expect("read prepared source"),
            "checked out\nprepared\n"
        );
        assert_eq!(
            checkout.cleanup_root,
            Some(source_preparation_root(&SourcePreparationRequest {
                run_id: RunId("run-1".to_owned()),
                run_kind: RunKind::Review,
                event: probe_event(),
                state: pending_state("abc123"),
                workspace,
                pr: pr(),
                commit_sha: "abc123".to_owned(),
            }))
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
mechanical_pack = "mechanical.toml"
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
        assert!(!config.forgejo.core_applies_finish_label_on_convergence);
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
                RuntimeSourcePreparer,
            >,
        >();
    }

    fn forgejo_snapshot() -> ForgejoPullRequestSnapshot {
        forgejo_snapshot_with_head("head-after-fix", true)
    }

    fn forgejo_snapshot_without_finish_label() -> ForgejoPullRequestSnapshot {
        forgejo_snapshot_with_head("abc123", false)
    }

    fn forgejo_snapshot_with_head(
        head_sha: &str,
        include_finish_label: bool,
    ) -> ForgejoPullRequestSnapshot {
        ForgejoPullRequestSnapshot {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
            head_sha: head_sha.to_owned(),
            base_sha: "def456".to_owned(),
            branch_currency: ForgejoBranchCurrency::Current,
            cleanliness: ForgejoReviewCleanliness::Clean,
            mergeability: ForgejoMergeability::Mergeable,
            labels: include_finish_label
                .then(|| ForgejoLabelApplication {
                    name: "pump19-finish".to_owned(),
                    applied_by: Some(ForgejoActor {
                        id: "pump19-core".to_owned(),
                        display_name: "pump19-core".to_owned(),
                    }),
                })
                .into_iter()
                .collect(),
            actor_permissions: vec![ForgejoActorPermission {
                actor: ForgejoActor {
                    id: "pump19-core".to_owned(),
                    display_name: "pump19-core".to_owned(),
                },
                can_apply_finish_label: true,
                can_merge: true,
            }],
        }
    }

    fn pr() -> pump19_contract::PullRequestRef {
        pump19_contract::PullRequestRef {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
        }
    }

    fn run_git<const N: usize>(args: [&str; N]) {
        let output = Command::new("git").args(args).output().expect("run git");
        assert!(
            output.status.success(),
            "git failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }

    fn git_stdout<const N: usize>(args: [&str; N]) -> String {
        let output = Command::new("git").args(args).output().expect("run git");
        assert!(
            output.status.success(),
            "git failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        String::from_utf8(output.stdout)
            .expect("git stdout is UTF-8")
            .trim()
            .to_owned()
    }

    fn make_executable(path: &Path) {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;

            let mut permissions = fs::metadata(path).expect("script metadata").permissions();
            permissions.set_mode(0o755);
            fs::set_permissions(path, permissions).expect("chmod script");
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

    fn pending_state(head: &str) -> PrRunState {
        let mut state_extensions = Extensions::new();
        state_extensions.insert(
            "pump19.core.forge_facts".to_owned(),
            serde_json::to_value(contract_facts()).expect("forge facts JSON"),
        );
        PrRunState {
            contract_version: ContractVersion::current(),
            pr: pr(),
            commit_sha: head.to_owned(),
            current_head_sha: Some(head.to_owned()),
            pass_index: 1,
            status: pump19_contract::RunStatus::Pending,
            active_run: None,
            run_history: Vec::new(),
            loop_history: Vec::new(),
            superseded_by: None,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            publication: PublicationState::default(),
            ceiling: None,
            extensions: state_extensions,
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
                model: model_for_family(family),
            },
        }
    }

    fn model_for_family(family: &str) -> String {
        match family {
            "glm" => "openrouter/z-ai/glm-4.6".to_owned(),
            _ => format!("{family}-2026-06"),
        }
    }

    fn loop_rules() -> Vec<TriggerRule> {
        vec![
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
                    reviewers: vec![
                        target("reviewer-codex", AgentRole::Reviewer, "codex"),
                        target("reviewer-claude", AgentRole::Reviewer, "claude"),
                    ],
                    fixers: Vec::new(),
                    judge: Some(target("judge-glm", AgentRole::Judge, "glm")),
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
                    judge: Some(target("judge-glm", AgentRole::Judge, "glm")),
                    finishers: Vec::new(),
                },
            },
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
                    judge: Some(target("judge-glm-noop", AgentRole::Judge, "glm")),
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
                            state: StateCriterion::HasConverged,
                        },
                        Criteria::State {
                            state: StateCriterion::CleanAndCurrent,
                        },
                    ],
                },
                agent_plan: AgentPlan {
                    reviewers: Vec::new(),
                    fixers: Vec::new(),
                    judge: None,
                    finishers: vec![target("finisher-codex", AgentRole::Finish, "codex")],
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
                ensemble_archive_path: None,
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
            ensemble_archive_path: None,
        }
    }
}
