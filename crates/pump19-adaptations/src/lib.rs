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
    JudgementBrief, load_judgement_briefs_from_dir, write_baseline_briefs_to_dir,
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
    #[error("workflow script in pack {pack_id:?} has an empty id")]
    EmptyWorkflowScriptId { pack_id: String },
    #[error("workflow script {script_id:?} in pack {pack_id:?} has an empty path")]
    EmptyWorkflowScriptPath { pack_id: String, script_id: String },
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
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub workflow_scripts: Vec<WorkflowScript>,
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

/// A versioned workflow script adaptation invoked by the run body.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct WorkflowScript {
    pub id: String,
    pub run_kind: RunKind,
    pub path: PathBuf,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: Extensions,
}

/// Prompt content loaded from a manifest plus existing judgement TOML files.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PromptPack {
    pub manifest: PromptPackManifest,
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

/// Loads a prompt pack manifest and its referenced judgement material.
///
/// # Errors
///
/// Returns an error when the manifest or brief directory cannot be read, parsed,
/// or validated.
pub fn load_prompt_pack(manifest_path: &Path) -> Result<PromptPack, AdaptationError> {
    let manifest = read_toml::<PromptPackManifest>(manifest_path)?;
    validate_header(
        "prompt pack",
        &manifest.id,
        manifest.schema_version,
        manifest.contract_version,
    )?;

    let root = manifest_path.parent().unwrap_or_else(|| Path::new("."));
    let briefs = load_judgement_briefs_from_dir(&root.join(&manifest.brief_dir))?;
    let pack = PromptPack { manifest, briefs };
    validate_prompt_pack(&pack)?;
    Ok(pack)
}

/// Writes a compact baseline prompt pack into `root`.
///
/// # Errors
///
/// Returns an error when the manifest, baseline briefs, or workflow script cannot
/// be serialised or written.
pub fn write_baseline_prompt_pack(root: &Path, id: &str) -> Result<PathBuf, AdaptationError> {
    fs::create_dir_all(root).map_err(|source| AdaptationError::Io {
        path: root.display().to_string(),
        source,
    })?;
    let brief_dir = root.join("briefs");
    write_baseline_briefs_to_dir(&brief_dir)?;
    let workflow_dir = root.join("workflows");
    fs::create_dir_all(&workflow_dir).map_err(|source| AdaptationError::Io {
        path: workflow_dir.display().to_string(),
        source,
    })?;
    for (path, source) in baseline_workflow_sources() {
        let script_path = root.join(path);
        fs::write(&script_path, source).map_err(|source| AdaptationError::Io {
            path: script_path.display().to_string(),
            source,
        })?;
    }
    let manifest = PromptPackManifest {
        schema_version: AdaptationSchemaVersion::current(),
        contract_version: ContractVersion::current(),
        id: id.to_owned(),
        prompt_templates: baseline_prompt_templates(),
        workflow_scripts: baseline_workflow_scripts(),
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

/// Returns baseline workflow script declarations for review, judge, and fix runs.
#[must_use]
pub fn baseline_workflow_scripts() -> Vec<WorkflowScript> {
    vec![
        WorkflowScript {
            id: "review-ensemble".to_owned(),
            run_kind: RunKind::Review,
            path: PathBuf::from("workflows/review.js"),
            extensions: Extensions::new(),
        },
        WorkflowScript {
            id: "judge-ensemble".to_owned(),
            run_kind: RunKind::Judge,
            path: PathBuf::from("workflows/judge.js"),
            extensions: Extensions::new(),
        },
        WorkflowScript {
            id: "fix-ensemble".to_owned(),
            run_kind: RunKind::Fix,
            path: PathBuf::from("workflows/fix.js"),
            extensions: Extensions::new(),
        },
    ]
}

fn baseline_workflow_sources() -> Vec<(&'static str, &'static str)> {
    vec![
        ("workflows/review.js", REVIEW_WORKFLOW_JS),
        ("workflows/judge.js", JUDGE_WORKFLOW_JS),
        ("workflows/fix.js", FIX_WORKFLOW_JS),
    ]
}

const REVIEW_WORKFLOW_JS: &str = r#"export const meta = {
  name: "pump19-review",
  description: "Run authorised Pump-19 reviewers over judgement briefs"
};

const reviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["status", "stdout", "stderr"],
  properties: {
    status: { type: "string", enum: ["passed", "failed"] },
    stdout: { type: "string" },
    stderr: { type: "string" }
  }
};

function optionsFor(target, label) {
  return {
    engine: target.engine,
    model: target.model,
    label,
    schema: reviewSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  };
}

const calls = [];
for (const brief of args.briefs) {
  for (const reviewer of args.reviewers) {
    calls.push({ brief, reviewer });
  }
}

const outputs = await parallel(calls.map(({ brief, reviewer }) => () =>
  agent(
    `## Task
Review this Pump-19 judgement brief independently. Return JSON matching the schema.

## Verdict
Use status "failed" when you find a material concern. Use status "passed" only when the brief is satisfied.

<brief id="${brief.id}">
${brief.prompt}
</brief>`,
    optionsFor(reviewer, `${reviewer.agent_id}:${brief.id}`)
  )
));

