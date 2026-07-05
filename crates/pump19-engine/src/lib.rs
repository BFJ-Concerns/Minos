#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "engine adapter tests use small fake CLIs and direct fixture assertions"
    )
)]

use std::{
    collections::BTreeMap,
    fs,
    io::Write as _,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use thiserror::Error;

const STDOUT_TRANSCRIPT: &str = "stdout.log";
const STDERR_TRANSCRIPT: &str = "stderr.log";
const FINAL_OUTPUT: &str = "final-output.json";

/// The agent CLI family launched by the core-owned engine adapter.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EngineKind {
    Claude,
    Codex,
    Opencode,
}

/// Whether the launched session should be able to mutate the tree it sees.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WriteAccess {
    ReadOnly,
    Writable,
}

/// Native or frame-owned bound support for one launch.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LaunchBounds {
    pub wall_clock: Duration,
    pub max_budget_usd: Option<String>,
    pub max_total_tokens: Option<u64>,
}

impl LaunchBounds {
    #[must_use]
    pub const fn new(wall_clock: Duration) -> Self {
        Self {
            wall_clock,
            max_budget_usd: None,
            max_total_tokens: None,
        }
    }
}

/// A fully resolved request to launch one headless engine session.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LaunchSpec {
    pub engine: EngineKind,
    pub executable: PathBuf,
    pub working_dir: PathBuf,
    pub prompt_path: PathBuf,
    pub schema_path: Option<PathBuf>,
    pub archive_dir: PathBuf,
    pub requested_model: Option<String>,
    pub write_access: WriteAccess,
    pub bounds: LaunchBounds,
    pub env: BTreeMap<String, String>,
}

/// The process invocation derived from a [`LaunchSpec`].
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CommandInvocation {
    pub program: PathBuf,
    pub args: Vec<String>,
    pub cwd: PathBuf,
    pub stdin: String,
    pub env: BTreeMap<String, String>,
}

/// The final outcome class after process execution, parsing, and frame-owned bounds.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum ExitClassification {
    Success,
    ProcessFailure { code: Option<i32> },
    Timeout,
    BudgetExceeded { limit: String },
    TokenLimitExceeded { limit: u64, observed: u64 },
    InvalidOutput { reason: String },
}

/// Engine launch provenance captured from the CLI surface.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct EngineProvenance {
    pub engine: EngineKind,
    pub session_id: Option<String>,
    pub requested_model: Option<String>,
    pub resolved_model: Option<String>,
    pub model_source: ModelSource,
    pub usage: TokenUsage,
    pub cost_usd: Option<String>,
}

/// How honestly the adapter can describe the model in the launch record.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelSource {
    ResolvedByCli,
    RequestedAsOperatorAssertion,
    NotReported,
}

/// Token and cost fields normalised from whichever stream the CLI exposes.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct TokenUsage {
    pub input_tokens: u64,
    pub cached_input_tokens: u64,
    pub output_tokens: u64,
    pub reasoning_output_tokens: u64,
    pub total_tokens: u64,
}

/// A captured transcript file written under the launch archive directory.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct TranscriptPath {
    pub kind: TranscriptKind,
    pub path: PathBuf,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TranscriptKind {
    Stdout,
    Stderr,
    FinalOutput,
}

/// Result returned by the engine adapter for one process launch.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct EngineRun {
    pub classification: ExitClassification,
    pub output: Option<Value>,
    pub final_text: Option<String>,
    pub provenance: EngineProvenance,
    pub transcripts: Vec<TranscriptPath>,
    pub repair_attempts: u32,
}

/// One bounded schema-repair attempt delegated to the caller's strategy.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RepairAttempt {
    pub attempt: u32,
    pub engine: EngineKind,
    pub schema_path: Option<PathBuf>,
    pub invalid_output: String,
    pub error: String,
}

/// Strategy hook for bounded frame-owned output repair.
pub trait RepairStrategy {
    /// Returns repaired final output text, or `None` when no repair is available.
    ///
    /// # Errors
    ///
    /// Returns an error when the repair mechanism itself fails.
    fn repair(&mut self, attempt: RepairAttempt) -> Result<Option<String>, EngineError>;
}

/// Repair strategy that never repairs; useful when native schema enforcement is
/// expected or the caller wants invalid output to fail immediately.
#[derive(Debug, Default)]
pub struct NoRepair;

