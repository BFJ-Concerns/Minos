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
    env,
    fmt::Write as _,
    fs,
    path::{Path, PathBuf},
    process::Command,
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant},
};

use pump19_contract::{
    AgentId, AgentRole, BarCheckRecord, CertaintyClass, CitedEvidence, Confidence, ContractVersion,
    CoverageRecord, Decision, DecisionSubject, DecisionVerdict, Extensions, FamilySplit, Finding,
    FindingId, FindingLocation, FindingVerification, ForgeFacts, ManifestBounds, ManifestBrief,
    ManifestLoopHistory, ManifestRepositoryPolicy, ManifestWorkspace, ModelProvenance, Patch,
    PatchChange, PatchId, PriorityClass, ReviewVerdict, RunId, RunKind, RunManifest, RunOutcome,
    Severity, SourceRange, VerificationStatus,
};
use pump19_core::{
    AgentLaunchSpec, CoreError, PreparedAgent, RunLaunchOutcome, RunLaunchRequest, RunLauncher,
    WorkspaceExecutor,
};
use pump19_engine::{
    EngineError, EngineKind, EngineProvenance, EngineRun, EngineSessionLauncher,
    ExitClassification, LaunchBounds, LaunchSpec, RepairAttempt, RepairStrategy, TranscriptKind,
    WriteAccess,
};
use pump19_judgement::{
    IntentStatement, JudgementBrief, JudgementBriefResult, JudgementRun, JudgementStatus,
    ReviewerResult, evidence_text,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use thiserror::Error;

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt as _;
#[cfg(unix)]
use std::os::unix::process::CommandExt as _;

const EXT_FORGE_FACTS: &str = "pump19.core.forge_facts";
const EXT_RAW_STDOUT: &str = "pump19.runs.raw_stdout";
const EXT_RAW_STDERR: &str = "pump19.runs.raw_stderr";
const EXT_MODEL_FAMILY: &str = "pump19.runs.model_family";
const EXT_AGENT_ENGINE: &str = "pump19.core.agent_engine";
const EXT_FRAME_TITLE: &str = "pump19.frame.title";
const EXT_FRAME_EXPLANATION: &str = "pump19.frame.explanation";
const EXT_FRAME_SUGGESTION: &str = "pump19.frame.suggestion";
const EXT_FRAME_PRIORITY: &str = "pump19.frame.priority";
const EXT_FRAME_VERIFICATION: &str = "pump19.frame.verification";
const EXT_BRIEF_WARNINGS: &str = "pump19.frame.brief_warnings";
const REVIEW_EVIDENCE_DIR: &str = ".pump19/review";
const REVIEW_DIFF_FILE: &str = "diff.patch";
const INLINE_REVIEW_DIFF_BYTE_LIMIT: usize = 64 * 1024;
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
    #[error("frame I/O failed while trying to {action}: {source}")]
    FrameIo {
        action: String,
        #[source]
        source: std::io::Error,
    },
    #[error("engine session failed: {0}")]
    Engine(#[from] EngineError),
    #[error("lead session ended without usable output: {0}")]
    LeadSession(String),
    #[error("lead output failed validation: {0}")]
    LeadValidation(String),
    #[error("review output failed a publication gate: {0}")]
    PostGate(String),
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

/// Launch seam for the lead engine session.
pub trait LeadEngineSession {
    /// Launches the lead session against a prepared run directory.
    ///
    /// # Errors
    ///
    /// Returns an error when the engine process cannot launch, times out, or the
    /// configured repair path fails.
    fn launch_lead(
        &mut self,
        spec: &LaunchSpec,
        repair: &mut dyn RepairStrategy,
    ) -> Result<EngineRun, EngineError>;
}

impl LeadEngineSession for EngineSessionLauncher {
    fn launch_lead(
        &mut self,
        spec: &LaunchSpec,
        repair: &mut dyn RepairStrategy,
    ) -> Result<EngineRun, EngineError> {
        self.launch(spec, repair)
    }
}

/// Stable workflow slots exposed to the lead session through `pump19-workflow`.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum ReviewWorkflowSlot {
    SpecialistFanout,
    VerifyFindings,
    AssembleReview,
    BarCheck,
    RepairOutput,
}

impl ReviewWorkflowSlot {
    const fn as_str(self) -> &'static str {
        match self {
            Self::SpecialistFanout => "specialist-fanout",
            Self::VerifyFindings => "verify-findings",
            Self::AssembleReview => "assemble-review",
            Self::BarCheck => "bar-check",
            Self::RepairOutput => "repair-output",
        }
    }
}

/// One workflow script made available in a run directory.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LeadWorkflowScript {
    pub slot: ReviewWorkflowSlot,
    pub path: PathBuf,
}

/// Engine settings for the lead session.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LeadEngineConfig {
    pub engine: EngineKind,
    pub executable: PathBuf,
    pub requested_model: Option<String>,
    pub bounds: LaunchBounds,
    pub write_access: WriteAccess,
    pub env: BTreeMap<String, String>,
}

/// Review-frame policy that remains additive until the stage-5 core switch.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LeadSessionFrameConfig {
    pub run_root: PathBuf,
    pub node_program: PathBuf,
    pub ensemble_launcher: PathBuf,
    pub workflows: Vec<LeadWorkflowScript>,
    pub mission_template: String,
    pub selected_briefs: Vec<ManifestBrief>,
    pub brief_warnings: Vec<String>,
    pub occasion: String,
    pub materiality_threshold: PriorityClass,
    pub lead_engine: LeadEngineConfig,
}

/// Result from one frame-driven review pass.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LeadSessionFrameResult {
    pub manifest: RunManifest,
    pub manifest_path: PathBuf,
    pub run_dir: PathBuf,
    pub findings: Vec<Finding>,
    pub suppressed_findings: Vec<SuppressedFinding>,
    pub coverage: CoverageRecord,
    pub verdict: ReviewVerdict,
    pub lead_provenance: EngineProvenance,
    pub lead_transcripts: Vec<PathBuf>,
    pub workflow_archives: Vec<PathBuf>,
    pub repair_attempts: u32,
}

/// Candidate finding rejected by a frame publication gate.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SuppressedFinding {
    pub dedup_key: String,
    pub reason: String,
    pub value: Value,
}

/// Review body backed by one lead engine session framed by deterministic code.
#[derive(Clone, Debug)]
pub struct LeadSessionReviewBody<E, R> {
    engine: E,
    workflow_runner: R,
    config: LeadSessionFrameConfig,
    last_result: Option<LeadSessionFrameResult>,
}

impl<E, R> LeadSessionReviewBody<E, R> {
    #[must_use]
    pub const fn new(engine: E, workflow_runner: R, config: LeadSessionFrameConfig) -> Self {
        Self {
            engine,
            workflow_runner,
            config,
            last_result: None,
        }
    }

    #[must_use]
    pub const fn last_result(&self) -> Option<&LeadSessionFrameResult> {
        self.last_result.as_ref()
    }
}

impl<E, R> ReviewRunBody for LeadSessionReviewBody<E, R>
where
    E: LeadEngineSession,
    R: EnsembleWorkflowRunner,
{
    fn run_review(
        &mut self,
        request: &RunLaunchRequest,
        workspace: &mut dyn WorkspaceExecutor,
    ) -> Result<Vec<Finding>, RunBodyError> {
        let _ = workspace;
        self.last_result = None;
        let result = run_lead_session_frame(
            &mut self.engine,
            &mut self.workflow_runner,
            &self.config,
            request,
        )?;
        let findings = result.findings.clone();
        self.last_result = Some(result);
        Ok(findings)
    }

    fn last_ensemble_archive_path(&self) -> Option<String> {
        self.last_result
            .as_ref()
            .and_then(|result| result.workflow_archives.first())
            .map(|path| path.display().to_string())
    }
}