if (outputs.some((output) => output === null)) {
  throw new Error("reviewer output failed schema validation");
}

const briefResults = [];
for (const brief of args.briefs) {
  const reviews = [];
  for (let i = 0; i < calls.length; i += 1) {
    if (calls[i].brief.id !== brief.id) continue;
    const reviewer = calls[i].reviewer;
    const output = outputs[i];
    reviews.push({
      agent_id: reviewer.agent_id,
      model_family: reviewer.model_family,
      status: output.status,
      stdout: output.stdout,
      stderr: output.stderr
    });
  }
  briefResults.push({
    brief_id: brief.id,
    status: reviews.some((review) => review.status === "failed") ? "failed" : "passed",
    reviews
  });
}

return {
  status: briefResults.some((brief) => brief.status === "failed") ? "failed" : "passed",
  briefs: briefResults,
  model_families: [...new Set(args.reviewers.map((reviewer) => reviewer.model_family))].sort()
};
"#;

const JUDGE_WORKFLOW_JS: &str = r#"export const meta = {
  name: "pump19-judge",
  description: "Run the authorised Pump-19 significance judge"
};

const judgeSchema = {
  type: "object",
  additionalProperties: false,
  required: ["decisions"],
  properties: {
    decisions: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["finding_id", "verdict", "rationale"],
        properties: {
          finding_id: { type: "string" },
          verdict: { type: "string", enum: ["material", "minor"] },
          rationale: { type: "string" }
        }
      }
    }
  }
};

const judge = args.judges[0];
const result = await agent(
  `## Task
Judge whether each finding is material enough to justify another fix pass.

<findings>
${JSON.stringify(args.findings)}
</findings>

<loop_history>
${JSON.stringify({ pass_index: args.pass_index, prior_decisions: args.decisions })}
</loop_history>`,
  {
    engine: judge.engine,
    model: judge.model,
    label: judge.agent_id,
    schema: judgeSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  }
);

if (result === null) {
  throw new Error("judge output failed schema validation");
}

return result.decisions;
"#;

const FIX_WORKFLOW_JS: &str = r#"export const meta = {
  name: "pump19-fix",
  description: "Run the authorised Pump-19 fixer"
};

const fixSchema = {
  type: "object",
  additionalProperties: false,
  required: ["kind", "summary"],
  properties: {
    kind: { type: "string", enum: ["description"] },
    summary: { type: "string" }
  }
};

const fixer = args.fixers[0];
const result = await agent(
  `## Task
Produce a fix description for the material findings. Return JSON matching the schema.

<material_findings>
${JSON.stringify(args.material_findings)}
</material_findings>`,
  {
    engine: fixer.engine,
    model: fixer.model,
    label: fixer.agent_id,
    schema: fixSchema,
    timeoutMs: args.agent_timeout_ms || 300000
  }
);

if (result === null) {
  throw new Error("fixer output failed schema validation");
}

return result;
"#;

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
/// Returns an error when the pack has malformed workflow scripts, briefs, or prompt templates.
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
    if pack.manifest.workflow_scripts.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "prompt pack",
            id: pack.manifest.id.clone(),
            items: "workflow scripts",
        });
    }
    let mut workflow_ids = BTreeSet::new();
    for script in &pack.manifest.workflow_scripts {
        if script.id.trim().is_empty() {
            return Err(AdaptationError::EmptyWorkflowScriptId {
                pack_id: pack.manifest.id.clone(),
            });
        }
        if !workflow_ids.insert(script.id.clone()) {
            return Err(AdaptationError::DuplicateId {
                unit_kind: "prompt pack workflow scripts",
                id: pack.manifest.id.clone(),
                duplicate: script.id.clone(),
            });
        }
        if script.path.as_os_str().is_empty() {
            return Err(AdaptationError::EmptyWorkflowScriptPath {
                pack_id: pack.manifest.id.clone(),
                script_id: script.id.clone(),
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
        AgentEngine, AgentLaunchSpec, AgentLaunchTarget, Core, CoreError, Criteria,
        DispatchOutcome, EventKind, EventSource, JsonRunStateStore, LaunchProof, PreparedAgent,
        RunLaunchOutcome, RunLaunchRequest, RunLauncher, WorkspaceExecOutput, WorkspaceExecRequest,
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
        assert_eq!(pack.manifest.workflow_scripts.len(), 3);
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
            engine: engine_for_family(family),
            vendor: "test-vendor".to_owned(),
            control_plane: "test-control".to_owned(),
            lineage: ModelLineage {
                family: ModelFamily(family.to_owned()),
                model: format!("{family}-stable"),
            },
        }
    }

    fn engine_for_family(family: &str) -> AgentEngine {
        match family {
            "claude" => AgentEngine::Claude,
            "codex" => AgentEngine::Codex,
            _ => AgentEngine::Opencode,
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
