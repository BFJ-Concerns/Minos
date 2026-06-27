use pump19_contract::{ActorCapability, ActorRef, DecisionVerdict, FinishLabel, PullRequestRef};
use thiserror::Error;

use crate::credentialed::{
    ForgejoClientError, ForgejoCommandClient, ForgejoCommandMetadata, ForgejoCommandReceipt,
};

/// Authorisation evidence the core passes with an already-approved forge operation.
///
/// The adapter records and shape-validates this context; it must not reinterpret
/// the evidence as permission to decide whether the operation is allowed.
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

/// Merge method requested from Forgejo.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum MergeMethod {
    Merge,
    Squash,
    Rebase,
}

impl MergeMethod {
    #[must_use]
    pub const fn as_forgejo_str(self) -> &'static str {
        match self {
            Self::Merge => "merge",
            Self::Squash => "squash",
            Self::Rebase => "rebase",
        }
    }
}

/// Receipt returned by an outbound forge operation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgeOperationReceipt {
    pub operation_id: String,
    pub idempotency_key: String,
}

/// Proposed core-facing seam for executing already-authorised forge side effects.
pub trait ForgeOperations {
    /// Posts an authorised PR comment.
    ///
    /// # Errors
    ///
    /// Returns an error when authorisation context is malformed or Forgejo rejects
    /// the operation.
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Applies an authorised PR label.
    ///
    /// # Errors
    ///
    /// Returns an error when authorisation context is malformed or Forgejo rejects
    /// the operation.
    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;

    /// Merges an authorised PR.
    ///
    /// # Errors
    ///
    /// Returns an error when authorisation context is malformed or Forgejo rejects
    /// the operation.
    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError>;
}

/// Errors raised by the outbound operation seam.
#[derive(Debug, Error)]
pub enum ForgeOperationError {
    #[error("authorised forge operation has invalid shape: {0}")]
    InvalidRequest(&'static str),
    #[error("credentialed Forgejo client failed: {0}")]
    Client(#[from] ForgejoClientError),
}

/// Forgejo implementation of the proposed `ForgeOperations` seam.
#[derive(Clone, Debug)]
pub struct ForgejoForgeOperations<C> {
    client: C,
}

impl<C> ForgejoForgeOperations<C> {
    #[must_use]
    pub const fn new(client: C) -> Self {
        Self { client }
    }

    #[must_use]
    pub const fn client(&self) -> &C {
        &self.client
    }
}

impl<C> ForgeOperations for ForgejoForgeOperations<C>
where
    C: ForgejoCommandClient,
{
    fn post_comment(
        &mut self,
        request: AuthorisedComment,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        validate_authorisation(&request.authorisation)?;
        validate_non_empty(&request.body, "comment body is empty")?;
        let receipt = self.client.post_pr_comment(
            &request.authorisation.pr,
            &request.body,
            &metadata(&request.authorisation),
        )?;
        Ok(receipt_for(receipt, &request.authorisation))
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        validate_authorisation(&request.authorisation)?;
        validate_non_empty(&request.label, "label is empty")?;
        let receipt = self.client.apply_pr_label(
            &request.authorisation.pr,
            &request.label,
            &metadata(&request.authorisation),
        )?;
        Ok(receipt_for(receipt, &request.authorisation))
    }

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        validate_authorisation(&request.authorisation)?;
        let receipt = self.client.merge_pr(
            &request.authorisation.pr,
            request.method.as_forgejo_str(),
            &metadata(&request.authorisation),
        )?;
        Ok(receipt_for(receipt, &request.authorisation))
    }
}

fn validate_authorisation(context: &AuthorisationContext) -> Result<(), ForgeOperationError> {
    validate_non_empty(&context.pr.repository, "PR repository is empty")?;
    validate_non_empty(&context.pr.id, "PR id is empty")?;
    validate_non_empty(&context.observed_head_sha, "observed head SHA is empty")?;
    validate_non_empty(&context.idempotency_key, "idempotency key is empty")?;
    validate_non_empty(&context.actor.id, "authorised actor id is empty")?;
    validate_non_empty(&context.reason, "authorisation reason is empty")?;
    if context.evidence.is_empty() {
        return Err(ForgeOperationError::InvalidRequest(
            "authorisation evidence is empty",
        ));
    }
    Ok(())
}

const fn validate_non_empty(value: &str, reason: &'static str) -> Result<(), ForgeOperationError> {
    if value.is_empty() {
        return Err(ForgeOperationError::InvalidRequest(reason));
    }
    Ok(())
}

fn metadata(context: &AuthorisationContext) -> ForgejoCommandMetadata {
    ForgejoCommandMetadata {
        observed_head_sha: context.observed_head_sha.clone(),
        idempotency_key: context.idempotency_key.clone(),
        reason: context.reason.clone(),
    }
}

fn receipt_for(
    receipt: ForgejoCommandReceipt,
    context: &AuthorisationContext,
) -> ForgeOperationReceipt {
    ForgeOperationReceipt {
        operation_id: receipt.operation_id,
        idempotency_key: context.idempotency_key.clone(),
    }
}

#[cfg(test)]
mod tests {
    use pump19_contract::ActorCapability;

