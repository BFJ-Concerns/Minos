use pump19_core::{CoreError, EventSource};
use thiserror::Error;

use crate::{
    ForgejoActivity, ForgejoNormalisationConfig, NormalisationError, normalise::contract_event,
};

/// Source of raw Forgejo activity for the adapter.
pub trait ForgejoActivitySource {
    /// Returns the next Forgejo activity, or `None` when the source has no work.
    ///
    /// # Errors
    ///
    /// Returns an error when the backing Forgejo source cannot be read.
    fn next_activity(&mut self) -> Result<Option<ForgejoActivity>, ForgejoActivityError>;
}

/// Errors raised before Forgejo activity reaches contract normalisation.
#[derive(Debug, Error)]
pub enum ForgejoActivityError {
    #[error("Forgejo activity source failed: {0}")]
    Source(String),
}

/// `EventSource` implementation that normalises Forgejo activity into the contract.
#[derive(Clone, Debug)]
pub struct ForgejoEventSource<S> {
    source: S,
    config: ForgejoNormalisationConfig,
}

impl<S> ForgejoEventSource<S> {
    #[must_use]
    pub const fn new(source: S, config: ForgejoNormalisationConfig) -> Self {
        Self { source, config }
    }

    #[must_use]
    pub const fn source(&self) -> &S {
        &self.source
    }
}

impl<S> EventSource for ForgejoEventSource<S>
where
    S: ForgejoActivitySource,
{
    fn next_event(&mut self) -> Result<Option<pump19_contract::ContractEvent>, CoreError> {
        loop {
            let Some(activity) = self
                .source
                .next_activity()
                .map_err(|source| CoreError::EventSource(source.to_string()))?
            else {
                return Ok(None);
            };
            let event = contract_event(&self.config, &activity)
                .map_err(|error| normalisation_error_to_core_error(&error))?;
            if event.is_some() {
                return Ok(event);
            }
        }
    }
}

fn normalisation_error_to_core_error(error: &NormalisationError) -> CoreError {
    CoreError::EventSource(error.to_string())
}

#[cfg(test)]
mod tests {
    use std::collections::VecDeque;

    use pump19_contract::EventPayload;
    use pump19_core::EventSource;

    use super::*;
    use crate::{
        ForgejoLabelApplication, ForgejoMergeability, ForgejoPullRequestSnapshot,
        ForgejoReviewCleanliness,
    };

    #[derive(Debug)]
    struct FakeActivitySource {
        activities: VecDeque<ForgejoActivity>,
    }

    impl ForgejoActivitySource for FakeActivitySource {
        fn next_activity(&mut self) -> Result<Option<ForgejoActivity>, ForgejoActivityError> {
            Ok(self.activities.pop_front())
        }
    }

    #[test]
    fn event_source_skips_irrelevant_labels_and_returns_next_contract_event() {
        let source = FakeActivitySource {
            activities: VecDeque::from([
                ForgejoActivity::LabelApplied {
                    event_id: "label-1".to_owned(),
                    repository: "acme/widgets".to_owned(),
                    pr_id: "42".to_owned(),
                    label: ForgejoLabelApplication {
                        name: "not-finish".to_owned(),
                        applied_by: None,
                    },
                },
                ForgejoActivity::PullRequestUpdated {
                    event_id: "pr-2".to_owned(),
                    snapshot: ForgejoPullRequestSnapshot {
                        repository: "acme/widgets".to_owned(),
                        id: "42".to_owned(),
                        head_sha: "abc123".to_owned(),
                        base_sha: "def456".to_owned(),
                        branch_currency: crate::ForgejoBranchCurrency::Current,
                        cleanliness: ForgejoReviewCleanliness::Dirty,
                        mergeability: ForgejoMergeability::Unknown,
                        labels: Vec::new(),
                        actor_permissions: Vec::new(),
                    },
                },
            ]),
        };
        let mut event_source =
            ForgejoEventSource::new(source, ForgejoNormalisationConfig::new("pump19-finish"));

        let event = event_source
            .next_event()
            .expect("source succeeds")
            .expect("contract event");

        assert!(matches!(
            event.payload,
            EventPayload::PullRequestUpdated { .. }
        ));
    }
}