impl RepairStrategy for NoRepair {
    fn repair(&mut self, _attempt: RepairAttempt) -> Result<Option<String>, EngineError> {
        Ok(None)
    }
}

/// Configuration for frame-owned repair retries.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct RepairPolicy {
    pub attempts: u32,
}

impl Default for RepairPolicy {
    fn default() -> Self {
        Self { attempts: 2 }
    }
}

/// Launches engine CLI sessions from structured specs.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct EngineSessionLauncher {
    repair_policy: RepairPolicy,
}

impl Default for EngineSessionLauncher {
    fn default() -> Self {
        Self::new(RepairPolicy::default())
    }
}

impl EngineSessionLauncher {
    #[must_use]
    pub const fn new(repair_policy: RepairPolicy) -> Self {
        Self { repair_policy }
    }

    /// Builds the process invocation for the requested engine CLI.
    ///
    /// # Errors
    ///
    /// Returns an error when the prompt cannot be read or the launch spec asks for
    /// a native option the chosen engine cannot support.
    pub fn build_invocation(spec: &LaunchSpec) -> Result<CommandInvocation, EngineError> {
        let stdin = fs::read_to_string(&spec.prompt_path).map_err(|source| EngineError::Io {
            action: format!("read prompt {}", spec.prompt_path.display()),
            source,
        })?;
        let mut args = match spec.engine {
            EngineKind::Claude => claude_args(spec)?,
            EngineKind::Codex => codex_args(spec),
            EngineKind::Opencode => opencode_args(spec)?,
        };
        if spec.engine == EngineKind::Codex {
            args.push("-".to_owned());
        }
        Ok(CommandInvocation {
            program: spec.executable.clone(),
            args,
            cwd: spec.working_dir.clone(),
            stdin,
            env: spec.env.clone(),
        })
    }

    /// Launches the configured CLI, captures transcripts, and classifies its result.
    ///
    /// # Errors
    ///
    /// Returns an error when the process cannot be spawned, transcript files cannot
    /// be written, or the repair strategy fails.
    pub fn launch(
        &self,
        spec: &LaunchSpec,
        repair: &mut dyn RepairStrategy,
    ) -> Result<EngineRun, EngineError> {
        let invocation = Self::build_invocation(spec)?;
        let process = run_process(&invocation, spec.bounds.wall_clock)?;
        fs::create_dir_all(&spec.archive_dir).map_err(|source| EngineError::Io {
            action: format!("create archive dir {}", spec.archive_dir.display()),
            source,
        })?;
        let mut transcripts = write_transcripts(&spec.archive_dir, &process)?;
        let mut parsed = parse_engine_output(spec, &process);
        let mut repair_attempts = 0;

        if matches!(
            parsed.classification,
            ExitClassification::InvalidOutput { .. }
        ) {
            while repair_attempts < self.repair_policy.attempts {
                repair_attempts += 1;
                let error = invalid_output_reason(&parsed.classification);
                let attempt = RepairAttempt {
                    attempt: repair_attempts,
                    engine: spec.engine,
                    schema_path: spec.schema_path.clone(),
                    invalid_output: parsed
                        .final_text
                        .clone()
                        .unwrap_or_else(|| process.stdout.clone()),
                    error,
                };
                let Some(repaired) = repair.repair(attempt)? else {
                    break;
                };
                parsed = parse_final_text(spec, &repaired, parsed.provenance.clone());
                if parsed.classification == ExitClassification::Success {
                    let output_path = spec.archive_dir.join(FINAL_OUTPUT);
                    fs::write(&output_path, repaired.as_bytes()).map_err(|source| {
                        EngineError::Io {
                            action: format!("write final output {}", output_path.display()),
                            source,
                        }
                    })?;
                    transcripts.push(TranscriptPath {
                        kind: TranscriptKind::FinalOutput,
                        path: output_path,
                    });
                    break;
                }
            }
        }

        let mut classification = if process.timed_out {
            ExitClassification::Timeout
        } else {
            parsed.classification
        };
        if classification == ExitClassification::Success
            && let Some(limit) = spec.bounds.max_total_tokens
            && parsed.provenance.usage.total_tokens > limit
        {
            classification = ExitClassification::TokenLimitExceeded {
                limit,
                observed: parsed.provenance.usage.total_tokens,
            };
        }
        Ok(EngineRun {
            classification,
            output: parsed.output,
            final_text: parsed.final_text,
            provenance: parsed.provenance,
            transcripts,
            repair_attempts,
        })
    }
}

