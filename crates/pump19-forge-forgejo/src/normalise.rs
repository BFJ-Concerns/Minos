use std::collections::{BTreeMap, BTreeSet};

use pump19_contract::{
    ActorCapability, ActorPermissions, ActorRef, BranchCurrency, ContractEvent, ContractVersion,
    EventPayload, Extensions, FinishLabel, ForgeFacts, Mergeability, PullRequestRef,
    ReviewCleanliness, Revision, RunId, RunKind, RunOutcome,
};
use serde::{Deserialize, Serialize};
use serde_json::json;
use thiserror::Error;

const EXT_BRANCH_CURRENCY_EVIDENCE: &str = "pump19.forgejo.branch_currency_evidence";
const EXT_FORGEJO_EVENT_KIND: &str = "pump19.forgejo.event_kind";

/// Normalisation settings for one Forgejo-backed Pump-19 installation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForgejoNormalisationConfig {
    pub finish_label: String,
}

impl ForgejoNormalisationConfig {
    #[must_use]
    pub fn new(finish_label: impl Into<String>) -> Self {
        Self {
            finish_label: finish_label.into(),
        }
    }
}

/// Forgejo actor evidence as supplied by API payloads or timeline fixtures.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgejoActor {
    pub id: String,
    pub display_name: String,
}

impl ForgejoActor {
    #[must_use]
    pub fn actor_ref(&self) -> ActorRef {
        ActorRef {
            id: self.id.clone(),
            display_name: self.display_name.clone(),
        }
    }
}

/// Permission evidence for a Forgejo actor at the PR/repository boundary.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgejoActorPermission {
    pub actor: ForgejoActor,
    #[serde(default)]
    pub can_apply_finish_label: bool,
    #[serde(default)]
    pub can_merge: bool,
}

/// A label application observed in Forgejo's issue/PR timeline.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgejoLabelApplication {
    pub name: String,
    pub applied_by: Option<ForgejoActor>,
}

/// Evidence that the PR head was judged against the current base revision.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ForgejoBranchCurrency {
    Current,
    Stale,
    Unknown,
}

/// Forgejo mergeability evidence normalised into the contract vocabulary.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ForgejoMergeability {
    Mergeable,
    Conflicting,
    Unknown,
}

/// Review cleanliness evidence observed by the adapter from Pump-19 state/signals.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ForgejoReviewCleanliness {
    Clean,
    Dirty,
}

/// Forgejo PR state needed to produce contract-level forge facts.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ForgejoPullRequestSnapshot {
    pub repository: String,
    pub id: String,
    /// PR title evidence used by older Forgejo/Gitea installations that encode
    /// work-in-progress state as a configurable title prefix.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// Native draft evidence from newer forge payloads. Absence means the
    /// server did not expose a draft flag, not that normalisation failed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub draft: Option<bool>,
    pub head_sha: String,
    pub base_sha: String,
    pub branch_currency: ForgejoBranchCurrency,
    pub cleanliness: ForgejoReviewCleanliness,
    pub mergeability: ForgejoMergeability,
    #[serde(default)]
    pub labels: Vec<ForgejoLabelApplication>,
    #[serde(default)]
    pub actor_permissions: Vec<ForgejoActorPermission>,
    /// The Forgejo login of the PR author, absent when the API omits it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub author_login: Option<String>,
}

/// Forgejo activity that the adapter can turn into the contract event vocabulary.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum ForgejoActivity {
    PullRequestOpened {
        event_id: String,
        snapshot: ForgejoPullRequestSnapshot,
    },
    PullRequestUpdated {
        event_id: String,
        snapshot: ForgejoPullRequestSnapshot,
    },
    LabelApplied {
        event_id: String,
        repository: String,
        pr_id: String,
        label: ForgejoLabelApplication,
    },
    RunCompleted {
        event_id: String,
        run_id: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        run_kind: Option<RunKind>,
        outcome: RunOutcome,
    },
}

