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
    collections::BTreeMap,
    io::Write,
    process::{Command, Stdio},
};

use pump19_contract::{
    AgentId, AgentRole, CertaintyClass, Confidence, ContractVersion, Decision, DecisionSubject,
    DecisionVerdict, Extensions, Finding, FindingId, FindingLocation, ForgeFacts, ModelProvenance,
    Patch, PatchChange, PatchId, RunId, RunOutcome, Severity,
};
use pump19_core::{
    AgentLaunchSpec, CoreError, PreparedAgent, RunKind, RunLaunchOutcome, RunLaunchRequest,
    RunLauncher,
};
use pump19_judgement::{
    JudgementBriefResult, JudgementRun, JudgementStatus, ReviewerResult, run_judgement,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use thiserror::Error;

const EXT_FORGE_FACTS: &str = "pump19.core.forge_facts";
const EXT_RAW_STDOUT: &str = "pump19.runs.raw_stdout";
const EXT_RAW_STDERR: &str = "pump19.runs.raw_stderr";
const EXT_MODEL_FAMILY: &str = "pump19.runs.model_family";

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
    #[error("command is empty")]
    EmptyCommand,
    #[error("command {program:?} failed to start: {source}")]
    CommandStart {
        program: String,
        #[source]
        source: std::io::Error,
    },
    #[error("command {program:?} failed: {stderr}")]
    CommandFailed { program: String, stderr: String },
    #[error("command {program:?} produced non-UTF-8 stdout")]
    NonUtf8Stdout { program: String },
    #[error("command {program:?} produced non-UTF-8 stderr")]
    NonUtf8Stderr { program: String },
    #[error("command {program:?} returned invalid JSON: {source}")]
    CommandJson {
        program: String,
        #[source]
        source: serde_json::Error,
    },
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
    fn run_review(&mut self, request: &RunLaunchRequest) -> Result<Vec<Finding>, RunBodyError>;
}

/// Executes a significance-judge run after the core has passed the launch gate.
pub trait JudgeRunBody {
    /// Rates findings as material or minor.
    ///
    /// # Errors
    ///
    /// Returns an error when judging cannot execute or decisions cannot be built.
    fn run_judge(&mut self, request: &RunLaunchRequest) -> Result<Vec<Decision>, RunBodyError>;
}

/// Executes a fix run after the core has passed the launch gate.
pub trait FixRunBody {
    /// Produces patches answering material findings.
    ///
    /// # Errors
    ///
    /// Returns an error when fixing cannot execute or patch artefacts cannot be built.
    fn run_fix(&mut self, request: &RunLaunchRequest) -> Result<Vec<Patch>, RunBodyError>;
}

/// Executes a finish run after the core has passed the launch gate.
pub trait FinishRunBody {
    /// Determines whether the finish action can proceed from contract facts.
    ///
    /// # Errors
    ///
    /// Returns an error when required forge facts are absent or malformed.
    fn run_finish(&mut self, request: &RunLaunchRequest) -> Result<RunOutcome, RunBodyError>;
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

    fn launch_run(&mut self, request: RunLaunchRequest) -> Result<RunLaunchOutcome, CoreError> {
        let result = match request.run_kind {
            RunKind::Review => self
                .review
                .run_review(&request)
                .map(|findings| RunLaunchOutcome {
                    outcome: RunOutcome::Succeeded,
                    findings,
                    decisions: Vec::new(),
                    patches: Vec::new(),
                    token_usage: None,
                }),
            RunKind::Judge => self
                .judge
                .run_judge(&request)
                .map(|decisions| RunLaunchOutcome {
                    outcome: RunOutcome::Succeeded,
                    findings: Vec::new(),
                    decisions,
                    patches: Vec::new(),
                    token_usage: None,
                }),
            RunKind::Fix => self.fix.run_fix(&request).map(|patches| RunLaunchOutcome {
                outcome: RunOutcome::Succeeded,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches,
                token_usage: None,
            }),
            RunKind::Finish => self
                .finish
                .run_finish(&request)
                .map(|outcome| RunLaunchOutcome {
                    outcome,
                    findings: Vec::new(),
                    decisions: Vec::new(),
                    patches: Vec::new(),
                    token_usage: None,
                }),
        };
        result.map_err(|error| CoreError::Launcher(error.to_string()))
    }
}

/// Review body backed by the landed `pump19-judgement` crate.
#[derive(Clone, Copy, Debug, Default)]
pub struct JudgementReviewBody;

impl ReviewRunBody for JudgementReviewBody {
    fn run_review(&mut self, request: &RunLaunchRequest) -> Result<Vec<Finding>, RunBodyError> {
        let run = run_judgement(&request.workspace.root)?;
        findings_from_judgement(request, &run)
    }
}

/// Significance judge body backed by an external JSON command.
#[derive(Clone, Debug)]
pub struct JsonCommandJudgeBody {
    command: Vec<String>,
}

impl JsonCommandJudgeBody {
    #[must_use]
    pub const fn new(command: Vec<String>) -> Self {
        Self { command }
    }
}

impl JudgeRunBody for JsonCommandJudgeBody {
    fn run_judge(&mut self, request: &RunLaunchRequest) -> Result<Vec<Decision>, RunBodyError> {
        let provenance = provenance_for_role(request, AgentRole::Judge)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "findings": request.state.findings,
        });
        let outputs = run_json_command::<Vec<JudgeDecisionOutput>>(&self.command, &input)?;
        Ok(outputs
            .into_iter()
            .map(|output| output.into_decision(&request.run_id, &provenance))
            .collect())
    }
}