fn run_lead_session_frame(
    engine: &mut dyn LeadEngineSession,
    workflow_runner: &mut dyn EnsembleWorkflowRunner,
    config: &LeadSessionFrameConfig,
    request: &RunLaunchRequest,
) -> Result<LeadSessionFrameResult, RunBodyError> {
    let review_evidence = load_review_evidence(&request.workspace.root)?;
    let run_dir = prepare_run_directory(config, request, &review_evidence)?;
    let manifest = build_run_manifest(config, request, &review_evidence);
    let manifest_path = run_dir.join("manifest.json");
    write_json_file(&manifest_path, &manifest)?;
    write_mission(
        &run_dir.join("mission.md"),
        &config.mission_template,
        &manifest,
    )?;
    write_schema(&run_dir.join("schema/review-output.schema.json"))?;
    write_workflow_shim(config, &run_dir)?;
    write_exec_shim(&run_dir)?;

    let mut repair = WorkflowRepairStrategy {
        runner: workflow_runner,
        config,
        run_dir: run_dir.clone(),
    };
    let engine_run = engine.launch_lead(
        &LaunchSpec {
            engine: config.lead_engine.engine,
            executable: config.lead_engine.executable.clone(),
            working_dir: run_dir.clone(),
            prompt_path: run_dir.join("mission.md"),
            schema_path: Some(run_dir.join("schema/review-output.schema.json")),
            archive_dir: run_dir.join("archive/lead"),
            requested_model: config.lead_engine.requested_model.clone(),
            write_access: config.lead_engine.write_access,
            bounds: config.lead_engine.bounds.clone(),
            env: config.lead_engine.env.clone(),
        },
        &mut repair,
    )?;

    if engine_run.classification != ExitClassification::Success {
        return Err(RunBodyError::LeadSession(format!(
            "{:?}",
            engine_run.classification
        )));
    }
    let Some(output) = engine_run.output.clone() else {
        return Err(RunBodyError::LeadSession(
            "engine reported success without JSON output".to_owned(),
        ));
    };
    let (payload, frame_repair_attempts) = parse_or_repair_lead_payload(
        &output,
        &mut repair,
        config.lead_engine.engine,
        run_dir.join("schema/review-output.schema.json"),
    )?;
    let coverage = payload.coverage;
    let (findings, suppressed_findings) = frame_findings(request, &manifest, payload.findings)?;
    let verdict = frame_verdict(
        payload.verdict_proposal,
        &coverage,
        findings.len(),
        suppressed_findings.len(),
    )?;
    let workflow_archives = workflow_archive_dirs(&run_dir)?;
    Ok(LeadSessionFrameResult {
        manifest,
        manifest_path,
        run_dir,
        findings,
        suppressed_findings,
        coverage,
        verdict,
        lead_provenance: engine_run.provenance,
        lead_transcripts: engine_run
            .transcripts
            .into_iter()
            .filter(|transcript| transcript.kind != TranscriptKind::FinalOutput)
            .map(|transcript| transcript.path)
            .collect(),
        workflow_archives,
        repair_attempts: engine_run.repair_attempts + frame_repair_attempts,
    })
}

fn parse_or_repair_lead_payload(
    output: &Value,
    repair: &mut dyn RepairStrategy,
    engine: EngineKind,
    schema_path: PathBuf,
) -> Result<(LeadReviewPayload, u32), RunBodyError> {
    match serde_json::from_value::<LeadReviewPayload>(output.clone()) {
        Ok(payload) => Ok((payload, 0)),
        Err(error) => {
            let attempt = RepairAttempt {
                attempt: 1,
                engine,
                schema_path: Some(schema_path),
                invalid_output: output.to_string(),
                error: error.to_string(),
            };
            let Some(repaired) = repair.repair(attempt)? else {
                return Err(RunBodyError::EnsembleJson(error));
            };
            serde_json::from_str::<LeadReviewPayload>(&repaired)
                .map(|payload| (payload, 1))
                .map_err(RunBodyError::EnsembleJson)
        }
    }
}

fn prepare_run_directory(
    config: &LeadSessionFrameConfig,
    request: &RunLaunchRequest,
    review_evidence: &ReviewEvidence,
) -> Result<PathBuf, RunBodyError> {
    let run_dir = config.run_root.join(safe_path_segment(&request.run_id.0));
    create_dir(&run_dir, "create run directory")?;
    for relative in ["bin", "workflows", "out", "archive/workflows", "schema"] {
        create_dir(&run_dir.join(relative), "create run directory member")?;
    }
    for script in &config.workflows {
        let target = run_dir
            .join("workflows")
            .join(format!("{}.js", script.slot.as_str()));
        copy_file(&script.path, &target, "copy workflow script")?;
    }
    copy_file(
        &review_evidence.diff_path,
        &run_dir.join("out/diff.patch"),
        "copy review diff into run directory",
    )?;
    Ok(run_dir)
}

fn build_run_manifest(
    config: &LeadSessionFrameConfig,
    request: &RunLaunchRequest,
    review_evidence: &ReviewEvidence,
) -> RunManifest {
    let changed_lines = changed_lines_from_diff(&review_evidence.diff);
    let changed_files = changed_lines.keys().cloned().collect::<Vec<_>>();
    let mut extensions = BTreeMap::new();
    if !config.brief_warnings.is_empty() {
        extensions.insert(
            EXT_BRIEF_WARNINGS.to_owned(),
            Value::Array(
                config
                    .brief_warnings
                    .iter()
                    .cloned()
                    .map(Value::String)
                    .collect(),
            ),
        );
    }
    RunManifest {
        contract_version: ContractVersion::current(),
        run_id: request.run_id.clone(),
        run_kind: request.run_kind,
        pr: request.state.pr.clone(),
        commit_sha: request.state.commit_sha.clone(),
        base_sha: base_sha(request).unwrap_or_default(),
        occasion: config.occasion.clone(),
        workspace: ManifestWorkspace {
            tree_root: request.workspace.root.display().to_string(),
            diff_path: review_evidence.diff_path.display().to_string(),
            governing_content_dir: request
                .workspace
                .root
                .join(".pump19/review/governing")
                .display()
                .to_string(),
        },
        changed_files,
        changed_lines,
        selected_briefs: config.selected_briefs.clone(),
        loop_history: ManifestLoopHistory {
            pass_count: request.state.pass_index,
            prior_verdicts: request
                .state
                .loop_history
                .iter()
                .filter_map(|record| record.judge_verdict)
                .map(|verdict| format!("{verdict:?}"))
                .collect(),
            fix_survival_by_dedup_key: fix_survival_by_dedup_key(request),
        },
        bounds: ManifestBounds {
            wall_clock_ms: u64::try_from(config.lead_engine.bounds.wall_clock.as_millis())
                .unwrap_or(u64::MAX),
            max_budget_usd: config.lead_engine.bounds.max_budget_usd.clone(),
            max_total_tokens: config.lead_engine.bounds.max_total_tokens,
        },
        repository_policy: ManifestRepositoryPolicy {
            fix_before_merge_priority: config.materiality_threshold,
        },
        extensions,
    }
}

fn base_sha(request: &RunLaunchRequest) -> Option<String> {
    request
        .state
        .extensions
        .get(EXT_FORGE_FACTS)
        .and_then(|value| value.get("base"))
        .and_then(|base| base.get("sha"))
        .and_then(Value::as_str)
        .map(ToOwned::to_owned)
}

fn fix_survival_by_dedup_key(request: &RunLaunchRequest) -> BTreeMap<String, u32> {
    let mut survived = BTreeMap::new();
    for record in &request.state.loop_history {
        if record.fix_outcome == Some(RunOutcome::NoOp) {
            for finding in &record.findings {
                *survived.entry(finding.dedup_key.clone()).or_insert(0) += 1;
            }
        }
    }
    survived
}

fn write_mission(path: &Path, template: &str, manifest: &RunManifest) -> Result<(), RunBodyError> {
    let manifest_json =
        serde_json::to_string_pretty(manifest).map_err(RunBodyError::EnsembleJson)?;
    let rendered = render_prompt_template(
        template,
        &[
            ("manifest_path", "manifest.json".to_owned()),
            ("manifest", manifest_json),
            ("run_id", manifest.run_id.0.clone()),
        ],
    )?;
    write_file(path, rendered.as_bytes(), "write mission prompt")
}

fn write_schema(path: &Path) -> Result<(), RunBodyError> {
    write_file(
        path,
        REVIEW_OUTPUT_SCHEMA.as_bytes(),
        "write review output schema",
    )
}

