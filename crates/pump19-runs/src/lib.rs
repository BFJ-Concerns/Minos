#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "unit tests use small fakes and direct fixture assertions"
    )
)]

use std::{
    collections::{BTreeMap, BTreeSet},
    env, fs,
    path::{Path, PathBuf},
    process::Command,
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant},
};

use pump19_contract::{
    AgentId, AgentRole, CertaintyClass, Confidence, ContractVersion, Decision, DecisionSubject,
    DecisionVerdict, Extensions, Finding, FindingId, FindingLocation, ForgeFacts, ModelProvenance,
    Patch, PatchChange, PatchId, RunId, RunKind, RunOutcome, Severity,
};
use pump19_core::{
    AgentLaunchSpec, CoreError, PreparedAgent, RunLaunchOutcome, RunLaunchRequest, RunLauncher,
    WorkspaceExecutor,
};
use pump19_judgement::{
    IntentStatement, JudgementBrief, JudgementBriefResult, JudgementRun, JudgementStatus,
    ReviewerResult, evidence_text,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use thiserror::Error;

const EXT_FORGE_FACTS: &str = "pump19.core.forge_facts";
const EXT_RAW_STDOUT: &str = "pump19.runs.raw_stdout";
const EXT_RAW_STDERR: &str = "pump19.runs.raw_stderr";
const EXT_MODEL_FAMILY: &str = "pump19.runs.model_family";
const EXT_AGENT_ENGINE: &str = "pump19.core.agent_engine";
const REVIEW_EVIDENCE_DIR: &str = ".pump19/review";
const REVIEW_DIFF_FILE: &str = "diff.patch";
const ENSEMBLE_ENV_EXACT_ALLOWLIST: &[&str] = &[
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_MODEL",
    "AWS_ACCESS_KEY_ID",
    "AWS_DEFAULT_REGION",
    "AWS_PROFILE",
    "AWS_REGION",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_SESSION_TOKEN",
    "AZURE_OPENAI_API_KEY",
    "AZURE_OPENAI_ENDPOINT",
    "AZURE_OPENAI_API_VERSION",
    "CLAUDE_CODE_OAUTH_TOKEN",
    "CODEX_HOME",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "GOOGLE_APPLICATION_CREDENTIALS",
    "GOOGLE_CLOUD_PROJECT",
    "GOOGLE_GENERATIVE_AI_API_KEY",
    "GOOGLE_VERTEX_LOCATION",
    "GROQ_API_KEY",
    "HOME",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "LOGNAME",
    "MISTRAL_API_KEY",
    "NODE_EXTRA_CA_CERTS",
    "OPENAI_API_BASE",
    "OPENAI_API_KEY",
    "OPENAI_BASE_URL",
    "OPENAI_ORG_ID",
    "OPENAI_ORGANIZATION",
    "OPENAI_PROJECT",
    "OPENROUTER_API_KEY",
    "OPENCODE_CONFIG_DIR",
    "PATH",
    "SHELL",
    "SSL_CERT_DIR",
    "SSL_CERT_FILE",
    "TEMP",
    "TMP",
    "TMPDIR",
    "USER",
    "XAI_API_KEY",
    "XDG_CACHE_HOME",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_STATE_HOME",
];

/// Errors raised while preparing sessions or executing run bodies.
#[derive(Debug, Error)]
pub enum RunBodyError {
    #[error("session preparation failed: {0}")]
    Session(String),
    #[error("required model family is unavailable: {0}")]
    RequiredFamilyUnavailable(String),
    #[error("judgement failed: {0}")]
    Judgement(#[from] pump19_judgement::JudgementError),
    #[error("missing provenance for role {0:?}")]
    MissingRoleProvenance(AgentRole),
    #[error("missing reviewer provenance for agent {0:?}")]
    MissingReviewerProvenance(String),
    #[error("state does not contain normalised forge facts")]
    MissingForgeFacts,
    #[error("invalid normalised forge facts: {0}")]
    InvalidForgeFacts(#[source] serde_json::Error),
    #[error("merge readiness check failed to execute: {0}")]
    MergeReadiness(String),
    #[error("ensemble workflow failed: {0}")]
    Ensemble(String),
    #[error("ensemble workflow returned invalid JSON: {0}")]
    EnsembleJson(#[source] serde_json::Error),
    #[error("ensemble archive is absent at {0}")]
    MissingEnsembleArchive(String),
    #[error("ensemble archive is ambiguous under {0}")]
    AmbiguousEnsembleArchive(String),
    #[error("ensemble archive is malformed at {path}: {source}")]
    MalformedEnsembleArchive {
        path: String,
        #[source]
        source: serde_json::Error,
    },
    #[error("ensemble archive does not prove the authorised lineup: {0}")]
    EnsembleArchiveMismatch(String),
    #[error("prompt template failed: {0}")]
    PromptTemplate(String),
    #[error("review evidence is missing: {0}")]
    MissingReviewEvidence(String),
}

/// Prepares a concrete agent session for a core-owned launch target.
pub trait AgentSessionPreparer {
    /// Prepares one agent session.
    ///
    /// # Errors
    ///
    /// Returns an error when the control plane cannot allocate or bind the requested
    /// session.
    fn prepare(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, RunBodyError>;
}

/// Executes a review run after the core has passed the launch gate.
pub trait ReviewRunBody {
    /// Runs review and returns contract findings.
    ///
    /// # Errors
    ///
    /// Returns an error when review cannot execute or its output cannot be mapped
    /// onto contract findings.
    fn run_review(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Finding>, RunBodyError>;

    fn last_ensemble_archive_path(&self) -> Option<String> {
        None
    }
}

/// Executes a significance-judge run after the core has passed the launch gate.
pub trait JudgeRunBody {
    /// Rates findings as material or minor.
    ///
    /// # Errors
    ///
    /// Returns an error when judging cannot execute or decisions cannot be built.
    fn run_judge(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Decision>, RunBodyError>;

    fn last_ensemble_archive_path(&self) -> Option<String> {
        None
    }
}

/// Executes a fix run after the core has passed the launch gate.
pub trait FixRunBody {
    /// Produces patches answering material findings.
    ///
    /// # Errors
    ///
    /// Returns an error when fixing cannot execute or patch artefacts cannot be built.
    fn run_fix(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Patch>, RunBodyError>;

    fn last_ensemble_archive_path(&self) -> Option<String> {
        None
    }
}

/// Executes a finish run after the core has passed the launch gate.
pub trait FinishRunBody {
    /// Determines whether the finish action can proceed from contract facts.
    ///
    /// # Errors
    ///
    /// Returns an error when required forge facts are absent or malformed.
    fn run_finish(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunOutcome, RunBodyError>;

    fn last_ensemble_archive_path(&self) -> Option<String> {
        None
    }
}

/// Runs a versioned ensemble workflow on the host and returns its JSON result.
pub trait EnsembleWorkflowRunner {
    /// Executes one workflow invocation.
    ///
    /// # Errors
    ///
    /// Returns an error when the workflow cannot start, times out, fails, or emits
    /// output that cannot be reconciled by the caller.
    fn run_workflow(
        &mut self,
        request: EnsembleWorkflowRequest,
    ) -> Result<EnsembleWorkflowOutput, RunBodyError>;
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EnsembleWorkflowConfig {
    pub script: PathBuf,
    pub archive_root: PathBuf,
    pub timeout_ms: u64,
    pub prompt_template: String,
    pub briefs: Vec<JudgementBrief>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EnsembleWorkflowRequest {
    pub script: PathBuf,
    pub args: Value,
    pub archive_dir: PathBuf,
    pub timeout_ms: u64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EnsembleWorkflowOutput {
    pub value: Value,
    pub archive_dir: PathBuf,
}

/// Site-side description of the repository under review.
///
/// This is operator configuration, not subject-repository state. A bare
/// repository gets a neutral fallback from its `owner/repo` identity.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct SubjectIntent {
    pub slug: String,
    pub name: String,
    pub purpose: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub invariants: Vec<IntentStatement>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub behaviours: Vec<IntentStatement>,
}

impl SubjectIntent {
    #[must_use]
    pub fn neutral_for_repository(repository: &str) -> Self {
        Self {
            slug: repository.to_owned(),
            name: repository.to_owned(),
            purpose: format!(
                "Review changes to {repository} for correctness, safety, maintainability, and alignment with the judgement brief."
            ),
            invariants: Vec::new(),
            behaviours: Vec::new(),
        }
    }
}

/// Thin adapter for the prebuilt ensemble launcher.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct HostEnsembleWorkflowRunner {
    node_program: PathBuf,
    launcher_path: PathBuf,
}

impl HostEnsembleWorkflowRunner {
    #[must_use]
    pub fn new(node_program: impl Into<PathBuf>, launcher_path: impl Into<PathBuf>) -> Self {
        Self {
            node_program: node_program.into(),
            launcher_path: launcher_path.into(),
        }
    }
}

impl EnsembleWorkflowRunner for HostEnsembleWorkflowRunner {
    fn run_workflow(
        &mut self,
        request: EnsembleWorkflowRequest,
    ) -> Result<EnsembleWorkflowOutput, RunBodyError> {
        fs::create_dir_all(&request.archive_dir)
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let wrapper =
            TemporaryWorkflowScript::write(&request.script, &request.args, &request.archive_dir)?;
        let mut child = Command::new(&self.node_program)
            .env_clear()
            .envs(filtered_ensemble_env(env::vars()))
            .arg(&self.launcher_path)
            .arg("--timeout")
            .arg(request.timeout_ms.to_string())
            .arg(wrapper.path())
            .env("ENSEMBLE_RUN_RECORD_DIR", &request.archive_dir)
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let deadline = Instant::now() + Duration::from_millis(request.timeout_ms);
        loop {
            if let Some(_status) = child
                .try_wait()
                .map_err(|error| RunBodyError::Ensemble(error.to_string()))?
            {
                break;
            }
            if Instant::now() >= deadline {
                child
                    .kill()
                    .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
                let _ = child.wait();
                return Err(RunBodyError::Ensemble(format!(
                    "launcher exceeded hard timeout of {} ms and was killed",
                    request.timeout_ms
                )));
            }
            thread::sleep(Duration::from_millis(10));
        }
        let output = child
            .wait_with_output()
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let stdout = String::from_utf8(output.stdout)
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let stderr = String::from_utf8(output.stderr)
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        if !output.status.success() {
            return Err(RunBodyError::Ensemble(format!(
                "launcher exited with {:?}: {stderr}",
                output.status.code()
            )));
        }
        let value = serde_json::from_str(&stdout).map_err(RunBodyError::EnsembleJson)?;
        Ok(EnsembleWorkflowOutput {
            value,
            archive_dir: request.archive_dir,
        })
    }
}

#[derive(Debug)]
struct TemporaryWorkflowScript {
    path: PathBuf,
}

impl TemporaryWorkflowScript {
    fn write(script: &Path, args: &Value, run_dir: &Path) -> Result<Self, RunBodyError> {
        static COUNTER: AtomicU64 = AtomicU64::new(0);

        let source = fs::read_to_string(script).map_err(|error| {
            RunBodyError::Ensemble(format!(
                "read workflow script {}: {error}",
                script.display()
            ))
        })?;
        let (meta, body) = split_workflow_source(&source)?;
        let args_json = serde_json::to_string(args).map_err(RunBodyError::EnsembleJson)?;
        let args_literal = serde_json::to_string(&args_json).map_err(RunBodyError::EnsembleJson)?;
        let wrapper = format!(
            "export const meta = {meta};\nconst args = JSON.parse({args_literal});\n{body}"
        );
        let path = run_dir.join(format!(
            ".pump19-workflow-{}-{}.js",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::Relaxed)
        ));
        fs::write(&path, wrapper).map_err(|error| {
            RunBodyError::Ensemble(format!(
                "write workflow wrapper {}: {error}",
                path.display()
            ))
        })?;
        Ok(Self { path })
    }

    fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for TemporaryWorkflowScript {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}

fn split_workflow_source(source: &str) -> Result<(&str, &str), RunBodyError> {
    let source = source.strip_prefix('\u{feff}').unwrap_or(source);
    let trimmed = source.trim_start();
    let leading_whitespace = source.len() - trimmed.len();
    let source = &source[leading_whitespace..];
    let Some(after_export) = source.strip_prefix("export const meta") else {
        return Err(RunBodyError::Ensemble(
            "workflow script must begin with `export const meta`".to_owned(),
        ));
    };
    let equals_offset = source.len() - after_export.len()
        + after_export.find('=').ok_or_else(|| {
            RunBodyError::Ensemble("workflow meta export is missing `=`".to_owned())
        })?;
    let object_start = source[equals_offset + 1..]
        .find('{')
        .map(|offset| equals_offset + 1 + offset)
        .ok_or_else(|| {
            RunBodyError::Ensemble("workflow meta is missing object literal".to_owned())
        })?;
    let object_end = find_balanced_object_end(source, object_start)?;
    let mut body_start = object_end + 1;
    while source[body_start..]
        .chars()
        .next()
        .is_some_and(char::is_whitespace)
    {
        body_start += source[body_start..]
            .chars()
            .next()
            .map_or(0, char::len_utf8);
    }
    if source[body_start..].starts_with(';') {
        body_start += 1;
    }
    Ok((&source[object_start..=object_end], &source[body_start..]))
}

fn find_balanced_object_end(source: &str, start: usize) -> Result<usize, RunBodyError> {
    let mut depth = 0_u32;
    let mut index = start;
    while index < source.len() {
        let Some(ch) = source[index..].chars().next() else {
            break;
        };
        match ch {
            '{' | '[' | '(' => {
                depth += 1;
                index += ch.len_utf8();
            }
            '}' | ']' | ')' => {
                depth = depth.checked_sub(1).ok_or_else(|| {
                    RunBodyError::Ensemble("workflow meta object closed early".to_owned())
                })?;
                if depth == 0 {
                    if ch == '}' {
                        return Ok(index);
                    }
                    return Err(RunBodyError::Ensemble(
                        "workflow meta object closed with the wrong delimiter".to_owned(),
                    ));
                }
                index += ch.len_utf8();
            }
            '"' | '\'' => {
                index = skip_quoted_string(source, index, ch)?;
            }
            '`' => {
                index = skip_template_literal(source, index)?;
            }
            _ => {
                index += ch.len_utf8();
            }
        }
    }
    Err(RunBodyError::Ensemble(
        "workflow meta object is not closed".to_owned(),
    ))
}

fn skip_quoted_string(source: &str, start: usize, quote: char) -> Result<usize, RunBodyError> {
    let mut escaped = false;
    let mut index = start + quote.len_utf8();
    while index < source.len() {
        let Some(ch) = source[index..].chars().next() else {
            break;
        };
        index += ch.len_utf8();
        if escaped {
            escaped = false;
        } else if ch == '\\' {
            escaped = true;
        } else if ch == quote {
            return Ok(index);
        }
    }
    Err(RunBodyError::Ensemble(
        "workflow meta string literal is not closed".to_owned(),
    ))
}

fn skip_template_literal(source: &str, start: usize) -> Result<usize, RunBodyError> {
    let mut escaped = false;
    let mut index = start + 1;
    while index < source.len() {
        let Some(ch) = source[index..].chars().next() else {
            break;
        };
        index += ch.len_utf8();
        if escaped {
            escaped = false;
        } else if ch == '\\' {
            escaped = true;
        } else if ch == '`' {
            return Ok(index);
        }
    }
    Err(RunBodyError::Ensemble(
        "workflow meta template literal is not closed".to_owned(),
    ))
}

fn filtered_ensemble_env(
    vars: impl IntoIterator<Item = (String, String)>,
) -> BTreeMap<String, String> {
    vars.into_iter()
        .filter(|(name, _value)| ensemble_env_allowed(name))
        .collect()
}

fn ensemble_env_allowed(name: &str) -> bool {
    ENSEMBLE_ENV_EXACT_ALLOWLIST.contains(&name)
}

/// `RunLauncher` implementation composed from narrow, testable run-body seams.
#[derive(Debug)]
pub struct Pump19RunLauncher<S, R, J, F, N> {
    sessions: S,
    review: R,
    judge: J,
    fix: F,
    finish: N,
}

impl<S, R, J, F, N> Pump19RunLauncher<S, R, J, F, N> {
    #[must_use]
    pub const fn new(sessions: S, review: R, judge: J, fix: F, finish: N) -> Self {
        Self {
            sessions,
            review,
            judge,
            fix,
            finish,
        }
    }
}

impl<S, R, J, F, N> RunLauncher for Pump19RunLauncher<S, R, J, F, N>
where
    S: AgentSessionPreparer,
    R: ReviewRunBody,
    J: JudgeRunBody,
    F: FixRunBody,
    N: FinishRunBody,
{
    fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
        let target = spec.target.clone();
        self.sessions.prepare(spec).map_err(|error| match error {
            RunBodyError::RequiredFamilyUnavailable(reason) => {
                CoreError::RequiredFamilyUnavailable {
                    agent_id: target.agent_id,
                    family: target.lineage.family,
                    reason,
                }
            }
            error => CoreError::Launcher(error.to_string()),
        })
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        let result = match request.run_kind {
            RunKind::Review => self.review.run_review(&request, workspace).map(|findings| {
                let ensemble_archive_path = self.review.last_ensemble_archive_path();
                RunLaunchOutcome {
                    outcome: RunOutcome::Succeeded,
                    findings,
                    decisions: Vec::new(),
                    patches: Vec::new(),
                    token_usage: None,
                    ensemble_archive_path,
                }
            }),
            RunKind::Judge => {
                self.judge
                    .run_judge(&request, workspace)
                    .and_then(|mut decisions| {
                        if decisions.is_empty() && request.state.findings.is_empty() {
                            let provenance = provenance_for_role(&request, AgentRole::Judge)?;
                            decisions.push(convergence_decision(&request, &provenance));
                        }
                        Ok(RunLaunchOutcome {
                            outcome: RunOutcome::Succeeded,
                            findings: Vec::new(),
                            decisions,
                            patches: Vec::new(),
                            token_usage: None,
                            ensemble_archive_path: self.judge.last_ensemble_archive_path(),
                        })
                    })
            }
            RunKind::Fix => self.fix.run_fix(&request, workspace).map(|patches| {
                let ensemble_archive_path = self.fix.last_ensemble_archive_path();
                RunLaunchOutcome {
                    outcome: if patches.is_empty() {
                        RunOutcome::NoOp
                    } else {
                        RunOutcome::Succeeded
                    },
                    findings: Vec::new(),
                    decisions: Vec::new(),
                    patches,
                    token_usage: None,
                    ensemble_archive_path,
                }
            }),
            RunKind::Finish => {
                self.finish
                    .run_finish(&request, workspace)
                    .map(|outcome| RunLaunchOutcome {
                        outcome,
                        findings: Vec::new(),
                        decisions: Vec::new(),
                        patches: Vec::new(),
                        token_usage: None,
                        ensemble_archive_path: self.finish.last_ensemble_archive_path(),
                    })
            }
        };
        result.map_err(|error| CoreError::Launcher(error.to_string()))
    }
}

/// Review body backed by a host-side ensemble workflow.
#[derive(Clone, Debug)]
pub struct EnsembleReviewBody<R> {
    runner: R,
    config: EnsembleWorkflowConfig,
    subject_intents: BTreeMap<String, SubjectIntent>,
    last_archive_path: Option<String>,
}

impl<R> EnsembleReviewBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self::with_subject_intents(runner, config, BTreeMap::new())
    }

    #[must_use]
    pub const fn with_subject_intents(
        runner: R,
        config: EnsembleWorkflowConfig,
        subject_intents: BTreeMap<String, SubjectIntent>,
    ) -> Self {
        Self {
            runner,
            config,
            subject_intents,
            last_archive_path: None,
        }
    }

    fn subject_for_repository(&self, repository: &str) -> SubjectIntent {
        self.subject_intents
            .get(repository)
            .cloned()
            .unwrap_or_else(|| SubjectIntent::neutral_for_repository(repository))
    }
}

impl<R> ReviewRunBody for EnsembleReviewBody<R>
where
    R: EnsembleWorkflowRunner,
{
    fn run_review(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Finding>, RunBodyError> {
        self.last_archive_path = None;
        let _ = workspace;
        let subject = self.subject_for_repository(&request.state.pr.repository);
        let review_evidence = load_review_evidence(&request.workspace.root)?;
        let brief_inputs = self
            .config
            .briefs
            .iter()
            .map(|brief| {
                let evidence = review_evidence_text(&review_evidence, brief)?;
                let prompt = render_prompt_template(
                    &self.config.prompt_template,
                    &[
                        ("brief_id", brief.id.clone()),
                        ("brief_title", brief.title.clone()),
                        ("brief_body", brief.brief.clone()),
                        ("subject_name", subject.name.clone()),
                        ("subject_slug", subject.slug.clone()),
                        ("subject_purpose", subject.purpose.clone()),
                        ("evidence", evidence.clone()),
                    ],
                )?;
                Ok(json!({
                    "id": brief.id,
                    "title": brief.title,
                    "prompt": prompt,
                    "brief": brief.brief,
                    "evidence": evidence,
                }))
            })
            .collect::<Result<Vec<_>, RunBodyError>>()?;
        let reviewers = expected_targets(request, AgentRole::Reviewer)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "evidence": {
                "workspace_root": review_evidence.workspace_root,
                "diff_path": review_evidence.diff_path,
                "diff": review_evidence.diff,
            },
            "reviewers": reviewers,
            "subject": subject,
            "briefs": brief_inputs,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &reviewers)?;
        self.last_archive_path = Some(output.archive_dir.display().to_string());
        let run = serde_json::from_value::<JudgementRun>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        findings_from_judgement(request, &run)
    }

    fn last_ensemble_archive_path(&self) -> Option<String> {
        self.last_archive_path.clone()
    }
}

/// Significance judge body backed by a host-side ensemble workflow.
#[derive(Clone, Debug)]
pub struct EnsembleJudgeBody<R> {
    runner: R,
    config: EnsembleWorkflowConfig,
    last_archive_path: Option<String>,
}

impl<R> EnsembleJudgeBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self {
            runner,
            config,
            last_archive_path: None,
        }
    }
}

impl<R> JudgeRunBody for EnsembleJudgeBody<R>
where
    R: EnsembleWorkflowRunner,
{
    fn run_judge(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Decision>, RunBodyError> {
        self.last_archive_path = None;
        let _ = workspace;
        let targets = expected_targets(request, AgentRole::Judge)?;
        let provenance = provenance_for_role(request, AgentRole::Judge)?;
        let judge_findings = judge_findings(request);
        let loop_context = json!({
            "pass_count": request.state.pass_index,
            "prior_verdicts": prior_judge_verdicts(request),
            "fix_outcomes": prior_fix_outcomes(request),
            "loop_history": request.state.loop_history,
        });
        let prompt = render_prompt_template(
            &self.config.prompt_template,
            &[
                ("run_id", request.run_id.0.clone()),
                ("commit_sha", request.state.commit_sha.clone()),
                ("findings", prompt_json(&judge_findings)?),
                ("loop_context", prompt_json(&loop_context)?),
            ],
        )?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "judges": targets,
            "prompt": prompt,
            "pass_count": request.state.pass_index,
            "current_findings": judge_findings,
            "findings": judge_findings,
            "prior_verdicts": prior_judge_verdicts(request),
            "fix_outcomes": prior_fix_outcomes(request),
            "loop_history": request.state.loop_history,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &targets)?;
        self.last_archive_path = Some(output.archive_dir.display().to_string());
        let outputs = serde_json::from_value::<Vec<JudgeDecisionOutput>>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        if outputs.is_empty() && judge_findings.is_empty() {
            return Ok(vec![convergence_decision(request, &provenance)]);
        }
        if outputs.is_empty() {
            return Err(RunBodyError::Ensemble(
                "judge returned no decisions for standing findings".to_owned(),
            ));
        }
        Ok(outputs
            .into_iter()
            .map(|output| output.into_decision(&request.run_id, &provenance))
            .collect())
    }

    fn last_ensemble_archive_path(&self) -> Option<String> {
        self.last_archive_path.clone()
    }
}

/// Fix body backed by a host-side ensemble workflow.
#[derive(Clone, Debug)]
pub struct EnsembleFixBody<R> {
    runner: R,
    config: EnsembleWorkflowConfig,
    last_archive_path: Option<String>,
}

impl<R> EnsembleFixBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self {
            runner,
            config,
            last_archive_path: None,
        }
    }
}

impl<R> FixRunBody for EnsembleFixBody<R>
where
    R: EnsembleWorkflowRunner,
{
    fn run_fix(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Patch>, RunBodyError> {
        self.last_archive_path = None;
        let _ = workspace;
        let findings = material_findings(request);
        if findings.is_empty() {
            return Ok(Vec::new());
        }
        let targets = expected_targets(request, AgentRole::Fixer)?;
        let provenance = provenance_for_role(request, AgentRole::Fixer)?;
        let prompt = render_prompt_template(
            &self.config.prompt_template,
            &[
                ("run_id", request.run_id.0.clone()),
                ("commit_sha", request.state.commit_sha.clone()),
                ("material_findings", prompt_json(&findings)?),
            ],
        )?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "fixers": targets,
            "prompt": prompt,
            "material_findings": findings,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &targets)?;
        self.last_archive_path = Some(output.archive_dir.display().to_string());
        let change = serde_json::from_value::<PatchChange>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        Ok(vec![patch_from_change(
            request, findings, change, provenance,
        )])
    }

    fn last_ensemble_archive_path(&self) -> Option<String> {
        self.last_archive_path.clone()
    }
}

/// Finish body that consumes the contract merge gate and performs no forge side effect.
#[derive(Clone, Copy, Debug, Default)]
pub struct MergeGateFinishBody;

impl FinishRunBody for MergeGateFinishBody {
    fn run_finish(
        &mut self,
        request: &RunLaunchRequest,
        _workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunOutcome, RunBodyError> {
        let facts = forge_facts(request)?;
        if pump19_contract::merge_gate_clean_and_current(&facts) {
            Ok(RunOutcome::Succeeded)
        } else {
            Ok(RunOutcome::Failed)
        }
    }
}

/// The verdict of a trusted host-side merge-readiness verification.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum MergeReadiness {
    Ready,
    NotReady { reason: String },
}

/// Trusted host-side verification that a converged PR is actually ready to
/// merge — conflict probe against the live base, build, tests — before the
/// core performs the merge.
///
/// A failed *verdict* is `Ok(NotReady)`; `Err` means the check itself could
/// not execute.
pub trait MergeReadinessCheck {
    /// Verifies the PR named by the request.
    ///
    /// # Errors
    ///
    /// Returns an error when the verification cannot execute at all.
    fn verify(&mut self, request: &RunLaunchRequest) -> Result<MergeReadiness, RunBodyError>;
}

/// The absent check: no readiness step is configured, so the merge gate alone
/// decides.
impl<C: MergeReadinessCheck> MergeReadinessCheck for Option<C> {
    fn verify(&mut self, request: &RunLaunchRequest) -> Result<MergeReadiness, RunBodyError> {
        self.as_mut()
            .map_or(Ok(MergeReadiness::Ready), |check| check.verify(request))
    }
}

/// Finish body that applies the contract merge gate and then a configured
/// merge-readiness verification. The gate is checked first so an unclean or
/// stale PR never pays for a build.
#[derive(Clone, Copy, Debug, Default)]
pub struct VerifiedMergeGateFinishBody<C> {
    check: C,
}

impl<C> VerifiedMergeGateFinishBody<C> {
    pub const fn new(check: C) -> Self {
        Self { check }
    }
}

impl<C: MergeReadinessCheck> FinishRunBody for VerifiedMergeGateFinishBody<C> {
    fn run_finish(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunOutcome, RunBodyError> {
        if MergeGateFinishBody.run_finish(request, workspace)? != RunOutcome::Succeeded {
            return Ok(RunOutcome::Failed);
        }
        match self.check.verify(request)? {
            MergeReadiness::Ready => Ok(RunOutcome::Succeeded),
            MergeReadiness::NotReady { .. } => Ok(RunOutcome::Failed),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
struct JudgeDecisionOutput {
    finding_id: FindingId,
    verdict: DecisionVerdict,
    rationale: String,
}

impl JudgeDecisionOutput {
    fn into_decision(self, run_id: &RunId, provenance: &ModelProvenance) -> Decision {
        let finding_id = self.finding_id;
        Decision {
            contract_version: ContractVersion::current(),
            id: stable_id("decision", [run_id.0.as_str(), finding_id.0.as_str()]),
            subject: DecisionSubject::Finding { finding_id },
            verdict: self.verdict,
            rationale: self.rationale,
            provenance: provenance.clone(),
            extensions: BTreeMap::new(),
        }
    }
}

fn convergence_decision(request: &RunLaunchRequest, provenance: &ModelProvenance) -> Decision {
    Decision {
        contract_version: ContractVersion::current(),
        id: stable_id("decision-converged", [request.run_id.0.as_str()]),
        subject: DecisionSubject::FindingSet {
            finding_ids: Vec::new(),
        },
        verdict: DecisionVerdict::Converged,
        rationale: "No material findings remain for the current pass.".to_owned(),
        provenance: provenance.clone(),
        extensions: BTreeMap::new(),
    }
}

fn prior_judge_verdicts(request: &RunLaunchRequest) -> Vec<DecisionVerdict> {
    request
        .state
        .loop_history
        .iter()
        .filter_map(|pass| pass.judge_verdict)
        .collect()
}

fn prior_fix_outcomes(request: &RunLaunchRequest) -> Vec<RunOutcome> {
    request
        .state
        .loop_history
        .iter()
        .filter_map(|pass| pass.fix_outcome)
        .collect()
}

fn judge_findings(request: &RunLaunchRequest) -> Vec<Finding> {
    if !request.state.findings.is_empty() {
        return request.state.findings.clone();
    }
    request
        .state
        .loop_history
        .iter()
        .rev()
        .find(|pass| pass.fix_outcome == Some(RunOutcome::NoOp) && !pass.findings.is_empty())
        .map(|pass| pass.findings.clone())
        .unwrap_or_default()
}

fn findings_from_judgement(
    request: &RunLaunchRequest,
    run: &JudgementRun,
) -> Result<Vec<Finding>, RunBodyError> {
    let mut findings = Vec::new();
    for brief in &run.briefs {
        for review in &brief.reviews {
            if review.status == JudgementStatus::Failed {
                findings.push(finding_from_review(request, brief, review)?);
            }
        }
    }
    Ok(findings)
}

fn finding_from_review(
    request: &RunLaunchRequest,
    brief: &JudgementBriefResult,
    review: &ReviewerResult,
) -> Result<Finding, RunBodyError> {
    let provenance = reviewer_provenance(request, &review.agent_id)?.clone();
    let summary = first_line(&review.stdout)
        .or_else(|| first_line(&review.stderr))
        .unwrap_or_else(|| format!("Judgement brief {} failed.", brief.brief_id));
    let dedup_key = finding_dedup_key(brief);
    let mut extensions = Extensions::new();
    extensions.insert(
        EXT_RAW_STDOUT.to_owned(),
        Value::String(review.stdout.clone()),
    );
    extensions.insert(
        EXT_RAW_STDERR.to_owned(),
        Value::String(review.stderr.clone()),
    );
    extensions.insert(
        EXT_MODEL_FAMILY.to_owned(),
        Value::String(review.model_family.clone()),
    );

    Ok(Finding {
        contract_version: ContractVersion::current(),
        id: FindingId(stable_id(
            "finding",
            [
                request.run_id.0.as_str(),
                brief.brief_id.as_str(),
                review.agent_id.as_str(),
            ],
        )),
        dedup_key,
        source_brief: brief.brief_id.clone(),
        dimension: "judgement".to_owned(),
        summary,
        severity: Severity::Medium,
        confidence: Confidence::Medium,
        certainty: CertaintyClass::Advisory,
        provenance,
        locations: vec![FindingLocation::General {
            description: format!("Judgement brief {} failed.", brief.brief_id),
        }],
        extensions,
    })
}

fn finding_dedup_key(brief: &JudgementBriefResult) -> String {
    stable_id("judgement", [brief.brief_id.as_str(), "general"])
}

fn material_findings(request: &RunLaunchRequest) -> Vec<&Finding> {
    request
        .state
        .decisions
        .iter()
        .filter(|decision| decision.verdict == DecisionVerdict::Material)
        .filter_map(|decision| match &decision.subject {
            DecisionSubject::Finding { finding_id } => request
                .state
                .findings
                .iter()
                .find(|finding| finding.id == *finding_id),
            DecisionSubject::FindingSet { finding_ids } => request
                .state
                .findings
                .iter()
                .find(|finding| finding_ids.contains(&finding.id)),
        })
        .collect()
}

fn patch_from_change(
    request: &RunLaunchRequest,
    findings: Vec<&Finding>,
    change: PatchChange,
    provenance: ModelProvenance,
) -> Patch {
    let finding_ids = findings
        .into_iter()
        .map(|finding| finding.id.clone())
        .collect::<Vec<_>>();
    let idempotency_key = stable_id(
        "patch",
        [
            request.run_id.0.as_str(),
            request.state.commit_sha.as_str(),
            &finding_ids
                .iter()
                .map(|id| id.0.as_str())
                .collect::<Vec<_>>()
                .join(","),
        ],
    );
    Patch {
        contract_version: ContractVersion::current(),
        id: PatchId(idempotency_key.clone()),
        run_id: request.run_id.clone(),
        commit_sha: request.state.commit_sha.clone(),
        idempotency_key,
        answers_findings: finding_ids,
        change,
        provenance,
        extensions: BTreeMap::new(),
    }
}

fn run_ensemble_workflow(
    runner: &mut dyn EnsembleWorkflowRunner,
    config: &EnsembleWorkflowConfig,
    request: &RunLaunchRequest,
    args: Value,
) -> Result<EnsembleWorkflowOutput, RunBodyError> {
    let archive_dir = config
        .archive_root
        .join(safe_path_segment(&request.run_id.0));
    runner.run_workflow(EnsembleWorkflowRequest {
        script: config.script.clone(),
        args,
        archive_dir,
        timeout_ms: config.timeout_ms,
    })
}

fn render_prompt_template(
    template: &str,
    variables: &[(&str, String)],
) -> Result<String, RunBodyError> {
    let variable_map = variables
        .iter()
        .map(|(name, value)| (*name, value.as_str()))
        .collect::<BTreeMap<_, _>>();
    let mut rendered = String::with_capacity(template.len());
    let mut remaining = template;

    while let Some(start) = remaining.find("{{") {
        rendered.push_str(&remaining[..start]);
        let after_start = &remaining[start + 2..];
        let Some(end) = after_start.find("}}") else {
            return Err(RunBodyError::PromptTemplate(
                "unclosed {{placeholder}} in prompt template".to_owned(),
            ));
        };
        let name = after_start[..end].trim();
        let Some(value) = variable_map.get(name) else {
            return Err(RunBodyError::PromptTemplate(format!(
                "unknown {{{{{name}}}}} placeholder in prompt template"
            )));
        };
        rendered.push_str(value);
        remaining = &after_start[end + 2..];
    }
    rendered.push_str(remaining);

    Ok(rendered)
}

fn prompt_json(value: &impl Serialize) -> Result<String, RunBodyError> {
    serde_json::to_string_pretty(value).map_err(RunBodyError::EnsembleJson)
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct ReviewEvidence {
    workspace_root: PathBuf,
    diff_path: PathBuf,
    diff: String,
}

fn load_review_evidence(workspace_root: &Path) -> Result<ReviewEvidence, RunBodyError> {
    if !workspace_root.is_dir() {
        return Err(RunBodyError::MissingReviewEvidence(format!(
            "prepared workspace tree is absent at {}",
            workspace_root.display()
        )));
    }
    let diff_path = workspace_root
        .join(REVIEW_EVIDENCE_DIR)
        .join(REVIEW_DIFF_FILE);
    let diff = fs::read_to_string(&diff_path).map_err(|error| {
        RunBodyError::MissingReviewEvidence(format!(
            "PR diff could not be read from {}: {error}",
            diff_path.display()
        ))
    })?;
    if diff.trim().is_empty() {
        return Err(RunBodyError::MissingReviewEvidence(format!(
            "PR diff at {} is empty",
            diff_path.display()
        )));
    }
    Ok(ReviewEvidence {
        workspace_root: workspace_root.to_path_buf(),
        diff_path,
        diff,
    })
}

fn review_evidence_text(
    review_evidence: &ReviewEvidence,
    brief: &JudgementBrief,
) -> Result<String, RunBodyError> {
    let mut text = format!(
        "Prepared workspace tree: {}\nPR diff path: {}\n\n<pr_diff>\n{}\n</pr_diff>",
        review_evidence.workspace_root.display(),
        review_evidence.diff_path.display(),
        review_evidence.diff
    );
    if !brief.evidence_paths.is_empty() {
        text.push_str("\n\nAdditional brief evidence:\n");
        text.push_str(&evidence_text(&review_evidence.workspace_root, brief)?);
    }
    Ok(text)
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
struct ExpectedAgentTarget {
    agent_id: AgentId,
    role: AgentRole,
    engine: String,
    model_family: String,
    model: String,
}

fn expected_targets(
    request: &RunLaunchRequest,
    role: AgentRole,
) -> Result<Vec<ExpectedAgentTarget>, RunBodyError> {
    request
        .provenance
        .iter()
        .filter(|provenance| provenance.role == role)
        .map(expected_target)
        .collect()
}

fn expected_target(provenance: &ModelProvenance) -> Result<ExpectedAgentTarget, RunBodyError> {
    let engine = provenance
        .extensions
        .get(EXT_AGENT_ENGINE)
        .and_then(Value::as_str)
        .ok_or_else(|| {
            RunBodyError::EnsembleArchiveMismatch(format!(
                "missing engine for authorised agent {}",
                provenance.agent_id.0
            ))
        })?
        .to_owned();
    let pump19_contract::ProvenanceVerification::Verified { lineage, .. } =
        &provenance.verification
    else {
        return Err(RunBodyError::EnsembleArchiveMismatch(format!(
            "authorised agent {} is not verified",
            provenance.agent_id.0
        )));
    };
    Ok(ExpectedAgentTarget {
        agent_id: provenance.agent_id.clone(),
        role: provenance.role,
        engine,
        model_family: lineage.family.0.clone(),
        model: lineage.model.clone(),
    })
}

fn reconcile_ensemble_archive(
    archive_dir: &Path,
    expected_targets: &[ExpectedAgentTarget],
) -> Result<EnsembleArchiveEvidence, RunBodyError> {
    let manifest_path = find_single_manifest(archive_dir)?;
    let manifest_text = fs::read_to_string(&manifest_path)
        .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
    let manifest =
        serde_json::from_str::<EnsembleRunManifest>(&manifest_text).map_err(|source| {
            RunBodyError::MalformedEnsembleArchive {
                path: manifest_path.display().to_string(),
                source,
            }
        })?;
    if manifest.status != "complete" || manifest.result.exit_code != 0 {
        return Err(RunBodyError::EnsembleArchiveMismatch(format!(
            "archive status {:?} with exit code {}",
            manifest.status, manifest.result.exit_code
        )));
    }

    let run_dir = manifest_path
        .parent()
        .ok_or_else(|| RunBodyError::MissingEnsembleArchive(archive_dir.display().to_string()))?;
    let agent_paths = manifest
        .files
        .iter()
        .filter(|file| {
            file.path
                .file_name()
                .is_some_and(|name| name == "agent.json")
        })
        .map(|file| run_dir.join(&file.path))
        .collect::<Vec<_>>();
    if agent_paths.is_empty() && !expected_targets.is_empty() {
        return Err(RunBodyError::EnsembleArchiveMismatch(
            "archive contains no agent records".to_owned(),
        ));
    }

    let mut seen = BTreeSet::new();
    for path in agent_paths {
        let text =
            fs::read_to_string(&path).map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let record = serde_json::from_str::<EnsembleAgentRecord>(&text).map_err(|source| {
            RunBodyError::MalformedEnsembleArchive {
                path: path.display().to_string(),
                source,
            }
        })?;
        let target = expected_for_record(&record, expected_targets)?;
        if record.status != "complete" {
            return Err(RunBodyError::EnsembleArchiveMismatch(format!(
                "agent {} ended with status {}",
                target.agent_id.0, record.status
            )));
        }
        if record.validated_output.is_none() {
            return Err(RunBodyError::EnsembleArchiveMismatch(format!(
                "agent {} has no schema-validated output",
                target.agent_id.0
            )));
        }
        if record.engine != target.engine {
            return Err(RunBodyError::EnsembleArchiveMismatch(format!(
                "agent {} ran engine {} instead of {}",
                target.agent_id.0, record.engine, target.engine
            )));
        }
        let actual_model = archive_model_for_engine(&record, &target.engine);
        if actual_model != Some(&target.model) {
            return Err(RunBodyError::EnsembleArchiveMismatch(format!(
                "agent {} ran model {:?} instead of {}",
                target.agent_id.0, actual_model, target.model
            )));
        }
        seen.insert(target.agent_id.0.clone());
    }

    for expected in expected_targets {
        if !seen.contains(&expected.agent_id.0) {
            return Err(RunBodyError::EnsembleArchiveMismatch(format!(
                "authorised agent {} did not run",
                expected.agent_id.0
            )));
        }
    }

    Ok(EnsembleArchiveEvidence {
        archive_dir: archive_dir.to_path_buf(),
        run_id: manifest.run_id,
        agents: seen.into_iter().collect(),
    })
}

fn archive_model_for_engine<'a>(
    record: &'a EnsembleAgentRecord,
    engine: &str,
) -> Option<&'a String> {
    if engine == "opencode" {
        record.resolved_model.as_ref().or(record.model.as_ref())
    } else {
        record.model.as_ref()
    }
}

fn expected_for_record<'a>(
    record: &EnsembleAgentRecord,
    expected_targets: &'a [ExpectedAgentTarget],
) -> Result<&'a ExpectedAgentTarget, RunBodyError> {
    let Some(label) = record.label.as_deref() else {
        return Err(RunBodyError::EnsembleArchiveMismatch(format!(
            "agent record {} has no label",
            record.id
        )));
    };
    let agent_id = label.split(':').next().unwrap_or(label);
    expected_targets
        .iter()
        .find(|target| target.agent_id.0 == agent_id)
        .ok_or_else(|| {
            RunBodyError::EnsembleArchiveMismatch(format!(
                "archive contains unauthorised agent label {label:?}"
            ))
        })
}

fn find_single_manifest(root: &Path) -> Result<PathBuf, RunBodyError> {
    let mut manifests = Vec::new();
    collect_manifest_paths(root, &mut manifests)?;
    match manifests.as_slice() {
        [] => Err(RunBodyError::MissingEnsembleArchive(
            root.display().to_string(),
        )),
        [manifest] => Ok(manifest.clone()),
        _ => Err(RunBodyError::AmbiguousEnsembleArchive(
            root.display().to_string(),
        )),
    }
}

fn collect_manifest_paths(root: &Path, paths: &mut Vec<PathBuf>) -> Result<(), RunBodyError> {
    if !root.exists() {
        return Ok(());
    }
    let entries = fs::read_dir(root).map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
    for entry in entries {
        let path = entry
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?
            .path();
        if path.is_dir() {
            collect_manifest_paths(&path, paths)?;
        } else if path.file_name().is_some_and(|name| name == "manifest.json") {
            paths.push(path);
        }
    }
    Ok(())
}

fn safe_path_segment(value: &str) -> String {
    value
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '-' | '_') {
                c
            } else {
                '_'
            }
        })
        .collect()
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EnsembleArchiveEvidence {
    pub archive_dir: PathBuf,
    pub run_id: String,
    pub agents: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct EnsembleRunManifest {
    files: Vec<EnsembleArchiveFile>,
    result: EnsembleArchiveResult,
    run_id: String,
    status: String,
}

#[derive(Debug, Deserialize)]
struct EnsembleArchiveFile {
    path: PathBuf,
}

#[derive(Debug, Deserialize)]
struct EnsembleArchiveResult {
    exit_code: i32,
}

#[derive(Debug, Deserialize)]
struct EnsembleAgentRecord {
    id: u64,
    engine: String,
    #[serde(default)]
    label: Option<String>,
    #[serde(default)]
    model: Option<String>,
    #[serde(default)]
    resolved_model: Option<String>,
    status: String,
    #[serde(default)]
    validated_output: Option<Value>,
}

fn forge_facts(request: &RunLaunchRequest) -> Result<ForgeFacts, RunBodyError> {
    let value = request
        .state
        .extensions
        .get(EXT_FORGE_FACTS)
        .ok_or(RunBodyError::MissingForgeFacts)?;
    serde_json::from_value(value.clone()).map_err(RunBodyError::InvalidForgeFacts)
}

fn provenance_for_role(
    request: &RunLaunchRequest,
    role: AgentRole,
) -> Result<ModelProvenance, RunBodyError> {
    request
        .provenance
        .iter()
        .find(|provenance| provenance.role == role)
        .cloned()
        .ok_or(RunBodyError::MissingRoleProvenance(role))
}

fn reviewer_provenance<'a>(
    request: &'a RunLaunchRequest,
    agent_id: &str,
) -> Result<&'a ModelProvenance, RunBodyError> {
    request
        .provenance
        .iter()
        .find(|provenance| {
            provenance.role == AgentRole::Reviewer
                && provenance.agent_id == AgentId(agent_id.to_owned())
        })
        .ok_or_else(|| RunBodyError::MissingReviewerProvenance(agent_id.to_owned()))
}

fn first_line(text: &str) -> Option<String> {
    text.lines()
        .map(str::trim)
        .find(|line| !line.is_empty())
        .map(ToOwned::to_owned)
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

#[cfg(test)]
mod tests {
    use std::{
        collections::{BTreeMap, VecDeque},
        fs,
        path::{Path, PathBuf},
    };

    use pump19_contract::{
        DecisionSubject, LoopPassRecord, ModelFamily, ModelLineage, ProvenanceVerification,
        PullRequestRef, RunStatus, SessionFreshness, SessionId,
    };
    use pump19_core::{
        LaunchProof, WorkspaceExecOutput, WorkspaceExecRequest, WorkspaceIsolation, WorkspaceLease,
    };
    use pump19_judgement::{ReviewerResult, baseline_judgement_briefs};
    use serde_json::json;

    use super::*;

    #[test]
    fn host_runner_env_filter_keeps_model_auth_and_drops_forge_credentials() {
        let env = filtered_ensemble_env([
            ("PATH".to_owned(), "/usr/bin".to_owned()),
            ("HOME".to_owned(), "/home/pump19".to_owned()),
            ("OPENAI_API_KEY".to_owned(), "openai".to_owned()),
            ("ANTHROPIC_API_KEY".to_owned(), "anthropic".to_owned()),
            ("GEMINI_API_KEY".to_owned(), "gemini".to_owned()),
            ("FORGEJO_TOKEN".to_owned(), "forgejo".to_owned()),
            ("GITEA_TOKEN".to_owned(), "gitea".to_owned()),
            ("GITHUB_TOKEN".to_owned(), "github".to_owned()),
        ]);

        assert_eq!(env.get("OPENAI_API_KEY"), Some(&"openai".to_owned()));
        assert_eq!(env.get("ANTHROPIC_API_KEY"), Some(&"anthropic".to_owned()));
        assert_eq!(env.get("GEMINI_API_KEY"), Some(&"gemini".to_owned()));
        assert_eq!(env.get("PATH"), Some(&"/usr/bin".to_owned()));
        assert_eq!(env.get("HOME"), Some(&"/home/pump19".to_owned()));
        assert!(!env.contains_key("FORGEJO_TOKEN"));
        assert!(!env.contains_key("GITEA_TOKEN"));
        assert!(!env.contains_key("GITHUB_TOKEN"));
    }

    #[derive(Debug, Default)]
    struct FakeWorkspace {
        outputs: VecDeque<WorkspaceExecOutput>,
        execs: Vec<WorkspaceExecRequest>,
    }

    impl WorkspaceExecutor for FakeWorkspace {
        fn exec(
            &mut self,
            _lease: &WorkspaceLease,
            request: WorkspaceExecRequest,
        ) -> Result<WorkspaceExecOutput, CoreError> {
            self.execs.push(request);
            Ok(self
                .outputs
                .pop_front()
                .unwrap_or_else(|| WorkspaceExecOutput {
                    exit_code: 0,
                    stdout: b"[]".to_vec(),
                    stderr: Vec::new(),
                }))
        }
    }

    #[derive(Clone, Debug, Eq, PartialEq)]
    struct ArchiveAgentFixture {
        label: String,
        engine: String,
        model: String,
        resolved_model: Option<String>,
        status: String,
        validated_output: Option<Value>,
    }

    #[derive(Debug)]
    struct FakeEnsembleRunner {
        value: Value,
        agents: Vec<ArchiveAgentFixture>,
        requests: Vec<EnsembleWorkflowRequest>,
    }

    impl FakeEnsembleRunner {
        fn new(value: Value, agents: Vec<ArchiveAgentFixture>) -> Self {
            Self {
                value,
                agents,
                requests: Vec::new(),
            }
        }
    }

    impl EnsembleWorkflowRunner for FakeEnsembleRunner {
        fn run_workflow(
            &mut self,
            request: EnsembleWorkflowRequest,
        ) -> Result<EnsembleWorkflowOutput, RunBodyError> {
            write_archive(&request.archive_dir, &self.agents);
            self.requests.push(request.clone());
            Ok(EnsembleWorkflowOutput {
                value: self.value.clone(),
                archive_dir: request.archive_dir,
            })
        }
    }

    #[derive(Debug)]
    struct FailingEnsembleRunner {
        message: String,
    }

    impl EnsembleWorkflowRunner for FailingEnsembleRunner {
        fn run_workflow(
            &mut self,
            _request: EnsembleWorkflowRequest,
        ) -> Result<EnsembleWorkflowOutput, RunBodyError> {
            Err(RunBodyError::Ensemble(self.message.clone()))
        }
    }

    #[derive(Debug)]
    struct FakeSessions;

    impl AgentSessionPreparer for FakeSessions {
        fn prepare(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, RunBodyError> {
            Ok(PreparedAgent {
                agent_id: spec.target.agent_id,
                role: spec.target.role,
                session_id: SessionId(format!("session-{}", spec.pass_index)),
                proof: LaunchProof::EstablishedFresh,
            })
        }
    }

    #[derive(Debug)]
    struct FakeReview {
        findings: Vec<Finding>,
    }

    impl ReviewRunBody for FakeReview {
        fn run_review(
            &mut self,
            _request: &RunLaunchRequest,
            _workspace: &mut dyn WorkspaceExecutor,
        ) -> Result<Vec<Finding>, RunBodyError> {
            Ok(self.findings.clone())
        }
    }

    #[derive(Debug)]
    struct FakeJudge {
        verdict: DecisionVerdict,
    }

    impl JudgeRunBody for FakeJudge {
        fn run_judge(
            &mut self,
            request: &RunLaunchRequest,
            _workspace: &mut dyn WorkspaceExecutor,
        ) -> Result<Vec<Decision>, RunBodyError> {
            let provenance = provenance_for_role(request, AgentRole::Judge)?;
            Ok(request
                .state
                .findings
                .iter()
                .map(|finding| Decision {
                    contract_version: ContractVersion::current(),
                    id: stable_id(
                        "decision",
                        [request.run_id.0.as_str(), finding.id.0.as_str()],
                    ),
                    subject: DecisionSubject::Finding {
                        finding_id: finding.id.clone(),
                    },
                    verdict: self.verdict,
                    rationale: "test verdict".to_owned(),
                    provenance: provenance.clone(),
                    extensions: BTreeMap::new(),
                })
                .collect())
        }
    }

    #[derive(Debug)]
    struct FakeFix;

    impl FixRunBody for FakeFix {
        fn run_fix(
            &mut self,
            request: &RunLaunchRequest,
            _workspace: &mut dyn WorkspaceExecutor,
        ) -> Result<Vec<Patch>, RunBodyError> {
            let provenance = provenance_for_role(request, AgentRole::Fixer)?;
            let findings = material_findings(request);
            if findings.is_empty() {
                return Ok(Vec::new());
            }
            Ok(vec![patch_from_change(
                request,
                findings,
                PatchChange::Description {
                    summary: "fixed material findings".to_owned(),
                },
                provenance,
            )])
        }
    }

    fn provenance(agent_id: &str, role: AgentRole, family: &str) -> ModelProvenance {
        let mut extensions = BTreeMap::new();
        extensions.insert(
            EXT_AGENT_ENGINE.to_owned(),
            Value::String(engine_for_family(family).to_owned()),
        );
        ModelProvenance {
            contract_version: ContractVersion::current(),
            agent_id: AgentId(agent_id.to_owned()),
            role,
            session_id: SessionId(format!("{agent_id}-session")),
            freshness: SessionFreshness::FreshForPass { pass_index: 1 },
            verification: ProvenanceVerification::Verified {
                vendor: "local".to_owned(),
                control_plane: "test".to_owned(),
                lineage: ModelLineage {
                    family: ModelFamily(family.to_owned()),
                    model: format!("{family}-2026"),
                },
            },
            extensions,
        }
    }

    fn request(run_kind: RunKind, provenance: Vec<ModelProvenance>) -> RunLaunchRequest {
        RunLaunchRequest {
            run_id: RunId(format!("{run_kind:?}-run")),
            run_kind,
            event: pump19_contract::ContractEvent {
                contract_version: ContractVersion::current(),
                id: "event-1".to_owned(),
                payload: pump19_contract::EventPayload::RunCompleted {
                    run_id: RunId("previous-run".to_owned()),
                    run_kind: None,
                    outcome: RunOutcome::Succeeded,
                },
                extensions: BTreeMap::new(),
            },
            state: pump19_contract::PrRunState {
                contract_version: ContractVersion::current(),
                pr: PullRequestRef {
                    repository: "acme/widgets".to_owned(),
                    id: "42".to_owned(),
                },
                commit_sha: "abc123".to_owned(),
                current_head_sha: Some("abc123".to_owned()),
                pass_index: 1,
                status: RunStatus::Running,
                active_run: None,
                run_history: Vec::new(),
                loop_history: Vec::new(),
                superseded_by: None,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                publication: pump19_contract::PublicationState::default(),
                ceiling: None,
                extensions: BTreeMap::new(),
            },
            workspace: WorkspaceLease {
                id: "workspace".to_owned(),
                root: PathBuf::from("/tmp/pump19-runs-test"),
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: true,
                    egress_bounded: true,
                    resource_bounded: true,
                    ephemeral: true,
                },
            },
            provenance,
        }
    }

    fn finding() -> Finding {
        Finding {
            contract_version: ContractVersion::current(),
            id: FindingId("finding-1".to_owned()),
            dedup_key: "dedup".to_owned(),
            source_brief: "brief".to_owned(),
            dimension: "judgement".to_owned(),
            summary: "finding".to_owned(),
            severity: Severity::High,
            confidence: Confidence::High,
            certainty: CertaintyClass::Advisory,
            provenance: provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
            locations: vec![FindingLocation::General {
                description: "whole change".to_owned(),
            }],
            extensions: BTreeMap::new(),
        }
    }

    fn engine_for_family(family: &str) -> &'static str {
        match family {
            "claude" => "claude",
            "codex" => "codex",
            _ => "opencode",
        }
    }

    fn archive_agent(agent_id: &str, family: &str) -> ArchiveAgentFixture {
        ArchiveAgentFixture {
            label: format!("{agent_id}:purpose"),
            engine: engine_for_family(family).to_owned(),
            model: format!("{family}-2026"),
            resolved_model: Some(format!("{family}-2026")),
            status: "complete".to_owned(),
            validated_output: Some(json!({"ok": true})),
        }
    }

    fn ensemble_config(root: &Path) -> EnsembleWorkflowConfig {
        EnsembleWorkflowConfig {
            script: root.join("workflow.js"),
            archive_root: root.join("archives"),
            timeout_ms: 5_000,
            prompt_template: "configured prompt".to_owned(),
            briefs: baseline_judgement_briefs(),
        }
    }

    fn write_review_diff(root: &Path, diff: &str) {
        let evidence_dir = root.join(REVIEW_EVIDENCE_DIR);
        fs::create_dir_all(&evidence_dir).expect("create review evidence dir");
        fs::write(evidence_dir.join(REVIEW_DIFF_FILE), diff).expect("write review diff");
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
                    "resolved_model": agent.resolved_model,
                    "status": agent.status,
                    "validated_output": agent.validated_output,
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

    #[test]
    fn review_maps_failed_judgement_to_advisory_finding() {
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.run_id = RunId("review-run".to_owned());
        let run = JudgementRun {
            status: JudgementStatus::Failed,
            model_families: vec!["codex".to_owned()],
            briefs: vec![JudgementBriefResult {
                brief_id: "purpose".to_owned(),
                status: JudgementStatus::Failed,
                reviews: vec![ReviewerResult {
                    agent_id: "reviewer-codex".to_owned(),
                    model_family: "codex".to_owned(),
                    status: JudgementStatus::Failed,
                    stdout: "PUMP19_JUDGEMENT: FAIL stale state".to_owned(),
                    stderr: String::new(),
                }],
            }],
        };

        let findings = findings_from_judgement(&req, &run).expect("map findings");

        assert_eq!(findings.len(), 1);
        assert_eq!(findings[0].certainty, CertaintyClass::Advisory);
        assert_eq!(
            findings[0].provenance.agent_id,
            AgentId("reviewer-codex".to_owned())
        );
        assert_eq!(findings[0].source_brief, "purpose");
    }

    #[test]
    fn review_finding_dedup_key_is_stable_across_reviewer_rewording() {
        let mut first = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        first.run_id = RunId("review-run-1".to_owned());
        let mut second = first.clone();
        second.run_id = RunId("review-run-2".to_owned());
        let run = |stdout: &str| JudgementRun {
            status: JudgementStatus::Failed,
            model_families: vec!["codex".to_owned()],
            briefs: vec![JudgementBriefResult {
                brief_id: "purpose".to_owned(),
                status: JudgementStatus::Failed,
                reviews: vec![ReviewerResult {
                    agent_id: "reviewer-codex".to_owned(),
                    model_family: "codex".to_owned(),
                    status: JudgementStatus::Failed,
                    stdout: stdout.to_owned(),
                    stderr: String::new(),
                }],
            }],
        };

        let first_findings =
            findings_from_judgement(&first, &run("PUMP19_JUDGEMENT: FAIL stale state"))
                .expect("first findings");
        let second_findings = findings_from_judgement(
            &second,
            &run("PUMP19_JUDGEMENT: FAIL old review state accepted"),
        )
        .expect("second findings");

        assert_ne!(first_findings[0].id, second_findings[0].id);
        assert_eq!(first_findings[0].dedup_key, second_findings[0].dedup_key);
    }

    #[test]
    fn review_body_runs_host_ensemble_and_reconciles_archive() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.run_id = RunId("review-run".to_owned());
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let run = JudgementRun {
            status: JudgementStatus::Failed,
            model_families: vec!["codex".to_owned()],
            briefs: vec![JudgementBriefResult {
                brief_id: "purpose".to_owned(),
                status: JudgementStatus::Failed,
                reviews: vec![ReviewerResult {
                    agent_id: "reviewer-codex".to_owned(),
                    model_family: "codex".to_owned(),
                    status: JudgementStatus::Failed,
                    stdout: "PUMP19_JUDGEMENT: FAIL stale state".to_owned(),
                    stderr: String::new(),
                }],
            }],
        };
        let value = serde_json::to_value(run).expect("serialise judgement run");
        let runner = FakeEnsembleRunner::new(value, vec![archive_agent("reviewer-codex", "codex")]);
        let mut config = ensemble_config(root.path());
        config.prompt_template =
            "external review template marker {{brief_id}}\n{{subject_name}}\n{{brief_title}}\n{{brief_body}}\n{{evidence}}"
                .to_owned();
        let mut body = EnsembleReviewBody::new(runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("review body uses ensemble");

        assert_eq!(findings.len(), 1);
        assert!(workspace.execs.is_empty());
        assert_eq!(body.runner.requests.len(), 1);
        assert!(body.runner.requests[0].archive_dir.ends_with("review-run"));
        let prompt = body.runner.requests[0].args["briefs"][0]["prompt"]
            .as_str()
            .expect("workflow brief prompt");
        assert!(prompt.contains("external review template marker reviewer-independence"));
        assert!(prompt.contains("acme/widgets"));
        assert!(prompt.contains("Reviewer independence"));
        assert!(prompt.contains("Prepared workspace tree:"));
        assert!(prompt.contains(root.path().to_string_lossy().as_ref()));
        assert!(prompt.contains("diff --git a/src/lib.rs b/src/lib.rs"));
        assert!(!prompt.contains("No separate evidence paths were supplied."));
        assert_eq!(
            body.runner.requests[0].args["evidence"]["workspace_root"].as_str(),
            Some(root.path().to_string_lossy().as_ref())
        );
        assert!(
            body.runner.requests[0].args["evidence"]["diff"]
                .as_str()
                .expect("workflow diff evidence")
                .contains("+pub fn changed() {}")
        );
        assert_eq!(
            body.runner.requests[0].args["subject"]["name"].as_str(),
            Some("acme/widgets")
        );
    }

    #[test]
    fn review_body_uses_configured_pump_side_subject_intent() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.run_id = RunId("review-run".to_owned());
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let runner = FakeEnsembleRunner::new(
            serde_json::to_value(JudgementRun {
                status: JudgementStatus::Passed,
                model_families: vec!["codex".to_owned()],
                briefs: Vec::new(),
            })
            .expect("serialise judgement run"),
            vec![archive_agent("reviewer-codex", "codex")],
        );
        let mut config = ensemble_config(root.path());
        config.prompt_template =
            "{{subject_name}}\n{{subject_slug}}\n{{subject_purpose}}".to_owned();
        let mut subject_intents = BTreeMap::new();
        subject_intents.insert(
            "acme/widgets".to_owned(),
            SubjectIntent {
                slug: "widgets".to_owned(),
                name: "Acme Widgets".to_owned(),
                purpose: "Keep widget rendering honest.".to_owned(),
                invariants: Vec::new(),
                behaviours: Vec::new(),
            },
        );
        let mut body = EnsembleReviewBody::with_subject_intents(runner, config, subject_intents);
        let mut workspace = FakeWorkspace::default();

        body.run_review(&req, &mut workspace)
            .expect("configured subject does not require workspace intent");

        let request = &body.runner.requests[0];
        let prompt = request.args["briefs"][0]["prompt"]
            .as_str()
            .expect("workflow brief prompt");
        assert!(prompt.contains("Acme Widgets"));
        assert!(prompt.contains("widgets"));
        assert!(prompt.contains("Keep widget rendering honest."));
        assert_eq!(
            request.args["subject"]["name"].as_str(),
            Some("Acme Widgets")
        );
    }

    #[test]
    fn review_body_fails_closed_before_launch_without_diff_evidence() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        let runner = FakeEnsembleRunner::new(
            serde_json::to_value(JudgementRun {
                status: JudgementStatus::Passed,
                model_families: vec!["codex".to_owned()],
                briefs: Vec::new(),
            })
            .expect("serialise judgement run"),
            vec![archive_agent("reviewer-codex", "codex")],
        );
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("review without diff evidence fails closed");

        assert!(
            matches!(error, RunBodyError::MissingReviewEvidence(message) if message.contains("PR diff"))
        );
        assert!(body.runner.requests.is_empty());
    }

    #[test]
    fn prompt_template_allows_brace_bearing_variable_content() {
        let rendered = render_prompt_template(
            "Evidence:\n{{evidence}}\nFinding:\n{{finding}}",
            &[
                (
                    "evidence",
                    "Vue template: <h1>{{ title }}</h1>\nLiteral later placeholder: {{finding}}"
                        .to_owned(),
                ),
                ("finding", "actual finding text".to_owned()),
            ],
        )
        .expect("render template");

        assert!(rendered.contains("<h1>{{ title }}</h1>"));
        assert!(rendered.contains("Literal later placeholder: {{finding}}"));
        assert!(rendered.contains("Finding:\nactual finding text"));
    }

    #[test]
    fn prompt_template_rejects_unknown_template_placeholder_before_rendering_values() {
        let error = render_prompt_template(
            "{{known}}\n{{missing}}",
            &[("known", "content with {{missing}} braces".to_owned())],
        )
        .expect_err("unknown template placeholder is rejected");

        assert!(
            error
                .to_string()
                .contains("unknown {{missing}} placeholder")
        );
    }

    #[test]
    fn review_body_fails_closed_on_malformed_workflow_output() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let runner = FakeEnsembleRunner::new(
            json!({"not": "a judgement run"}),
            vec![archive_agent("reviewer-codex", "codex")],
        );
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("malformed output fails closed");

        assert!(matches!(error, RunBodyError::EnsembleJson(_)));
    }

    #[test]
    fn archive_schema_null_output_fails_closed() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let mut agent = archive_agent("reviewer-codex", "codex");
        agent.validated_output = None;
        let runner = FakeEnsembleRunner::new(
            serde_json::to_value(JudgementRun {
                status: JudgementStatus::Passed,
                model_families: vec!["codex".to_owned()],
                briefs: Vec::new(),
            })
            .expect("serialise judgement run"),
            vec![agent],
        );
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("schema-null archive fails closed");

        assert!(
            matches!(error, RunBodyError::EnsembleArchiveMismatch(message) if message.contains("schema-validated"))
        );
    }

    #[test]
    fn archive_engine_mismatch_fails_closed() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let mut agent = archive_agent("reviewer-codex", "codex");
        agent.engine = "claude".to_owned();
        let runner = FakeEnsembleRunner::new(
            serde_json::to_value(JudgementRun {
                status: JudgementStatus::Passed,
                model_families: vec!["codex".to_owned()],
                briefs: Vec::new(),
            })
            .expect("serialise judgement run"),
            vec![agent],
        );
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("engine mismatch fails closed");

        assert!(
            matches!(error, RunBodyError::EnsembleArchiveMismatch(message) if message.contains("ran engine"))
        );
    }

    #[test]
    fn claude_archive_must_match_requested_model_not_resolved_alias() {
        let root = tempfile::tempdir().expect("archive root");
        let mut agent = archive_agent("reviewer-claude", "claude");
        agent.model = "opus".to_owned();
        agent.resolved_model = Some("claude-opus-4-8".to_owned());
        write_archive(root.path(), &[agent]);
        let expected = ExpectedAgentTarget {
            agent_id: AgentId("reviewer-claude".to_owned()),
            role: AgentRole::Reviewer,
            engine: "claude".to_owned(),
            model_family: "claude".to_owned(),
            model: "claude-opus-4-8".to_owned(),
        };

        let error = reconcile_ensemble_archive(root.path(), &[expected])
            .expect_err("claude requested model mismatch fails closed");

        assert!(
            matches!(error, RunBodyError::EnsembleArchiveMismatch(message)
                if message.contains("ran model Some(\"opus\") instead of claude-opus-4-8"))
        );
    }

    #[test]
    fn runner_error_propagates_as_workflow_failure() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        write_review_diff(
            root.path(),
            "diff --git a/src/lib.rs b/src/lib.rs\n+pub fn changed() {}\n",
        );
        let runner = FailingEnsembleRunner {
            message: "workflow timed out".to_owned(),
        };
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("runner errors fail closed");

        assert!(matches!(error, RunBodyError::Ensemble(message) if message.contains("timed out")));
    }

    #[test]
    fn host_runner_kills_launcher_at_hard_timeout() {
        let root = tempfile::tempdir().expect("host runner root");
        let launcher = root.path().join("sleep-launcher.sh");
        fs::write(&launcher, "#!/bin/sh\nsleep 5\n").expect("write launcher");
        let workflow = root.path().join("workflow.js");
        fs::write(
            &workflow,
            "export const meta = { name: \"timeout\" };\nreturn null;\n",
        )
        .expect("write workflow");
        let mut runner = HostEnsembleWorkflowRunner::new("sh", &launcher);
        let request = EnsembleWorkflowRequest {
            script: workflow,
            args: json!({}),
            archive_dir: root.path().join("archive"),
            timeout_ms: 50,
        };
        let started = Instant::now();

        let error = runner
            .run_workflow(request)
            .expect_err("hard timeout should kill launcher");

        assert!(started.elapsed() < Duration::from_secs(2));
        assert!(
            matches!(error, RunBodyError::Ensemble(message) if message.contains("hard timeout"))
        );
    }

    #[test]
    fn host_runner_passes_large_payload_through_temporary_workflow_file() {
        let root = tempfile::tempdir().expect("host runner root");
        let launcher = root.path().join("fake-ensemble.sh");
        let captured_argv = root.path().join("argv.txt");
        let captured_script_path = root.path().join("script-path.txt");
        let captured_wrapper = root.path().join("wrapper.js");
        fs::write(
            &launcher,
            format!(
                r#"#!/bin/sh
printf '%s\n' "$@" > "{captured_argv}"
script=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --timeout)
      shift 2
      ;;
    --*)
      shift
      ;;
    *)
      script=$1
      shift
      ;;
  esac