/// Fix body backed by an external JSON command.
#[derive(Clone, Debug)]
pub struct JsonCommandFixBody {
    command: Vec<String>,
}

impl JsonCommandFixBody {
    #[must_use]
    pub const fn new(command: Vec<String>) -> Self {
        Self { command }
    }
}

impl FixRunBody for JsonCommandFixBody {
    fn run_fix(&mut self, request: &RunLaunchRequest) -> Result<Vec<Patch>, RunBodyError> {
        let findings = material_findings(request);
        if findings.is_empty() {
            return Ok(Vec::new());
        }
        let provenance = provenance_for_role(request, AgentRole::Fixer)?;
        let input = json!({
            "run_id": request.run_id,
            "pr": request.state.pr,
            "commit_sha": request.state.commit_sha,
            "material_findings": findings,
        });
        let change = run_json_command::<PatchChange>(&self.command, &input)?;
        Ok(vec![patch_from_change(
            request, findings, change, provenance,
        )])
    }
}

/// Finish body that consumes the contract merge gate and performs no forge side effect.
#[derive(Clone, Copy, Debug, Default)]
pub struct MergeGateFinishBody;

impl FinishRunBody for MergeGateFinishBody {
    fn run_finish(&mut self, request: &RunLaunchRequest) -> Result<RunOutcome, RunBodyError> {
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

fn run_json_command<T>(command: &[String], input: &Value) -> Result<T, RunBodyError>
where
    T: for<'de> Deserialize<'de>,
{
    let (program, args) = command.split_first().ok_or(RunBodyError::EmptyCommand)?;
    let mut child = Command::new(program)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|source| RunBodyError::CommandStart {
            program: program.clone(),
            source,
        })?;
    if let Some(stdin) = child.stdin.as_mut() {
        serde_json::to_writer(&mut *stdin, input).map_err(|source| RunBodyError::CommandJson {
            program: program.clone(),
            source,
        })?;
        stdin
            .write_all(b"\n")
            .map_err(|source| RunBodyError::CommandStart {
                program: program.clone(),
                source,
            })?;
    }
    let output = child
        .wait_with_output()
        .map_err(|source| RunBodyError::CommandStart {
            program: program.clone(),
            source,
        })?;
    let stdout = String::from_utf8(output.stdout).map_err(|_| RunBodyError::NonUtf8Stdout {
        program: program.clone(),
    })?;
    let stderr = String::from_utf8(output.stderr).map_err(|_| RunBodyError::NonUtf8Stderr {
        program: program.clone(),
    })?;
    if !output.status.success() {
        return Err(RunBodyError::CommandFailed {
            program: program.clone(),
            stderr,
        });
    }
    serde_json::from_str(&stdout).map_err(|source| RunBodyError::CommandJson {
        program: program.clone(),
        source,
    })
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use pump19_contract::{
        DecisionSubject, ModelFamily, ModelLineage, ProvenanceVerification, PullRequestRef,
        RunStatus, SessionFreshness, SessionId,
    };
    use pump19_core::{LaunchProof, WorkspaceIsolation, WorkspaceLease};
    use pump19_judgement::ReviewerResult;
    use serde_json::json;

    use super::*;

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
        ) -> Result<Vec<Finding>, RunBodyError> {
            Ok(self.findings.clone())
        }
    }

    #[derive(Debug)]
    struct FakeJudge {
        verdict: DecisionVerdict,
    }

    impl JudgeRunBody for FakeJudge {
        fn run_judge(&mut self, request: &RunLaunchRequest) -> Result<Vec<Decision>, RunBodyError> {
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
        fn run_fix(&mut self, request: &RunLaunchRequest) -> Result<Vec<Patch>, RunBodyError> {
            let provenance = provenance_for_role(request, AgentRole::Fixer)?;
            let findings = material_findings(request);
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
            extensions: BTreeMap::new(),
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
                pass_index: 1,
                status: RunStatus::Running,
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

        let outcome = launcher.launch_run(req).expect("launch judge");

        assert_eq!(outcome.outcome, RunOutcome::Succeeded);
        assert_eq!(outcome.decisions.len(), 1);
        assert_eq!(outcome.decisions[0].verdict, DecisionVerdict::Material);
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

        let outcome = launcher.launch_run(req).expect("launch fix");

        assert_eq!(outcome.patches.len(), 1);
        assert_eq!(
            outcome.patches[0].answers_findings,
            vec![FindingId("finding-1".to_owned())]
        );
        assert!(outcome.patches[0].idempotency_key.contains("patch-"));
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

        let outcome = finish.run_finish(&req).expect("finish");

        assert_eq!(outcome, RunOutcome::Succeeded);
    }

    #[test]
    fn json_command_fix_body_reads_patch_change_from_stdout() {
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
        let mut fix = JsonCommandFixBody::new(vec![
            "sh".to_owned(),
            "-c".to_owned(),
            "cat >/dev/null; printf '%s\n' '{\"kind\":\"description\",\"summary\":\"fixed\"}'"
                .to_owned(),
        ]);

        let patches = fix.run_fix(&req).expect("fix");

        assert!(matches!(
            patches[0].change,
            PatchChange::Description { ref summary } if summary == "fixed"
        ));
    }
}
