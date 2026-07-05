#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "contract conformance tests use direct fixture assertions"
    )
)]

use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// Open extension data carried by every contract artefact.
///
/// Extensions are deliberately opaque to the core contract. They let adaptations
/// carry forge-specific or organisation-specific data without turning those needs
/// into new required fields.
pub type Extensions = BTreeMap<String, Value>;

/// The current public contract version for the review-and-fix service.
pub const CURRENT_CONTRACT_VERSION: ContractVersion = ContractVersion { major: 1, minor: 7 };

/// A version marker present on every top-level contract artefact.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ContractVersion {
    pub major: u16,
    pub minor: u16,
}

impl ContractVersion {
    /// Returns the version produced by this crate.
    #[must_use]
    pub const fn current() -> Self {
        CURRENT_CONTRACT_VERSION
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct AgentId(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct CommentId(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct FindingId(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct ModelFamily(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct PatchId(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct RunId(pub String);

#[derive(Clone, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(transparent)]
pub struct SessionId(pub String);

/// The role an agent played in the review-and-fix loop.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AgentRole {
    Lead,
    Reviewer,
    Verifier,
    BarCheck,
    Fixer,
    Judge,
    Finish,
}

/// The independent run types the service can dispatch and compose through events.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunKind {
    Review,
    Judge,
    Fix,
    Finish,
}

/// The model metadata the core has either verified or failed to verify.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ModelProvenance {
    pub contract_version: ContractVersion,
    pub agent_id: AgentId,
    pub role: AgentRole,
    pub session_id: SessionId,
    pub freshness: SessionFreshness,
    pub verification: ProvenanceVerification,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

impl ModelProvenance {
    /// Returns the verified model family, or `None` when provenance is unverified.
    #[must_use]
    pub const fn verified_family(&self) -> Option<&ModelFamily> {
        match &self.verification {
            ProvenanceVerification::Verified { lineage, .. } => Some(&lineage.family),
            ProvenanceVerification::Unverified { .. } => None,
        }
    }
}

/// A fresh session is established by the core at launch time, not self-reported.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "state")]
pub enum SessionFreshness {
    FreshForPass { pass_index: u32 },
    Reused { original_session_id: SessionId },
    Unknown { reason: String },
}

/// Verified provenance makes the family non-optional.
///
/// That removes the illegal state "verified, but no family", which would be a
/// dangerous thing for the independence predicates to interpret.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "state")]
pub enum ProvenanceVerification {
    Verified {
        vendor: String,
        control_plane: String,
        lineage: ModelLineage,
    },
    Unverified {
        reason: String,
    },
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ModelLineage {
    pub family: ModelFamily,
    pub model: String,
}

/// A review finding raised by an independent reviewer.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Finding {
    pub contract_version: ContractVersion,
    pub id: FindingId,
    pub dedup_key: String,
    pub source_brief: String,
    pub dimension: String,
    pub summary: String,
    pub severity: Severity,
    pub confidence: Confidence,
    pub certainty: CertaintyClass,
    pub provenance: ModelProvenance,
    pub locations: Vec<FindingLocation>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Severity {
    Low,
    Medium,
    High,
    Critical,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Confidence {
    Low,
    Medium,
    High,
}

/// The certainty tier carried by every finding.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CertaintyClass {
    Advisory,
    Blocking,
}

/// Repository policy priority for a review finding.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PriorityClass {
    P0,
    P1,
    P2,
    P3,
}

/// Verification state for a candidate finding.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "status")]
pub enum VerificationStatus {
    Verified,
    Rejected { reason: String },
    Unverified { reason: String },
}

/// Whether a verifier came from a different model family from the producer.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FamilySplit {
    CrossFamily,
    SameFamily,
    Unknown,
}

/// Evidence cited by a verifier or review-bar checker.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct CitedEvidence {
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_range: Option<SourceRange>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub quote: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Independent verification attached to a candidate finding.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct FindingVerification {
    pub status: VerificationStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub verifier: Option<ModelProvenance>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<CitedEvidence>,
    pub cross_family: FamilySplit,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Agent-owned account of review coverage for one pass.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct CoverageRecord {
    pub complete: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub visited: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub unvisited: Vec<String>,
    pub account: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Independent second-opinion record for the assembled review.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct BarCheckRecord {
    pub passed: bool,
    pub provenance: ModelProvenance,
    pub rationale: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Review pass verdict emitted by the new frame.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "verdict")]
pub enum ReviewVerdict {
    Converged {
        bar_check: BarCheckRecord,
    },
    FindingsPosted {
        material: u32,
        suppressed: u32,
    },
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

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum FindingLocation {
    File {
        path: String,
        line: Option<u32>,
        range: Option<SourceRange>,
    },
    General {
        description: String,
    },
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct SourceRange {
    pub start_line: u32,
    pub start_column: Option<u32>,
    pub end_line: u32,
    pub end_column: Option<u32>,
}

/// A fix run's patch for one or more findings.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Patch {
    pub contract_version: ContractVersion,
    pub id: PatchId,
    pub run_id: RunId,
    pub commit_sha: String,
    pub idempotency_key: String,
    pub answers_findings: Vec<FindingId>,
    pub change: PatchChange,
    pub provenance: ModelProvenance,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// A forge operation receipt recorded durably in run state.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgeReceipt {
    pub operation_id: String,
    pub idempotency_key: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub new_head_sha: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum PatchChange {
    UnifiedDiff { diff: String },
    Description { summary: String },
}

/// Normalised data for a comment that an adaptation may render and post.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Comment {
    pub contract_version: ContractVersion,
    pub id: CommentId,
    pub finding_ids: Vec<FindingId>,
    pub target: CommentTarget,
    pub payload: CommentPayload,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum CommentTarget {
    PullRequest { pr: PullRequestRef },
    FindingLocation { finding_id: FindingId },
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct CommentPayload {
    pub summary: String,
    pub details: Vec<String>,
}

/// The significance judge's decision over one finding or a set of findings.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Decision {
    pub contract_version: ContractVersion,
    pub id: String,
    pub subject: DecisionSubject,
    pub verdict: DecisionVerdict,
    pub rationale: String,
    pub provenance: ModelProvenance,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum DecisionSubject {
    Finding { finding_id: FindingId },
    FindingSet { finding_ids: Vec<FindingId> },
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DecisionVerdict {
    Material,
    Minor,
    Converged,
}

/// Durable publication state for everything Pump-19 writes back to a PR.
#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct PublicationState {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attempts: Vec<PublicationAttempt>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub finding_comments: Vec<FindingCommentPublication>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fix_pushes: Vec<FixPushPublication>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub merges: Vec<MergePublication>,
}

impl PublicationState {
    #[must_use]
    pub const fn is_empty(&self) -> bool {
        self.attempts.is_empty()
            && self.finding_comments.is_empty()
            && self.fix_pushes.is_empty()
            && self.merges.is_empty()
    }
}

/// One attempted outbound publication, successful or failed.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PublicationAttempt {
    pub contract_version: ContractVersion,
    pub run_id: RunId,
    pub operation: PublicationOperation,
    pub idempotency_key: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_head_sha: Option<String>,
    pub status: PublicationAttemptStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub receipt: Option<ForgeReceipt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refusal: Option<PublicationRefusal>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum PublicationOperation {
    PostFindingComment {
        finding_id: FindingId,
        finding_dedup_key: String,
    },
    UpdateFindingComment {
        finding_id: FindingId,
        finding_dedup_key: String,
        comment_operation_id: String,
    },
    ResolveFindingComment {
        finding_dedup_key: String,
        comment_operation_id: String,
    },
    PushFixCommits {
        patch_ids: Vec<PatchId>,
    },
    ApplyFinishLabel {
        label: String,
    },
    MergePullRequest,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PublicationAttemptStatus {
    Succeeded,
    Refused,
    Failed,
}

/// A publication the core authorised but deliberately did not let through.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PublicationRefusal {
    pub reason: PublicationRefusalReason,
    pub message: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "reason")]
pub enum PublicationRefusalReason {
    HeadMoved {
        expected_head_sha: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        actual_head_sha: Option<String>,
    },
}

/// The live PR comment currently associated with one stable finding identity.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct FindingCommentPublication {
    pub finding_dedup_key: String,
    pub latest_finding_id: FindingId,
    pub comment_operation_id: String,
    pub status: FindingCommentStatus,
    pub last_receipt: ForgeReceipt,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FindingCommentStatus {
    Open,
    Resolved,
}

/// Durable record of a fix run's credentialed push to the PR head branch.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct FixPushPublication {
    pub run_id: RunId,
    pub patch_ids: Vec<PatchId>,
    pub commits: Vec<PublishedFixCommit>,
    pub receipt: ForgeReceipt,
}

/// Durable record of an authorised merge applied to the pull request.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct MergePublication {
    pub run_id: RunId,
    pub receipt: ForgeReceipt,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PublishedFixCommit {
    pub patch_id: PatchId,
    pub author_agent_id: AgentId,
    pub provenance: ModelProvenance,
}

/// Durable per-PR run state keyed to a pull request and commit SHA.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PrRunState {
    pub contract_version: ContractVersion,
    pub pr: PullRequestRef,
    pub commit_sha: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub current_head_sha: Option<String>,
    pub pass_index: u32,
    pub status: RunStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub active_run: Option<RunRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_history: Vec<RunRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub loop_history: Vec<LoopPassRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub superseded_by: Option<String>,
    pub findings: Vec<Finding>,
    pub decisions: Vec<Decision>,
    pub patches: Vec<Patch>,
    #[serde(default, skip_serializing_if = "PublicationState::is_empty")]
    pub publication: PublicationState,
    pub ceiling: Option<RunCeiling>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunStatus {
    Pending,
    Running,
    Completed,
    Skipped,
    Failed,
    Superseded,
}

/// One launched run recorded inside the PR loop state.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunRecord {
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub event_id: String,
    pub rule_id: String,
    pub pass_index: u32,
    pub commit_sha: String,
    pub status: RunStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub outcome: Option<RunOutcome>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refusal: Option<RunRefusal>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ensemble_archive_path: Option<String>,
    /// The verified provenance the run launched with. Persisted so later
    /// launch gates (judge independence, family spread) can see reviewers
    /// even when a run produced no findings.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub provenance: Vec<ModelProvenance>,
}

/// A launch refusal recorded in run history.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunRefusal {
    pub reason: RunRefusalReason,
    pub message: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunRefusalReason {
    RunCeilingReached,
    RequiredFamilyUnavailable,
    WorkspaceIsolationMissing,
    UnverifiedProvenance,
    InsufficientReviewerFamilies,
    ReviewerFixerOverlap,
    MissingIndependentJudge,
    NonFreshSession,
}

/// Archived working set for one completed loop pass.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct LoopPassRecord {
    pub pass_index: u32,
    pub commit_sha: String,
    pub findings: Vec<Finding>,
    pub decisions: Vec<Decision>,
    pub patches: Vec<Patch>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub judge_verdict: Option<DecisionVerdict>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fix_outcome: Option<RunOutcome>,
}

/// Optional runaway guard. `None` means the core has no ceiling enabled.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunCeiling {
    pub max_passes: Option<u32>,
    pub token_budget: Option<u64>,
}

/// Typed description of the run directory and review inputs prepared for a lead
/// session.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunManifest {
    pub contract_version: ContractVersion,
    pub run_id: RunId,
    pub run_kind: RunKind,
    pub pr: PullRequestRef,
    pub commit_sha: String,
    pub base_sha: String,
    pub occasion: String,
    pub workspace: ManifestWorkspace,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<String>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub changed_lines: BTreeMap<String, Vec<u32>>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_briefs: Vec<ManifestBrief>,
    pub loop_history: ManifestLoopHistory,
    pub bounds: ManifestBounds,
    pub repository_policy: ManifestRepositoryPolicy,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Workspace paths exposed to the lead session.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ManifestWorkspace {
    pub tree_root: String,
    pub diff_path: String,
    pub governing_content_dir: String,
}

/// One review brief selected for the run.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ManifestBrief {
    pub id: String,
    pub title: String,
    pub occasion: String,
}

/// Loop-history summary provided to the lead session.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ManifestLoopHistory {
    pub pass_count: u32,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub prior_verdicts: Vec<String>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub fix_survival_by_dedup_key: BTreeMap<String, u32>,
}

/// Bounds that frame the lead session.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ManifestBounds {
    pub wall_clock_ms: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_budget_usd: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_total_tokens: Option<u64>,
}