/// Errors raised while converting Forgejo evidence into the contract.
#[derive(Debug, Error)]
pub enum NormalisationError {
    #[error("configured finish label is empty")]
    EmptyFinishLabel,
    #[error("Forgejo PR snapshot has an empty {field}")]
    EmptySnapshotField { field: &'static str },
    #[error("finish label {label:?} was applied but Forgejo did not identify who applied it")]
    MissingFinishLabelActor { label: String },
}

/// Converts Forgejo PR state into forge-neutral facts for the core.
///
/// # Errors
///
/// Returns an error when required PR identity/revision fields are empty, or when
/// the configured finish label is present without reliable actor evidence.
pub fn forge_facts(
    config: &ForgejoNormalisationConfig,
    snapshot: &ForgejoPullRequestSnapshot,
) -> Result<ForgeFacts, NormalisationError> {
    validate_config(config)?;
    validate_snapshot(snapshot)?;

    let mut extensions = Extensions::new();
    let branch_currency = match snapshot.branch_currency {
        ForgejoBranchCurrency::Current => BranchCurrency::Current,
        ForgejoBranchCurrency::Stale => BranchCurrency::Stale,
        ForgejoBranchCurrency::Unknown => {
            extensions.insert(
                EXT_BRANCH_CURRENCY_EVIDENCE.to_owned(),
                json!("unknown_treated_as_stale"),
            );
            BranchCurrency::Stale
        }
    };

    Ok(ForgeFacts {
        contract_version: ContractVersion::current(),
        pr: PullRequestRef {
            repository: snapshot.repository.clone(),
            id: snapshot.id.clone(),
        },
        head: Revision {
            sha: snapshot.head_sha.clone(),
        },
        base: Revision {
            sha: snapshot.base_sha.clone(),
        },
        branch_currency,
        cleanliness: map_cleanliness(snapshot.cleanliness),
        mergeability: map_mergeability(snapshot.mergeability),
        finish_label: finish_label(config, &snapshot.labels)?,
        actor_permissions: actor_permissions(&snapshot.actor_permissions),
        author_login: snapshot.author_login.clone(),
        work_in_progress: snapshot_work_in_progress(snapshot),
        extensions,
    })
}

fn snapshot_work_in_progress(snapshot: &ForgejoPullRequestSnapshot) -> bool {
    snapshot.draft == Some(true)
        || snapshot
            .title
            .as_deref()
            .is_some_and(title_has_work_in_progress_prefix)
}

fn title_has_work_in_progress_prefix(title: &str) -> bool {
    let title = title.trim_start();
    ascii_case_insensitive_starts_with(title, "WIP:")
        || ascii_case_insensitive_starts_with(title, "[WIP]")
}

fn ascii_case_insensitive_starts_with(value: &str, prefix: &str) -> bool {
    value
        .get(..prefix.len())
        .is_some_and(|candidate| candidate.eq_ignore_ascii_case(prefix))
}

/// Converts a Forgejo activity into a contract event.
///
/// Non-finish-label applications return `Ok(None)` because the current contract
/// event vocabulary only needs the finish label for run criteria.
///
/// # Errors
///
/// Returns an error when the activity lacks required contract evidence.
pub fn contract_event(
    config: &ForgejoNormalisationConfig,
    activity: &ForgejoActivity,
) -> Result<Option<ContractEvent>, NormalisationError> {
    validate_config(config)?;
    match activity {
        ForgejoActivity::PullRequestOpened { event_id, snapshot } => {
            let mut extensions = event_extensions("pull_request_opened");
            Ok(Some(ContractEvent {
                contract_version: ContractVersion::current(),
                id: event_id.clone(),
                payload: EventPayload::PullRequestOpened {
                    facts: forge_facts(config, snapshot)?,
                },
                extensions: std::mem::take(&mut extensions),
            }))
        }
        ForgejoActivity::PullRequestUpdated { event_id, snapshot } => {
            let mut extensions = event_extensions("pull_request_updated");
            Ok(Some(ContractEvent {
                contract_version: ContractVersion::current(),
                id: event_id.clone(),
                payload: EventPayload::PullRequestUpdated {
                    facts: forge_facts(config, snapshot)?,
                },
                extensions: std::mem::take(&mut extensions),
            }))
        }
        ForgejoActivity::LabelApplied {
            event_id,
            repository,
            pr_id,
            label,
        } => {
            if label.name != config.finish_label {
                return Ok(None);
            }
            let applied_by = label.applied_by.as_ref().ok_or_else(|| {
                NormalisationError::MissingFinishLabelActor {
                    label: label.name.clone(),
                }
            })?;
            Ok(Some(ContractEvent {
                contract_version: ContractVersion::current(),
                id: event_id.clone(),
                payload: EventPayload::LabelApplied {
                    pr: PullRequestRef {
                        repository: repository.clone(),
                        id: pr_id.clone(),
                    },
                    label: FinishLabel {
                        name: label.name.clone(),
                        applied_by: applied_by.actor_ref(),
                    },
                },
                extensions: event_extensions("label_applied"),
            }))
        }
        ForgejoActivity::RunCompleted {
            event_id,
            run_id,
            run_kind,
            outcome,
        } => Ok(Some(ContractEvent {
            contract_version: ContractVersion::current(),
            id: event_id.clone(),
            payload: EventPayload::RunCompleted {
                run_id: RunId(run_id.clone()),
                run_kind: *run_kind,
                outcome: *outcome,
            },
            extensions: event_extensions("run_completed"),
        })),
    }
}

const fn validate_config(config: &ForgejoNormalisationConfig) -> Result<(), NormalisationError> {
    if config.finish_label.is_empty() {
        return Err(NormalisationError::EmptyFinishLabel);
    }
    Ok(())
}

fn validate_snapshot(snapshot: &ForgejoPullRequestSnapshot) -> Result<(), NormalisationError> {
    for (field, value) in [
        ("repository", snapshot.repository.as_str()),
        ("id", snapshot.id.as_str()),
        ("head_sha", snapshot.head_sha.as_str()),
        ("base_sha", snapshot.base_sha.as_str()),
    ] {
        if value.is_empty() {
            return Err(NormalisationError::EmptySnapshotField { field });
        }
    }
    Ok(())
}

fn finish_label(
    config: &ForgejoNormalisationConfig,
    labels: &[ForgejoLabelApplication],
) -> Result<Option<FinishLabel>, NormalisationError> {
    let Some(label) = labels
        .iter()
        .rev()
        .find(|label| label.name == config.finish_label)
    else {
        return Ok(None);
    };
    let applied_by =
        label
            .applied_by
            .as_ref()
            .ok_or_else(|| NormalisationError::MissingFinishLabelActor {
                label: label.name.clone(),
            })?;
    Ok(Some(FinishLabel {
        name: label.name.clone(),
        applied_by: applied_by.actor_ref(),
    }))
}

fn actor_permissions(permissions: &[ForgejoActorPermission]) -> Vec<ActorPermissions> {
    permissions
        .iter()
        .map(|permission| {
            let mut capabilities = BTreeSet::new();
            if permission.can_apply_finish_label {
                capabilities.insert(ActorCapability::ApplyFinishLabel);
            }
            if permission.can_merge {
                capabilities.insert(ActorCapability::Merge);
            }
            ActorPermissions {
                actor: permission.actor.actor_ref(),
                capabilities,
            }
        })
        .collect()
}

const fn map_cleanliness(cleanliness: ForgejoReviewCleanliness) -> ReviewCleanliness {
    match cleanliness {
        ForgejoReviewCleanliness::Clean => ReviewCleanliness::Clean,
        ForgejoReviewCleanliness::Dirty => ReviewCleanliness::Dirty,
    }
}

const fn map_mergeability(mergeability: ForgejoMergeability) -> Mergeability {
    match mergeability {
        ForgejoMergeability::Mergeable => Mergeability::Mergeable,
        ForgejoMergeability::Conflicting => Mergeability::Conflicting,
        ForgejoMergeability::Unknown => Mergeability::Unknown,
    }
}

fn event_extensions(kind: &str) -> BTreeMap<String, serde_json::Value> {
    std::iter::once((EXT_FORGEJO_EVENT_KIND.to_owned(), json!(kind))).collect()
}

#[cfg(test)]
mod tests {
    use pump19_contract::{ActorCapability, BranchCurrency, EventPayload, Mergeability};

