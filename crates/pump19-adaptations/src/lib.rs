#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "unit tests use compact fixtures with direct assertions"
    )
)]

use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::{Path, PathBuf},
};

use pump19_contract::{ContractVersion, Extensions, RunKind};
use pump19_core::{AgentPlan, Criteria, TriggerRule};
use pump19_judgement::{
    JudgementBrief, ReviewerConfig, default_reviewer_config, load_judgement_briefs_from_dir,
    load_reviewer_config_from_path, write_baseline_briefs_to_dir,
};
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// The first adaptation manifest schema.
pub const CURRENT_ADAPTATION_SCHEMA_VERSION: AdaptationSchemaVersion =
    AdaptationSchemaVersion { major: 1, minor: 0 };

/// A version marker for external adaptation manifest structure.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct AdaptationSchemaVersion {
    pub major: u16,
    pub minor: u16,
}

impl AdaptationSchemaVersion {
    /// Returns the version produced by this crate.
    #[must_use]
    pub const fn current() -> Self {
        CURRENT_ADAPTATION_SCHEMA_VERSION
    }
}

/// Errors raised while loading or validating adaptation units.
#[derive(Debug, Error)]
pub enum AdaptationError {
    #[error("I/O error at {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("TOML parse error at {path}: {source}")]
    ParseToml {
        path: String,
        #[source]
        source: toml::de::Error,
    },
    #[error("TOML serialise error: {0}")]
    SerialiseToml(#[from] toml::ser::Error),
    #[error("judgement adaptation failed: {0}")]
    Judgement(#[from] pump19_judgement::JudgementError),
    #[error("{unit_kind} {id:?} uses unsupported schema version {actual:?}; expected {expected:?}")]
    UnsupportedSchemaVersion {
        unit_kind: &'static str,
        id: String,
        actual: AdaptationSchemaVersion,
        expected: AdaptationSchemaVersion,
    },
    #[error("{unit_kind} {id:?} uses unsupported contract version {actual:?}; current {current:?}")]
    UnsupportedContractVersion {
        unit_kind: &'static str,
        id: String,
        actual: ContractVersion,
        current: ContractVersion,
    },
    #[error("{unit_kind} has an empty id")]
    EmptyUnitId { unit_kind: &'static str },
    #[error("{unit_kind} {id:?} has no {items}")]
    EmptyCollection {
        unit_kind: &'static str,
        id: String,
        items: &'static str,
    },
    #[error("{unit_kind} {id:?} contains duplicate id {duplicate:?}")]
    DuplicateId {
        unit_kind: &'static str,
        id: String,
        duplicate: String,
    },
    #[error("trigger rule in pack {pack_id:?} has an empty id")]
    EmptyTriggerRuleId { pack_id: String },
    #[error("trigger rule {rule_id:?} has an empty criteria group")]
    EmptyCriteriaGroup { rule_id: String },
    #[error("trigger rule {rule_id:?} has an empty agent target id")]
    EmptyAgentTargetId { rule_id: String },
    #[error("trigger rule {rule_id:?} has an empty agent model family")]
    EmptyAgentModelFamily { rule_id: String },
    #[error("prompt pack {pack_id:?} has an empty reviewer id")]
    EmptyReviewerId { pack_id: String },
    #[error("prompt pack {pack_id:?} reviewer {reviewer_id:?} has an empty model family")]
    EmptyReviewerFamily {
        pack_id: String,
        reviewer_id: String,
    },
    #[error("prompt pack {pack_id:?} reviewer {reviewer_id:?} has an empty command")]
    EmptyReviewerCommand {
        pack_id: String,
        reviewer_id: String,
    },
    #[error("prompt pack {pack_id:?} brief has an empty id")]
    EmptyBriefId { pack_id: String },
    #[error("prompt pack {pack_id:?} brief {brief_id:?} has no prompt text")]
    EmptyBriefText { pack_id: String, brief_id: String },
    #[error("prompt template in pack {pack_id:?} has an empty id")]
    EmptyPromptTemplateId { pack_id: String },
    #[error("prompt pack {pack_id:?} template {template_id:?} has no prompt text")]
    EmptyPromptTemplateText {
        pack_id: String,
        template_id: String,
    },
    #[error("mechanical step in pack {pack_id:?} has an empty id")]
    EmptyMechanicalStepId { pack_id: String },
    #[error("mechanical step {step_id:?} custom kind has an empty name")]
    EmptyMechanicalCustomKind { step_id: String },
    #[error("mechanical step {step_id:?} command has an empty program")]
    EmptyMechanicalProgram { step_id: String },
    #[error("mechanical step {step_id:?} container has an empty image")]
    EmptyMechanicalImage { step_id: String },
}

/// A versioned external prompt pack manifest.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PromptPackManifest {
    pub schema_version: AdaptationSchemaVersion,
    pub contract_version: ContractVersion,
    pub id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub prompt_templates: Vec<PromptTemplate>,
    pub reviewers_file: PathBuf,
    pub brief_dir: PathBuf,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// External prompt text for one LLM-run kind.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PromptTemplate {
    pub id: String,
    pub run_kind: RunKind,
    pub template: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Prompt content loaded from a manifest plus existing judgement TOML files.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PromptPack {
    pub manifest: PromptPackManifest,
    pub reviewers: ReviewerConfig,
    pub briefs: Vec<JudgementBrief>,
}

/// A versioned external trigger pack.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct TriggerPack {
    pub schema_version: AdaptationSchemaVersion,
    pub contract_version: ContractVersion,
    pub id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub rules: Vec<TriggerRule>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// A versioned external mechanical-step pack.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct MechanicalPack {
    pub schema_version: AdaptationSchemaVersion,
    pub contract_version: ContractVersion,
    pub id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<MechanicalStep>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// A declared trusted mechanical action the integration layer can invoke.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct MechanicalStep {
    pub id: String,
    pub kind: MechanicalStepKind,
    pub execution: MechanicalExecution,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub inputs: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub outputs: Vec<String>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// The role a mechanical action plays around a run.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "kind")]
pub enum MechanicalStepKind {
    Checkout,
    Prepare,
    FormatComments,
    Publish,
    Custom { name: String },
}

/// The executable shape for a declared mechanical action.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "executor")]
pub enum MechanicalExecution {
    Command {
        program: String,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        args: Vec<String>,
    },
    Container {
        image: String,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        args: Vec<String>,
    },
}

/// Loads a prompt pack manifest and its referenced judgement configuration.
///
/// # Errors
///
/// Returns an error when the manifest, reviewer file, or brief directory cannot
/// be read, parsed, or validated.
pub fn load_prompt_pack(manifest_path: &Path) -> Result<PromptPack, AdaptationError> {
    let manifest = read_toml::<PromptPackManifest>(manifest_path)?;
    validate_header(
        "prompt pack",
        &manifest.id,
        manifest.schema_version,
        manifest.contract_version,
    )?;

    let root = manifest_path.parent().unwrap_or_else(|| Path::new("."));
    let reviewers = load_reviewer_config_from_path(&root.join(&manifest.reviewers_file))?;
    let briefs = load_judgement_briefs_from_dir(&root.join(&manifest.brief_dir))?;
    let pack = PromptPack {
        manifest,
        reviewers,
        briefs,
    };
    validate_prompt_pack(&pack)?;
    Ok(pack)
}

/// Writes a compact baseline prompt pack into `root`.
///
/// # Errors
///
/// Returns an error when the manifest, baseline briefs, or reviewer config cannot
/// be serialised or written.
pub fn write_baseline_prompt_pack(root: &Path, id: &str) -> Result<PathBuf, AdaptationError> {
    fs::create_dir_all(root).map_err(|source| AdaptationError::Io {
        path: root.display().to_string(),
        source,
    })?;
    let brief_dir = root.join("briefs");
    write_baseline_briefs_to_dir(&brief_dir)?;
    let reviewers_file = root.join("reviewers.toml");
    write_toml(&reviewers_file, &default_reviewer_config())?;
    let manifest = PromptPackManifest {
        schema_version: AdaptationSchemaVersion::current(),
        contract_version: ContractVersion::current(),
        id: id.to_owned(),
        prompt_templates: baseline_prompt_templates(),
        reviewers_file: PathBuf::from("reviewers.toml"),
        brief_dir: PathBuf::from("briefs"),
        extensions: Extensions::new(),
    };
    let manifest_path = root.join("prompt-pack.toml");
    write_toml(&manifest_path, &manifest)?;
    Ok(manifest_path)
}

/// Loads a trigger pack from TOML.
///
/// # Errors
///
/// Returns an error when the file cannot be read, parsed, or validated.
pub fn load_trigger_pack(path: &Path) -> Result<TriggerPack, AdaptationError> {
    let pack = read_toml::<TriggerPack>(path)?;
    validate_trigger_pack(&pack)?;
    Ok(pack)
}

/// Loads trigger rules from a versioned trigger pack.
///
/// # Errors
///
/// Returns an error when the file cannot be read, parsed, or validated.
pub fn load_trigger_rules(path: &Path) -> Result<Vec<TriggerRule>, AdaptationError> {
    Ok(load_trigger_pack(path)?.rules)
}

/// Loads a mechanical-step pack from TOML.
///
/// # Errors
///
/// Returns an error when the file cannot be read, parsed, or validated.
pub fn load_mechanical_pack(path: &Path) -> Result<MechanicalPack, AdaptationError> {
    let pack = read_toml::<MechanicalPack>(path)?;
    validate_mechanical_pack(&pack)?;
    Ok(pack)
}

/// Returns the compact baseline prompt templates for review, judge, and fix runs.
#[must_use]
pub fn baseline_prompt_templates() -> Vec<PromptTemplate> {
    vec![
        PromptTemplate {
            id: "review-contract-findings".to_owned(),
            run_kind: RunKind::Review,
            template:
                "Review the supplied change, contract facts, intent, and judgement briefs; return material findings using the Pump-19 contract."
                    .to_owned(),
            extensions: Extensions::new(),
        },
        PromptTemplate {
            id: "judge-materiality".to_owned(),
            run_kind: RunKind::Judge,
            template:
                "Judge whether each finding is material enough to justify another fix pass; return decisions using the Pump-19 contract."
                    .to_owned(),
            extensions: Extensions::new(),
        },
        PromptTemplate {
            id: "fix-material-findings".to_owned(),
            run_kind: RunKind::Fix,
            template:
                "Fix only the material findings assigned to this pass; return patches using the Pump-19 contract."
                    .to_owned(),
            extensions: Extensions::new(),
        },
    ]
}

/// Validates an already-loaded trigger pack.
///
/// # Errors
///
/// Returns an error when the pack has an unsupported version or malformed rule.
pub fn validate_trigger_pack(pack: &TriggerPack) -> Result<(), AdaptationError> {
    validate_header(
        "trigger pack",
        &pack.id,
        pack.schema_version,
        pack.contract_version,
    )?;
    if pack.rules.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "trigger pack",
            id: pack.id.clone(),
            items: "rules",
        });
    }
    let mut ids = BTreeSet::new();
    for rule in &pack.rules {
        if rule.id.trim().is_empty() {
            return Err(AdaptationError::EmptyTriggerRuleId {
                pack_id: pack.id.clone(),
            });
        }
        if !ids.insert(rule.id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "trigger pack",
                id: pack.id.clone(),
                duplicate: rule.id.clone(),
            });
        }
        validate_criteria(&rule.id, &rule.criteria)?;
        validate_agent_plan(&rule.id, &rule.agent_plan)?;
    }
    Ok(())
}

