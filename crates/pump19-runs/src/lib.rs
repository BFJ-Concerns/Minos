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
    fs,
    path::{Path, PathBuf},
    process::Command,
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
    JudgementBriefResult, JudgementRun, JudgementStatus, ReviewerResult, judgement_prompt,
    load_intent, load_judgement_briefs,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use thiserror::Error;

const EXT_FORGE_FACTS: &str = "pump19.core.forge_facts";
const EXT_RAW_STDOUT: &str = "pump19.runs.raw_stdout";
const EXT_RAW_STDERR: &str = "pump19.runs.raw_stderr";
const EXT_MODEL_FAMILY: &str = "pump19.runs.model_family";
const EXT_AGENT_ENGINE: &str = "pump19.core.agent_engine";

/// Errors raised while preparing sessions or executing run bodies.
#[derive(Debug, Error)]
pub enum RunBodyError {
    #[error("session preparation failed: {0}")]
    Session(String),
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
        let json_args = serde_json::to_string(&request.args).map_err(RunBodyError::EnsembleJson)?;
        let output = Command::new(&self.node_program)
            .arg(&self.launcher_path)
            .arg("--json-args")
            .arg(json_args)
            .arg("--timeout")
            .arg(request.timeout_ms.to_string())
            .arg(&request.script)
            .env("ENSEMBLE_RUN_RECORD_DIR", &request.archive_dir)
            .output()
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
        self.sessions
            .prepare(spec)
            .map_err(|error| CoreError::Launcher(error.to_string()))
    }

    fn launch_run(
        &mut self,
        request: RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<RunLaunchOutcome, CoreError> {
        let result = match request.run_kind {
            RunKind::Review => {
                self.review
                    .run_review(&request, workspace)
                    .map(|findings| RunLaunchOutcome {
                        outcome: RunOutcome::Succeeded,
                        findings,
                        decisions: Vec::new(),
                        patches: Vec::new(),
                        token_usage: None,
                    })
            }
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
                        })
                    })
            }
            RunKind::Fix => self
                .fix
                .run_fix(&request, workspace)
                .map(|patches| RunLaunchOutcome {
                    outcome: if patches.is_empty() {
                        RunOutcome::NoOp
                    } else {
                        RunOutcome::Succeeded
                    },
                    findings: Vec::new(),
                    decisions: Vec::new(),
                    patches,
                    token_usage: None,
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
}

impl<R> EnsembleReviewBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self { runner, config }
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
        let _ = workspace;
        let intent = load_intent(&request.workspace.root)?;
        let brief_inputs = load_judgement_briefs(&request.workspace.root)?
            .into_iter()
            .map(|brief| {
                let prompt = judgement_prompt(&request.workspace.root, &intent, &brief)?;
                Ok(json!({
                    "id": brief.id,
                    "title": brief.title,
                    "prompt": prompt,
                }))
            })
            .collect::<Result<Vec<_>, RunBodyError>>()?;
        let reviewers = expected_targets(request, AgentRole::Reviewer)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "reviewers": reviewers,
            "briefs": brief_inputs,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &reviewers)?;
        let run = serde_json::from_value::<JudgementRun>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        findings_from_judgement(request, &run)
    }
}

/// Significance judge body backed by a host-side ensemble workflow.
#[derive(Clone, Debug)]
pub struct EnsembleJudgeBody<R> {
    runner: R,
    config: EnsembleWorkflowConfig,
}

impl<R> EnsembleJudgeBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self { runner, config }
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
        let _ = workspace;
        let targets = expected_targets(request, AgentRole::Judge)?;
        let provenance = provenance_for_role(request, AgentRole::Judge)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "judges": targets,
            "pass_count": request.state.pass_index,
            "current_findings": request.state.findings,
            "findings": request.state.findings,
            "prior_verdicts": prior_judge_verdicts(request),
            "fix_outcomes": prior_fix_outcomes(request),
            "loop_history": request.state.loop_history,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &targets)?;
        let outputs = serde_json::from_value::<Vec<JudgeDecisionOutput>>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        if outputs.is_empty() && request.state.findings.is_empty() {
            return Ok(vec![convergence_decision(request, &provenance)]);
        }
        Ok(outputs
            .into_iter()
            .map(|output| output.into_decision(&request.run_id, &provenance))
            .collect())
    }
}