/// Errors raised while launching or interpreting an engine CLI.
#[derive(Debug, Error)]
pub enum EngineError {
    #[error("{action}: {source}")]
    Io {
        action: String,
        #[source]
        source: std::io::Error,
    },
    #[error("claude only accepts inline JSON schema text; read {path}: {source}")]
    ClaudeSchema {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("opencode has no native schema-output flag on the pump.example target version")]
    OpencodeNativeSchemaUnavailable,
    #[error("repair failed: {0}")]
    Repair(String),
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct ProcessOutput {
    stdout: String,
    stderr: String,
    code: Option<i32>,
    timed_out: bool,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct ParsedRun {
    classification: ExitClassification,
    output: Option<Value>,
    final_text: Option<String>,
    provenance: EngineProvenance,
}

fn claude_args(spec: &LaunchSpec) -> Result<Vec<String>, EngineError> {
    let mut args = vec![
        "-p".to_owned(),
        "--output-format".to_owned(),
        "json".to_owned(),
        "--no-session-persistence".to_owned(),
    ];
    match spec.write_access {
        WriteAccess::ReadOnly => {
            args.push("--permission-mode".to_owned());
            args.push("plan".to_owned());
        }
        WriteAccess::Writable => {}
    }
    if let Some(model) = &spec.requested_model {
        args.push("--model".to_owned());
        args.push(model.clone());
    }
    if let Some(limit) = &spec.bounds.max_budget_usd {
        args.push("--max-budget-usd".to_owned());
        args.push(limit.clone());
    }
    if let Some(schema_path) = &spec.schema_path {
        let schema =
            fs::read_to_string(schema_path).map_err(|source| EngineError::ClaudeSchema {
                path: schema_path.display().to_string(),
                source,
            })?;
        args.push("--json-schema".to_owned());
        args.push(schema);
    }
    Ok(args)
}

fn codex_args(spec: &LaunchSpec) -> Vec<String> {
    let mut args = vec![
        "-a".to_owned(),
        "never".to_owned(),
        "exec".to_owned(),
        "--skip-git-repo-check".to_owned(),
        "--ephemeral".to_owned(),
        "--json".to_owned(),
    ];
    args.push("-s".to_owned());
    args.push(
        match spec.write_access {
            WriteAccess::ReadOnly => "read-only",
            WriteAccess::Writable => "workspace-write",
        }
        .to_owned(),
    );
    if let Some(model) = &spec.requested_model {
        args.push("--model".to_owned());
        args.push(model.clone());
    }
    if let Some(schema_path) = &spec.schema_path {
        args.push("--output-schema".to_owned());
        args.push(schema_path.display().to_string());
    }
    args
}

fn opencode_args(spec: &LaunchSpec) -> Result<Vec<String>, EngineError> {
    if spec.schema_path.is_some() {
        return Err(EngineError::OpencodeNativeSchemaUnavailable);
    }
    let mut args = vec![
        "run".to_owned(),
        "--format".to_owned(),
        "json".to_owned(),
        "--pure".to_owned(),
    ];
    if let Some(model) = &spec.requested_model {
        args.push("--model".to_owned());
        args.push(model.clone());
    }
    args.push("-".to_owned());
    Ok(args)
}

fn run_process(
    invocation: &CommandInvocation,
    timeout: Duration,
) -> Result<ProcessOutput, EngineError> {
    let mut command = Command::new(&invocation.program);
    command
        .args(&invocation.args)
        .current_dir(&invocation.cwd)
        .envs(&invocation.env)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = spawn_with_text_busy_retry(&mut command, &invocation.program)?;
    let Some(mut stdin) = child.stdin.take() else {
        return Err(EngineError::Io {
            action: "open child stdin".to_owned(),
            source: std::io::Error::other("child stdin unavailable"),
        });
    };
    stdin
        .write_all(invocation.stdin.as_bytes())
        .map_err(|source| EngineError::Io {
            action: "write child stdin".to_owned(),
            source,
        })?;
    drop(stdin);

    let deadline = Instant::now() + timeout;
    loop {
        if child
            .try_wait()
            .map_err(|source| EngineError::Io {
                action: "poll child".to_owned(),
                source,
            })?
            .is_some()
        {
            break;
        }
        if Instant::now() >= deadline {
            child.kill().map_err(|source| EngineError::Io {
                action: "kill timed-out child".to_owned(),
                source,
            })?;
            let output = child.wait_with_output().map_err(|source| EngineError::Io {
                action: "collect timed-out child".to_owned(),
                source,
            })?;
            return Ok(ProcessOutput {
                stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
                stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
                code: output.status.code(),
                timed_out: true,
            });
        }
        thread::sleep(Duration::from_millis(10));
    }

    let output = child.wait_with_output().map_err(|source| EngineError::Io {
        action: "collect child".to_owned(),
        source,
    })?;
    Ok(ProcessOutput {
        stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
        stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
        code: output.status.code(),
        timed_out: false,
    })
}

fn spawn_with_text_busy_retry(
    command: &mut Command,
    program: &Path,
) -> Result<std::process::Child, EngineError> {
    const TEXT_FILE_BUSY: i32 = 26;

    let mut last_error = None;
    for _attempt in 0..5 {
        match command.spawn() {
            Ok(child) => return Ok(child),
            Err(error) if error.raw_os_error() == Some(TEXT_FILE_BUSY) => {
                last_error = Some(error);
                thread::sleep(Duration::from_millis(10));
            }
            Err(source) => {
                return Err(EngineError::Io {
                    action: format!("spawn {}", program.display()),
                    source,
                });
            }
        }
    }
    Err(EngineError::Io {
        action: format!("spawn {}", program.display()),
        source: last_error.unwrap_or_else(|| std::io::Error::other("executable remained busy")),
    })
}

fn write_transcripts(
    archive_dir: &Path,
    process: &ProcessOutput,
) -> Result<Vec<TranscriptPath>, EngineError> {
    let stdout_path = archive_dir.join(STDOUT_TRANSCRIPT);
    fs::write(&stdout_path, process.stdout.as_bytes()).map_err(|source| EngineError::Io {
        action: format!("write stdout transcript {}", stdout_path.display()),
        source,
    })?;
    let stderr_path = archive_dir.join(STDERR_TRANSCRIPT);
    fs::write(&stderr_path, process.stderr.as_bytes()).map_err(|source| EngineError::Io {
        action: format!("write stderr transcript {}", stderr_path.display()),
        source,
    })?;
    Ok(vec![
        TranscriptPath {
            kind: TranscriptKind::Stdout,
            path: stdout_path,
        },
        TranscriptPath {
            kind: TranscriptKind::Stderr,
            path: stderr_path,
        },
    ])
}

fn parse_engine_output(spec: &LaunchSpec, process: &ProcessOutput) -> ParsedRun {
    if process.timed_out {
        return ParsedRun {
            classification: ExitClassification::Timeout,
            output: None,
            final_text: None,
            provenance: empty_provenance(spec),
        };
    }
    match spec.engine {
        EngineKind::Claude => parse_claude(spec, process),
        EngineKind::Codex => parse_codex(spec, process),
        EngineKind::Opencode => parse_opencode(spec, process),
    }
}

fn parse_claude(spec: &LaunchSpec, process: &ProcessOutput) -> ParsedRun {
    let Ok(value) = serde_json::from_str::<Value>(&process.stdout) else {
        return invalid_parse(spec, process, "stdout is not a JSON object");
    };
    let mut provenance = empty_provenance(spec);
    provenance.session_id = string_field(&value, "session_id");
    provenance.cost_usd = value.get("total_cost_usd").map(Value::to_string);
    provenance.usage = claude_usage(&value);
    if let Some((model, _usage)) = value
        .get("modelUsage")
        .and_then(Value::as_object)
        .and_then(|usage| usage.iter().next())
    {
        provenance.resolved_model = Some(model.clone());
        provenance.model_source = ModelSource::ResolvedByCli;
    }
    if !process_success(process) {
        let classification =
            if string_field(&value, "subtype").as_deref() == Some("error_max_budget_usd") {
                ExitClassification::BudgetExceeded {
                    limit: spec
                        .bounds
                        .max_budget_usd
                        .clone()
                        .unwrap_or_else(|| "unknown".to_owned()),
                }
            } else {
                ExitClassification::ProcessFailure { code: process.code }
            };
        return ParsedRun {
            classification,
            output: Some(value),
            final_text: None,
            provenance,
        };
    }
    let final_text = value
        .get("structured_output")
        .map(Value::to_string)
        .or_else(|| string_field(&value, "result"));
    parse_final_text(spec, final_text.as_deref().unwrap_or_default(), provenance)
}

fn parse_codex(spec: &LaunchSpec, process: &ProcessOutput) -> ParsedRun {
    let mut provenance = empty_provenance(spec);
    let mut final_text = None;
    for line in process
        .stdout
        .lines()
        .filter(|line| !line.trim().is_empty())
    {
        let Ok(value) = serde_json::from_str::<Value>(line) else {
            return invalid_parse(spec, process, "JSONL stream contains malformed JSON");
        };
        if value.get("type").and_then(Value::as_str) == Some("thread.started") {
            provenance.session_id = string_field(&value, "thread_id");
        }
        if value.get("type").and_then(Value::as_str) == Some("turn.completed") {
            provenance.usage = codex_usage(&value);
        }
        if value.get("type").and_then(Value::as_str) == Some("item.completed") {
            final_text = value
                .get("item")
                .and_then(|item| item.get("text"))
                .and_then(Value::as_str)
                .map(ToOwned::to_owned);
        }
    }
    if !process_success(process) {
        return ParsedRun {
            classification: ExitClassification::ProcessFailure { code: process.code },
            output: None,
            final_text,
            provenance,
        };
    }
    parse_final_text(spec, final_text.as_deref().unwrap_or_default(), provenance)
}

fn parse_opencode(spec: &LaunchSpec, process: &ProcessOutput) -> ParsedRun {
    let mut provenance = empty_provenance(spec);
    let mut final_text = String::new();
    let mut saw_event = false;
    for line in process
        .stdout
        .lines()
        .filter(|line| !line.trim().is_empty())
    {
        let Ok(value) = serde_json::from_str::<Value>(line) else {
            return invalid_parse(spec, process, "JSON event stream contains malformed JSON");
        };
        saw_event = true;
        provenance.session_id = provenance
            .session_id
            .or_else(|| string_field(&value, "sessionID"));
        if value.get("type").and_then(Value::as_str) == Some("text")
            && let Some(text) = value
                .get("part")
                .and_then(|part| part.get("text"))
                .and_then(Value::as_str)
        {
            final_text.push_str(text);
        }
        if value.get("type").and_then(Value::as_str) == Some("step_finish") {
            provenance.usage = opencode_usage(&value);
            provenance.cost_usd = value
                .get("part")
                .and_then(|part| part.get("cost"))
                .map(Value::to_string);
        }
    }
    if !process_success(process) {
        return ParsedRun {
            classification: ExitClassification::ProcessFailure { code: process.code },
            output: None,
            final_text: (!final_text.is_empty()).then_some(final_text),
            provenance,
        };
    }
    if !saw_event {
        return invalid_parse(spec, process, "stdout contained no JSON events");
    }
    parse_final_text(spec, &final_text, provenance)
}

fn parse_final_text(spec: &LaunchSpec, text: &str, provenance: EngineProvenance) -> ParsedRun {
    match serde_json::from_str::<Value>(text) {
        Ok(value) => ParsedRun {
            classification: ExitClassification::Success,
            output: Some(value),
            final_text: Some(text.to_owned()),
            provenance,
        },
        Err(error) => ParsedRun {
            classification: ExitClassification::InvalidOutput {
                reason: error.to_string(),
            },
            output: None,
            final_text: Some(text.to_owned()),
            provenance: fill_requested_provenance(spec, provenance),
        },
    }
}

fn invalid_parse(spec: &LaunchSpec, process: &ProcessOutput, reason: &str) -> ParsedRun {
    ParsedRun {
        classification: ExitClassification::InvalidOutput {
            reason: reason.to_owned(),
        },
        output: None,
        final_text: Some(process.stdout.clone()),
        provenance: empty_provenance(spec),
    }
}

fn empty_provenance(spec: &LaunchSpec) -> EngineProvenance {
    fill_requested_provenance(
        spec,
        EngineProvenance {
            engine: spec.engine,
            session_id: None,
            requested_model: spec.requested_model.clone(),
            resolved_model: None,
            model_source: ModelSource::NotReported,
            usage: TokenUsage::default(),
            cost_usd: None,
        },
    )
}

fn fill_requested_provenance(
    spec: &LaunchSpec,
    mut provenance: EngineProvenance,
) -> EngineProvenance {
    if provenance.resolved_model.is_none() && spec.requested_model.is_some() {
        provenance.resolved_model.clone_from(&spec.requested_model);
        provenance.model_source = ModelSource::RequestedAsOperatorAssertion;
    }
    provenance
}

fn process_success(process: &ProcessOutput) -> bool {
    process.code == Some(0)
}

fn string_field(value: &Value, field: &str) -> Option<String> {
    value
        .get(field)
        .and_then(Value::as_str)
        .map(ToOwned::to_owned)
}

fn invalid_output_reason(classification: &ExitClassification) -> String {
    match classification {
        ExitClassification::InvalidOutput { reason } => reason.clone(),
        other => format!("{other:?}"),
    }
}

fn claude_usage(value: &Value) -> TokenUsage {
    let Some(model_usage) = value.get("modelUsage").and_then(Value::as_object) else {
        return TokenUsage::default();
    };
    let mut usage = TokenUsage::default();
    for item in model_usage.values() {
        usage.input_tokens += u64_field(item, "inputTokens");
        usage.output_tokens += u64_field(item, "outputTokens");
        usage.total_tokens += u64_field(item, "inputTokens") + u64_field(item, "outputTokens");
    }
    usage
}

fn codex_usage(value: &Value) -> TokenUsage {
    let Some(usage) = value.get("usage") else {
        return TokenUsage::default();
    };
    let input = u64_field(usage, "input_tokens");
    let cached = u64_field(usage, "cached_input_tokens");
    let output = u64_field(usage, "output_tokens");
    let reasoning = u64_field(usage, "reasoning_output_tokens");
    TokenUsage {
        input_tokens: input,
        cached_input_tokens: cached,
        output_tokens: output,
        reasoning_output_tokens: reasoning,
        total_tokens: input + output + reasoning,
    }
}

fn opencode_usage(value: &Value) -> TokenUsage {
    let tokens = value
        .get("part")
        .and_then(|part| part.get("tokens"))
        .unwrap_or(&Value::Null);
    let input = u64_field(tokens, "input");
    let output = u64_field(tokens, "output");
    let reasoning = u64_field(tokens, "reasoning");
    let cached = tokens
        .get("cache")
        .map(|cache| u64_field(cache, "read"))
        .unwrap_or_default();
    TokenUsage {
        input_tokens: input,
        cached_input_tokens: cached,
        output_tokens: output,
        reasoning_output_tokens: reasoning,
        total_tokens: u64_field(tokens, "total").max(input + output + reasoning),
    }
}

fn u64_field(value: &Value, field: &str) -> u64 {
    value.get(field).and_then(Value::as_u64).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use std::{
        fs,
        path::{Path, PathBuf},
    };

    use serde_json::json;
    use tempfile::TempDir;

    use super::*;

    #[test]
    fn claude_invocation_uses_native_schema_budget_and_plan_mode() {
        let fixture = Fixture::new(EngineKind::Claude);
        fs::write(fixture.schema(), r#"{"type":"object"}"#).expect("write schema");

        let invocation = EngineSessionLauncher::build_invocation(&fixture.spec()).expect("build");

        assert_eq!(invocation.args[0], "-p");
        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair == ["--output-format", "json"])
        );
        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair == ["--permission-mode", "plan"])
        );
        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair == ["--max-budget-usd", "0.01"])
        );
        assert!(
            invocation
                .args
                .iter()
                .any(|arg| arg == r#"{"type":"object"}"#)
        );
    }

    #[test]
    fn codex_invocation_uses_jsonl_schema_and_read_only_sandbox() {
        let fixture = Fixture::new(EngineKind::Codex);

        let invocation = EngineSessionLauncher::build_invocation(&fixture.spec()).expect("build");

        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair == ["-a", "never"])
        );
        assert!(invocation.args.iter().any(|arg| arg == "exec"));
        assert!(invocation.args.iter().any(|arg| arg == "--json"));
        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair == ["-s", "read-only"])
        );
        assert!(
            invocation
                .args
                .windows(2)
                .any(|pair| pair[0] == "--output-schema"
                    && pair[1] == fixture.schema().display().to_string())
        );
        assert_eq!(invocation.args.last().map(String::as_str), Some("-"));
    }

    #[test]
    fn opencode_rejects_native_schema_because_pump_disc_has_no_flag() {
        let fixture = Fixture::new(EngineKind::Opencode);

        let error = EngineSessionLauncher::build_invocation(&fixture.spec())
            .expect_err("schema unsupported");

        assert!(matches!(
            error,
            EngineError::OpencodeNativeSchemaUnavailable
        ));
    }

    #[test]
    fn claude_success_captures_resolved_model_and_output() {
        let fixture = Fixture::new(EngineKind::Claude);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"subtype":"success","result":"{\"ok\":true}","session_id":"claude-session","total_cost_usd":0.25,"modelUsage":{"claude-opus-4-8[1m]":{"inputTokens":2,"outputTokens":3}}}'