    use super::*;

    fn config() -> ForgejoNormalisationConfig {
        ForgejoNormalisationConfig::new("pump19-finish")
    }

    fn snapshot_fixture() -> ForgejoPullRequestSnapshot {
        serde_json::from_str(
            r#"{
                "repository": "acme/widgets",
                "id": "42",
                "head_sha": "abc123",
                "base_sha": "def456",
                "branch_currency": "current",
                "cleanliness": "clean",
                "mergeability": "mergeable",
                "labels": [
                    {
                        "name": "pump19-finish",
                        "applied_by": {
                            "id": "core",
                            "display_name": "Pump-19 Core"
                        }
                    }
                ],
                "actor_permissions": [
                    {
                        "actor": {
                            "id": "core",
                            "display_name": "Pump-19 Core"
                        },
                        "can_apply_finish_label": true,
                        "can_merge": true
                    }
                ]
            }"#,
        )
        .expect("fixture deserialises")
    }

    #[test]
    fn pull_request_opened_maps_to_contract_event_with_authority_facts() {
        let activity = ForgejoActivity::PullRequestOpened {
            event_id: "forgejo-event-1".to_owned(),
            snapshot: snapshot_fixture(),
        };

        let event = contract_event(&config(), &activity)
            .expect("normalise event")
            .expect("event is relevant");

        assert!(matches!(
            event.payload,
            EventPayload::PullRequestOpened { .. }
        ));
        let EventPayload::PullRequestOpened { facts } = event.payload else {
            return;
        };
        assert_eq!(facts.pr.repository, "acme/widgets");
        assert_eq!(facts.head.sha, "abc123");
        assert_eq!(facts.base.sha, "def456");
        assert_eq!(facts.branch_currency, BranchCurrency::Current);
        assert_eq!(facts.mergeability, Mergeability::Mergeable);
        assert_eq!(
            facts.finish_label.expect("finish label").applied_by.id,
            "core"
        );
        assert!(
            facts.actor_permissions[0]
                .capabilities
                .contains(&ActorCapability::ApplyFinishLabel)
        );
        assert!(
            facts.actor_permissions[0]
                .capabilities
                .contains(&ActorCapability::Merge)
        );
    }