/// Validates an already-loaded mechanical-step pack.
///
/// # Errors
///
/// Returns an error when the pack has an unsupported version or malformed step.
pub fn validate_mechanical_pack(pack: &MechanicalPack) -> Result<(), AdaptationError> {
    validate_header(
        "mechanical pack",
        &pack.id,
        pack.schema_version,
        pack.contract_version,
    )?;
    if pack.steps.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "mechanical pack",
            id: pack.id.clone(),
            items: "steps",
        });
    }
    let mut ids = BTreeSet::new();
    for step in &pack.steps {
        if step.id.trim().is_empty() {
            return Err(AdaptationError::EmptyMechanicalStepId {
                pack_id: pack.id.clone(),
            });
        }
        if !ids.insert(step.id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "mechanical pack",
                id: pack.id.clone(),
                duplicate: step.id.clone(),
            });
        }
        if let MechanicalStepKind::Custom { name } = &step.kind
            && name.trim().is_empty()
        {
            return Err(AdaptationError::EmptyMechanicalCustomKind {
                step_id: step.id.clone(),
            });
        }
        match &step.execution {
            MechanicalExecution::Command { program, .. } if program.trim().is_empty() => {
                return Err(AdaptationError::EmptyMechanicalProgram {
                    step_id: step.id.clone(),
                });
            }
            MechanicalExecution::Container { image, .. } if image.trim().is_empty() => {
                return Err(AdaptationError::EmptyMechanicalImage {
                    step_id: step.id.clone(),
                });
            }
            MechanicalExecution::Command { .. } | MechanicalExecution::Container { .. } => {}
        }
    }
    Ok(())
}

