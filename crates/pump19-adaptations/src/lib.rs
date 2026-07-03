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

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt as _;

use pump19_contract::{
    AgentId, AgentRole, ContractVersion, Extensions, ModelFamily, ModelLineage, RunKind, RunOutcome,
};
use pump19_core::{
    AgentEngine, AgentLaunchTarget, AgentPlan, Criteria, EventKind, StateCriterion, TriggerRule,
};
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

/// Writes the baseline review/fix/judge loop trigger pack into `root`.
///
/// # Errors
///
/// Returns an error when the trigger pack cannot be serialised or written.
pub fn write_baseline_trigger_pack(root: &Path, id: &str) -> Result<PathBuf, AdaptationError> {
    fs::create_dir_all(root).map_err(|source| AdaptationError::Io {
        path: root.display().to_string(),
        source,
    })?;
    let pack_path = root.join("triggers.toml");
    write_toml(&pack_path, &baseline_trigger_pack(id))?;
    Ok(pack_path)
}

/// Writes the baseline mechanical pack into `root`.
///
/// # Errors
///
/// Returns an error when the mechanical pack cannot be serialised or written.
pub fn write_baseline_mechanical_pack(root: &Path, id: &str) -> Result<PathBuf, AdaptationError> {
    fs::create_dir_all(root).map_err(|source| AdaptationError::Io {
        path: root.display().to_string(),
        source,
    })?;
    let pack_path = root.join("mechanical.toml");
    write_toml(&pack_path, &baseline_mechanical_pack(id))?;
    Ok(pack_path)
}

/// Paths written by [`write_baseline_deployment_packs`].
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BaselineDeploymentPackPaths {
    pub prompt_pack: PathBuf,
    pub trigger_pack: PathBuf,
    pub mechanical_pack: PathBuf,
}

/// Paths written by [`write_baseline_forgejo_commands`].
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BaselineForgejoCommandPaths {
    pub poll_command: PathBuf,
    pub operation_command: PathBuf,
}

/// Paths written by [`write_baseline_deployment_assets`].
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BaselineDeploymentAssetPaths {
    pub packs: BaselineDeploymentPackPaths,
    pub commands: BaselineForgejoCommandPaths,
}

/// Writes all baseline deployment adaptation packs into conventional subdirectories.
///
/// # Errors
///
/// Returns an error when any baseline pack cannot be serialised or written.
pub fn write_baseline_deployment_packs(
    root: &Path,
) -> Result<BaselineDeploymentPackPaths, AdaptationError> {
    Ok(BaselineDeploymentPackPaths {
        prompt_pack: write_baseline_prompt_pack(&root.join("prompt"), "baseline-prompt")?,
        trigger_pack: write_baseline_trigger_pack(&root.join("trigger"), "baseline-loop")?,
        mechanical_pack: write_baseline_mechanical_pack(
            &root.join("mechanical"),
            "baseline-mechanical",
        )?,
    })
}

/// Writes the baseline Forgejo command scripts into `root`.
///
/// # Errors
///
/// Returns an error when either command script cannot be written.
pub fn write_baseline_forgejo_commands(
    root: &Path,
) -> Result<BaselineForgejoCommandPaths, AdaptationError> {
    fs::create_dir_all(root).map_err(|source| AdaptationError::Io {
        path: root.display().to_string(),
        source,
    })?;
    let poll_command = root.join("pump19-forgejo-poll");
    let operation_command = root.join("pump19-forgejo-operation");
    write_executable_text(&poll_command, BASELINE_FORGEJO_POLL_SH)?;
    write_executable_text(&operation_command, BASELINE_FORGEJO_OPERATION_SH)?;
    Ok(BaselineForgejoCommandPaths {
        poll_command,
        operation_command,
    })
}

/// Writes the complete baseline deployment asset set into `root`.
///
/// # Errors
///
/// Returns an error when any pack or Forgejo command script cannot be written.
pub fn write_baseline_deployment_assets(
    root: &Path,
) -> Result<BaselineDeploymentAssetPaths, AdaptationError> {
    Ok(BaselineDeploymentAssetPaths {
        packs: write_baseline_deployment_packs(&root.join("adaptations"))?,
        commands: write_baseline_forgejo_commands(&root.join("commands"))?,
    })
}

fn write_executable_text(path: &Path, source: &str) -> Result<(), AdaptationError> {
    fs::write(path, source).map_err(|source| AdaptationError::Io {
        path: path.display().to_string(),
        source,
    })?;
    #[cfg(unix)]
    {
        let permissions = fs::Permissions::from_mode(0o755);
        fs::set_permissions(path, permissions).map_err(|source| AdaptationError::Io {
            path: path.display().to_string(),
            source,
        })?;
    }
    Ok(())
}

const BASELINE_FORGEJO_POLL_SH: &str = r#"#!/bin/sh
set -eu

base_url=
finish_label=pump19-finish
curl_bin=${PUMP19_FORGEJO_CURL:-curl}

usage() {
  echo "usage: pump19-forgejo-poll --base-url URL [--finish-label LABEL] owner/repo" >&2
  exit 64
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base-url)
      [ "$#" -ge 2 ] || usage
      base_url=${2%/}
      shift 2
      ;;
    --finish-label)
      [ "$#" -ge 2 ] || usage
      finish_label=$2
      shift 2
      ;;
    --help|-h)
      usage
      ;;
    --*)
      echo "unknown option: $1" >&2
      usage
      ;;
    *)
      repository=$1
      shift
      [ "$#" -eq 0 ] || usage
      ;;
  esac