done
printf '%s' "$script" > "{captured_script_path}"
cp "$script" "{captured_wrapper}"
printf '{{"ok":true}}\n'
"#,
                captured_argv = captured_argv.display(),
                captured_script_path = captured_script_path.display(),
                captured_wrapper = captured_wrapper.display(),
            ),
        )
        .expect("write launcher");
        let workflow = root.path().join("workflow.js");
        fs::write(
            &workflow,
            "export const meta = { name: \"payload\" };\nreturn { marker: args.marker };\n",
        )
        .expect("write workflow");
        let marker = "payload-marker-".repeat(20_000);
        let request = EnsembleWorkflowRequest {
            script: workflow,
            args: json!({ "marker": marker }),
            archive_dir: root.path().join("archive"),
            timeout_ms: 5_000,
        };
        let mut runner = HostEnsembleWorkflowRunner::new("sh", &launcher);

        let output = runner.run_workflow(request).expect("workflow launches");

        assert_eq!(output.value, json!({"ok": true}));
        let argv = fs::read_to_string(captured_argv).expect("read argv");
        assert!(!argv.contains("--json-args"));
        assert!(!argv.contains("payload-marker-"));
        let wrapper = fs::read_to_string(captured_wrapper).expect("read wrapper");
        assert!(wrapper.contains("const args = JSON.parse("));
        assert!(wrapper.contains("payload-marker-"));
        let wrapper_path = fs::read_to_string(captured_script_path).expect("read wrapper path");
        assert!(
            !Path::new(&wrapper_path).exists(),
            "temporary wrapper should be removed after launch"
        );
    }

    #[test]
    fn judge_run_produces_material_decisions_for_findings() {
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        req.state.findings.push(finding());
        let mut launcher = Pump19RunLauncher::new(
            FakeSessions,
            FakeReview {
                findings: Vec::new(),
            },
            FakeJudge {
                verdict: DecisionVerdict::Material,
            },
            FakeFix,
            MergeGateFinishBody,
        );
        let mut workspace = FakeWorkspace::default();

        let outcome = launcher
            .launch_run(req, &mut workspace)
            .expect("launch judge");

        assert_eq!(outcome.outcome, RunOutcome::Succeeded);
        assert_eq!(outcome.decisions.len(), 1);
        assert_eq!(outcome.decisions[0].verdict, DecisionVerdict::Material);
    }

    #[test]
    fn judge_workflow_receives_loop_history_without_stale_findings_as_current() {
        let root = tempfile::tempdir().expect("judge root");
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        req.workspace.root = root.path().to_path_buf();
        let stale = finding();
        req.state.loop_history.push(LoopPassRecord {
            pass_index: 1,
            commit_sha: "old-sha".to_owned(),
            findings: vec![stale.clone()],
            decisions: vec![Decision {
                contract_version: ContractVersion::current(),
                id: "decision-old".to_owned(),
                subject: DecisionSubject::Finding {
                    finding_id: stale.id,
                },
                verdict: DecisionVerdict::Material,
                rationale: "was material".to_owned(),
                provenance: provenance("judge-old", AgentRole::Judge, "glm"),
                extensions: BTreeMap::new(),
            }],
            patches: Vec::new(),
            judge_verdict: Some(DecisionVerdict::Material),
            fix_outcome: Some(RunOutcome::Succeeded),
        });
        req.state.pass_index = 2;
        req.state.findings.push(finding());
        let runner = FakeEnsembleRunner::new(
            json!([{"finding_id":"finding-1","verdict":"minor","rationale":"below threshold"}]),
            vec![archive_agent("judge", "glm")],
        );
        let mut judge = EnsembleJudgeBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let decisions = judge.run_judge(&req, &mut workspace).expect("judge");

        assert_eq!(decisions.len(), 1);
        let workflow_args = &judge.runner.requests[0].args;
        assert_eq!(workflow_args["pass_count"], json!(2));
        assert_eq!(
            workflow_args["current_findings"]
                .as_array()
                .expect("current")
                .len(),
            1
        );
        assert_eq!(
            workflow_args["findings"].as_array().expect("compat").len(),
            1
        );
        assert_eq!(
            workflow_args["loop_history"]
                .as_array()
                .expect("history")
                .len(),
            1
        );
        assert_eq!(workflow_args["prior_verdicts"], json!(["material"]));
        assert_eq!(workflow_args["fix_outcomes"], json!(["succeeded"]));
    }

    #[test]
    fn judge_run_fails_when_standing_findings_get_no_decisions() {
        let root = tempfile::tempdir().expect("judge root");
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        req.workspace.root = root.path().to_path_buf();
        req.state.findings.push(finding());
        let runner = FakeEnsembleRunner::new(json!([]), vec![archive_agent("judge", "glm")]);
        let mut judge = EnsembleJudgeBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = judge
            .run_judge(&req, &mut workspace)
            .expect_err("empty decisions with findings fail");

        assert!(
            matches!(error, RunBodyError::Ensemble(message) if message.contains("no decisions"))
        );
    }

    #[test]
    fn judge_replays_noop_fix_findings_from_loop_history() {
        let root = tempfile::tempdir().expect("judge root");
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        req.workspace.root = root.path().to_path_buf();
        let standing = finding();
        req.state.loop_history.push(LoopPassRecord {
            pass_index: 1,
            commit_sha: "abc123".to_owned(),
            findings: vec![standing.clone()],
            decisions: vec![Decision {
                contract_version: ContractVersion::current(),
                id: "decision-material".to_owned(),
                subject: DecisionSubject::Finding {
                    finding_id: standing.id,
                },
                verdict: DecisionVerdict::Material,
                rationale: "was material before the NoOp fix".to_owned(),
                provenance: provenance("judge-old", AgentRole::Judge, "glm"),
                extensions: BTreeMap::new(),
            }],
            patches: Vec::new(),
            judge_verdict: Some(DecisionVerdict::Material),
            fix_outcome: Some(RunOutcome::NoOp),
        });
        let runner = FakeEnsembleRunner::new(
            json!([{"finding_id":"finding-1","verdict":"minor","rationale":"NoOp exhausted the useful fix path"}]),
            vec![archive_agent("judge", "glm")],
        );
        let mut judge = EnsembleJudgeBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let decisions = judge.run_judge(&req, &mut workspace).expect("judge");

        assert_eq!(decisions.len(), 1);
        assert_eq!(decisions[0].verdict, DecisionVerdict::Minor);
        let workflow_args = &judge.runner.requests[0].args;
        assert_eq!(
            workflow_args["current_findings"]
                .as_array()
                .expect("current findings")
                .len(),
            1
        );
        assert_eq!(workflow_args["fix_outcomes"], json!(["no_op"]));
    }

    #[test]
    fn judge_fails_empty_decisions_for_noop_history_findings() {
        let root = tempfile::tempdir().expect("judge root");
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        req.workspace.root = root.path().to_path_buf();
        let standing = finding();
        req.state.loop_history.push(LoopPassRecord {
            pass_index: 1,
            commit_sha: "abc123".to_owned(),
            findings: vec![standing.clone()],
            decisions: vec![Decision {
                contract_version: ContractVersion::current(),
                id: "decision-material".to_owned(),
                subject: DecisionSubject::Finding {
                    finding_id: standing.id,
                },
                verdict: DecisionVerdict::Material,
                rationale: "was material before the NoOp fix".to_owned(),
                provenance: provenance("judge-old", AgentRole::Judge, "glm"),
                extensions: BTreeMap::new(),
            }],
            patches: Vec::new(),
            judge_verdict: Some(DecisionVerdict::Material),
            fix_outcome: Some(RunOutcome::NoOp),
        });
        let runner = FakeEnsembleRunner::new(json!([]), vec![archive_agent("judge", "glm")]);
        let mut judge = EnsembleJudgeBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = judge
            .run_judge(&req, &mut workspace)
            .expect_err("NoOp-history findings need decisions");

        assert!(
            matches!(error, RunBodyError::Ensemble(message) if message.contains("no decisions"))
        );
    }

    #[test]
    fn judge_run_with_no_current_findings_records_convergence() {
        let req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "glm")],
        );
        let mut launcher = Pump19RunLauncher::new(
            FakeSessions,
            FakeReview {
                findings: Vec::new(),
            },
            FakeJudge {
                verdict: DecisionVerdict::Material,
            },
            FakeFix,
            MergeGateFinishBody,
        );
        let mut workspace = FakeWorkspace::default();

        let outcome = launcher
            .launch_run(req, &mut workspace)
            .expect("launch judge");

        assert_eq!(outcome.outcome, RunOutcome::Succeeded);
        assert_eq!(outcome.decisions.len(), 1);
        assert_eq!(outcome.decisions[0].verdict, DecisionVerdict::Converged);
    }

    #[test]
    fn fix_run_emits_patch_answering_material_findings() {
        let mut req = request(
            RunKind::Fix,
            vec![provenance("fixer", AgentRole::Fixer, "codex")],
        );
        req.state.findings.push(finding());
        req.state.decisions.push(Decision {
            contract_version: ContractVersion::current(),
            id: "decision-1".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: FindingId("finding-1".to_owned()),
            },
            verdict: DecisionVerdict::Material,
            rationale: "worth fixing".to_owned(),
            provenance: provenance("judge", AgentRole::Judge, "glm"),
            extensions: BTreeMap::new(),
        });
        let mut launcher = Pump19RunLauncher::new(
            FakeSessions,
            FakeReview {
                findings: Vec::new(),
            },
            FakeJudge {
                verdict: DecisionVerdict::Minor,
            },
            FakeFix,
            MergeGateFinishBody,
        );
        let mut workspace = FakeWorkspace::default();

        let outcome = launcher
            .launch_run(req, &mut workspace)
            .expect("launch fix");

        assert_eq!(outcome.patches.len(), 1);
        assert_eq!(
            outcome.patches[0].answers_findings,
            vec![FindingId("finding-1".to_owned())]
        );
        assert!(outcome.patches[0].idempotency_key.contains("patch-"));
    }

    #[test]
    fn fix_run_that_executes_without_material_findings_is_no_op() {
        let req = request(
            RunKind::Fix,
            vec![provenance("fixer", AgentRole::Fixer, "codex")],
        );
        let mut launcher = Pump19RunLauncher::new(
            FakeSessions,
            FakeReview {
                findings: Vec::new(),
            },
            FakeJudge {
                verdict: DecisionVerdict::Minor,
            },
            FakeFix,
            MergeGateFinishBody,
        );
        let mut workspace = FakeWorkspace::default();

        let outcome = launcher
            .launch_run(req, &mut workspace)
            .expect("launch fix");

        assert_eq!(outcome.outcome, RunOutcome::NoOp);
        assert!(outcome.patches.is_empty());
    }

    #[test]
    fn finish_uses_contract_clean_and_current_gate_without_forge_side_effects() {
        let mut req = request(RunKind::Finish, Vec::new());
        req.state.extensions.insert(
            EXT_FORGE_FACTS.to_owned(),
            json!({
                "contract_version": { "major": 1, "minor": 0 },
                "pr": { "repository": "acme/widgets", "id": "42" },
                "head": { "sha": "abc123" },
                "base": { "sha": "def456" },
                "branch_currency": "current",
                "cleanliness": "clean",
                "mergeability": "mergeable",
                "finish_label": null,
                "actor_permissions": []
            }),
        );
        let mut finish = MergeGateFinishBody;
        let mut workspace = FakeWorkspace::default();

        let outcome = finish.run_finish(&req, &mut workspace).expect("finish");

        assert_eq!(outcome, RunOutcome::Succeeded);
    }

    #[derive(Clone, Debug, Default)]
    struct FakeReadiness {
        verdict: Option<MergeReadiness>,
        calls: u32,
    }

    impl MergeReadinessCheck for FakeReadiness {
        fn verify(&mut self, _request: &RunLaunchRequest) -> Result<MergeReadiness, RunBodyError> {
            self.calls += 1;
            self.verdict
                .clone()
                .ok_or_else(|| RunBodyError::MergeReadiness("readiness exploded".to_owned()))
        }
    }

    fn finish_request_with_gate(clean_and_current: bool) -> RunLaunchRequest {
        let mut req = request(RunKind::Finish, Vec::new());
        let (currency, cleanliness) = if clean_and_current {
            ("current", "clean")
        } else {
            ("stale", "dirty")
        };
        req.state.extensions.insert(
            EXT_FORGE_FACTS.to_owned(),
            json!({
                "contract_version": { "major": 1, "minor": 0 },
                "pr": { "repository": "acme/widgets", "id": "42" },
                "head": { "sha": "abc123" },
                "base": { "sha": "def456" },
                "branch_currency": currency,
                "cleanliness": cleanliness,
                "mergeability": "mergeable",
                "finish_label": null,
                "actor_permissions": []
            }),
        );
        req
    }

    #[test]
    fn verified_finish_succeeds_when_gate_passes_and_check_is_ready() {
        let req = finish_request_with_gate(true);
        let mut finish = VerifiedMergeGateFinishBody::new(FakeReadiness {
            verdict: Some(MergeReadiness::Ready),
            calls: 0,
        });

        let outcome = finish
            .run_finish(&req, &mut FakeWorkspace::default())
            .expect("finish");

        assert_eq!(outcome, RunOutcome::Succeeded);
    }

    #[test]
    fn verified_finish_fails_when_check_says_not_ready() {
        let req = finish_request_with_gate(true);
        let mut finish = VerifiedMergeGateFinishBody::new(FakeReadiness {
            verdict: Some(MergeReadiness::NotReady {
                reason: "tests failed".to_owned(),
            }),
            calls: 0,
        });

        let outcome = finish
            .run_finish(&req, &mut FakeWorkspace::default())
            .expect("finish");

        assert_eq!(outcome, RunOutcome::Failed);
    }

    #[test]
    fn verified_finish_skips_the_check_when_the_gate_fails() {
        let req = finish_request_with_gate(false);
        let mut finish = VerifiedMergeGateFinishBody::new(FakeReadiness {
            verdict: Some(MergeReadiness::Ready),
            calls: 0,
        });

        let outcome = finish
            .run_finish(&req, &mut FakeWorkspace::default())
            .expect("finish");

        assert_eq!(outcome, RunOutcome::Failed);
        assert_eq!(finish.check.calls, 0);
    }

    #[test]
    fn verified_finish_propagates_check_execution_errors() {
        let req = finish_request_with_gate(true);
        let mut finish = VerifiedMergeGateFinishBody::new(FakeReadiness::default());

        let error = finish
            .run_finish(&req, &mut FakeWorkspace::default())
            .expect_err("check execution failure surfaces");

        assert!(matches!(error, RunBodyError::MergeReadiness(_)));
    }

    #[test]
    fn absent_readiness_check_leaves_the_gate_in_charge() {
        let req = finish_request_with_gate(true);
        let mut finish = VerifiedMergeGateFinishBody::new(None::<FakeReadiness>);

        let outcome = finish
            .run_finish(&req, &mut FakeWorkspace::default())
            .expect("finish");

        assert_eq!(outcome, RunOutcome::Succeeded);
    }
}