    #[test]
    fn unknown_branch_currency_fails_closed_as_stale() {
        let mut snapshot = snapshot_fixture();
        snapshot.branch_currency = ForgejoBranchCurrency::Unknown;

        let facts = forge_facts(&config(), &snapshot).expect("normalise facts");

        assert_eq!(facts.branch_currency, BranchCurrency::Stale);
        assert_eq!(
            facts.extensions[EXT_BRANCH_CURRENCY_EVIDENCE],
            json!("unknown_treated_as_stale")
        );
    }

    #[test]
    fn work_in_progress_title_prefixes_normalise_to_contract_fact() {
        for title in [
            "WIP: still shaping this",
            "[WIP] still shaping this",
            "wip: still shaping this",
            "[wip] still shaping this",
            "WiP: still shaping this",
            "[WiP] still shaping this",
        ] {
            let mut snapshot = snapshot_fixture();
            snapshot.title = Some(title.to_owned());

            let facts = forge_facts(&config(), &snapshot).expect("normalise facts");

            assert!(facts.work_in_progress);
        }
    }

    #[test]
    fn native_draft_flag_normalises_to_contract_fact() {
        let mut snapshot = snapshot_fixture();
        snapshot.draft = Some(true);
        snapshot.title = Some("Ready-looking title".to_owned());

        let facts = forge_facts(&config(), &snapshot).expect("normalise facts");

        assert!(facts.work_in_progress);
    }

    #[test]
    fn absent_work_in_progress_evidence_means_ready() {
        let facts = forge_facts(&config(), &snapshot_fixture()).expect("normalise facts");

        assert!(!facts.work_in_progress);
    }

    #[test]
    fn finish_label_without_actor_is_rejected() {
        let mut snapshot = snapshot_fixture();
        snapshot.labels[0].applied_by = None;

        let error = forge_facts(&config(), &snapshot).expect_err("missing actor fails");

        assert!(matches!(
            error,
            NormalisationError::MissingFinishLabelActor { .. }
        ));
    }

    #[test]
    fn non_finish_label_application_is_ignored() {
        let activity = ForgejoActivity::LabelApplied {
            event_id: "forgejo-event-2".to_owned(),
            repository: "acme/widgets".to_owned(),
            pr_id: "42".to_owned(),
            label: ForgejoLabelApplication {
                name: "needs-review".to_owned(),
                applied_by: None,
            },
        };

        let event = contract_event(&config(), &activity).expect("normalise event");

        assert_eq!(event, None);
    }
}