done

[ -n "${base_url}" ] || usage
[ -n "${repository:-}" ] || usage
[ -n "${FORGEJO_TOKEN:-}" ] || {
  echo "FORGEJO_TOKEN is required" >&2
  exit 78
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required" >&2
  exit 78
}

api_json() {
  "$curl_bin" -fsS \
    -H "Authorization: token ${FORGEJO_TOKEN}" \
    -H "Accept: application/json" \
    "$base_url/api/v1$1"
}

user_json=$(api_json "/user")
pulls_json=$(api_json "/repos/$repository/pulls?state=open")

tmp=${TMPDIR:-/tmp}/pump19-forgejo-poll.$$
mkdir -p "$tmp"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

printf '%s' "$pulls_json" | jq -c '.[]' > "$tmp/pulls.jsonl"
: > "$tmp/snapshots.jsonl"

while IFS= read -r pull_json; do
  pr_id=$(printf '%s' "$pull_json" | jq -r '(.number // .id | tostring)')
  timeline_json=$(api_json "/repos/$repository/issues/$pr_id/timeline" 2>/dev/null || printf '[]')
  labels_json=$(printf '%s\n%s\n' "$pull_json" "$timeline_json" | jq -c -n --arg finish "$finish_label" '
    input as $pull
    | input as $timeline
    | ($pull.labels // []) as $labels
    | ($timeline // []) as $events
    | $labels
    | map(select(.name == $finish))
    | map({
        name,
        applied_by: (
          $events
          | map(select((.label.name // .label.Name // "") == $finish))
          | .[-1]
          | if . == null then null else {
              id: ((.user.id // .user.login // .user.username // "unknown") | tostring),
              display_name: (.user.full_name // .user.login // .user.username // "unknown")
            } end
        )
      })
  ')
  cleanliness=$(printf '%s' "$labels_json" | jq -r 'if length > 0 then "clean" else "dirty" end')
  actor_permissions_json=$(printf '%s' "$user_json" | jq -c --arg finish "$finish_label" '[
    {
      actor: {
        id: ((.id // .login // .username // "forgejo-token") | tostring),
        display_name: (.full_name // .login // .username // "Forgejo token")
      },
      can_apply_finish_label: true,
      can_merge: true
    }
  ]')
  snapshot=$(printf '%s\n%s\n%s\n' "$pull_json" "$labels_json" "$actor_permissions_json" | jq -c -n --arg repository "$repository" --arg cleanliness "$cleanliness" '
    input as $pull
    | input as $labels
    | input as $actor_permissions
    | {
        repository: $repository,
        id: (($pull.number // $pull.id) | tostring),
        head_sha: ($pull.head.sha // ""),
        base_sha: ($pull.base.sha // ""),
        branch_currency: (
          if ($pull.merge_base // "") != "" and ($pull.base.sha // "") != "" and $pull.merge_base == $pull.base.sha
          then "current"
          else "unknown"
          end
        ),
        cleanliness: $cleanliness,
        mergeability: (
          if $pull.mergeable == true then "mergeable"
          elif $pull.mergeable == false then "conflicting"
          else "unknown"
          end
        ),
        labels: $labels,
        actor_permissions: $actor_permissions
      }
  ')
  printf '%s\n' "$snapshot" >> "$tmp/snapshots.jsonl"
done < "$tmp/pulls.jsonl"

jq -s '.' "$tmp/snapshots.jsonl"
"#;

const BASELINE_FORGEJO_OPERATION_SH: &str = r#"#!/bin/sh
set -eu

base_url=
git_base_url=
curl_bin=${PUMP19_FORGEJO_CURL:-curl}

usage() {
  echo "usage: pump19-forgejo-operation --base-url URL [--git-base-url URL]" >&2
  exit 64
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base-url)
      [ "$#" -ge 2 ] || usage
      base_url=${2%/}
      shift 2
      ;;
    --git-base-url)
      [ "$#" -ge 2 ] || usage
      git_base_url=${2%/}
      shift 2
      ;;
    --help|-h)
      usage
      ;;
    *)
      echo "unknown option: $1" >&2
      usage
      ;;
  esac
done

[ -n "${base_url}" ] || usage
[ -n "${git_base_url}" ] || git_base_url=$base_url
[ -n "${FORGEJO_TOKEN:-}" ] || {
  echo "FORGEJO_TOKEN is required" >&2
  exit 78
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required" >&2
  exit 78
}

input=$(cat)
operation=$(printf '%s' "$input" | jq -r '.operation')
repository=$(printf '%s' "$input" | jq -r '.pr.repository')
pr_id=$(printf '%s' "$input" | jq -r '.pr.id')

api_json() {
  method=$1
  path=$2
  body=${3:-}
  if [ -n "$body" ]; then
    "$curl_bin" -fsS \
      -X "$method" \
      -H "Authorization: token ${FORGEJO_TOKEN}" \
      -H "Accept: application/json" \
      -H "Content-Type: application/json" \
      --data "$body" \
      "$base_url/api/v1$path"
  else
    "$curl_bin" -fsS \
      -X "$method" \
      -H "Authorization: token ${FORGEJO_TOKEN}" \
      -H "Accept: application/json" \
      "$base_url/api/v1$path"
  fi
}

pull_json() {
  api_json GET "/repos/$repository/pulls/$pr_id"
}

guard_expected_head() {
  expected=$1
  [ -n "$expected" ] && [ "$expected" != "null" ] || return 0
  current=$(pull_json | jq -r '.head.sha // empty')
  if [ "$current" != "$expected" ]; then
    jq -nc --arg expected "$expected" --arg actual "$current" \
      '{error:"head_moved", expected_head_sha:$expected, actual_head_sha:(if $actual == "" then null else $actual end)}' >&2
    exit 75
  fi
}

receipt() {
  operation_id=$1
  new_head=${2:-}
  jq -nc --arg operation_id "$operation_id" --arg new_head "$new_head" \
    '{operation_id:$operation_id, new_head_sha:(if $new_head == "" then null else $new_head end)}'
}

metadata_expected=$(printf '%s' "$input" | jq -r '.metadata.expected_head_sha // empty')

case "$operation" in
  post_comment)
    guard_expected_head "$metadata_expected"
    body=$(printf '%s' "$input" | jq -c '{body:.body}')
    response=$(api_json POST "/repos/$repository/issues/$pr_id/comments" "$body")
    receipt "$(printf '%s' "$response" | jq -r '(.id // .html_url // .url) | tostring')"
    ;;
  update_comment)
    guard_expected_head "$metadata_expected"
    comment_id=$(printf '%s' "$input" | jq -r '.comment_operation_id')
    body=$(printf '%s' "$input" | jq -c '{body:.body}')
    response=$(api_json PATCH "/repos/$repository/issues/comments/$comment_id" "$body")
    receipt "$(printf '%s' "$response" | jq -r '(.id // "'"$comment_id"'") | tostring')"
    ;;
  resolve_comment)
    guard_expected_head "$metadata_expected"
    comment_id=$(printf '%s' "$input" | jq -r '.comment_operation_id')
    reason=$(printf '%s' "$input" | jq -r '.reason')
    body=$(jq -nc --arg reason "$reason" '{body:("Resolved by Pump-19: " + $reason)}')
    response=$(api_json PATCH "/repos/$repository/issues/comments/$comment_id" "$body")
    receipt "$(printf '%s' "$response" | jq -r '(.id // "'"$comment_id"'") | tostring')"
    ;;
  apply_label)
    label=$(printf '%s' "$input" | jq -r '.label')
    body=$(jq -nc --arg label "$label" '{labels:[$label]}')
    response=$(api_json POST "/repos/$repository/issues/$pr_id/labels" "$body")
    receipt "$(printf '%s' "$response" | jq -r 'if type == "array" then (.[-1].id // .[-1].name // "'"$label"'") else (.id // .name // "'"$label"'") end | tostring')"
    ;;
  merge)
    method=$(printf '%s' "$input" | jq -r '.method')
    body=$(jq -nc --arg method "$method" '{Do:$method}')
    response=$(api_json POST "/repos/$repository/pulls/$pr_id/merge" "$body")
    receipt "$(printf '%s' "$response" | jq -r '(.sha // .commit_id // "merge") | tostring')"
    ;;
  push_fix_commits)
    expected=$(printf '%s' "$input" | jq -r '.expected_head_sha')
    guard_expected_head "$expected"
    command -v git >/dev/null 2>&1 || {
      echo "git is required for push_fix_commits" >&2
      exit 78
    }
    pr_json=$(pull_json)
    head_ref=$(printf '%s' "$pr_json" | jq -r '.head.ref // empty')
    [ -n "$head_ref" ] || {
      echo "PR head ref is unavailable" >&2
      exit 65
    }
    tmp=${TMPDIR:-/tmp}/pump19-forgejo-operation.$$
    rm -rf "$tmp"
    mkdir -p "$tmp"
    trap 'rm -rf "$tmp"' EXIT HUP INT TERM
    git_url="$git_base_url/$repository.git"
    git -c "http.extraHeader=Authorization: token ${FORGEJO_TOKEN}" clone "$git_url" "$tmp/repo" >/dev/null 2>&1
    cd "$tmp/repo"
    git checkout "$head_ref" >/dev/null 2>&1 || git checkout -b "$head_ref" "origin/$head_ref" >/dev/null 2>&1
    actual=$(git rev-parse HEAD)
    if [ "$actual" != "$expected" ]; then
      jq -nc --arg expected "$expected" --arg actual "$actual" \
        '{error:"head_moved", expected_head_sha:$expected, actual_head_sha:$actual}' >&2
      exit 75
    fi
    printf '%s' "$input" | jq -c '.commits[]' > "$tmp/commits.jsonl"
    while IFS= read -r commit_json; do
      message=$(printf '%s' "$commit_json" | jq -r '.message')
      author=$(printf '%s' "$commit_json" | jq -r '.author_agent_id')
      kind=$(printf '%s' "$commit_json" | jq -r '.change.kind')
      if [ "$kind" = "unified_diff" ]; then
        printf '%s' "$commit_json" | jq -r '.change.diff' | git apply
      fi
      if [ "$kind" != "description" ]; then
        git add -A
      fi
      GIT_AUTHOR_NAME="$author" GIT_AUTHOR_EMAIL="pump19@example.invalid" \
        GIT_COMMITTER_NAME="Pump-19" GIT_COMMITTER_EMAIL="pump19@example.invalid" \
        git commit --allow-empty -m "$message" >/dev/null 2>&1
    done < "$tmp/commits.jsonl"
    new_head=$(git rev-parse HEAD)
    git -c "http.extraHeader=Authorization: token ${FORGEJO_TOKEN}" push origin "HEAD:$head_ref" >/dev/null 2>&1
    receipt "push:$new_head" "$new_head"
    ;;
  *)
    echo "unknown operation: $operation" >&2
    exit 64
    ;;
esac
"#;

/// Returns the baseline mechanical pack used by the deployable example set.
#[must_use]
pub fn baseline_mechanical_pack(id: &str) -> MechanicalPack {
    MechanicalPack {
        schema_version: AdaptationSchemaVersion::current(),
        contract_version: ContractVersion::current(),
        id: id.to_owned(),
        steps: vec![MechanicalStep {
            id: "prepare-source".to_owned(),
            kind: MechanicalStepKind::Checkout,
            execution: MechanicalExecution::Command {
                program: "pump19-prepare-source".to_owned(),
                args: Vec::new(),
            },
            inputs: vec![
                "source_preparation_command_input_json".to_owned(),
                "forge_repository".to_owned(),
                "pull_request_head".to_owned(),
            ],
            outputs: vec![
                "prepared_source_tree".to_owned(),
                "prepared_revision".to_owned(),
            ],
            extensions: Extensions::new(),
        }],
        extensions: Extensions::new(),
    }
}

/// Returns the baseline trigger pack that composes the review/fix/judge loop.
#[must_use]
pub fn baseline_trigger_pack(id: &str) -> TriggerPack {
    TriggerPack {
        schema_version: AdaptationSchemaVersion::current(),
        contract_version: ContractVersion::current(),
        id: id.to_owned(),
        rules: baseline_trigger_rules(),
        extensions: Extensions::new(),
    }
}

/// Returns the baseline review/fix/judge loop trigger rules.
#[must_use]
pub fn baseline_trigger_rules() -> Vec<TriggerRule> {
    vec![
        review_on_pr_change_rule(),
        judge_after_review_rule(),
        judge_after_noop_fix_rule(),
        fix_after_material_judge_rule(),
        finish_on_label_rule(),
    ]
}

fn review_on_pr_change_rule() -> TriggerRule {
    TriggerRule {
        id: "review-on-pr-change".to_owned(),
        run_kind: RunKind::Review,
        criteria: Criteria::Any {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::PullRequestOpened,
                },
                Criteria::Event {
                    event: EventKind::PullRequestUpdated,
                },
            ],
        },
        agent_plan: AgentPlan {
            reviewers: baseline_reviewers(),
            fixers: Vec::new(),
            judge: Some(baseline_target("judge-glm", AgentRole::Judge, "glm")),
            finishers: Vec::new(),
        },
    }
}

fn judge_after_review_rule() -> TriggerRule {
    TriggerRule {
        id: "judge-after-review".to_owned(),
        run_kind: RunKind::Judge,
        criteria: Criteria::Event {
            event: EventKind::RunCompleted {
                run_kind: Some(RunKind::Review),
                outcome: Some(RunOutcome::Succeeded),
            },
        },
        agent_plan: judge_plan(),
    }
}

fn judge_after_noop_fix_rule() -> TriggerRule {
    TriggerRule {
        id: "judge-after-noop-fix".to_owned(),
        run_kind: RunKind::Judge,
        criteria: Criteria::Event {
            event: EventKind::RunCompleted {
                run_kind: Some(RunKind::Fix),
                outcome: Some(RunOutcome::NoOp),
            },
        },
        agent_plan: judge_plan(),
    }
}

fn fix_after_material_judge_rule() -> TriggerRule {
    TriggerRule {
        id: "fix-after-material-judge".to_owned(),
        run_kind: RunKind::Fix,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::RunCompleted {
                        run_kind: Some(RunKind::Judge),
                        outcome: Some(RunOutcome::Succeeded),
                    },
                },
                Criteria::State {
                    state: StateCriterion::HasMaterialDecision,
                },
            ],
        },
        agent_plan: AgentPlan {
            reviewers: Vec::new(),
            fixers: vec![baseline_target("fixer-codex", AgentRole::Fixer, "codex")],
            judge: None,
            finishers: Vec::new(),
        },
    }
}

fn finish_on_label_rule() -> TriggerRule {
    TriggerRule {
        id: "finish-on-label".to_owned(),
        run_kind: RunKind::Finish,
        criteria: Criteria::All {
            criteria: vec![
                Criteria::Event {
                    event: EventKind::LabelApplied {
                        name: Some("pump19-finish".to_owned()),
                    },
                },
                Criteria::State {
                    state: StateCriterion::HasConverged,
                },
                Criteria::State {
                    state: StateCriterion::CleanAndCurrent,
                },
            ],
        },
        agent_plan: AgentPlan {
            reviewers: Vec::new(),
            fixers: Vec::new(),
            judge: None,
            finishers: vec![baseline_target(
                "finisher-codex",
                AgentRole::Finish,
                "codex",
            )],
        },
    }
}

fn judge_plan() -> AgentPlan {
    AgentPlan {
        reviewers: Vec::new(),
        fixers: Vec::new(),
        judge: Some(baseline_target("judge-glm", AgentRole::Judge, "glm")),
        finishers: Vec::new(),
    }
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

fn baseline_reviewers() -> Vec<AgentLaunchTarget> {
    vec![
        baseline_target("reviewer-codex", AgentRole::Reviewer, "codex"),
        baseline_target("reviewer-claude", AgentRole::Reviewer, "claude"),
    ]
}

fn baseline_target(agent_id: &str, role: AgentRole, family: &str) -> AgentLaunchTarget {
    AgentLaunchTarget {
        agent_id: AgentId(agent_id.to_owned()),
        role,
        engine: baseline_engine_for_family(family),
        vendor: "baseline".to_owned(),
        control_plane: "pump19-core".to_owned(),
        lineage: ModelLineage {
            family: ModelFamily(family.to_owned()),
            model: baseline_model_for_family(family),
        },
    }
}

fn baseline_engine_for_family(family: &str) -> AgentEngine {
    match family {
        "claude" => AgentEngine::Claude,
        "codex" => AgentEngine::Codex,
        _ => AgentEngine::Opencode,
    }
}

fn baseline_model_for_family(family: &str) -> String {
    match family {
        "glm" => "openrouter/z-ai/glm-4.6".to_owned(),
        _ => format!("{family}-stable"),
    }
}

/// Returns the compact baseline prompt templates for review, judge, and fix runs.
#[must_use]
pub fn baseline_prompt_templates() -> Vec<PromptTemplate> {
    vec![
        PromptTemplate {
            id: "review-contract-findings".to_owned(),
            run_kind: RunKind::Review,
            template: "You are an independent Pump-19 judgement reviewer.
Subject: {{subject_name}} ({{subject_slug}})
Purpose: {{subject_purpose}}

## Task
Review this Pump-19 judgement brief independently. Return JSON matching the schema.

## Verdict
Use status \"failed\" when you find a material concern. Use status \"passed\" only when the brief is satisfied. Put the short reason in stdout and diagnostics, if any, in stderr.

<brief id=\"{{brief_id}}\" title=\"{{brief_title}}\">
{{brief_body}}
</brief>

Evidence:
{{evidence}}"
                .to_owned(),
            extensions: Extensions::new(),
        },
        PromptTemplate {
            id: "judge-materiality".to_owned(),
            run_kind: RunKind::Judge,
            template: "## Task
Judge whether each finding is material enough to justify another fix pass.

<findings>
{{findings}}
</findings>

<loop_history>
{{loop_context}}
</loop_history>"
                .to_owned(),
            extensions: Extensions::new(),
        },
        PromptTemplate {
            id: "fix-material-findings".to_owned(),
            run_kind: RunKind::Fix,
            template: "## Task
Produce a fix description for the material findings. Return JSON matching the schema.

<material_findings>
{{material_findings}}
</material_findings>"
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
  agent(brief.prompt, optionsFor(reviewer, `${reviewer.agent_id}:${brief.id}`))
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
  args.prompt,
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

if ((args.current_findings || args.findings || []).length > 0 && result.decisions.length === 0) {
  throw new Error("judge returned no decisions for standing findings");
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
  args.prompt,
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
    if pack.manifest.prompt_templates.is_empty() {
        return Err(AdaptationError::EmptyCollection {
            unit_kind: "prompt pack",
            id: pack.manifest.id.clone(),
            items: "prompt templates",
        });
    }
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
    use std::{fs, path::PathBuf, process::Command};

    #[cfg(unix)]
    use std::os::unix::fs::PermissionsExt as _;

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
    use tempfile::tempdir;

    use super::{
        AdaptationError, AdaptationSchemaVersion, MechanicalExecution, MechanicalPack,
        MechanicalStep, MechanicalStepKind, TriggerPack, baseline_mechanical_pack,
        baseline_trigger_pack, load_mechanical_pack, load_prompt_pack, load_trigger_rules,
        write_baseline_deployment_assets, write_baseline_forgejo_commands,
        write_baseline_mechanical_pack, write_baseline_prompt_pack, write_baseline_trigger_pack,
        write_toml,
    };

    #[test]
    fn baseline_prompt_pack_loads_schema_only_review_template()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let manifest = write_baseline_prompt_pack(dir.path(), "baseline-review")?;

        let pack = load_prompt_pack(&manifest)?;

        assert_eq!(pack.manifest.id, "baseline-review");
        assert_eq!(pack.manifest.prompt_templates.len(), 3);
        assert_eq!(pack.manifest.workflow_scripts.len(), 3);
        assert_eq!(pack.briefs.len(), 3);
        let review_template = &pack.manifest.prompt_templates[0].template;
        assert!(review_template.contains("Return JSON matching the schema."));
        assert!(review_template.contains("Put the short reason in stdout"));
        assert!(!review_template.contains("PUMP19_JUDGEMENT"));
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
    fn baseline_trigger_pack_pins_review_loop_composition() -> Result<(), Box<dyn std::error::Error>>
    {
        let dir = tempdir()?;
        let pack_path = write_baseline_trigger_pack(dir.path(), "baseline-loop")?;
        let pack = baseline_trigger_pack("baseline-loop");
        let loaded = load_trigger_rules(&pack_path)?;
        let rule_ids = loaded
            .iter()
            .map(|rule| rule.id.as_str())
            .collect::<Vec<_>>();

        assert_eq!(pack.rules, loaded);
        assert_eq!(
            rule_ids,
            vec![
                "review-on-pr-change",
                "judge-after-review",
                "judge-after-noop-fix",
                "fix-after-material-judge",
                "finish-on-label",
            ]
        );
        assert!(matches!(
            loaded[0].criteria,
            Criteria::Any { ref criteria }
                if matches!(
                    criteria.as_slice(),
                    [
                        Criteria::Event {
                            event: EventKind::PullRequestOpened
                        },
                        Criteria::Event {
                            event: EventKind::PullRequestUpdated
                        },
                    ]
                )
        ));
        assert!(loaded.iter().any(|rule| matches!(
            &rule.criteria,
            Criteria::Event {
                event: EventKind::RunCompleted {
                    run_kind: Some(RunKind::Fix),
                    outcome: Some(RunOutcome::NoOp),
                }
            }
        )));
        assert!(!loaded.iter().any(|rule| matches!(
            &rule.criteria,
            Criteria::Event {
                event: EventKind::RunCompleted {
                    run_kind: Some(RunKind::Fix),
                    outcome: Some(RunOutcome::Succeeded),
                }
            } if rule.run_kind == RunKind::Review
        )));
        Ok(())
    }

    #[test]
    fn baseline_mechanical_pack_pins_source_preparation_contract()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let pack_path = write_baseline_mechanical_pack(dir.path(), "baseline-mechanical")?;
        let loaded = load_mechanical_pack(&pack_path)?;

        assert_eq!(loaded, baseline_mechanical_pack("baseline-mechanical"));
        assert!(matches!(
            loaded.steps.as_slice(),
            [MechanicalStep {
                id,
                kind: MechanicalStepKind::Checkout,
                execution: MechanicalExecution::Command { program, args },
                ..
            }] if id == "prepare-source"
                && program == "pump19-prepare-source"
                && args.is_empty()
        ));
        Ok(())
    }

    #[test]
    fn checked_in_deployment_assets_are_generated_from_baselines()
    -> Result<(), Box<dyn std::error::Error>> {
        let generated = tempdir()?;
        let assets = write_baseline_deployment_assets(generated.path())?;
        let checked_in = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../..")
            .join("examples/deployment");

        assert_eq!(
            assets.packs.trigger_pack,
            generated.path().join("adaptations/trigger/triggers.toml")
        );
        assert_directories_match(
            &generated.path().join("adaptations"),
            &checked_in.join("adaptations"),
        )?;
        assert_directories_match(
            &generated.path().join("commands"),
            &checked_in.join("commands"),
        )?;
        Ok(())
    }

    #[test]
    fn baseline_forgejo_poll_command_emits_snapshot_contract()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let commands = write_baseline_forgejo_commands(&dir.path().join("commands"))?;
        let fixtures = dir.path().join("fixtures");
        fs::create_dir_all(&fixtures)?;
        fs::write(
            fixtures.join("user.json"),
            r#"{"id":7,"login":"pump19","full_name":"Pump 19"}"#,
        )?;
        fs::write(
            fixtures.join("pulls.json"),
            r#"[{"number":42,"head":{"sha":"abc123"},"base":{"sha":"def456"},"merge_base":"def456","mergeable":true,"labels":[{"name":"pump19-finish"}]}]"#,
        )?;
        fs::write(
            fixtures.join("timeline.json"),
            r#"[{"label":{"name":"pump19-finish"},"user":{"id":7,"login":"pump19","full_name":"Pump 19"}}]"#,
        )?;
        let fake_curl = write_fake_curl(
            dir.path(),
            r#"case "$url" in
  */api/v1/user) cat "$PUMP19_FIXTURES/user.json" ;;
  */api/v1/repos/acme/widgets/pulls\?state=open) cat "$PUMP19_FIXTURES/pulls.json" ;;
  */api/v1/repos/acme/widgets/issues/42/timeline) cat "$PUMP19_FIXTURES/timeline.json" ;;
  *) echo "unexpected URL: $url" >&2; exit 44 ;;
esac
"#,
        )?;

        let output = Command::new(&commands.poll_command)
            .args([
                "--base-url",
                "https://forgejo.example",
                "--finish-label",
                "pump19-finish",
                "acme/widgets",
            ])
            .env("FORGEJO_TOKEN", "test-token")
            .env("PUMP19_FORGEJO_CURL", fake_curl)
            .env("PUMP19_FIXTURES", &fixtures)
            .output()?;

        assert_command_success(&output);
        let snapshots: serde_json::Value = serde_json::from_slice(&output.stdout)?;
        assert_eq!(snapshots[0]["repository"], "acme/widgets");
        assert_eq!(snapshots[0]["id"], "42");
        assert_eq!(snapshots[0]["head_sha"], "abc123");
        assert_eq!(snapshots[0]["branch_currency"], "current");
        assert_eq!(snapshots[0]["cleanliness"], "clean");
        assert_eq!(snapshots[0]["mergeability"], "mergeable");
        assert_eq!(snapshots[0]["labels"][0]["applied_by"]["id"], "7");
        Ok(())
    }

    #[test]
    fn baseline_forgejo_operation_command_posts_comment_receipt()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let commands = write_baseline_forgejo_commands(&dir.path().join("commands"))?;
        let fake_curl = write_fake_curl(
            dir.path(),
            r#"case "$url" in
  */api/v1/repos/acme/widgets/pulls/42) printf '{"head":{"sha":"abc123"}}' ;;
  */api/v1/repos/acme/widgets/issues/42/comments) printf '{"id":12345}' ;;
  *) echo "unexpected URL: $url" >&2; exit 44 ;;
