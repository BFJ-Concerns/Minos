use std::collections::{BTreeMap, VecDeque};

use pump19_core::{CoreError, EventSource};
use thiserror::Error;

use crate::{
    ForgejoActivity, ForgejoLabelApplication, ForgejoNormalisationConfig,
    ForgejoPullRequestSnapshot, NormalisationError, normalise::contract_event,
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

/// Polling client used by the Forgejo activity source.
pub trait ForgejoPollingClient {
    /// Lists currently open pull requests for one repository.
    ///
    /// # Errors
    ///
    /// Returns an error when the Forgejo API cannot be reached or decoded.
    fn open_pull_requests(
        &mut self,
        repository: &str,
    ) -> Result<Vec<ForgejoPullRequestSnapshot>, ForgejoActivityError>;
}

/// Errors raised before Forgejo activity reaches contract normalisation.
#[derive(Debug, Error)]
pub enum ForgejoActivityError {
    #[error("Forgejo activity source failed: {0}")]
    Source(String),
}

/// Polling configuration for a single-tenant Forgejo installation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgejoPollingConfig {
    pub repositories: Vec<String>,
    pub finish_label: String,
}

impl ForgejoPollingConfig {
    #[must_use]
    pub fn new(repositories: Vec<String>, finish_label: impl Into<String>) -> Self {
        Self {
            repositories,
            finish_label: finish_label.into(),
        }
    }
}

/// Polling-backed activity source.
///
/// The source emits contract-shapable activity only when a PR first appears, its
/// head/facts change, or the configured finish label newly appears. Webhook
/// ingress can implement `ForgejoActivitySource` alongside this without changing
/// the normalisation or core dispatch seams.
#[derive(Clone, Debug)]
pub struct PollingForgejoActivitySource<C> {
    client: C,
    config: ForgejoPollingConfig,
    observed: BTreeMap<String, ObservedPullRequest>,
    pending: VecDeque<ForgejoActivity>,
}

impl<C> PollingForgejoActivitySource<C> {
    #[must_use]
    pub const fn new(client: C, config: ForgejoPollingConfig) -> Self {
        Self {
            client,
            config,
            observed: BTreeMap::new(),
            pending: VecDeque::new(),
        }
    }
}

impl<C> ForgejoActivitySource for PollingForgejoActivitySource<C>
where
    C: ForgejoPollingClient,
{
    fn next_activity(&mut self) -> Result<Option<ForgejoActivity>, ForgejoActivityError> {
        if let Some(activity) = self.pending.pop_front() {
            return Ok(Some(activity));
        }
        self.poll_once()?;
        Ok(self.pending.pop_front())
    }
}