/// Validates an already-loaded prompt pack.
///
/// # Errors
///
/// Returns an error when the pack has malformed reviewers, briefs, or prompt templates.
pub fn validate_prompt_pack(pack: &PromptPack) -> Result<(), AdaptationError> {
    let mut template_ids = BTreeSet::new();
    for template in &pack.manifest.prompt_templates {
        if template.id.trim().is_empty() {
            return Err(AdaptationError::EmptyPromptTemplateId {
                pack_id: pack.manifest.id.clone(),
            });
        }
        if !template_ids.insert(template.id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "prompt pack templates",
                id: pack.manifest.id.clone(),
                duplicate: template.id.clone(),
            });
        }
        if template.template.trim().is_empty() {
            return Err(AdaptationError::EmptyPromptTemplateText {
                pack_id: pack.manifest.id.clone(),
                template_id: template.id.clone(),
            });
        }
    }
    if pack.reviewers.reviewers.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "prompt pack",
            id: pack.manifest.id.clone(),
            items: "reviewers",
        });
    }
    let mut reviewer_ids = BTreeSet::new();
    for reviewer in &pack.reviewers.reviewers {
        if reviewer.agent_id.trim().is_empty() {
            return Err(AdaptationError::EmptyReviewerId {
                pack_id: pack.manifest.id.clone(),
            });
        }
        if !reviewer_ids.insert(reviewer.agent_id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "prompt pack reviewers",
                id: pack.manifest.id.clone(),
                duplicate: reviewer.agent_id.clone(),
            });
        }
        if reviewer.model_family.trim().is_empty() {
            return Err(AdaptationError::EmptyReviewerFamily {
                pack_id: pack.manifest.id.clone(),
                reviewer_id: reviewer.agent_id.clone(),
            });
        }
        if reviewer.command.is_empty() {
            return Err(AdaptationError::EmptyReviewerCommand {
                pack_id: pack.manifest.id.clone(),
                reviewer_id: reviewer.agent_id.clone(),
            });
        }
    }
    if pack.briefs.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "prompt pack",
            id: pack.manifest.id.clone(),
            items: "briefs",
        });
    }
    let mut brief_ids = BTreeSet::new();
    for brief in &pack.briefs {
        if brief.id.trim().is_empty() {
            return Err(AdaptationError::EmptyBriefId {
                pack_id: pack.manifest.id.clone(),
            });
        }
        if !brief_ids.insert(brief.id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "prompt pack briefs",
                id: pack.manifest.id.clone(),
                duplicate: brief.id.clone(),
            });
        }
        if brief.brief.trim().is_empty() {
            return Err(AdaptationError::EmptyBriefText {
                pack_id: pack.manifest.id.clone(),
                brief_id: brief.id.clone(),
            });
        }
    }
    Ok(())
}