esac
"#,
        )?;
        let input = serde_json::json!({
            "operation": "post_comment",
            "pr": {"repository": "acme/widgets", "id": "42"},
            "body": "Pump-19 finding",
            "metadata": {
                "observed_head_sha": "abc123",
                "expected_head_sha": "abc123",
                "idempotency_key": "comment-1",
                "reason": "publish finding"
            }
        });

        let output = run_operation_command(&commands.operation_command, &fake_curl, &input)?;

        assert_command_success(&output);
        let receipt: serde_json::Value = serde_json::from_slice(&output.stdout)?;
        assert_eq!(receipt["operation_id"], "12345");
        assert!(receipt["new_head_sha"].is_null());
        Ok(())
    }

    #[test]
    fn baseline_forgejo_operation_command_pushes_fix_commit_to_local_pr_head()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let commands = write_baseline_forgejo_commands(&dir.path().join("commands"))?;
        let git_root = dir.path().join("git");
        let bare_repo = git_root.join("acme/widgets.git");
        let work_repo = dir.path().join("work");
        fs::create_dir_all(bare_repo.parent().expect("bare parent"))?;
        run_git(["init", "--bare", bare_repo.to_str().expect("bare path")]);
        run_git(["init", work_repo.to_str().expect("work path")]);
        run_git_in(&work_repo, ["config", "user.name", "Fixture"]);
        run_git_in(
            &work_repo,
            ["config", "user.email", "fixture@example.invalid"],
        );
        fs::write(work_repo.join("README.md"), "before\n")?;
        run_git_in(&work_repo, ["add", "README.md"]);
        run_git_in(&work_repo, ["commit", "-m", "initial"]);
        run_git_in(&work_repo, ["checkout", "-b", "feature"]);
        run_git_in(
            &work_repo,
            [
                "remote",
                "add",
                "origin",
                bare_repo.to_str().expect("bare path"),
            ],
        );
        run_git_in(&work_repo, ["push", "origin", "feature"]);
        let expected_head = git_stdout_in(&work_repo, ["rev-parse", "HEAD"]);
        let fake_curl = write_fake_curl(
            dir.path(),
            &format!(
                r#"case "$url" in
  */api/v1/repos/acme/widgets/pulls/42) printf '{{"head":{{"sha":"{expected_head}","ref":"feature"}}}}' ;;
  *) echo "unexpected URL: $url" >&2; exit 44 ;;
