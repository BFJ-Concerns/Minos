#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "unit tests use temporary fixtures with direct assertions"
    )
)]

use std::{
    ffi::OsStr,
    fs,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};
use thiserror::Error;

const INTENT_FILE: &str = "pump19.intent.toml";
const VERIFICATION_DIR: &str = "verification";
const JUDGEMENT_DIR: &str = "judgement";
pub const JUDGEMENT_PASS_TOKEN: &str = "PUMP19_JUDGEMENT: PASS";
pub const JUDGEMENT_FAIL_TOKEN: &str = "PUMP19_JUDGEMENT: FAIL";

/// Errors raised while loading, preparing, or running judgement review.
#[derive(Debug, Error)]
pub enum JudgementError {
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
    #[error("JSON serialise error: {0}")]
    SerialiseJson(#[from] serde_json::Error),
}

/// The machine-readable intent needed by judgement prompts.
///
/// Pump-19's richer typed contract lands in a later unit. For this foundation,
/// intent stays deliberately small: enough to name the subject, carry the recorded
/// author, and ground baseline judgement briefs.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct IntentSpec {
    pub app: IntentApp,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub invariants: Vec<IntentStatement>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub behaviours: Vec<IntentStatement>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct IntentApp {
    pub slug: String,
    pub name: String,
    pub purpose: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub author_agent_id: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct IntentStatement {
    pub id: String,
    pub statement: String,
}

/// A single judgement instruction and the evidence files a reviewer should inspect.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct JudgementBrief {
    pub id: String,
    pub title: String,
    pub intent_ref: String,
    pub brief: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence_paths: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct JudgementRun {
    pub status: JudgementStatus,
    pub briefs: Vec<JudgementBriefResult>,
    pub model_families: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct JudgementBriefResult {
    pub brief_id: String,
    pub status: JudgementStatus,
    pub reviews: Vec<ReviewerResult>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ReviewerResult {
    pub agent_id: String,
    pub model_family: String,
    pub status: JudgementStatus,
    pub stdout: String,
    pub stderr: String,
}

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum JudgementStatus {
    Passed,
    Failed,
    #[default]
    NotRun,
}

#[must_use]
pub fn intent_path(root: &Path) -> PathBuf {
    root.join(INTENT_FILE)
}

#[must_use]
pub fn verification_dir(root: &Path) -> PathBuf {
    root.join(VERIFICATION_DIR)
}

#[must_use]
pub fn judgement_dir(root: &Path) -> PathBuf {
    verification_dir(root).join(JUDGEMENT_DIR)
}

/// Loads the subject's stated intent from `pump19.intent.toml`.
///
/// # Errors
///
/// Returns an error when the intent file cannot be read or parsed.
pub fn load_intent(root: &Path) -> Result<IntentSpec, JudgementError> {
    read_toml(&intent_path(root))
}

/// Saves the subject's stated intent to `pump19.intent.toml`.
///
/// # Errors
///
/// Returns an error when the file cannot be serialised or written.
pub fn save_intent(root: &Path, intent: &IntentSpec) -> Result<(), JudgementError> {
    write_toml(&intent_path(root), intent)
}

/// Installs the U1 judgement foundation into a repository.
///
/// This writes a compact intent file, baseline briefs, and working reviewer
/// defaults. It does not install deterministic oracle cases or any forge/run
/// machinery; those belong to later commissioned units.
///
/// # Errors
///
/// Returns an error when scaffold directories or TOML files cannot be written.
pub fn install_standalone(
    root: &Path,
    slug: &str,
    name: &str,
    purpose: &str,
    author_agent_id: Option<&str>,
) -> Result<(), JudgementError> {
    fs::create_dir_all(judgement_dir(root)).map_err(|source| JudgementError::Io {
        path: judgement_dir(root).display().to_string(),
        source,
    })?;

    save_intent(
        root,
        &IntentSpec {
            app: IntentApp {
                slug: slug.to_owned(),
                name: name.to_owned(),
                purpose: purpose.to_owned(),
                author_agent_id: author_agent_id.map(str::to_owned),
            },
            invariants: vec![
                IntentStatement {
                    id: "reviewer-independence".to_owned(),
                    statement:
                        "Reviewers must be independent of the recorded author and span model families."
                            .to_owned(),
                },
                IntentStatement {
                    id: "material-findings".to_owned(),
                    statement:
                        "Judgement should identify material correctness, safety, and maintainability risks."
                            .to_owned(),
                },
            ],
            behaviours: vec![IntentStatement {
                id: "purpose".to_owned(),
                statement: purpose.to_owned(),
            }],
        },
    )?;

    write_baseline_briefs(root)?;
    Ok(())
}

/// Returns Pump-19's compact baseline judgement briefs.
#[must_use]
pub fn baseline_judgement_briefs() -> Vec<JudgementBrief> {
    vec![
        JudgementBrief {
            id: "reviewer-independence".to_owned(),
            title: "Reviewer independence".to_owned(),
            intent_ref: "invariants.reviewer-independence".to_owned(),
            brief: "Judge whether the proposed verification setup preserves reviewer independence from the recorded author and uses at least two model families."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
        JudgementBrief {
            id: "material-findings".to_owned(),
            title: "Material findings".to_owned(),
            intent_ref: "invariants.material-findings".to_owned(),
            brief: "Judge whether the review identifies material correctness, safety, and maintainability issues rather than cosmetic noise."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
        JudgementBrief {
            id: "conformance-to-purpose".to_owned(),
            title: "Conformance to stated purpose".to_owned(),
            intent_ref: "behaviours.purpose".to_owned(),
            brief: "Judge whether the implementation meaningfully serves the subject purpose supplied in the review prompt."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
    ]
}

/// Writes Pump-19's baseline judgement briefs.
///
/// # Errors
///
/// Returns an error when any brief file cannot be serialised or written.
pub fn write_baseline_briefs(root: &Path) -> Result<(), JudgementError> {
    write_baseline_briefs_to_dir(&judgement_dir(root))
}

/// Writes Pump-19's baseline judgement briefs to a supplied directory.
///
/// # Errors
///
/// Returns an error when any brief file cannot be serialised or written.
pub fn write_baseline_briefs_to_dir(dir: &Path) -> Result<(), JudgementError> {
    for brief in baseline_judgement_briefs() {
        write_toml(&dir.join(format!("{}.toml", brief.id)), &brief)?;
    }
    Ok(())
}

/// Reads evidence files named by a judgement brief.
///
/// # Errors
///
/// Returns an error when any referenced evidence file cannot be read.
pub fn evidence_text(root: &Path, brief: &JudgementBrief) -> Result<String, JudgementError> {
    if brief.evidence_paths.is_empty() {
        return Ok("No separate evidence paths were supplied.".to_owned());
    }
    let mut text = String::new();
    for evidence_path in &brief.evidence_paths {
        let path = root.join(evidence_path);
        let content = fs::read_to_string(&path).map_err(|source| JudgementError::Io {
            path: path.display().to_string(),
            source,
        })?;
        text.push_str("\n--- ");
        text.push_str(evidence_path);
        text.push_str(" ---\n");
        text.push_str(&content);
    }
    Ok(text)
}

/// Loads judgement briefs from `verification/judgement/*.toml`.
///
/// # Errors
///
/// Returns an error when the directory cannot be read or a brief cannot be parsed.
pub fn load_judgement_briefs(root: &Path) -> Result<Vec<JudgementBrief>, JudgementError> {
    load_judgement_briefs_from_dir(&judgement_dir(root))
}

/// Loads judgement briefs from a supplied directory.
///
/// # Errors
///
/// Returns an error when the directory cannot be read or a brief cannot be parsed.
pub fn load_judgement_briefs_from_dir(dir: &Path) -> Result<Vec<JudgementBrief>, JudgementError> {
    load_toml_dir(dir)
}

fn load_toml_dir<T>(dir: &Path) -> Result<Vec<T>, JudgementError>
where
    T: for<'de> Deserialize<'de>,
{
    if !dir.exists() {
        return Ok(Vec::new());
    }
    let mut paths = fs::read_dir(dir)
        .map_err(|source| JudgementError::Io {
            path: dir.display().to_string(),
            source,
        })?
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| {
            path.extension()
                .is_some_and(|extension| extension == OsStr::new("toml"))
        })
        .collect::<Vec<_>>();
    paths.sort();
    paths.iter().map(|path| read_toml(path)).collect()
}

fn read_toml<T>(path: &Path) -> Result<T, JudgementError>
where
    T: for<'de> Deserialize<'de>,
{
    let text = fs::read_to_string(path).map_err(|source| JudgementError::Io {
        path: path.display().to_string(),
        source,
    })?;
    toml::from_str(&text).map_err(|source| JudgementError::ParseToml {
        path: path.display().to_string(),
        source,
    })
}

fn write_toml<T>(path: &Path, value: &T) -> Result<(), JudgementError>
where
    T: Serialize,
{
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|source| JudgementError::Io {
            path: parent.display().to_string(),
            source,
        })?;
    }
    let text = toml::to_string_pretty(value)?;
    fs::write(path, text).map_err(|source| JudgementError::Io {
        path: path.display().to_string(),
        source,
    })
}

#[cfg(test)]
mod tests {
    use std::fs;

    use tempfile::tempdir;

    use super::{
        IntentApp, IntentSpec, IntentStatement, JUDGEMENT_FAIL_TOKEN, JUDGEMENT_PASS_TOKEN,
        JudgementBrief, install_standalone, judgement_dir, load_judgement_briefs, save_intent,
        write_toml,
    };

    fn intent(author_agent_id: Option<&str>) -> IntentSpec {
        IntentSpec {
            app: IntentApp {
                slug: "sample".to_owned(),
                name: "Sample".to_owned(),
                purpose: "Prove extracted judgement is standalone".to_owned(),
                author_agent_id: author_agent_id.map(str::to_owned),
            },
            invariants: Vec::new(),
            behaviours: vec![IntentStatement {
                id: "purpose".to_owned(),
                statement: "The sample exists to test judgement extraction.".to_owned(),
            }],
        }
    }

    #[test]
    fn standalone_install_writes_intent_and_baseline_briefs()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        install_standalone(
            dir.path(),
            "sample",
            "Sample",
            "Prove standalone judgement installs",
            Some("author"),
        )?;

        assert!(dir.path().join("pump19.intent.toml").exists());
        assert!(
            dir.path()
                .join("verification/judgement/reviewer-independence.toml")
                .exists()
        );
        assert_eq!(load_judgement_briefs(dir.path())?.len(), 3);
        Ok(())
    }

    #[test]
    fn evidence_text_includes_referenced_evidence() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        fs::write(dir.path().join("evidence.txt"), "Observed rendered output")?;
        let evidence = super::evidence_text(
            dir.path(),
            &JudgementBrief {
                id: "purpose".to_owned(),
                title: "Purpose".to_owned(),
                intent_ref: "behaviours.purpose".to_owned(),
                brief: "Judge the implementation against its purpose.".to_owned(),
                evidence_paths: vec!["evidence.txt".to_owned()],
            },
        )?;

        assert!(evidence.contains("Observed rendered output"));
        Ok(())
    }

    #[test]
    fn judgement_tokens_have_single_contract_spelling() {
        assert_eq!(JUDGEMENT_PASS_TOKEN, "PUMP19_JUDGEMENT: PASS");
        assert_eq!(JUDGEMENT_FAIL_TOKEN, "PUMP19_JUDGEMENT: FAIL");
    }

    #[test]
    fn judgement_briefs_load_from_toml() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        save_intent(dir.path(), &intent(Some("author")))?;
        write_toml(
            &judgement_dir(dir.path()).join("purpose.toml"),
            &JudgementBrief {
                id: "purpose".to_owned(),
                title: "Purpose".to_owned(),
                intent_ref: "behaviours.purpose".to_owned(),
                brief: "Judge the implementation against its purpose.".to_owned(),
                evidence_paths: Vec::new(),
            },
        )?;

        let briefs = load_judgement_briefs(dir.path())?;

        assert_eq!(briefs.len(), 1);
        assert_eq!(briefs[0].id, "purpose");
        Ok(())
    }
}