"#,
        );

        let run = EngineSessionLauncher::default()
            .launch(&fixture.spec_without_schema(), &mut NoRepair)
            .expect("launch");

        assert_eq!(run.classification, ExitClassification::Success);
        assert_eq!(run.output, Some(json!({"ok": true})));
        assert_eq!(run.provenance.session_id.as_deref(), Some("claude-session"));
        assert_eq!(
            run.provenance.resolved_model.as_deref(),
            Some("claude-opus-4-8[1m]")
        );
        assert_eq!(run.provenance.model_source, ModelSource::ResolvedByCli);
        assert_eq!(run.provenance.usage.total_tokens, 5);
    }

    #[test]
    fn claude_budget_error_is_classified() {
        let fixture = Fixture::new(EngineKind::Claude);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"subtype":"error_max_budget_usd","is_error":true,"errors":["Reached maximum budget"],"modelUsage":{}}'
exit 1
"#,
        );

        let run = EngineSessionLauncher::default()
            .launch(&fixture.spec_without_schema(), &mut NoRepair)
            .expect("launch");

        assert_eq!(
            run.classification,
            ExitClassification::BudgetExceeded {
                limit: "0.01".to_owned()
            }
        );
    }

    #[test]
    fn codex_jsonl_success_records_requested_model_and_usage() {
        let fixture = Fixture::new(EngineKind::Codex);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"codex-thread"}'