impl<C> PollingForgejoActivitySource<C>
where
    C: ForgejoPollingClient,
{
    fn poll_once(&mut self) -> Result<(), ForgejoActivityError> {
        for repository in self.config.repositories.clone() {
            let snapshots = self.client.open_pull_requests(&repository)?;
            for snapshot in snapshots {
                self.record_snapshot(snapshot);
            }
        }
        Ok(())
    }

    fn record_snapshot(&mut self, snapshot: ForgejoPullRequestSnapshot) {
        let key = observed_key(&snapshot);
        let next = ObservedPullRequest::from_snapshot(&self.config.finish_label, &snapshot);
        let previous = self.observed.insert(key, next.clone());
        match &previous {
            None => self.pending.push_back(ForgejoActivity::PullRequestOpened {
                event_id: poll_event_id("opened", &snapshot),
                snapshot: snapshot.clone(),
            }),
            Some(previous) if previous.fingerprint != next.fingerprint => {
                self.pending.push_back(ForgejoActivity::PullRequestUpdated {
                    event_id: poll_event_id("updated", &snapshot),
                    snapshot: snapshot.clone(),
                });
            }
            Some(_) => {}
        }
        if previous
            .as_ref()
            .is_none_or(|previous| previous.finish_label.is_none())
            && let Some(label) = next.finish_label
        {
            self.pending.push_back(ForgejoActivity::LabelApplied {
                event_id: format!(
                    "forgejo-poll:label:{}:{}:{}",
                    snapshot.repository, snapshot.id, snapshot.head_sha
                ),
                repository: snapshot.repository,
                pr_id: snapshot.id,
                label,
            });
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct ObservedPullRequest {
    fingerprint: String,
    finish_label: Option<ForgejoLabelApplication>,
}

impl ObservedPullRequest {
    fn from_snapshot(finish_label: &str, snapshot: &ForgejoPullRequestSnapshot) -> Self {
        Self {
            fingerprint: snapshot_fingerprint(snapshot),
            finish_label: snapshot
                .labels
                .iter()
                .rev()
                .find(|label| label.name == finish_label)
                .cloned(),
        }
    }
}

fn observed_key(snapshot: &ForgejoPullRequestSnapshot) -> String {
    format!("{}#{}", snapshot.repository, snapshot.id)
}

fn poll_event_id(kind: &str, snapshot: &ForgejoPullRequestSnapshot) -> String {
    format!(
        "forgejo-poll:{kind}:{}:{}:{}",
        snapshot.repository, snapshot.id, snapshot.head_sha
    )
}

fn snapshot_fingerprint(snapshot: &ForgejoPullRequestSnapshot) -> String {
    serde_json::to_string(snapshot).unwrap_or_else(|_error| {
        format!(
            "{}:{}:{}:{}",
            snapshot.repository, snapshot.id, snapshot.head_sha, snapshot.base_sha
        )
    })
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
        ForgejoActor, ForgejoBranchCurrency, ForgejoLabelApplication, ForgejoMergeability,
        ForgejoPullRequestSnapshot, ForgejoReviewCleanliness,
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

    #[derive(Debug)]
    struct FakePollingClient {
        polls: VecDeque<Vec<ForgejoPullRequestSnapshot>>,
    }

    impl ForgejoPollingClient for FakePollingClient {
        fn open_pull_requests(
            &mut self,
            _repository: &str,
        ) -> Result<Vec<ForgejoPullRequestSnapshot>, ForgejoActivityError> {
            Ok(self.polls.pop_front().unwrap_or_default())
        }
    }

    fn snapshot(
        head_sha: &str,
        labels: Vec<ForgejoLabelApplication>,
    ) -> ForgejoPullRequestSnapshot {
        ForgejoPullRequestSnapshot {
            repository: "acme/widgets".to_owned(),
            id: "42".to_owned(),
            head_sha: head_sha.to_owned(),
            base_sha: "def456".to_owned(),
            branch_currency: ForgejoBranchCurrency::Current,
            cleanliness: ForgejoReviewCleanliness::Dirty,
            mergeability: ForgejoMergeability::Unknown,
            labels,
            actor_permissions: Vec::new(),
        }
    }

    fn finish_label() -> ForgejoLabelApplication {
        ForgejoLabelApplication {
            name: "pump19-finish".to_owned(),
            applied_by: Some(ForgejoActor {
                id: "core".to_owned(),
                display_name: "Pump-19 Core".to_owned(),
            }),
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

    #[test]
    fn polling_source_emits_open_update_and_new_finish_label_once() {
        let client = FakePollingClient {
            polls: VecDeque::from([
                vec![snapshot("abc123", Vec::new())],
                vec![snapshot("abc123", Vec::new())],
                vec![snapshot("def456", vec![finish_label()])],
                vec![snapshot("def456", vec![finish_label()])],
            ]),
        };
        let source = PollingForgejoActivitySource::new(
            client,
            ForgejoPollingConfig::new(vec!["acme/widgets".to_owned()], "pump19-finish"),
        );
        let mut events =
            ForgejoEventSource::new(source, ForgejoNormalisationConfig::new("pump19-finish"));

        let opened = events
            .next_event()
            .expect("poll succeeds")
            .expect("opened event");
        assert!(matches!(
            opened.payload,
            EventPayload::PullRequestOpened { .. }
        ));
        assert_eq!(events.next_event().expect("quiet poll"), None);
        let updated = events
            .next_event()
            .expect("update poll succeeds")
            .expect("updated event");
        assert!(matches!(
            updated.payload,
            EventPayload::PullRequestUpdated { .. }
        ));
        let labelled = events
            .next_event()
            .expect("pending label succeeds")
            .expect("label event");
        assert!(matches!(
            labelled.payload,
            EventPayload::LabelApplied { .. }
        ));
        assert_eq!(events.next_event().expect("label is not replayed"), None);
    }
}