fn validate_header(
    unit_kind: &'static str,
    id: &str,
    schema_version: AdaptationSchemaVersion,
    contract_version: ContractVersion,
) -> Result<(), AdaptationError> {
    if id.trim().is_empty() {
        return Err(AdaptationError::EmptyUnitId { unit_kind });
    }
    let expected = AdaptationSchemaVersion::current();
    if schema_version != expected {
        return Err(AdaptationError::UnsupportedSchemaVersion {
            unit_kind,
            id: id.to_owned(),
            actual: schema_version,
            expected,
        });
    }
    let current = ContractVersion::current();
    if contract_version.major != current.major || contract_version.minor > current.minor {
        return Err(AdaptationError::UnsupportedContractVersion {
            unit_kind,
            id: id.to_owned(),
            actual: contract_version,
            current,
        });
    }
    Ok(())
}

fn validate_criteria(rule_id: &str, criteria: &Criteria) -> Result<(), AdaptationError> {
    match criteria {
        Criteria::All { criteria } | Criteria::Any { criteria } if criteria.is_empty() => {
            Err(AdaptationError::EmptyCriteriaGroup {
                rule_id: rule_id.to_owned(),
            })
        }
        Criteria::All { criteria } | Criteria::Any { criteria } => {
            for child in criteria {
                validate_criteria(rule_id, child)?;
            }
            Ok(())
        }
        Criteria::Event { .. } | Criteria::State { .. } => Ok(()),
    }
}