fn write_workflow_shim(
    config: &LeadSessionFrameConfig,
    run_dir: &Path,
) -> Result<(), RunBodyError> {
    let mut cases = String::new();
    for script in &config.workflows {
        let _ = writeln!(
            cases,
            "  {}) workflow=\"$run_dir/workflows/{}.js\" ;;",
            script.slot.as_str(),
            script.slot.as_str()
        );
    }
    let source = format!(
        r#"#!/bin/sh
set -eu
run_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
slot="${{1:?slot is required}}"
input_path="${{2:?input JSON path is required}}"
case "$slot" in
{cases}  *) echo "unsupported workflow slot: $slot" >&2; exit 64 ;;
esac
mkdir -p "$run_dir/archive/workflows/$slot" "$run_dir/out"
printf '{{"slot":"%s","input_path":"%s"}}\n' "$slot" "$input_path" >> "$run_dir/out/workflow-boundary.jsonl"
wrapper="$run_dir/out/.pump19-workflow-$slot-$$.js"
trap 'rm -f "$wrapper"' EXIT INT TERM
awk '
  BEGIN {{ inserted = 0 }}
  {{ print }}
  inserted == 0 && $0 ~ /^}};[[:space:]]*$/ {{
    print "const args = JSON.parse((await import(\"node:fs\")).readFileSync(process.env.PUMP19_WORKFLOW_INPUT_PATH, \"utf8\"));"
    inserted = 1
  }}
  END {{
    if (inserted == 0) {{
      print "workflow script did not expose a top-level meta object terminator" > "/dev/stderr"
      exit 65
    }}
  }}
' "$workflow" > "$wrapper"
PUMP19_WORKFLOW_INPUT_PATH="$input_path" ENSEMBLE_RUN_RECORD_DIR="$run_dir/archive/workflows/$slot" "{node}" "{ensemble}" --timeout 180000 "$wrapper"
"#,
        node = config.node_program.display(),
        ensemble = config.ensemble_launcher.display(),
    );
    let path = run_dir.join("bin/pump19-workflow");
    write_file(&path, source.as_bytes(), "write workflow shim")?;
    make_executable(&path)
}

fn write_exec_shim(run_dir: &Path) -> Result<(), RunBodyError> {
    let path = run_dir.join("bin/pump19-exec");
    write_file(
        &path,
        b"#!/bin/sh\nset -eu\necho \"pump19-exec is provided by the stage-5 workspace lease integration\" >&2\nexit 69\n",
        "write exec shim",
    )?;
    make_executable(&path)
}

fn workflow_archive_dirs(run_dir: &Path) -> Result<Vec<PathBuf>, RunBodyError> {
    let root = run_dir.join("archive/workflows");
    if !root.exists() {
        return Ok(Vec::new());
    }
    fs::read_dir(&root)
        .map_err(|source| RunBodyError::FrameIo {
            action: format!("read workflow archive root {}", root.display()),
            source,
        })?
        .map(|entry| {
            entry
                .map(|entry| entry.path())
                .map_err(|source| RunBodyError::FrameIo {
                    action: format!("read workflow archive root {}", root.display()),
                    source,
                })
        })
        .filter(|entry| entry.as_ref().map_or(true, |path| path.is_dir()))
        .collect()
}

struct WorkflowRepairStrategy<'a> {
    runner: &'a mut dyn EnsembleWorkflowRunner,
    config: &'a LeadSessionFrameConfig,
    run_dir: PathBuf,
}

