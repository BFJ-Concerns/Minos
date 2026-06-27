#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "unit tests use compact fixtures and direct assertions"
    )
)]

pub mod credentialed;
pub mod normalise;
pub mod operations;
pub mod source;

pub use credentialed::{
    ForgejoClientError, ForgejoCommandClient, ForgejoCommandMetadata, ForgejoCommandReceipt,
    ForgejoCredential,
};
pub use normalise::{
    ForgejoActivity, ForgejoActor, ForgejoActorPermission, ForgejoBranchCurrency,
    ForgejoLabelApplication, ForgejoMergeability, ForgejoNormalisationConfig,
    ForgejoPullRequestSnapshot, ForgejoReviewCleanliness, NormalisationError, contract_event,
    forge_facts,
};
pub use operations::{
    AuthorisationContext, AuthorisationEvidence, AuthorisedComment, AuthorisedLabel,
    AuthorisedMerge, ForgeOperationError, ForgeOperationReceipt, ForgeOperations,
    ForgejoForgeOperations, MergeMethod,
};
pub use source::{ForgejoActivitySource, ForgejoEventSource};