fn validate_agent_plan(rule_id: &str, plan: &AgentPlan) -> Result<(), AdaptationError> {
    for target in plan
        .reviewers
        .iter()
        .chain(plan.fixers.iter())
        .chain(plan.judge.iter())
        .chain(plan.finishers.iter())
    {
        if target.agent_id.0.trim().is_empty() {
            return Err(AdaptationError::EmptyAgentTargetId {
                rule_id: rule_id.to_owned(),
            });
        }
        if target.lineage.family.0.trim().is_empty() {
            return Err(AdaptationError::EmptyAgentModelFamily {
                rule_id: rule_id.to_owned(),
            });
        }
    }
    Ok(())
}

fn read_toml<T>(path: &Path) -> Result<T, AdaptationError>
where
    T: for<'de> Deserialize<'de>,
{
    let text = fs::read_to_string(path).map_err(|source| AdaptationError::Io {
        path: path.display().to_string(),
        source,
    })?;
    toml::from_str(&text).map_err(|source| AdaptationError::ParseToml {
        path: path.display().to_string(),
        source,
    })
}

fn write_toml<T>(path: &Path, value: &T) -> Result<(), AdaptationError>
where
    T: Serialize,
{
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|source| AdaptationError::Io {
            path: parent.display().to_string(),
            source,
        })?;
    }
    let text = toml::to_string_pretty(value)?;
    fs::write(path, text).map_err(|source| AdaptationError::Io {
        path: path.display().to_string(),
        source,
    })
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use pump19_contract::{
        AgentId, AgentRole, BranchCurrency, ContractEvent, ContractVersion, EventPayload,
        Extensions, ForgeFacts, Mergeability, ModelFamily, ModelLineage, PullRequestRef,
        ReviewCleanliness, Revision, RunKind, RunOutcome, SessionId,
    };
    use pump19_core::{
        AgentLaunchSpec, AgentLaunchTarget, Core, CoreError, Criteria, DispatchOutcome, EventKind,
        EventSource, JsonRunStateStore, LaunchProof, PreparedAgent, RunLaunchOutcome,
        RunLaunchRequest, RunLauncher, WorkspaceExecOutput, WorkspaceExecRequest,
        WorkspaceExecutor, WorkspaceIsolation, WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
    };
    use pump19_judgement::{IntentApp, IntentSpec, judgement_prompt};
    use tempfile::tempdir;

    use super::{
        AdaptationError, AdaptationSchemaVersion, MechanicalExecution, MechanicalPack,
        MechanicalStep, MechanicalStepKind, TriggerPack, load_mechanical_pack, load_prompt_pack,
        load_trigger_rules, write_baseline_prompt_pack, write_toml,
    };

    #[test]
    fn baseline_prompt_pack_loads_and_feeds_judgement_prompt()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let manifest = write_baseline_prompt_pack(dir.path(), "baseline-review")?;

        let pack = load_prompt_pack(&manifest)?;
        let prompt = judgement_prompt(
            dir.path(),
            &IntentSpec {
                app: IntentApp {
                    slug: "sample".to_owned(),
                    name: "Sample".to_owned(),
                    purpose: "Prove prompt packs load through judgement seams".to_owned(),
                    author_agent_id: None,
                },
                invariants: Vec::new(),
                behaviours: Vec::new(),
            },
            &pack.briefs[0],
        )?;

        assert_eq!(pack.manifest.id, "baseline-review");
        assert_eq!(pack.manifest.prompt_templates.len(), 3);
        assert_eq!(pack.reviewers.reviewers.len(), 2);
        assert_eq!(pack.briefs.len(), 3);
        assert!(prompt.contains("PUMP19_JUDGEMENT: PASS"));
        assert!(prompt.contains(&pack.briefs[0].brief));
        Ok(())
    }

    #[test]
    fn external_trigger_rule_loads_and_dispatches_through_core()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let pack_path = dir.path().join("triggers.toml");
        write_toml(&pack_path, &trigger_pack())?;
        let rules = load_trigger_rules(&pack_path)?;

        let mut core = Core::new(
            NoEvents,
            StaticWorkspace {
                root: dir.path().to_path_buf(),
            },
            SuccessfulLauncher,
            JsonRunStateStore::new(dir.path().join("state"))?,
        );
        let outcomes = core.process_event(&pull_request_opened(), &rules)?;

        assert!(matches!(
            outcomes.as_slice(),
            [DispatchOutcome::Launched { rule_id, .. }] if rule_id == "review-on-open"
        ));
        Ok(())
    }

    #[test]
    fn wrong_schema_version_is_rejected() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let path = dir.path().join("triggers.toml");
        let mut pack = trigger_pack();
        pack.schema_version = AdaptationSchemaVersion { major: 2, minor: 0 };
        write_toml(&path, &pack)?;

        let error = load_trigger_rules(&path).expect_err("wrong schema should fail");

        assert!(matches!(
            error,
            AdaptationError::UnsupportedSchemaVersion {
                unit_kind: "trigger pack",
                ..
            }
        ));
        Ok(())
    }

    #[test]
    fn malformed_mechanical_step_is_rejected() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let path = dir.path().join("mechanical.toml");
        write_toml(
            &path,
            &MechanicalPack {
                schema_version: AdaptationSchemaVersion::current(),
                contract_version: ContractVersion::current(),
                id: "forgejo-mechanics".to_owned(),
                steps: vec![MechanicalStep {
                    id: "publish-comments".to_owned(),
                    kind: MechanicalStepKind::Publish,
                    execution: MechanicalExecution::Command {
                        program: String::new(),
                        args: Vec::new(),
                    },
                    inputs: Vec::new(),
                    outputs: Vec::new(),
                    extensions: Extensions::new(),
                }],
                extensions: Extensions::new(),
            },
        )?;

        let error = load_mechanical_pack(&path).expect_err("empty command should fail");

        assert!(matches!(
            error,
            AdaptationError::EmptyMechanicalProgram { step_id } if step_id == "publish-comments"
        ));
        Ok(())
    }

    fn trigger_pack() -> TriggerPack {
        TriggerPack {
            schema_version: AdaptationSchemaVersion::current(),
            contract_version: ContractVersion::current(),
            id: "default-triggers".to_owned(),
            rules: vec![pump19_core::TriggerRule {
                id: "review-on-open".to_owned(),
                run_kind: RunKind::Review,
                criteria: Criteria::Event {
                    event: EventKind::PullRequestOpened,
                },
                agent_plan: AgentPlanFixture::standard(),
            }],
            extensions: Extensions::new(),
        }
    }

    struct AgentPlanFixture;

    impl AgentPlanFixture {
        fn standard() -> pump19_core::AgentPlan {
            pump19_core::AgentPlan {
                reviewers: vec![
                    target("codex-reviewer", AgentRole::Reviewer, "codex"),
                    target("claude-reviewer", AgentRole::Reviewer, "claude"),
                ],
                fixers: Vec::new(),
                judge: Some(target("judge", AgentRole::Judge, "gemini")),
                finishers: Vec::new(),
            }
        }
    }

    fn target(id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
        AgentLaunchTarget {
            agent_id: AgentId(id.to_owned()),
            role,
            vendor: "test-vendor".to_owned(),
            control_plane: "test-control".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: format!("{family}-stable"),
            },
        }
    }

    fn pull_request_opened() -> ContractEvent {
        ContractEvent {
            contract_version: ContractVersion::current(),
            id: "event-1".to_owned(),
            payload: EventPayload::PullRequestOpened {
                facts: ForgeFacts {
                    contract_version: ContractVersion::current(),
                    pr: PullRequestRef {
                        repository: "org/repo".to_owned(),
                        id: "19".to_owned(),
                    },
                    head: Revision {
                        sha: "abc123".to_owned(),
                    },
                    base: Revision {
                        sha: "def456".to_owned(),
                    },
                    branch_currency: BranchCurrency::Current,
                    cleanliness: ReviewCleanliness::Dirty,
                    mergeability: Mergeability::Mergeable,
                    finish_label: None,
                    actor_permissions: Vec::new(),
                    extensions: Extensions::new(),
                },
            },
            extensions: Extensions::new(),
        }
    }

    #[derive(Debug)]
    struct NoEvents;

    impl EventSource for NoEvents {
        fn next_event(&mut self) -> Result<Option<ContractEvent>, CoreError> {
            Ok(None)
        }
    }

    #[derive(Debug)]
    struct StaticWorkspace {
        root: PathBuf,
    }

    impl WorkspaceProvider for StaticWorkspace {
        fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
            Ok(WorkspaceLease {
                id: request.run_id.0,
                root: self.root.clone(),
                isolation: WorkspaceIsolation {
                    isolated: true,
                    credential_free: true,
                    egress_bounded: true,
                    resource_bounded: true,
                    ephemeral: true,
                },
            })
        }

        fn exec(
            &mut self,
            _lease: &WorkspaceLease,
            _request: WorkspaceExecRequest,
        ) -> Result<WorkspaceExecOutput, CoreError> {
            Ok(WorkspaceExecOutput {
                exit_code: 0,
                stdout: Vec::new(),
                stderr: Vec::new(),
            })
        }

        fn cleanup(&mut self, _lease: &WorkspaceLease) -> Result<(), CoreError> {
            Ok(())
        }
    }

    #[derive(Debug)]
    struct SuccessfulLauncher;

    impl RunLauncher for SuccessfulLauncher {
        fn prepare_agent(&mut self, spec: AgentLaunchSpec) -> Result<PreparedAgent, CoreError> {
            Ok(PreparedAgent {
                agent_id: spec.target.agent_id,
                role: spec.target.role,
                session_id: SessionId(format!("session-{}", spec.pass_index)),
                proof: LaunchProof::EstablishedFresh,
            })
        }

        fn launch_run(
            &mut self,
            _request: RunLaunchRequest,
            _workspace: &mut dyn WorkspaceExecutor,
        ) -> Result<RunLaunchOutcome, CoreError> {
            Ok(RunLaunchOutcome {
                outcome: RunOutcome::Succeeded,
                findings: Vec::new(),
                decisions: Vec::new(),
                patches: Vec::new(),
                token_usage: None,
            })
        }
    }
}