esac
"#
            ),
        )?;
        let input = serde_json::json!({
            "operation": "push_fix_commits",
            "pr": {"repository": "acme/widgets", "id": "42"},
            "expected_head_sha": expected_head,
            "commits": [{
                "patch_id": "patch-1",
                "message": "Apply Pump-19 fix",
                "author_agent_id": "fixer-a",
                "model_provenance_json": "{}",
                "change": {
                    "kind": "description",
                    "summary": "Fixture-only empty commit"
                }
            }],
            "metadata": {
                "observed_head_sha": expected_head,
                "expected_head_sha": expected_head,
                "idempotency_key": "push-1",
                "reason": "push fixture fix"
            }
        });

        let output = run_operation_command_with_args(
            &commands.operation_command,
            &fake_curl,
            &[
                "--base-url",
                "https://forgejo.example",
                "--git-base-url",
                git_root.to_str().expect("git root path"),
            ],
            &input,
        )?;

        assert_command_success(&output);
        let receipt: serde_json::Value = serde_json::from_slice(&output.stdout)?;
        let new_head = receipt["new_head_sha"]
            .as_str()
            .expect("receipt has new head");
        assert_ne!(new_head, expected_head);
        assert_eq!(
            git_stdout([
                "--git-dir",
                bare_repo.to_str().expect("bare path"),
                "rev-parse",
                "feature",
            ]),
            new_head
        );
        Ok(())
    }

    #[test]
    fn baseline_forgejo_operation_command_rejects_moved_head()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let commands = write_baseline_forgejo_commands(&dir.path().join("commands"))?;
        let fake_curl = write_fake_curl(
            dir.path(),
            r#"case "$url" in
  */api/v1/repos/acme/widgets/pulls/42) printf '{"head":{"sha":"new456"}}' ;;
  *) echo "unexpected URL: $url" >&2; exit 44 ;;
