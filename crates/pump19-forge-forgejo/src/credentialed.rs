use pump19_contract::{PatchChange, PatchId, PullRequestRef};
use serde::Serialize;
use thiserror::Error;

/// Forgejo API credential owned by the adapter process.
///
/// This type deliberately exposes no accessors beyond construction. The adapter
/// can carry it into a real HTTP client later; PR workspaces never need this type.
#[derive(Clone, Eq, PartialEq)]
pub struct ForgejoCredential {
    token: String,
}

impl ForgejoCredential {
    #[must_use]
    pub fn new(token: impl Into<String>) -> Self {
        Self {
            token: token.into(),
        }
    }
}

impl std::fmt::Debug for ForgejoCredential {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("ForgejoCredential")
            .field("token", &"<redacted>")
            .finish()
    }
}

/// Metadata the core-authorised operation path passes to Forgejo writes.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ForgejoCommandMetadata {
    pub observed_head_sha: String,
    pub expected_head_sha: Option<String>,
    pub idempotency_key: String,
    pub reason: String,
}

/// Forge-side receipt returned after an operation has been accepted or completed.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgejoCommandReceipt {
    pub operation_id: String,
    pub new_head_sha: Option<String>,
}

/// One fix commit the credentialed client should create and push to the PR head.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ForgejoFixCommit {
    pub patch_id: PatchId,
    pub message: String,
    pub author_agent_id: String,
    pub model_provenance_json: String,
    pub change: PatchChange,
}

/// Errors from the credentialed Forgejo client boundary.
#[derive(Debug, Error)]
pub enum ForgejoClientError {
    #[error("Forgejo transport failed: {0}")]
    Transport(String),
    #[error("Forgejo rejected the operation: {0}")]
    Rejected(String),
}

/// Minimal credentialed command surface needed by authorised forge operations.
///
/// A concrete HTTP implementation belongs behind this trait. The trait carries no
/// workspace path or PR-content execution hook, so the token-bearing path remains
/// separate from untrusted code execution.
pub trait ForgejoCommandClient {
    /// Posts a PR comment if the PR head still matches the expected head in
    /// `metadata`.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request or the client cannot
    /// reach Forgejo.
    fn post_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        body: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;

    /// Updates a PR comment previously created by Pump-19 if the PR head still
    /// matches the expected head in `metadata`.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request or the client cannot
    /// reach Forgejo.
    fn update_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        comment_operation_id: &str,
        body: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;

    /// Resolves a PR comment previously created by Pump-19 if the PR head still
    /// matches the expected head in `metadata`.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request or the client cannot
    /// reach Forgejo.
    fn resolve_pr_comment(
        &mut self,
        pr: &PullRequestRef,
        comment_operation_id: &str,
        reason: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;

    /// Applies a label to a PR.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request or the client cannot
    /// reach Forgejo.
    fn apply_pr_label(
        &mut self,
        pr: &PullRequestRef,
        label: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;

    /// Merges a PR at the observed head revision.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request or the client cannot
    /// reach Forgejo.
    fn merge_pr(
        &mut self,
        pr: &PullRequestRef,
        method: &str,
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;

    /// Pushes ordinary, non-force fix commits to the PR head branch.
    ///
    /// # Errors
    ///
    /// Returns an error when Forgejo rejects the request, the observed head is no
    /// longer current, or the client cannot reach Forgejo.
    fn push_fix_commits_to_pr_head(
        &mut self,
        pr: &PullRequestRef,
        expected_head_sha: &str,
        commits: &[ForgejoFixCommit],
        metadata: &ForgejoCommandMetadata,
    ) -> Result<ForgejoCommandReceipt, ForgejoClientError>;
}