impl RepairStrategy for WorkflowRepairStrategy<'_> {
    fn repair(&mut self, attempt: RepairAttempt) -> Result<Option<String>, EngineError> {
        let Some(script) = self
            .config
            .workflows
            .iter()
            .find(|script| script.slot == ReviewWorkflowSlot::RepairOutput)
        else {
            return Ok(None);
        };
        let schema = attempt
            .schema_path
            .as_ref()
            .and_then(|path| fs::read_to_string(path).ok())
            .unwrap_or_else(|| "{}".to_owned());
        let output = self
            .runner
            .run_workflow(EnsembleWorkflowRequest {
                script: script.path.clone(),
                args: json!({
                    "repairer": {
                        "engine": format!("{:?}", attempt.engine).to_lowercase(),
                        "agent_id": format!("repair-attempt-{}", attempt.attempt),
                    },
                    "schema": serde_json::from_str::<Value>(&schema).unwrap_or(Value::Null),
                    "invalid_output": attempt.invalid_output,
                    "errors": [attempt.error],
                    "agent_timeout_ms": 120_000,
                }),
                archive_dir: self
                    .run_dir
                    .join("archive/workflows/repair-output")
                    .join(attempt.attempt.to_string()),
                timeout_ms: 120_000,
            })
            .map_err(|error| EngineError::Repair(error.to_string()))?;
        output
            .value
            .get("repaired_output")
            .and_then(Value::as_str)
            .map_or_else(
                || Ok(Some(output.value.to_string())),
                |text| Ok(Some(text.to_owned())),
            )
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct LeadReviewPayload {
    findings: Vec<LeadFindingPayload>,
    coverage: CoverageRecord,
    verdict_proposal: LeadVerdictProposal,
    #[serde(default)]
    session_notes: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct LeadFindingPayload {
    dedup_hint: String,
    source_brief: String,
    title: String,
    explanation: String,
    #[serde(default)]
    suggestion: Option<String>,
    priority: PriorityClass,
    #[serde(default = "default_certainty")]
    certainty: CertaintyClass,
    producer_agent_id: AgentId,
    locations: Vec<LeadFindingLocation>,
    verification: LeadFindingVerification,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct LeadFindingLocation {
    path: String,
    line: u32,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct LeadFindingVerification {
    status: WorkflowVerificationStatus,
    #[serde(default)]
    reason: Option<String>,
    #[serde(default)]
    verifier_agent_id: Option<AgentId>,
    #[serde(default)]
    evidence: Vec<WorkflowEvidence>,
    cross_family: WorkflowFamilySplit,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
enum WorkflowVerificationStatus {
    Verified,
    Rejected,
    Unverified,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct WorkflowEvidence {
    path: String,
    line: u32,
    #[serde(default)]
    quote: Option<String>,
    #[serde(default)]
    note: Option<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
enum WorkflowFamilySplit {
    CrossFamily,
    SameFamily,
    Unknown,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "snake_case", tag = "verdict")]
enum LeadVerdictProposal {
    Converged {
        bar_check: BarCheckRecord,
    },
    FindingsPosted,
    StandingFindings {
        finding_dedup_keys: Vec<String>,
        rationale: String,
    },
    BarFailed {
        bar_check: BarCheckRecord,
    },
    BarCheckDegraded {
        attempts: u32,
        last_error: String,
    },
    PartialCoverage,
}

const fn default_certainty() -> CertaintyClass {
    CertaintyClass::Advisory
}

fn frame_findings(
    request: &RunLaunchRequest,
    manifest: &RunManifest,
    payloads: Vec<LeadFindingPayload>,
) -> Result<(Vec<Finding>, Vec<SuppressedFinding>), RunBodyError> {
    let mut findings = Vec::new();
    let mut suppressed = Vec::new();
    let mut seen = BTreeSet::new();
    for payload in payloads {
        let value = serde_json::to_value(&payload).map_err(RunBodyError::EnsembleJson)?;
        match post_gate_finding(request, manifest, payload, &mut seen) {
            Ok(finding) => findings.push(finding),
            Err(reason) => suppressed.push(SuppressedFinding {
                dedup_key: value
                    .get("dedup_hint")
                    .and_then(Value::as_str)
                    .unwrap_or("unknown")
                    .to_owned(),
                reason,
                value,
            }),
        }
    }
    Ok((findings, suppressed))
}

fn post_gate_finding(
    request: &RunLaunchRequest,
    manifest: &RunManifest,
    payload: LeadFindingPayload,
    seen: &mut BTreeSet<String>,
) -> Result<Finding, String> {
    if !seen.insert(payload.dedup_hint.clone()) {
        return Err(format!(
            "duplicate finding dedup key {}",
            payload.dedup_hint
        ));
    }
    let verification = finding_verification(request, &payload)?;
    if verification.status != VerificationStatus::Verified {
        return Err("finding is not independently verified".to_owned());
    }
    let verifier = verification.verifier.as_ref().ok_or_else(|| {
        "verified finding is unpostable: Unverified{reason:\"missing verifier identity\"}"
            .to_owned()
    })?;
    for location in &payload.locations {
        let Some(lines) = manifest.changed_lines.get(&location.path) else {
            return Err(format!(
                "finding location {}:{} is outside the changed files",
                location.path, location.line
            ));
        };
        if !lines.contains(&location.line) {
            return Err(format!(
                "finding location {}:{} is outside changed lines",
                location.path, location.line
            ));
        }
    }
    let producer = request
        .provenance
        .iter()
        .find(|provenance| provenance.agent_id == payload.producer_agent_id)
        .cloned()
        .ok_or_else(|| {
            format!(
                "producer provenance {} is absent",
                payload.producer_agent_id.0
            )
        })?;
    if verifier.agent_id == producer.agent_id {
        return Err("finding verifier is the producing agent".to_owned());
    }
    if verifier.session_id == producer.session_id {
        return Err("finding verifier reused the producing session".to_owned());
    }
    let mut extensions = BTreeMap::new();
    extensions.insert(
        EXT_FRAME_TITLE.to_owned(),
        Value::String(payload.title.clone()),
    );
    extensions.insert(
        EXT_FRAME_EXPLANATION.to_owned(),
        Value::String(payload.explanation.clone()),
    );
    if let Some(suggestion) = &payload.suggestion {
        extensions.insert(
            EXT_FRAME_SUGGESTION.to_owned(),
            Value::String(suggestion.clone()),
        );
    }
    extensions.insert(
        EXT_FRAME_PRIORITY.to_owned(),
        serde_json::to_value(payload.priority).map_err(|error| error.to_string())?,
    );
    extensions.insert(
        EXT_FRAME_VERIFICATION.to_owned(),
        serde_json::to_value(&verification).map_err(|error| error.to_string())?,
    );
    Ok(Finding {
        contract_version: ContractVersion::current(),
        id: FindingId(stable_id(
            "finding",
            [request.run_id.0.as_str(), payload.dedup_hint.as_str()],
        )),
        dedup_key: payload.dedup_hint,
        source_brief: payload.source_brief,
        dimension: "review".to_owned(),
        summary: payload.title,
        severity: severity_for_priority(payload.priority),
        confidence: Confidence::High,
        certainty: payload.certainty,
        provenance: producer,
        locations: payload
            .locations
            .into_iter()
            .map(|location| FindingLocation::File {
                path: location.path,
                line: Some(location.line),
                range: None,
            })
            .collect(),
        extensions,
    })
}

fn finding_verification(
    request: &RunLaunchRequest,
    payload: &LeadFindingPayload,
) -> Result<FindingVerification, String> {
    let verifier = payload
        .verification
        .verifier_agent_id
        .as_ref()
        .map(|agent_id| {
            request
                .provenance
                .iter()
                .find(|provenance| provenance.agent_id == *agent_id)
                .cloned()
                .ok_or_else(|| format!("verifier provenance {} is absent", agent_id.0))
        })
        .transpose()?;
    Ok(FindingVerification {
        status: match &payload.verification.status {
            WorkflowVerificationStatus::Verified => VerificationStatus::Verified,
            WorkflowVerificationStatus::Rejected => VerificationStatus::Rejected {
                reason: payload
                    .verification
                    .reason
                    .clone()
                    .unwrap_or_else(|| "verifier rejected the finding".to_owned()),
            },
            WorkflowVerificationStatus::Unverified => VerificationStatus::Unverified {
                reason: payload
                    .verification
                    .reason
                    .clone()
                    .unwrap_or_else(|| "verification did not complete".to_owned()),
            },
        },
        verifier,
        evidence: payload
            .verification
            .evidence
            .iter()
            .map(|evidence| CitedEvidence {
                path: evidence.path.clone(),
                line_range: Some(SourceRange {
                    start_line: evidence.line,
                    start_column: None,
                    end_line: evidence.line,
                    end_column: None,
                }),
                quote: evidence.quote.clone(),
                note: evidence.note.clone(),
            })
            .collect(),
        cross_family: match payload.verification.cross_family {
            WorkflowFamilySplit::CrossFamily => FamilySplit::CrossFamily,
            WorkflowFamilySplit::SameFamily => FamilySplit::SameFamily,
            WorkflowFamilySplit::Unknown => FamilySplit::Unknown,
        },
        extensions: BTreeMap::new(),
    })
}

fn frame_verdict(
    proposal: LeadVerdictProposal,
    coverage: &CoverageRecord,
    material_count: usize,
    suppressed_count: usize,
) -> Result<ReviewVerdict, RunBodyError> {
    Ok(match proposal {
        LeadVerdictProposal::Converged { bar_check } => {
            if material_count != 0 {
                return Err(RunBodyError::PostGate(
                    "convergence proposal included postable material findings".to_owned(),
                ));
            }
            if !coverage.complete {
                ReviewVerdict::PartialCoverage
            } else if bar_check.passed {
                ReviewVerdict::Converged { bar_check }
            } else {
                ReviewVerdict::BarFailed { bar_check }
            }
        }
        LeadVerdictProposal::FindingsPosted => ReviewVerdict::FindingsPosted {
            material: u32::try_from(material_count).unwrap_or(u32::MAX),
            suppressed: u32::try_from(suppressed_count).unwrap_or(u32::MAX),
        },
        LeadVerdictProposal::StandingFindings {
            finding_dedup_keys,
            rationale,
        } => ReviewVerdict::StandingFindings {
            finding_dedup_keys,
            rationale,
        },
        LeadVerdictProposal::BarFailed { bar_check } => ReviewVerdict::BarFailed { bar_check },
        LeadVerdictProposal::BarCheckDegraded {
            attempts,
            last_error,
        } => ReviewVerdict::BarCheckDegraded {
            attempts,
            last_error,
        },
        LeadVerdictProposal::PartialCoverage => ReviewVerdict::PartialCoverage,
    })
}

const fn severity_for_priority(priority: PriorityClass) -> Severity {
    match priority {
        PriorityClass::P0 => Severity::Critical,
        PriorityClass::P1 => Severity::High,
        PriorityClass::P2 => Severity::Medium,
        PriorityClass::P3 => Severity::Low,
    }
}

fn changed_lines_from_diff(diff: &str) -> BTreeMap<String, Vec<u32>> {
    let mut changed = BTreeMap::<String, Vec<u32>>::new();
    let mut current_path = None;
    let mut new_line = 0_u32;
    for line in diff.lines() {
        if let Some(path) = line.strip_prefix("+++ b/") {
            current_path = Some(path.to_owned());
            continue;
        }
        if let Some((start, _count)) = parse_hunk_header(line) {
            new_line = start;
            continue;
        }
        let Some(path) = &current_path else {
            continue;
        };
        if line.starts_with('+') && !line.starts_with("+++") {
            changed.entry(path.clone()).or_default().push(new_line);
            new_line = new_line.saturating_add(1);
        } else if !line.starts_with('-') {
            new_line = new_line.saturating_add(1);
        }
    }
    changed
}

fn parse_hunk_header(line: &str) -> Option<(u32, u32)> {
    let hunk = line.strip_prefix("@@ ")?;
    let plus = hunk.split_whitespace().find(|part| part.starts_with('+'))?;
    let range = plus.trim_start_matches('+');
    let (start, count) = range.split_once(',').unwrap_or((range, "1"));
    Some((start.parse().ok()?, count.parse().ok()?))
}

fn create_dir(path: &Path, action: &str) -> Result<(), RunBodyError> {
    fs::create_dir_all(path).map_err(|source| RunBodyError::FrameIo {
        action: format!("{action} {}", path.display()),
        source,
    })
}

fn copy_file(source: &Path, target: &Path, action: &str) -> Result<(), RunBodyError> {
    fs::copy(source, target)
        .map(|_bytes| ())
        .map_err(|source_error| RunBodyError::FrameIo {
            action: format!("{action} {} to {}", source.display(), target.display()),
            source: source_error,
        })
}

fn write_file(path: &Path, bytes: &[u8], action: &str) -> Result<(), RunBodyError> {
    fs::write(path, bytes).map_err(|source| RunBodyError::FrameIo {
        action: format!("{action} {}", path.display()),
        source,
    })
}

fn write_json_file(path: &Path, value: &impl Serialize) -> Result<(), RunBodyError> {
    let bytes = serde_json::to_vec_pretty(value).map_err(RunBodyError::EnsembleJson)?;
    write_file(path, &bytes, "write JSON file")
}

fn make_executable(path: &Path) -> Result<(), RunBodyError> {
    #[cfg(unix)]
    {
        let metadata = fs::metadata(path).map_err(|source| RunBodyError::FrameIo {
            action: format!("read permissions for {}", path.display()),
            source,
        })?;
        let mut permissions = metadata.permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(path, permissions).map_err(|source| RunBodyError::FrameIo {
            action: format!("set executable bit on {}", path.display()),
            source,
        })
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        Ok(())
    }
}

const REVIEW_OUTPUT_SCHEMA: &str = r#"{
  "type": "object",
  "additionalProperties": false,
  "required": ["findings", "coverage", "verdict_proposal", "session_notes"],
  "properties": {
    "findings": { "type": "array" },
    "coverage": {
      "type": "object",
      "additionalProperties": true,
      "required": ["complete", "visited", "unvisited", "account"],
      "properties": {
        "complete": { "type": "boolean" },
        "visited": { "type": "array", "items": { "type": "string" } },
        "unvisited": { "type": "array", "items": { "type": "string" } },
        "account": { "type": "string" },
        "extensions": { "type": "object" }
      }
    },
    "verdict_proposal": { "type": "object" },
    "session_notes": { "type": "string" }
  }
}"#;

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
        let mut command = Command::new(&self.node_program);
        command
            .env_clear()
            .envs(filtered_ensemble_env(env::vars()))
            .arg(&self.launcher_path)
            .arg("--timeout")
            .arg(request.timeout_ms.to_string())
            .arg(wrapper.path())
            .env("ENSEMBLE_RUN_RECORD_DIR", &request.archive_dir)
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped());
        #[cfg(unix)]
        command.process_group(0);
        let mut child = command
            .spawn()
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let stdout = child.stdout.take().ok_or_else(|| {
            RunBodyError::Ensemble("launcher stdout pipe was unavailable".to_owned())
        })?;
        let stderr = child.stderr.take().ok_or_else(|| {
            RunBodyError::Ensemble("launcher stderr pipe was unavailable".to_owned())
        })?;
        let stdout_thread = thread::spawn(move || read_stream_to_string(stdout));
        let stderr_thread = thread::spawn(move || read_stream_to_string(stderr));
        let deadline = Instant::now() + Duration::from_millis(request.timeout_ms);
        let mut timed_out = false;
        loop {
            if let Some(_status) = child
                .try_wait()
                .map_err(|error| RunBodyError::Ensemble(error.to_string()))?
            {
                break;
            }
            if Instant::now() >= deadline {
                kill_child_group(&mut child)?;
                timed_out = true;
                break;
            }
            thread::sleep(Duration::from_millis(10));
        }
        let status = child
            .wait()
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        let stdout = join_stream_thread(stdout_thread, "read launcher stdout")?;
        let stderr = join_stream_thread(stderr_thread, "read launcher stderr")?;
        if timed_out {
            return Err(RunBodyError::Ensemble(format!(
                "launcher exceeded hard timeout of {} ms and was killed",
                request.timeout_ms
            )));
        }
        if !status.success() {
            return Err(RunBodyError::Ensemble(format!(
                "launcher exited with {:?}: {stderr}",
                status.code()
            )));
        }
        let value = serde_json::from_str(&stdout).map_err(RunBodyError::EnsembleJson)?;
        Ok(EnsembleWorkflowOutput {
            value,
            archive_dir: request.archive_dir,
        })
    }
}

fn kill_child_group(child: &mut std::process::Child) -> Result<(), RunBodyError> {
    #[cfg(unix)]
    {
        let status = Command::new("kill")
            .arg("-TERM")
            .arg(format!("-{}", child.id()))
            .status()
            .map_err(|error| RunBodyError::Ensemble(error.to_string()))?;
        if status.success() {
            return Ok(());
        }
    }
    child
        .kill()
        .map_err(|error| RunBodyError::Ensemble(error.to_string()))
}

fn read_stream_to_string(mut stream: impl std::io::Read) -> Result<String, std::io::Error> {
    let mut content = String::new();
    stream.read_to_string(&mut content)?;
    Ok(content)
}

fn join_stream_thread(
    thread: thread::JoinHandle<Result<String, std::io::Error>>,
    action: &str,
) -> Result<String, RunBodyError> {
    thread
        .join()
        .map_err(|_panic| RunBodyError::Ensemble(format!("{action}: I/O thread panicked")))?
        .map_err(|error| RunBodyError::Ensemble(format!("{action}: {error}")))
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

#[derive(Clone, Debug, Eq, PartialEq)]
struct InlineReviewDiff<'a> {
    excerpt: &'a str,
    omitted_bytes: usize,
    omitted_hunks: usize,
}

impl InlineReviewDiff<'_> {
    const fn is_truncated(&self) -> bool {
        self.omitted_bytes > 0
    }
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
    let inline_diff = inline_review_diff(&review_evidence.diff, INLINE_REVIEW_DIFF_BYTE_LIMIT);
    let mut text = format!(
        "Prepared workspace tree: {}\nPR diff path: {}\nFull PR diff evidence lives on disk at the path above. Read that file from the prepared workspace when the excerpt below is truncated, and treat the file as authoritative.\n\n",
        review_evidence.workspace_root.display(),
        review_evidence.diff_path.display(),
    );
    if inline_diff.is_truncated() {
        let _ = writeln!(
            text,
            "Bounded PR diff excerpt: showing {} bytes; omitted {} bytes across {} diff hunk(s).",
            inline_diff.excerpt.len(),
            inline_diff.omitted_bytes,
            inline_diff.omitted_hunks
        );
    } else {
        let _ = writeln!(
            text,
            "Complete PR diff excerpt: showing {} bytes; no diff bytes omitted.",
            inline_diff.excerpt.len()
        );
    }
    text.push_str("\n<pr_diff_excerpt>\n");
    text.push_str(inline_diff.excerpt);
    if !inline_diff.excerpt.ends_with('\n') {
        text.push('\n');
    }
    text.push_str("</pr_diff_excerpt>");
    if !brief.evidence_paths.is_empty() {
        text.push_str("\n\nAdditional brief evidence:\n");
        text.push_str(&evidence_text(&review_evidence.workspace_root, brief)?);
    }
    Ok(text)
}

fn inline_review_diff(diff: &str, byte_limit: usize) -> InlineReviewDiff<'_> {
    if diff.len() <= byte_limit {
        return InlineReviewDiff {
            excerpt: diff,
            omitted_bytes: 0,
            omitted_hunks: 0,
        };
    }

    let mut excerpt_end = 0;
    for line in diff.split_inclusive('\n') {
        let next_end = excerpt_end + line.len();
        if next_end > byte_limit {
            break;
        }
        excerpt_end = next_end;
    }

    // Keep the excerpt on a valid, reviewable boundary. An empty excerpt can only
    // happen when the first diff line exceeds the limit, which is still safer
    // than copying a partial line into an argv-bound prompt.
    let excerpt = &diff[..excerpt_end];
    let omitted = &diff[excerpt_end..];
    InlineReviewDiff {
        excerpt,
        omitted_bytes: omitted.len(),
        omitted_hunks: omitted
            .lines()
            .filter(|line| line.starts_with("@@ "))
            .count(),
    }
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
        process::Command,
        time::Duration,
    };

    use pump19_contract::{
        DecisionSubject, LoopPassRecord, ModelFamily, ModelLineage, ProvenanceVerification,
        PullRequestRef, RunStatus, SessionFreshness, SessionId,
    };
    use pump19_core::{
        LaunchProof, WorkspaceExecOutput, WorkspaceExecRequest, WorkspaceIsolation, WorkspaceLease,
    };
    use pump19_engine::{ModelSource, RepairPolicy, TokenUsage};
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
    struct ScriptedLeadEngine {
        output: Value,
        classification: ExitClassification,
        run_workflow_shim: bool,
        seen_specs: Vec<LaunchSpec>,
    }

    impl ScriptedLeadEngine {
        fn successful(output: Value) -> Self {
            Self {
                output,
                classification: ExitClassification::Success,
                run_workflow_shim: false,
                seen_specs: Vec::new(),
            }
        }
    }

    impl LeadEngineSession for ScriptedLeadEngine {
        fn launch_lead(
            &mut self,
            spec: &LaunchSpec,
            _repair: &mut dyn RepairStrategy,
        ) -> Result<EngineRun, EngineError> {
            self.seen_specs.push(spec.clone());
            if self.run_workflow_shim {
                let input = spec.working_dir.join("out/repair-input.json");
                fs::write(
                    &input,
                    r#"{"repairer":{"engine":"codex","agent_id":"repairer"},"schema":{},"invalid_output":"{}","errors":[],"agent_timeout_ms":1000}"#,
                )
                .expect("write repair input");
                let output = Command::new(spec.working_dir.join("bin/pump19-workflow"))
                    .current_dir(&spec.working_dir)
                    .arg("repair-output")
                    .arg(&input)
                    .output()
                    .expect("run workflow shim");
                assert!(
                    output.status.success(),
                    "workflow shim failed: {}",
                    String::from_utf8_lossy(&output.stderr)
                );
            }
            let stdout = spec.archive_dir.join("stdout.log");
            let stderr = spec.archive_dir.join("stderr.log");
            fs::create_dir_all(&spec.archive_dir).expect("create fake lead archive");
            fs::write(&stdout, self.output.to_string()).expect("write fake stdout");
            fs::write(&stderr, "").expect("write fake stderr");
            Ok(EngineRun {
                classification: self.classification.clone(),
                output: (self.classification == ExitClassification::Success)
                    .then_some(self.output.clone()),
                final_text: Some(self.output.to_string()),
                provenance: EngineProvenance {
                    engine: spec.engine,
                    session_id: Some("lead-session".to_owned()),
                    requested_model: spec.requested_model.clone(),
                    resolved_model: spec.requested_model.clone(),
                    model_source: ModelSource::RequestedAsOperatorAssertion,
                    usage: TokenUsage::default(),
                    cost_usd: None,
                },
                transcripts: vec![
                    pump19_engine::TranscriptPath {
                        kind: TranscriptKind::Stdout,
                        path: stdout,
                    },
                    pump19_engine::TranscriptPath {
                        kind: TranscriptKind::Stderr,
                        path: stderr,
                    },
                ],
                repair_attempts: 0,
            })
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

    fn lead_frame_config(root: &Path) -> LeadSessionFrameConfig {
        let workflow = root.join("repair-output.js");
        fs::write(
            &workflow,
            "export const meta = {\n  name: \"repair\"\n};\nreturn {};\n",
        )
        .expect("write workflow");
        let ensemble = root.join("ensemble.sh");
        fs::write(
            &ensemble,
            "#!/bin/sh\nset -eu\nmkdir -p \"$ENSEMBLE_RUN_RECORD_DIR\"\nprintf '{\"repaired_output\":\"{}\"}\\n'\n",
        )
        .expect("write fake ensemble");
        make_executable(&ensemble).expect("chmod fake ensemble");
        LeadSessionFrameConfig {
            run_root: root.join("frame-runs"),
            node_program: PathBuf::from("sh"),
            ensemble_launcher: ensemble,
            workflows: vec![LeadWorkflowScript {
                slot: ReviewWorkflowSlot::RepairOutput,
                path: workflow,
            }],
            mission_template:
                "Use {{manifest_path}} for run {{run_id}}.\n<manifest>\n{{manifest}}\n</manifest>"
                    .to_owned(),
            selected_briefs: vec![ManifestBrief {
                id: "repo:security".to_owned(),
                title: "Security".to_owned(),
                occasion: "every-pr".to_owned(),
            }],
            brief_warnings: vec!["repo:broken frontmatter loaded body-only".to_owned()],
            occasion: "every-pr".to_owned(),
            materiality_threshold: PriorityClass::P1,
            lead_engine: LeadEngineConfig {
                engine: EngineKind::Codex,
                executable: PathBuf::from("codex"),
                requested_model: Some("gpt-5.5".to_owned()),
                bounds: LaunchBounds::new(Duration::from_secs(30)),
                write_access: WriteAccess::ReadOnly,
                env: BTreeMap::new(),
            },
        }
    }

    fn frame_request(root: &Path) -> RunLaunchRequest {
        let mut req = request(
            RunKind::Review,
            vec![
                provenance("lead-claude", AgentRole::Lead, "claude"),
                provenance("reviewer-codex", AgentRole::Reviewer, "codex"),
                provenance("verifier-claude", AgentRole::Verifier, "claude"),
                provenance("bar-codex", AgentRole::BarCheck, "codex"),
            ],
        );
        req.run_id = RunId("lead-frame-review".to_owned());
        req.workspace.root = root.to_path_buf();
        req.state.extensions.insert(
            EXT_FORGE_FACTS.to_owned(),
            json!({
                "base": {"sha": "base-sha"}
            }),
        );
        write_review_diff(
            root,
            "diff --git a/src/lib.rs b/src/lib.rs\n--- a/src/lib.rs\n+++ b/src/lib.rs\n@@ -1,1 +1,2 @@\n old\n+new unsafe line\n",
        );
        req
    }

    fn bar_check() -> BarCheckRecord {
        BarCheckRecord {
            passed: true,
            provenance: provenance("bar-codex", AgentRole::BarCheck, "codex"),
            rationale: "review clears the bar".to_owned(),
            extensions: BTreeMap::new(),
        }
    }

    fn lead_payload(findings: &Value, complete: bool, verdict: &Value) -> Value {
        json!({
            "findings": findings,
            "coverage": {
                "complete": complete,
                "visited": ["src/lib.rs"],
                "unvisited": if complete { Vec::<String>::new() } else { vec!["src/other.rs".to_owned()] },
                "account": "read the changed file and relevant context",
                "extensions": {}
            },
            "verdict_proposal": verdict,
            "session_notes": "fixture"
        })
    }

    fn verified_finding(verifier: &str, line: u32) -> Value {
        json!({
            "dedup_hint": "src-lib-unsound",
            "source_brief": "repo:security",
            "title": "Unsafe state is accepted",
            "explanation": "The new branch accepts unsafe state.",
            "suggestion": "Reject unsafe state before storing it.",
            "priority": "p1",
            "certainty": "advisory",
            "producer_agent_id": "reviewer-codex",
            "locations": [{"path": "src/lib.rs", "line": line}],
            "verification": {
                "status": "verified",
                "verifier_agent_id": verifier,
                "evidence": [{
                    "path": "src/lib.rs",
                    "line": line,
                    "quote": "new unsafe line",
                    "note": "the changed branch accepts the bad state"
                }],
                "cross_family": "cross_family"
            }
        })
    }

    fn verified_finding_without_verifier(line: u32) -> Value {
        let mut finding = verified_finding("verifier-claude", line);
        finding["verification"]
            .as_object_mut()
            .expect("verification object")
            .remove("verifier_agent_id");
        finding
    }

    #[test]
    fn lead_frame_walking_skeleton_drives_workflow_shim_and_records_manifest() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([verified_finding("verifier-claude", 2)]);
        let verdict_payload = json!({"verdict": "findings_posted"});
        let output = lead_payload(&findings_payload, true, &verdict_payload);
        let mut engine = ScriptedLeadEngine::successful(output);
        engine.run_workflow_shim = true;
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("lead frame review succeeds");
        let result = body.last_result().expect("frame result recorded");

        assert_eq!(findings.len(), 1);
        assert_eq!(findings[0].summary, "Unsafe state is accepted");
        assert_eq!(result.manifest.base_sha, "base-sha");
        assert_eq!(
            result.manifest.extensions[EXT_BRIEF_WARNINGS][0].as_str(),
            Some("repo:broken frontmatter loaded body-only")
        );
        assert_eq!(
            result.manifest.changed_lines.get("src/lib.rs"),
            Some(&vec![2])
        );
        assert_eq!(result.manifest.selected_briefs[0].id, "repo:security");
        assert!(result.run_dir.join("bin/pump19-workflow").exists());
        assert!(result.run_dir.join("bin/pump19-exec").exists());
        assert!(
            fs::read_to_string(result.run_dir.join("out/workflow-boundary.jsonl"))
                .expect("read boundary log")
                .contains("repair-output")
        );
        assert_eq!(
            result.verdict,
            ReviewVerdict::FindingsPosted {
                material: 1,
                suppressed: 0
            }
        );
    }

    #[test]
    fn lead_frame_suppresses_self_verified_findings() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([verified_finding("reviewer-codex", 2)]);
        let verdict_payload = json!({"verdict": "findings_posted"});
        let output = lead_payload(&findings_payload, true, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("self-verified finding is suppressed, not fatal");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(result.suppressed_findings.len(), 1);
        assert!(
            result.suppressed_findings[0]
                .reason
                .contains("producing agent")
        );
    }

    #[test]
    fn lead_frame_suppresses_verified_finding_without_verifier_identity() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([verified_finding_without_verifier(2)]);
        let verdict_payload = json!({"verdict": "findings_posted"});
        let output = lead_payload(&findings_payload, true, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("missing verifier identity suppresses finding");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(result.suppressed_findings.len(), 1);
        assert!(
            result.suppressed_findings[0]
                .reason
                .contains("missing verifier identity")
        );
    }

    #[test]
    fn lead_frame_suppresses_finding_verified_by_producer_session() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = frame_request(root.path());
        let producer_session = req
            .provenance
            .iter()
            .find(|provenance| provenance.agent_id == AgentId("reviewer-codex".to_owned()))
            .expect("producer provenance")
            .session_id
            .clone();
        req.provenance
            .iter_mut()
            .find(|provenance| provenance.agent_id == AgentId("verifier-claude".to_owned()))
            .expect("verifier provenance")
            .session_id = producer_session;
        let findings_payload = json!([verified_finding("verifier-claude", 2)]);
        let verdict_payload = json!({"verdict": "findings_posted"});
        let output = lead_payload(&findings_payload, true, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("producer-session verification suppresses finding");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(result.suppressed_findings.len(), 1);
        assert!(
            result.suppressed_findings[0]
                .reason
                .contains("producing session")
        );
    }

    #[test]
    fn lead_frame_suppresses_findings_outside_changed_lines() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([verified_finding("verifier-claude", 1)]);
        let verdict_payload = json!({"verdict": "findings_posted"});
        let output = lead_payload(&findings_payload, true, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("out-of-range finding is suppressed, not fatal");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(result.suppressed_findings.len(), 1);
        assert!(
            result.suppressed_findings[0]
                .reason
                .contains("changed lines")
        );
    }

    #[test]
    fn lead_frame_records_partial_coverage_instead_of_convergence() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([]);
        let verdict_payload = json!({"verdict": "converged", "bar_check": bar_check()});
        let output = lead_payload(&findings_payload, false, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("partial coverage records a non-convergent verdict");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(result.verdict, ReviewVerdict::PartialCoverage);
        assert!(!result.coverage.complete);
    }

    #[test]
    fn lead_frame_preserves_degraded_bar_check_under_incomplete_coverage() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([]);
        let verdict_payload = json!({
            "verdict": "bar_check_degraded",
            "attempts": 2,
            "last_error": "checker unavailable"
        });
        let output = lead_payload(&findings_payload, false, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("bar-check degradation survives incomplete coverage");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(
            result.verdict,
            ReviewVerdict::BarCheckDegraded {
                attempts: 2,
                last_error: "checker unavailable".to_owned()
            }
        );
        assert!(!result.coverage.complete);
    }

    #[test]
    fn lead_frame_preserves_standing_findings_under_incomplete_coverage() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let findings_payload = json!([]);
        let verdict_payload = json!({
            "verdict": "standing_findings",
            "finding_dedup_keys": ["src-lib-unsound"],
            "rationale": "the same finding survived a fix"
        });
        let output = lead_payload(&findings_payload, false, &verdict_payload);
        let engine = ScriptedLeadEngine::successful(output);
        let runner = FakeEnsembleRunner::new(json!({}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("standing-finding rationale survives incomplete coverage");
        let result = body.last_result().expect("frame result recorded");

        assert!(findings.is_empty());
        assert_eq!(
            result.verdict,
            ReviewVerdict::StandingFindings {
                finding_dedup_keys: vec!["src-lib-unsound".to_owned()],
                rationale: "the same finding survived a fix".to_owned()
            }
        );
        assert!(!result.coverage.complete);
    }

    #[test]
    fn lead_frame_repairs_invalid_lead_json_through_repair_workflow_slot() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let codex = root.path().join("fake-codex.sh");
        fs::write(
            &codex,
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"fake-thread"}'
printf '%s\n' '{"type":"item.completed","item":{"text":"not json"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}'
"#,
        )
        .expect("write fake codex");
        make_executable(&codex).expect("chmod fake codex");
        let repaired_findings = json!([verified_finding("verifier-claude", 2)]);
        let repaired_verdict = json!({"verdict": "findings_posted"});
        let repaired = lead_payload(&repaired_findings, true, &repaired_verdict);
        let runner =
            FakeEnsembleRunner::new(json!({"repaired_output": repaired.to_string()}), Vec::new());
        let mut config = lead_frame_config(root.path());
        config.lead_engine.executable = codex;
        let engine = EngineSessionLauncher::new(RepairPolicy { attempts: 1 });
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("invalid lead JSON is repaired by workflow slot");
        let result = body.last_result().expect("frame result recorded");

        assert_eq!(findings.len(), 1);
        assert_eq!(result.repair_attempts, 1);
        assert_eq!(body.workflow_runner.requests.len(), 1);
        assert!(
            body.workflow_runner.requests[0]
                .archive_dir
                .to_string_lossy()
                .contains("repair-output")
        );
    }

    #[test]
    fn lead_frame_repairs_valid_json_with_wrong_review_shape() {
        let root = tempfile::tempdir().expect("workspace root");
        let req = frame_request(root.path());
        let repaired_findings = json!([verified_finding("verifier-claude", 2)]);
        let repaired_verdict = json!({"verdict": "findings_posted"});
        let repaired = lead_payload(&repaired_findings, true, &repaired_verdict);
        let engine = ScriptedLeadEngine::successful(json!({
            "findings": [],
            "verdict_proposal": {"verdict": "findings_posted"}
        }));
        let runner =
            FakeEnsembleRunner::new(json!({"repaired_output": repaired.to_string()}), Vec::new());
        let config = lead_frame_config(root.path());
        let mut body = LeadSessionReviewBody::new(engine, runner, config);
        let mut workspace = FakeWorkspace::default();

        let findings = body
            .run_review(&req, &mut workspace)
            .expect("shape-valid JSON is repaired by workflow slot");
        let result = body.last_result().expect("frame result recorded");

        assert_eq!(findings.len(), 1);
        assert_eq!(result.repair_attempts, 1);
        assert_eq!(body.workflow_runner.requests.len(), 1);
        assert!(
            body.workflow_runner.requests[0].args["errors"][0]
                .as_str()
                .expect("serde error")
                .contains("missing field")
        );
        assert_eq!(
            body.workflow_runner.requests[0].args["schema"]["type"],
            Value::String("object".to_owned())
        );
    }

    #[test]
    fn workflow_shim_passes_large_payload_by_file_not_launcher_argv() {
        let root = tempfile::tempdir().expect("shim root");
        let fixture = WorkflowShimFixture::new(root.path());
        write_workflow_shim(&fixture.config, &fixture.run_dir).expect("write shim");
        let marker = "large-payload-marker-".repeat(16_000);
        assert!(marker.len() > 256 * 1024);
        let input_path = fixture.run_dir.join("out/input.json");
        fs::write(
            &input_path,
            serde_json::to_vec(&json!({"marker": marker})).expect("serialise input"),
        )
        .expect("write large payload");

        let output = Command::new(fixture.run_dir.join("bin/pump19-workflow"))
            .current_dir(&fixture.run_dir)
            .arg("repair-output")
            .arg(&input_path)
            .output()
            .expect("run shim");

        assert!(
            output.status.success(),
            "shim failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        let argv = fs::read_to_string(&fixture.captured_argv).expect("read launcher argv");
        assert!(argv.contains("--timeout\n180000\n"));
        assert!(!argv.contains("--json-args"));
        assert!(!argv.contains("large-payload-marker-"));
        assert!(
            argv.len() < 4096,
            "launcher argv should stay bounded; got {} bytes",
            argv.len()
        );
        assert_eq!(
            fs::read_to_string(&fixture.captured_input_env).expect("read input env"),
            input_path.display().to_string()
        );
        let wrapper = fs::read_to_string(&fixture.captured_wrapper).expect("read wrapper");
        assert!(wrapper.contains("PUMP19_WORKFLOW_INPUT_PATH"));
        assert!(!wrapper.contains("large-payload-marker-"));
    }

    struct WorkflowShimFixture {
        run_dir: PathBuf,
        config: LeadSessionFrameConfig,
        captured_argv: PathBuf,
        captured_input_env: PathBuf,
        captured_wrapper: PathBuf,
    }

    impl WorkflowShimFixture {
        fn new(root: &Path) -> Self {
            let run_dir = root.join("run");
            fs::create_dir_all(run_dir.join("bin")).expect("create bin");
            fs::create_dir_all(run_dir.join("out")).expect("create out");
            fs::create_dir_all(run_dir.join("archive/workflows")).expect("create archive");
            fs::create_dir_all(run_dir.join("workflows")).expect("create workflow dir");
            let workflow = root.join("repair-output.js");
            fs::write(
                &workflow,
                "export const meta = {\n  name: \"payload-boundary\"\n};\nreturn { marker: args.marker };\n",
            )
            .expect("write workflow");
            fs::copy(&workflow, run_dir.join("workflows/repair-output.js"))
                .expect("copy workflow into run dir");
            let captured_argv = root.join("launcher-argv.txt");
            let captured_input_env = root.join("launcher-input-env.txt");
            let captured_wrapper = root.join("launcher-wrapper.js");
            let ensemble = root.join("fake-ensemble.sh");
            write_fake_shim_ensemble(
                &ensemble,
                &captured_argv,
                &captured_input_env,
                &captured_wrapper,
            );
            Self {
                run_dir,
                config: shim_test_config(root, &ensemble, &workflow),
                captured_argv,
                captured_input_env,
                captured_wrapper,
            }
        }
    }

    fn shim_test_config(root: &Path, ensemble: &Path, workflow: &Path) -> LeadSessionFrameConfig {
        LeadSessionFrameConfig {
            run_root: root.join("unused"),
            node_program: PathBuf::from("sh"),
            ensemble_launcher: ensemble.to_path_buf(),
            workflows: vec![LeadWorkflowScript {
                slot: ReviewWorkflowSlot::RepairOutput,
                path: workflow.to_path_buf(),
            }],
            mission_template: String::new(),
            selected_briefs: Vec::new(),
            brief_warnings: Vec::new(),
            occasion: "every-pr".to_owned(),
            materiality_threshold: PriorityClass::P1,
            lead_engine: LeadEngineConfig {
                engine: EngineKind::Codex,
                executable: PathBuf::from("codex"),
                requested_model: None,
                bounds: LaunchBounds::new(Duration::from_secs(30)),
                write_access: WriteAccess::ReadOnly,
                env: BTreeMap::new(),
            },
        }
    }

    fn write_fake_shim_ensemble(
        path: &Path,
        captured_argv: &Path,
        captured_input_env: &Path,
        captured_wrapper: &Path,
    ) {
        fs::write(
            path,
            format!(
                r#"#!/bin/sh
printf '%s\n' "$@" > "{captured_argv}"
printf '%s' "$PUMP19_WORKFLOW_INPUT_PATH" > "{captured_input_env}"
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
cp "$script" "{captured_wrapper}"
printf '{{"ok":true}}\n'
"#,
                captured_argv = captured_argv.display(),
                captured_input_env = captured_input_env.display(),
                captured_wrapper = captured_wrapper.display(),
            ),
        )
        .expect("write fake ensemble");
        make_executable(path).expect("chmod fake ensemble");
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
    fn review_body_bounds_inline_diff_evidence_but_preserves_full_workflow_evidence() {
        let root = tempfile::tempdir().expect("workspace root");
        let mut req = request(
            RunKind::Review,
            vec![provenance("reviewer-codex", AgentRole::Reviewer, "codex")],
        );
        req.run_id = RunId("review-run".to_owned());
        req.workspace.root = root.path().to_path_buf();
        let mut diff = String::from("diff --git a/src/lib.rs b/src/lib.rs\n");
        for index in 0..80_000 {
            write!(
                diff,
                "@@ -{index},1 +{index},1 @@\n-old_{index}\n+new_{index}\n"
            )
            .expect("write generated diff hunk");
        }
        diff.push_str("+final sentinel only present in the full diff\n");
        write_review_diff(root.path(), &diff);
        let full_diff_len = diff.len();
        assert!(full_diff_len > 2 * 1024 * 1024);
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
        config.prompt_template = "{{evidence}}".to_owned();
        let mut body = EnsembleReviewBody::new(runner, config);
        let mut workspace = FakeWorkspace::default();

        body.run_review(&req, &mut workspace)
            .expect("large diff review launches with bounded prompt evidence");

        let request = &body.runner.requests[0];
        let prompt = request.args["briefs"][0]["prompt"]
            .as_str()
            .expect("workflow brief prompt");
        assert!(
            prompt.len() < INLINE_REVIEW_DIFF_BYTE_LIMIT + 2 * 1024,
            "rendered prompt should keep generous argv headroom; got {} bytes",
            prompt.len()
        );
        assert!(prompt.contains("Bounded PR diff excerpt"));
        assert!(prompt.contains("Full PR diff evidence lives on disk"));
        assert!(prompt.contains("omitted "));
        assert!(prompt.contains("diff hunk(s)"));
        assert!(prompt.contains(root.path().to_string_lossy().as_ref()));
        assert!(prompt.contains(".pump19/review/diff.patch"));
        assert!(!prompt.contains("final sentinel only present in the full diff"));
        assert_eq!(
            request.args["evidence"]["workspace_root"].as_str(),
            Some(root.path().to_string_lossy().as_ref())
        );
        assert_eq!(
            request.args["evidence"]["diff_path"].as_str(),
            Some(
                root.path()
                    .join(REVIEW_EVIDENCE_DIR)
                    .join(REVIEW_DIFF_FILE)
                    .to_string_lossy()
                    .as_ref()
            )
        );
        let workflow_diff = request.args["evidence"]["diff"]
            .as_str()
            .expect("workflow diff evidence");
        assert_eq!(workflow_diff.len(), full_diff_len);
        assert!(workflow_diff.contains("final sentinel only present in the full diff"));
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
    fn host_runner_drains_large_stdout_while_launcher_runs() {
        let root = tempfile::tempdir().expect("host runner root");
        let launcher = root.path().join("large-output-launcher.sh");
        fs::write(
            &launcher,
            r#"#!/bin/sh
head -c 200000 /dev/zero | tr '\0' ' '
printf '{"ok":true}\n'
"#,
        )
        .expect("write launcher");
        let workflow = root.path().join("workflow.js");
        fs::write(
            &workflow,
            "export const meta = { name: \"large-output\" };\nreturn null;\n",
        )
        .expect("write workflow");
        let mut runner = HostEnsembleWorkflowRunner::new("sh", &launcher);
        let request = EnsembleWorkflowRequest {
            script: workflow,
            args: json!({}),
            archive_dir: root.path().join("archive"),
            timeout_ms: 2_000,
        };

        let output = runner.run_workflow(request).expect("workflow launches");

        assert_eq!(output.value, json!({"ok": true}));
    }

    #[test]
    fn host_runner_passes_large_payload_through_temporary_workflow_file() {
        let root = tempfile::tempdir().expect("host runner root");
        let launcher = root.path().join("fake-ensemble.sh");
        let captured_argv = root.path().join("argv.txt");
        let captured_env = root.path().join("env.json");
        let captured_script_path = root.path().join("script-path.txt");
        let captured_wrapper = root.path().join("wrapper.js");
        fs::write(
            &launcher,
            format!(
                r#"#!/bin/sh
printf '%s\n' "$@" > "{captured_argv}"
printf '{{"archive_dir":"%s","forge_token":"%s","path":"%s"}}' "$ENSEMBLE_RUN_RECORD_DIR" "$FORGEJO_TOKEN" "$PATH" > "{captured_env}"
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
                captured_env = captured_env.display(),
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
        assert!(argv.contains("--timeout\n5000\n"));
        assert!(!argv.contains("--json-args"));
        assert!(!argv.contains("payload-marker-"));
        let env: Value = serde_json::from_str(&fs::read_to_string(captured_env).expect("read env"))
            .expect("captured env json");
        assert_eq!(
            env["archive_dir"],
            root.path().join("archive").display().to_string()
        );
        assert_eq!(env["forge_token"], "");
        assert_ne!(env["path"], "");
        let wrapper = fs::read_to_string(captured_wrapper).expect("read wrapper");
        assert!(wrapper.contains("const args = JSON.parse("));
        assert!(wrapper.contains("export const meta = { name: \"payload\" };"));
        assert!(wrapper.contains("return { marker: args.marker };"));
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