printf '%s\n' '{"type":"item.completed","item":{"text":"{\"ok\":true}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2,"reasoning_output_tokens":1}}'
"#,
        );

        let run = EngineSessionLauncher::default()
            .launch(&fixture.spec(), &mut NoRepair)
            .expect("launch");

        assert_eq!(run.classification, ExitClassification::Success);
        assert_eq!(run.output, Some(json!({"ok": true})));
        assert_eq!(run.provenance.session_id.as_deref(), Some("codex-thread"));
        assert_eq!(
            run.provenance.resolved_model.as_deref(),
            Some("requested-model")
        );
        assert_eq!(
            run.provenance.model_source,
            ModelSource::RequestedAsOperatorAssertion
        );
        assert_eq!(run.provenance.usage.total_tokens, 13);
    }

    #[test]
    fn token_limit_exhaustion_is_frame_owned_for_codex() {
        let fixture = Fixture::new(EngineKind::Codex);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"codex-thread"}'
printf '%s\n' '{"type":"item.completed","item":{"text":"{\"ok\":true}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2,"reasoning_output_tokens":1}}'
"#,
        );
        let mut spec = fixture.spec();
        spec.bounds.max_total_tokens = Some(12);

        let run = EngineSessionLauncher::default()
            .launch(&spec, &mut NoRepair)
            .expect("launch");

        assert_eq!(
            run.classification,
            ExitClassification::TokenLimitExceeded {
                limit: 12,
                observed: 13
            }
        );
    }

    #[test]
    fn opencode_event_stream_success_records_tokens_and_requested_model() {
        let fixture = Fixture::new(EngineKind::Opencode);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"step_start","sessionID":"opencode-session"}'
