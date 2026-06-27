use pump19_core::{
    AuthorisationContext, AuthorisedComment, AuthorisedLabel, AuthorisedMerge, ForgeOperationError,
    ForgeOperationReceipt, ForgeOperations, MergeMethod,
};

use crate::credentialed::{ForgejoCommandClient, ForgejoCommandMetadata, ForgejoCommandReceipt};

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
        let receipt = self
            .client
            .post_pr_comment(
                &request.authorisation.pr,
                &request.body,
                &metadata(&request.authorisation),
            )
            .map_err(|error| ForgeOperationError::Client(error.to_string()))?;
        Ok(receipt_for(receipt, &request.authorisation))
    }

    fn apply_label(
        &mut self,
        request: AuthorisedLabel,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        validate_authorisation(&request.authorisation)?;
        validate_non_empty(&request.label, "label is empty")?;
        let receipt = self
            .client
            .apply_pr_label(
                &request.authorisation.pr,
                &request.label,
                &metadata(&request.authorisation),
            )
            .map_err(|error| ForgeOperationError::Client(error.to_string()))?;
        Ok(receipt_for(receipt, &request.authorisation))
    }

    fn merge(
        &mut self,
        request: AuthorisedMerge,
    ) -> Result<ForgeOperationReceipt, ForgeOperationError> {
        validate_authorisation(&request.authorisation)?;
        let receipt = self
            .client
            .merge_pr(
                &request.authorisation.pr,
                merge_method_as_forgejo_str(request.method),
                &metadata(&request.authorisation),
            )
            .map_err(|error| ForgeOperationError::Client(error.to_string()))?;
        Ok(receipt_for(receipt, &request.authorisation))
    }
}

const fn merge_method_as_forgejo_str(method: MergeMethod) -> &'static str {
    match method {
        MergeMethod::Merge => "merge",
        MergeMethod::Squash => "squash",
        MergeMethod::Rebase => "rebase",
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
    use pump19_contract::{ActorCapability, ActorRef, PullRequestRef};
    use pump19_core::AuthorisationEvidence;

    use super::*;
    use crate::ForgejoClientError;

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