/// Fix body backed by a host-side ensemble workflow.
#[derive(Clone, Debug)]
pub struct EnsembleFixBody<R> {
    runner: R,
    config: EnsembleWorkflowConfig,
}

impl<R> EnsembleFixBody<R> {
    #[must_use]
    pub const fn new(runner: R, config: EnsembleWorkflowConfig) -> Self {
        Self { runner, config }
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
        let _ = workspace;
        let findings = material_findings(request);
        if findings.is_empty() {
            return Ok(Vec::new());
        }
        let targets = expected_targets(request, AgentRole::Fixer)?;
        let provenance = provenance_for_role(request, AgentRole::Fixer)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "workspace_root": request.workspace.root,
            "fixers": targets,
            "material_findings": findings,
        });
        let output = run_ensemble_workflow(&mut self.runner, &self.config, request, input)?;
        reconcile_ensemble_archive(&output.archive_dir, &targets)?;
        let change = serde_json::from_value::<PatchChange>(output.value)
            .map_err(RunBodyError::EnsembleJson)?;
        Ok(vec![patch_from_change(
            request, findings, change, provenance,
        )])
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
    let dedup_key = stable_id(
        "judgement",
        [
            brief.brief_id.as_str(),
            review.agent_id.as_str(),
            summary.as_str(),
        ],
    );
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
        let actual_model = record.resolved_model.as_ref().or(record.model.as_ref());
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
        collections::VecDeque,
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
    use pump19_judgement::{ReviewerResult, install_standalone};
    use serde_json::json;

    use super::*;

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
            status: "complete".to_owned(),
            validated_output: Some(json!({"ok": true})),
        }
    }

    fn ensemble_config(root: &Path) -> EnsembleWorkflowConfig {
        EnsembleWorkflowConfig {
            script: root.join("workflow.js"),
            archive_root: root.join("archives"),
            timeout_ms: 5_000,
        }
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
    fn review_body_runs_host_ensemble_and_reconciles_archive() {
        let root = tempfile::tempdir().expect("workspace root");
        install_standalone(
            root.path(),
            "sample",
            "Sample",
            "Prove ensemble review body",
            None,
        )
        .expect("install judgement files");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.run_id = RunId("review-run".to_owned());
        req.workspace.root = root.path().to_path_buf();
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
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("review body uses ensemble");

        assert_eq!(findings.len(), 1);
        assert!(workspace.execs.is_empty());
        assert_eq!(body.runner.requests.len(), 1);
        assert!(body.runner.requests[0].archive_dir.ends_with("review-run"));
    }

    #[test]
    fn review_body_fails_closed_on_malformed_workflow_output() {
        let root = tempfile::tempdir().expect("workspace root");
        install_standalone(
            root.path(),
            "sample",
            "Sample",
            "Prove malformed output",
            None,
        )
        .expect("install judgement files");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
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
        install_standalone(root.path(), "sample", "Sample", "Prove null archive", None)
            .expect("install judgement files");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
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
        install_standalone(
            root.path(),
            "sample",
            "Sample",
            "Prove mismatch archive",
            None,
        )
        .expect("install judgement files");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
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
    fn workflow_timeout_error_fails_closed() {
        let root = tempfile::tempdir().expect("workspace root");
        install_standalone(root.path(), "sample", "Sample", "Prove timeout", None)
            .expect("install judgement files");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.workspace.root = root.path().to_path_buf();
        let runner = FailingEnsembleRunner {
            message: "workflow timed out".to_owned(),
        };
        let mut body = EnsembleReviewBody::new(runner, ensemble_config(root.path()));
        let mut workspace = FakeWorkspace::default();

        let error = body
            .run_review(&req, &mut workspace)
            .expect_err("timeout fails closed");

        assert!(matches!(error, RunBodyError::Ensemble(message) if message.contains("timed out")));
    }

    #[test]
    fn judge_run_produces_material_decisions_for_findings() {
        let mut req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "gemini")],
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
            vec![provenance("judge", AgentRole::Judge, "gemini")],
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
                provenance: provenance("judge-old", AgentRole::Judge, "gemini"),
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
            vec![archive_agent("judge", "gemini")],
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
    fn judge_run_with_no_current_findings_records_convergence() {
        let req = request(
            RunKind::Judge,
            vec![provenance("judge", AgentRole::Judge, "gemini")],
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
            provenance: provenance("judge", AgentRole::Judge, "gemini"),
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
}