printf '%s\n' '{"type":"text","sessionID":"opencode-session","part":{"text":"{\"ok\":true}"}}'
printf '%s\n' '{"type":"step_finish","sessionID":"opencode-session","part":{"tokens":{"total":20,"input":7,"output":3,"reasoning":1,"cache":{"read":9}},"cost":0.5}}'
"#,
        );

        let run = EngineSessionLauncher::default()
            .launch(&fixture.spec_without_schema(), &mut NoRepair)
            .expect("launch");

        assert_eq!(run.classification, ExitClassification::Success);
        assert_eq!(run.output, Some(json!({"ok": true})));
        assert_eq!(
            run.provenance.session_id.as_deref(),
            Some("opencode-session")
        );
        assert_eq!(
            run.provenance.model_source,
            ModelSource::RequestedAsOperatorAssertion
        );
        assert_eq!(run.provenance.usage.total_tokens, 20);
    }

    #[test]
    fn invalid_output_gets_bounded_repair() {
        let fixture = Fixture::new(EngineKind::Codex);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"codex-thread"}'
printf '%s\n' '{"type":"item.completed","item":{"text":"not json"}}'
"#,
        );
        let mut repair = StaticRepair {
            repaired: Some(r#"{"ok":true}"#.to_owned()),
            attempts: 0,
        };

        let run = EngineSessionLauncher::default()
            .launch(&fixture.spec(), &mut repair)
            .expect("launch");

        assert_eq!(run.classification, ExitClassification::Success);
        assert_eq!(run.output, Some(json!({"ok": true})));
        assert_eq!(run.repair_attempts, 1);
        assert_eq!(repair.attempts, 1);
        assert!(
            run.transcripts
                .iter()
                .any(|transcript| transcript.kind == TranscriptKind::FinalOutput)
        );
    }

    #[test]
    fn repair_exhaustion_keeps_invalid_output_classification() {
        let fixture = Fixture::new(EngineKind::Codex);
        write_test_executable(
            fixture.executable(),
            r#"#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"text":"not json"}}'
"#,
        );

        let run = EngineSessionLauncher::new(RepairPolicy { attempts: 2 })
            .launch(
                &fixture.spec(),
                &mut StaticRepair {
                    repaired: None,
                    attempts: 0,
                },
            )
            .expect("launch");

        assert!(matches!(
            run.classification,
            ExitClassification::InvalidOutput { .. }
        ));
        assert_eq!(run.repair_attempts, 1);
    }

    #[test]
    fn hard_wall_clock_timeout_kills_process() {
        let fixture = Fixture::new(EngineKind::Codex);
        write_test_executable(
            fixture.executable(),
            r"#!/bin/sh
sleep 5
",
        );
        let mut spec = fixture.spec();
        spec.bounds.wall_clock = Duration::from_millis(30);

        let run = EngineSessionLauncher::default()
            .launch(&spec, &mut NoRepair)
            .expect("launch");

        assert_eq!(run.classification, ExitClassification::Timeout);
    }

    struct StaticRepair {
        repaired: Option<String>,
        attempts: u32,
    }

    impl RepairStrategy for StaticRepair {
        fn repair(&mut self, _attempt: RepairAttempt) -> Result<Option<String>, EngineError> {
            self.attempts += 1;
            Ok(self.repaired.clone())
        }
    }

    struct Fixture {
        temp: TempDir,
        engine: EngineKind,
        executable: PathBuf,
        schema: PathBuf,
    }

    impl Fixture {
        fn new(engine: EngineKind) -> Self {
            let temp = TempDir::new().expect("tempdir");
            let executable = temp.path().join(match engine {
                EngineKind::Claude => "claude",
                EngineKind::Codex => "codex",
                EngineKind::Opencode => "opencode",
            });
            let schema = temp.path().join("schema.json");
            fs::write(temp.path().join("prompt.md"), "Return JSON").expect("write prompt");
            fs::write(&schema, r#"{"type":"object"}"#).expect("write schema");
            Self {
                temp,
                engine,
                executable,
                schema,
            }
        }

        fn spec(&self) -> LaunchSpec {
            LaunchSpec {
                engine: self.engine,
                executable: self.executable().to_owned(),
                working_dir: self.temp.path().to_owned(),
                prompt_path: self.temp.path().join("prompt.md"),
                schema_path: Some(self.schema().to_owned()),
                archive_dir: self.temp.path().join("archive"),
                requested_model: Some("requested-model".to_owned()),
                write_access: WriteAccess::ReadOnly,
                bounds: LaunchBounds {
                    wall_clock: Duration::from_secs(2),
                    max_budget_usd: Some("0.01".to_owned()),
                    max_total_tokens: Some(1_000),
                },
                env: BTreeMap::new(),
            }
        }

        fn spec_without_schema(&self) -> LaunchSpec {
            LaunchSpec {
                schema_path: None,
                ..self.spec()
            }
        }

        fn executable(&self) -> &Path {
            &self.executable
        }

        fn schema(&self) -> &Path {
            &self.schema
        }
    }

    fn write_test_executable(path: &Path, source: &str) {
        let temp_path = path.with_extension("tmp");
        fs::write(&temp_path, source).expect("write fake executable");
        make_executable(&temp_path);
        fs::rename(&temp_path, path).expect("publish fake executable");
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
}