    use super::*;

    #[derive(Debug, Default)]
    struct FakeClient {
        calls: Vec<String>,
        metadata: Vec<ForgejoCommandMetadata>,
    }

    impl ForgejoCommandClient for FakeClient {
        fn post_pr_comment(
            &mut self,
            pr: &PullRequestRef,
            body: &str,
            metadata: &ForgejoCommandMetadata,
        ) -> Result<ForgejoCommandReceipt, ForgejoClientError> {
            self.calls
                .push(format!("comment:{}:{}:{body}", pr.repository, pr.id));
            self.metadata.push(metadata.clone());
            Ok(ForgejoCommandReceipt {
                operation_id: "comment-1".to_owned(),
            })
        }

        fn apply_pr_label(
            &mut self,
            pr: &PullRequestRef,
            label: &str,
            metadata: &ForgejoCommandMetadata,
        ) -> Result<ForgejoCommandReceipt, ForgejoClientError> {
            self.calls
                .push(format!("label:{}:{}:{label}", pr.repository, pr.id));
            self.metadata.push(metadata.clone());
            Ok(ForgejoCommandReceipt {
                operation_id: "label-1".to_owned(),
            })
        }

        fn merge_pr(
            &mut self,
            pr: &PullRequestRef,
            method: &str,
            metadata: &ForgejoCommandMetadata,
        ) -> Result<ForgejoCommandReceipt, ForgejoClientError> {
            self.calls
                .push(format!("merge:{}:{}:{method}", pr.repository, pr.id));
            self.metadata.push(metadata.clone());
            Ok(ForgejoCommandReceipt {
                operation_id: "merge-1".to_owned(),
            })
        }
    }

    fn actor() -> ActorRef {
        ActorRef {
            id: "core".to_owned(),
            display_name: "Pump-19 Core".to_owned(),
        }
    }

    fn authorisation() -> AuthorisationContext {
        AuthorisationContext {
            pr: PullRequestRef {
                repository: "acme/widgets".to_owned(),
                id: "42".to_owned(),
            },
            observed_head_sha: "abc123".to_owned(),
            idempotency_key: "run-1:comment:finding-1".to_owned(),
            actor: actor(),
            reason: "core authorised operation from contract facts".to_owned(),
            evidence: vec![AuthorisationEvidence::ActorCapability {
                actor: actor(),
                capability: ActorCapability::Merge,
            }],
        }
    }

    #[test]
    fn authorised_merge_executes_with_core_context_but_no_redecision() {
        let client = FakeClient::default();
        let mut operations = ForgejoForgeOperations::new(client);

        let receipt = operations
            .merge(AuthorisedMerge {
                authorisation: authorisation(),
                method: MergeMethod::Squash,
            })
            .expect("merge executes");

        assert_eq!(receipt.operation_id, "merge-1");
        assert_eq!(receipt.idempotency_key, "run-1:comment:finding-1");
        assert_eq!(
            operations.client().calls,
            vec!["merge:acme/widgets:42:squash"]
        );
        assert_eq!(operations.client().metadata[0].observed_head_sha, "abc123");
        assert_eq!(
            operations.client().metadata[0].reason,
            "core authorised operation from contract facts"
        );
    }

    #[test]
    fn malformed_authorisation_is_rejected_before_credentialed_call() {
        let client = FakeClient::default();
        let mut operations = ForgejoForgeOperations::new(client);
        let mut authorisation = authorisation();
        authorisation.evidence.clear();

        let error = operations
            .apply_label(AuthorisedLabel {
                authorisation,
                label: "pump19-finish".to_owned(),
            })
            .expect_err("evidence is required");

        assert!(matches!(error, ForgeOperationError::InvalidRequest(_)));
        assert!(operations.client().calls.is_empty());
    }

    #[test]
    fn authorised_comment_carries_idempotency_to_credentialed_client() {
        let client = FakeClient::default();
        let mut operations = ForgejoForgeOperations::new(client);

        let receipt = operations
            .post_comment(AuthorisedComment {
                authorisation: authorisation(),
                body: "material finding".to_owned(),
            })
            .expect("comment executes");

        assert_eq!(receipt.operation_id, "comment-1");
        assert_eq!(
            operations.client().metadata[0].idempotency_key,
            "run-1:comment:finding-1"
        );
    }
}