/// Repository policy facts used by the frame's mechanical gates.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ManifestRepositoryPolicy {
    pub fix_before_merge_priority: PriorityClass,
}

/// Forge-neutral facts the core needs for label authority and merge gating.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgeFacts {
    pub contract_version: ContractVersion,
    pub pr: PullRequestRef,
    pub head: Revision,
    pub base: Revision,
    pub branch_currency: BranchCurrency,
    pub cleanliness: ReviewCleanliness,
    pub mergeability: Mergeability,
    pub finish_label: Option<FinishLabel>,
    pub actor_permissions: Vec<ActorPermissions>,
    /// The forge login of the PR author, when the forge exposes it. Optional
    /// because contract 1.4 producers predate it; criteria that filter on the
    /// author fail closed when it is absent.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub author_login: Option<String>,
    /// Whether the forge reports the PR as draft/work-in-progress. Absent means
    /// "ready" for contract 1.5 and older producers.
    #[serde(default, skip_serializing_if = "is_false")]
    pub work_in_progress: bool,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[allow(
    clippy::trivially_copy_pass_by_ref,
    reason = "serde skip_serializing_if predicates take values by reference"
)]
const fn is_false(value: &bool) -> bool {
    !*value
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PullRequestRef {
    pub repository: String,
    pub id: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Revision {
    pub sha: String,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BranchCurrency {
    Current,
    Stale,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ReviewCleanliness {
    Clean,
    Dirty,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Mergeability {
    Mergeable,
    Conflicting,
    Unknown,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct FinishLabel {
    pub name: String,
    pub applied_by: ActorRef,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ActorRef {
    pub id: String,
    pub display_name: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ActorPermissions {
    pub actor: ActorRef,
    pub capabilities: BTreeSet<ActorCapability>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ActorCapability {
    ApplyFinishLabel,
    Merge,
}

/// A small event vocabulary for criteria-triggered runs.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ContractEvent {
    pub contract_version: ContractVersion,
    pub id: String,
    pub payload: EventPayload,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "event")]
pub enum EventPayload {
    PullRequestOpened {
        facts: ForgeFacts,
    },
    PullRequestUpdated {
        facts: ForgeFacts,
    },
    RunCompleted {
        run_id: RunId,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        run_kind: Option<RunKind>,
        outcome: RunOutcome,
    },
    LabelApplied {
        pr: PullRequestRef,
        label: FinishLabel,
    },
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunOutcome {
    Succeeded,
    NoOp,
    Failed,
    Cancelled,
}

/// True when the reviewer set spans at least two verified model families.
#[must_use]
pub fn has_two_verified_reviewer_families(provenances: &[ModelProvenance]) -> bool {
    let families = provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
        .filter_map(ModelProvenance::verified_family)
        .collect::<BTreeSet<_>>();
    families.len() >= 2
}

/// True when no recorded reviewer also appears as a fixer.
#[must_use]
pub fn reviewers_disjoint_from_fixers(provenances: &[ModelProvenance]) -> bool {
    let reviewers = provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
        .map(|provenance| &provenance.agent_id)
        .collect::<BTreeSet<_>>();
    let fixers = provenances
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Fixer)
        .map(|provenance| &provenance.agent_id)
        .collect::<BTreeSet<_>>();
    reviewers.is_disjoint(&fixers)
}

/// True when the judge is verified and shares neither agent id nor family with reviewers.
#[must_use]
pub fn judge_independent_of_reviewers(
    judge: &ModelProvenance,
    reviewers: &[ModelProvenance],
) -> bool {
    if judge.role != AgentRole::Judge {
        return false;
    }
    let Some(judge_family) = judge.verified_family() else {
        return false;
    };
    for reviewer in reviewers
        .iter()
        .filter(|provenance| provenance.role == AgentRole::Reviewer)
    {
        if reviewer.agent_id == judge.agent_id {
            return false;
        }
        let Some(reviewer_family) = reviewer.verified_family() else {
            return false;
        };
        if reviewer_family == judge_family {
            return false;
        }
    }
    true
}

/// True when every supplied agent session is fresh for the requested pass.
#[must_use]
pub fn sessions_fresh_for_pass(provenances: &[ModelProvenance], pass_index: u32) -> bool {
    provenances.iter().all(|provenance| {
        matches!(
            provenance.freshness,
            SessionFreshness::FreshForPass { pass_index: freshness_pass } if freshness_pass == pass_index
        )
    })
}

/// True when the core's merge gate can pass on contract facts alone.
#[must_use]
pub const fn merge_gate_clean_and_current(facts: &ForgeFacts) -> bool {
    matches!(facts.cleanliness, ReviewCleanliness::Clean)
        && matches!(facts.branch_currency, BranchCurrency::Current)
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use serde::{Serialize, de::DeserializeOwned};
    use serde_json::json;

    use super::{
        ActorCapability, ActorPermissions, ActorRef, AgentId, AgentRole, BranchCurrency,
        CertaintyClass, Comment, CommentId, CommentPayload, CommentTarget, Confidence,
        ContractEvent, ContractVersion, Decision, DecisionSubject, DecisionVerdict, EventPayload,
        Finding, FindingCommentPublication, FindingCommentStatus, FindingId, FindingLocation,
        FinishLabel, FixPushPublication, ForgeFacts, ForgeReceipt, LoopPassRecord,
        MergePublication, Mergeability, ModelFamily, ModelLineage, ModelProvenance, Patch,
        PatchChange, PatchId, ProvenanceVerification, PublicationAttempt, PublicationAttemptStatus,
        PublicationOperation, PublicationRefusal, PublicationRefusalReason, PublicationState,
        PublishedFixCommit, PullRequestRef, ReviewCleanliness, Revision, RunCeiling, RunId,
        RunKind, RunOutcome, RunRecord, RunStatus, SessionFreshness, SessionId, Severity,
        SourceRange, has_two_verified_reviewer_families, judge_independent_of_reviewers,
        merge_gate_clean_and_current, reviewers_disjoint_from_fixers, sessions_fresh_for_pass,
    };

    fn round_trip<T>(value: &T)
    where
        T: Clone + std::fmt::Debug + PartialEq + Serialize + DeserializeOwned,
    {
        let encoded = serde_json::to_string(value).expect("serialise fixture");
        let decoded = serde_json::from_str::<T>(&encoded).expect("deserialise fixture");
        assert_eq!(decoded, value.clone());
    }

    fn version() -> ContractVersion {
        ContractVersion::current()
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

    fn extensions() -> super::Extensions {
        BTreeMap::new()
    }

    fn verified_provenance(agent_id: &str, role: AgentRole, family: &str) -> ModelProvenance {
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
                    model: format!("{family}-2026"),
                },
            },
            extensions: extensions(),
        }
    }

    fn finding() -> Finding {
        Finding {
            contract_version: version(),
            id: FindingId("finding-1".to_owned()),
            dedup_key: "brief:correctness:path:src/lib.rs:12".to_owned(),
            source_brief: "correctness".to_owned(),
            dimension: "soundness".to_owned(),
            summary: "The update accepts stale review state.".to_owned(),
            severity: Severity::High,
            confidence: Confidence::High,
            certainty: CertaintyClass::Advisory,
            provenance: verified_provenance("codex-reviewer", AgentRole::Reviewer, "codex"),
            locations: vec![FindingLocation::File {
                path: "src/lib.rs".to_owned(),
                line: Some(12),
                range: Some(SourceRange {
                    start_line: 12,
                    start_column: Some(5),
                    end_line: 15,
                    end_column: Some(9),
                }),
            }],
            extensions: extensions(),
        }
    }

    fn patch() -> Patch {
        Patch {
            contract_version: version(),
            id: PatchId("patch-1".to_owned()),
            run_id: RunId("run-fix-1".to_owned()),
            commit_sha: "abc123".to_owned(),
            idempotency_key: "run-fix-1:abc123:finding-1".to_owned(),
            answers_findings: vec![FindingId("finding-1".to_owned())],
            change: PatchChange::UnifiedDiff {
                diff: "--- a/src/lib.rs\n+++ b/src/lib.rs\n".to_owned(),
            },
            provenance: verified_provenance("fixer", AgentRole::Fixer, "codex"),
            extensions: extensions(),
        }
    }

    fn decision() -> Decision {
        Decision {
            contract_version: version(),
            id: "decision-1".to_owned(),
            subject: DecisionSubject::Finding {
                finding_id: FindingId("finding-1".to_owned()),
            },
            verdict: DecisionVerdict::Material,
            rationale: "The finding affects merge safety.".to_owned(),
            provenance: verified_provenance("judge", AgentRole::Judge, "glm"),
            extensions: extensions(),
        }
    }

    fn forge_facts() -> ForgeFacts {
        ForgeFacts {
            contract_version: version(),
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
                applied_by: actor("core"),
            }),
            actor_permissions: vec![ActorPermissions {
                actor: actor("core"),
                capabilities: [ActorCapability::ApplyFinishLabel, ActorCapability::Merge]
                    .into_iter()
                    .collect(),
            }],
            author_login: None,
            work_in_progress: false,
            extensions: extensions(),
        }
    }

    fn publication_state() -> PublicationState {
        PublicationState {
            attempts: vec![
                PublicationAttempt {
                    contract_version: version(),
                    run_id: RunId("run-judge-1".to_owned()),
                    operation: PublicationOperation::PostFindingComment {
                        finding_id: FindingId("finding-1".to_owned()),
                        finding_dedup_key: "brief:correctness:path:src/lib.rs:12".to_owned(),
                    },
                    idempotency_key: "comment-finding-1".to_owned(),
                    expected_head_sha: Some("abc123".to_owned()),
                    status: PublicationAttemptStatus::Succeeded,
                    receipt: Some(ForgeReceipt {
                        operation_id: "comment-1".to_owned(),
                        idempotency_key: "comment-finding-1".to_owned(),
                        new_head_sha: None,
                    }),
                    error: None,
                    refusal: None,
                },
                PublicationAttempt {
                    contract_version: version(),
                    run_id: RunId("run-judge-2".to_owned()),
                    operation: PublicationOperation::ApplyFinishLabel {
                        label: "pump19-finish".to_owned(),
                    },
                    idempotency_key: "apply-finish-label-1".to_owned(),
                    expected_head_sha: None,
                    status: PublicationAttemptStatus::Succeeded,
                    receipt: Some(ForgeReceipt {
                        operation_id: "finish-label-1".to_owned(),
                        idempotency_key: "apply-finish-label-1".to_owned(),
                        new_head_sha: None,
                    }),
                    error: None,
                    refusal: None,
                },
            ],
            finding_comments: vec![FindingCommentPublication {
                finding_dedup_key: "brief:correctness:path:src/lib.rs:12".to_owned(),
                latest_finding_id: FindingId("finding-1".to_owned()),
                comment_operation_id: "comment-1".to_owned(),
                status: FindingCommentStatus::Open,
                last_receipt: ForgeReceipt {
                    operation_id: "comment-1".to_owned(),
                    idempotency_key: "comment-finding-1".to_owned(),
                    new_head_sha: None,
                },
            }],
            fix_pushes: vec![FixPushPublication {
                run_id: RunId("run-fix-1".to_owned()),
                patch_ids: vec![PatchId("patch-1".to_owned())],
                commits: vec![PublishedFixCommit {
                    patch_id: PatchId("patch-1".to_owned()),
                    author_agent_id: AgentId("fixer".to_owned()),
                    provenance: verified_provenance("fixer", AgentRole::Fixer, "codex"),
                }],
                receipt: ForgeReceipt {
                    operation_id: "fix-push-1".to_owned(),
                    idempotency_key: "fix-push-1".to_owned(),
                    new_head_sha: Some("fed789".to_owned()),
                },
            }],
            merges: vec![MergePublication {
                run_id: RunId("run-finish-1".to_owned()),
                receipt: ForgeReceipt {
                    operation_id: "merge-1".to_owned(),
                    idempotency_key: "merge-1-key".to_owned(),
                    new_head_sha: None,
                },
            }],
        }
    }

    #[test]
    fn contract_artifacts_round_trip_through_json() {
        let comment = Comment {
            contract_version: version(),
            id: CommentId("comment-1".to_owned()),
            finding_ids: vec![FindingId("finding-1".to_owned())],
            target: CommentTarget::PullRequest { pr: pr() },
            payload: CommentPayload {
                summary: "Stale review state".to_owned(),
                details: vec!["The merge gate needs fresh facts.".to_owned()],
            },
            extensions: extensions(),
        };
        let run_state = super::PrRunState {
            contract_version: version(),
            pr: pr(),
            commit_sha: "abc123".to_owned(),
            current_head_sha: Some("abc123".to_owned()),
            pass_index: 1,
            status: RunStatus::Completed,
            active_run: None,
            run_history: vec![RunRecord {
                run_id: RunId("run-review-1".to_owned()),
                run_kind: RunKind::Review,
                event_id: "event-1".to_owned(),
                rule_id: "review".to_owned(),
                pass_index: 1,
                commit_sha: "abc123".to_owned(),
                status: RunStatus::Completed,
                outcome: Some(RunOutcome::Succeeded),
                refusal: None,
                ensemble_archive_path: Some("/var/lib/pump19/archives/run-review-1".to_owned()),
                provenance: Vec::new(),
            }],
            loop_history: vec![LoopPassRecord {
                pass_index: 1,
                commit_sha: "abc123".to_owned(),
                findings: vec![finding()],
                decisions: vec![decision()],
                patches: vec![patch()],
                judge_verdict: Some(DecisionVerdict::Material),
                fix_outcome: Some(RunOutcome::Succeeded),
            }],
            superseded_by: None,
            findings: vec![finding()],
            decisions: vec![decision()],
            patches: vec![patch()],
            publication: publication_state(),
            ceiling: Some(RunCeiling {
                max_passes: Some(5),
                token_budget: None,
            }),
            extensions: extensions(),
        };
        let event = ContractEvent {
            contract_version: version(),
            id: "event-1".to_owned(),
            payload: EventPayload::RunCompleted {
                run_id: RunId("run-review-1".to_owned()),
                run_kind: Some(super::RunKind::Review),
                outcome: RunOutcome::Succeeded,
            },
            extensions: extensions(),
        };

        round_trip(&finding());
        round_trip(&patch());
        round_trip(&comment);
        round_trip(&decision());
        round_trip(&run_state);
        round_trip(&verified_provenance(
            "codex-reviewer",
            AgentRole::Reviewer,
            "codex",
        ));
        round_trip(&forge_facts());
        round_trip(&event);
    }

    #[test]
    fn run_state_round_trips_archive_paths_and_refusals() {
        let state = super::PrRunState {
            contract_version: version(),
            pr: pr(),
            commit_sha: "abc123".to_owned(),
            current_head_sha: Some("abc123".to_owned()),
            pass_index: 3,
            status: RunStatus::Skipped,
            active_run: None,
            run_history: vec![RunRecord {
                run_id: RunId("run-review-3".to_owned()),
                run_kind: RunKind::Review,
                event_id: "event-3".to_owned(),
                rule_id: "review".to_owned(),
                pass_index: 3,
                commit_sha: "abc123".to_owned(),
                status: RunStatus::Skipped,
                outcome: None,
                refusal: Some(super::RunRefusal {
                    reason: super::RunRefusalReason::RunCeilingReached,
                    message: "run ceiling reached before launch".to_owned(),
                }),
                ensemble_archive_path: Some("/var/lib/pump19/archives/run-review-3".to_owned()),
                provenance: Vec::new(),
            }],
            loop_history: Vec::new(),
            superseded_by: None,
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            publication: PublicationState::default(),
            ceiling: Some(RunCeiling {
                max_passes: Some(3),
                token_budget: None,
            }),
            extensions: extensions(),
        };

        let encoded = serde_json::to_value(&state).expect("serialise state");
        assert_eq!(
            encoded["run_history"][0]["ensemble_archive_path"],
            json!("/var/lib/pump19/archives/run-review-3")
        );
        assert_eq!(
            encoded["run_history"][0]["refusal"]["reason"],
            json!("run_ceiling_reached")
        );
        let decoded =
            serde_json::from_value::<super::PrRunState>(encoded).expect("deserialise state");
        assert_eq!(decoded, state);
    }

    #[test]
    fn run_state_round_trips_refused_publication_attempts() {
        let state = super::PrRunState {
            contract_version: version(),
            pr: pr(),
            commit_sha: "abc123".to_owned(),
            current_head_sha: Some("def456".to_owned()),
            pass_index: 2,
            status: RunStatus::Superseded,
            active_run: None,
            run_history: Vec::new(),
            loop_history: Vec::new(),
            superseded_by: Some("def456".to_owned()),
            findings: Vec::new(),
            decisions: Vec::new(),
            patches: Vec::new(),
            publication: PublicationState {
                attempts: vec![PublicationAttempt {
                    contract_version: version(),
                    run_id: RunId("run-judge-2".to_owned()),
                    operation: PublicationOperation::PostFindingComment {
                        finding_id: FindingId("finding-1".to_owned()),
                        finding_dedup_key: "brief:correctness:path:src/lib.rs:12".to_owned(),
                    },
                    idempotency_key: "comment-finding-1".to_owned(),
                    expected_head_sha: Some("abc123".to_owned()),
                    status: PublicationAttemptStatus::Refused,
                    receipt: None,
                    error: Some(
                        "publication refused because PR head moved from abc123 to Some(\"def456\")"
                            .to_owned(),
                    ),
                    refusal: Some(PublicationRefusal {
                        reason: PublicationRefusalReason::HeadMoved {
                            expected_head_sha: "abc123".to_owned(),
                            actual_head_sha: Some("def456".to_owned()),
                        },
                        message:
                            "publication refused because PR head moved from abc123 to Some(\"def456\")"
                                .to_owned(),
                    }),
                }],
                finding_comments: Vec::new(),
                fix_pushes: Vec::new(),
                merges: Vec::new(),
            },
            ceiling: None,
            extensions: extensions(),
        };

        let encoded = serde_json::to_value(&state).expect("serialise state");
        assert_eq!(
            encoded["publication"]["attempts"][0]["status"],
            json!("refused")
        );
        assert_eq!(
            encoded["publication"]["attempts"][0]["refusal"]["reason"]["reason"],
            json!("head_moved")
        );
        assert_eq!(
            encoded["publication"]["attempts"][0]["refusal"]["reason"]["expected_head_sha"],
            json!("abc123")
        );
        assert_eq!(
            encoded["publication"]["attempts"][0]["refusal"]["reason"]["actual_head_sha"],
            json!("def456")
        );

        let decoded =
            serde_json::from_value::<super::PrRunState>(encoded).expect("deserialise state");
        assert_eq!(decoded, state);
    }

    #[test]
    fn open_extensions_survive_unknown_json() {
        let payload = json!({
            "contract_version": { "major": 1, "minor": 0 },
            "id": "finding-1",
            "dedup_key": "brief:correctness:path:src/lib.rs:12",
            "source_brief": "correctness",
            "dimension": "soundness",
            "summary": "The update accepts stale review state.",
            "severity": "high",
            "confidence": "high",
            "certainty": "advisory",
            "provenance": verified_provenance("codex-reviewer", AgentRole::Reviewer, "codex"),
            "locations": [{ "kind": "general", "description": "whole change" }],
            "extensions": {
                "forgejo.thread": { "id": 99, "state": "open" },
                "reviewer.raw": ["line one", "line two"]
            }
        });
        let finding = serde_json::from_value::<Finding>(payload).expect("deserialise finding");
        assert_eq!(
            finding.extensions["forgejo.thread"],
            json!({ "id": 99, "state": "open" })
        );

        let encoded = serde_json::to_value(&finding).expect("serialise finding");
        assert_eq!(
            encoded["extensions"]["reviewer.raw"],
            json!(["line one", "line two"])
        );
    }

    #[test]
    fn older_run_completed_payloads_default_to_unknown_run_kind() {
        let payload = json!({
            "contract_version": { "major": 1, "minor": 0 },
            "id": "legacy-run-completed",
            "payload": {
                "event": "run_completed",
                "run_id": "run-review-1",
                "outcome": "succeeded"
            }
        });

        let event = serde_json::from_value::<ContractEvent>(payload).expect("legacy event");

        assert!(matches!(
            event.payload,
            EventPayload::RunCompleted {
                run_kind: None,
                outcome: RunOutcome::Succeeded,
                ..
            }
        ));
    }

    #[test]
    fn older_forge_facts_default_to_ready_pull_request() {
        let mut payload = serde_json::to_value(forge_facts()).expect("serialise facts");
        payload
            .as_object_mut()
            .expect("facts are an object")
            .remove("work_in_progress");

        let facts = serde_json::from_value::<ForgeFacts>(payload).expect("legacy facts");

        assert!(!facts.work_in_progress);
    }

    #[test]
    fn older_run_state_payloads_default_to_empty_loop_controls() {
        let payload = json!({
            "contract_version": { "major": 1, "minor": 1 },
            "pr": { "repository": "acme/widgets", "id": "42" },
            "commit_sha": "abc123",
            "pass_index": 1,
            "status": "completed",
            "findings": [],
            "decisions": [],
            "patches": [],
            "ceiling": null
        });

        let state = serde_json::from_value::<super::PrRunState>(payload).expect("legacy state");

        assert_eq!(state.current_head_sha, None);
        assert_eq!(state.active_run, None);
        assert!(state.run_history.is_empty());
        assert!(state.loop_history.is_empty());
        assert_eq!(state.superseded_by, None);
        assert!(state.publication.is_empty());
        assert!(
            state
                .run_history
                .iter()
                .all(|record| record.refusal.is_none() && record.ensemble_archive_path.is_none())
        );
    }

    #[test]
    fn v1_2_run_state_payloads_default_to_empty_publication_state() {
        let payload = json!({
            "contract_version": { "major": 1, "minor": 2 },
            "pr": { "repository": "acme/widgets", "id": "42" },
            "commit_sha": "abc123",
            "current_head_sha": "abc123",
            "pass_index": 2,
            "status": "completed",
            "active_run": null,
            "run_history": [],
            "loop_history": [],
            "superseded_by": null,
            "findings": [],
            "decisions": [],
            "patches": [],
            "ceiling": null
        });

        let state = serde_json::from_value::<super::PrRunState>(payload).expect("v1.2 state");

        assert_eq!(
            state.contract_version,
            ContractVersion { major: 1, minor: 2 }
        );
        assert_eq!(state.current_head_sha.as_deref(), Some("abc123"));
        assert!(state.publication.is_empty());
        assert!(
            state
                .run_history
                .iter()
                .all(|record| record.refusal.is_none() && record.ensemble_archive_path.is_none())
        );
    }

    #[test]
    fn older_publication_attempt_payloads_default_new_fields() {
        let payload = json!({
            "contract_version": { "major": 1, "minor": 3 },
            "pr": { "repository": "acme/widgets", "id": "42" },
            "commit_sha": "abc123",
            "current_head_sha": "abc123",
            "pass_index": 2,
            "status": "completed",
            "active_run": null,
            "run_history": [],
            "loop_history": [],
            "superseded_by": null,
            "findings": [],
            "decisions": [],
            "patches": [],
            "publication": {
                "attempts": [{
                    "contract_version": { "major": 1, "minor": 3 },
                    "run_id": "run-judge-2",
                    "operation": {
                        "kind": "post_finding_comment",
                        "finding_id": "finding-1",
                        "finding_dedup_key": "brief:correctness:path:src/lib.rs:12"
                    },
                    "idempotency_key": "comment-finding-1",
                    "status": "succeeded",
                    "receipt": {
                        "operation_id": "comment-1",
                        "idempotency_key": "comment-finding-1",
                        "new_head_sha": null
                    },
                    "error": null
                }],
                "finding_comments": [],
                "fix_pushes": [],
                "merges": []
            },
            "ceiling": null
        });

        let state = serde_json::from_value::<super::PrRunState>(payload).expect("v1.3 state");
        let attempt = state
            .publication
            .attempts
            .first()
            .expect("publication attempt");

        assert_eq!(attempt.expected_head_sha, None);
        assert_eq!(attempt.refusal, None);
    }

    #[test]
    fn soundness_predicates_are_expressible_from_contract_types() {
        let reviewers = vec![
            verified_provenance("codex-reviewer", AgentRole::Reviewer, "codex"),
            verified_provenance("claude-reviewer", AgentRole::Reviewer, "claude"),
        ];
        let fixer = verified_provenance("fixer", AgentRole::Fixer, "codex");
        let judge = verified_provenance("judge", AgentRole::Judge, "glm");
        let mut all_provenance = reviewers.clone();
        all_provenance.push(fixer);
        all_provenance.push(judge.clone());

        assert!(has_two_verified_reviewer_families(&all_provenance));
        assert!(reviewers_disjoint_from_fixers(&all_provenance));
        assert!(judge_independent_of_reviewers(&judge, &reviewers));
        assert!(sessions_fresh_for_pass(&all_provenance, 1));
        assert!(merge_gate_clean_and_current(&forge_facts()));
    }

    #[test]
    fn unverified_provenance_does_not_count_as_a_reviewer_family() {
        let unverified = ModelProvenance {
            contract_version: version(),
            agent_id: AgentId("unknown-reviewer".to_owned()),
            role: AgentRole::Reviewer,
            session_id: SessionId("unknown-session".to_owned()),
            freshness: SessionFreshness::Unknown {
                reason: "adapter did not provide launch proof".to_owned(),
            },
            verification: ProvenanceVerification::Unverified {
                reason: "core could not verify provider lineage".to_owned(),
            },
            extensions: extensions(),
        };
        let provenances = vec![
            verified_provenance("codex-reviewer", AgentRole::Reviewer, "codex"),
            unverified,
        ];

        assert!(!has_two_verified_reviewer_families(&provenances));
    }
}