esac
"#,
        )?;
        let input = serde_json::json!({
            "operation": "post_comment",
            "pr": {"repository": "acme/widgets", "id": "42"},
            "body": "Pump-19 finding",
            "metadata": {
                "observed_head_sha": "abc123",
                "expected_head_sha": "abc123",
                "idempotency_key": "comment-1",
                "reason": "publish finding"
            }
        });

        let output = run_operation_command(&commands.operation_command, &fake_curl, &input)?;

        assert_eq!(output.status.code(), Some(75));
        let error: serde_json::Value = serde_json::from_slice(&output.stderr)?;
        assert_eq!(error["error"], "head_moved");
        assert_eq!(error["expected_head_sha"], "abc123");
        assert_eq!(error["actual_head_sha"], "new456");
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

    fn assert_directories_match(
        generated: &std::path::Path,
        checked_in: &std::path::Path,
    ) -> Result<(), Box<dyn std::error::Error>> {
        let generated_files = sorted_relative_files(generated)?;
        let checked_in_files = sorted_relative_files(checked_in)?;
        assert_eq!(generated_files, checked_in_files);
        for file in generated_files {
            let generated_text = fs::read_to_string(generated.join(&file))?;
            let checked_in_text = fs::read_to_string(checked_in.join(&file))?;
            assert_eq!(generated_text, checked_in_text, "{}", file.display());
        }
        Ok(())
    }

    fn sorted_relative_files(
        root: &std::path::Path,
    ) -> Result<Vec<PathBuf>, Box<dyn std::error::Error>> {
        let mut files = Vec::new();
        collect_relative_files(root, root, &mut files)?;
        files.sort();
        Ok(files)
    }

    fn collect_relative_files(
        root: &std::path::Path,
        dir: &std::path::Path,
        files: &mut Vec<PathBuf>,
    ) -> Result<(), Box<dyn std::error::Error>> {
        for entry in fs::read_dir(dir)? {
            let path = entry?.path();
            if path.is_dir() {
                collect_relative_files(root, &path, files)?;
            } else {
                files.push(path.strip_prefix(root)?.to_path_buf());
            }
        }
        Ok(())
    }

    fn write_fake_curl(
        root: &std::path::Path,
        response_cases: &str,
    ) -> Result<PathBuf, Box<dyn std::error::Error>> {
        let path = root.join("fake-curl");
        fs::write(
            &path,
            format!(
                r"#!/bin/sh
set -eu
url=
for arg do
  url=$arg
done
{response_cases}
"
            ),
        )?;
        set_executable(&path)?;
        Ok(path)
    }

    fn run_operation_command(
        command: &std::path::Path,
        fake_curl: &std::path::Path,
        input: &serde_json::Value,
    ) -> Result<std::process::Output, Box<dyn std::error::Error>> {
        run_operation_command_with_args(
            command,
            fake_curl,
            &["--base-url", "https://forgejo.example"],
            input,
        )
    }

    fn run_operation_command_with_args(
        command: &std::path::Path,
        fake_curl: &std::path::Path,
        args: &[&str],
        input: &serde_json::Value,
    ) -> Result<std::process::Output, Box<dyn std::error::Error>> {
        use std::io::Write as _;

        let mut child = Command::new(command)
            .args(args)
            .env("FORGEJO_TOKEN", "test-token")
            .env("PUMP19_FORGEJO_CURL", fake_curl)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()?;
        child
            .stdin
            .as_mut()
            .expect("operation command stdin")
            .write_all(input.to_string().as_bytes())?;
        Ok(child.wait_with_output()?)
    }

    fn run_git<const N: usize>(args: [&str; N]) {
        let output = Command::new("git").args(args).output().expect("run git");
        assert_command_success(&output);
    }

    fn run_git_in<const N: usize>(dir: &std::path::Path, args: [&str; N]) {
        let output = Command::new("git")
            .current_dir(dir)
            .args(args)
            .output()
            .expect("run git");
        assert_command_success(&output);
    }

    fn git_stdout<const N: usize>(args: [&str; N]) -> String {
        let output = Command::new("git").args(args).output().expect("run git");
        assert_command_success(&output);
        String::from_utf8(output.stdout)
            .expect("git stdout is UTF-8")
            .trim()
            .to_owned()
    }

    fn git_stdout_in<const N: usize>(dir: &std::path::Path, args: [&str; N]) -> String {
        let output = Command::new("git")
            .current_dir(dir)
            .args(args)
            .output()
            .expect("run git");
        assert_command_success(&output);
        String::from_utf8(output.stdout)
            .expect("git stdout is UTF-8")
            .trim()
            .to_owned()
    }

    fn assert_command_success(output: &std::process::Output) {
        assert!(
            output.status.success(),
            "status: {:?}\nstdout: {}\nstderr: {}",
            output.status.code(),
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
    }

    fn set_executable(path: &std::path::Path) -> Result<(), Box<dyn std::error::Error>> {
        #[cfg(unix)]
        {
            fs::set_permissions(path, fs::Permissions::from_mode(0o755))?;
        }
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
                judge: Some(target("judge", AgentRole::Judge, "glm")),
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
                model: model_for_family(family),
            },
        }
    }

    fn model_for_family(family: &str) -> String {
        match family {
            "glm" => "openrouter/z-ai/glm-4.6".to_owned(),
            _ => format!("{family}-stable"),
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
                ensemble_archive_path: None,
            })
        }
    }
}
