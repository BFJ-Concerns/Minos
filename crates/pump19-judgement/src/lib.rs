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

/// A named intent statement supplied by site configuration and threaded into review prompts.
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
pub fn verification_dir(root: &Path) -> PathBuf {
    root.join(VERIFICATION_DIR)
}

#[must_use]
pub fn judgement_dir(root: &Path) -> PathBuf {
    verification_dir(root).join(JUDGEMENT_DIR)
}

/// Returns Pump-19's compact baseline judgement briefs.
#[must_use]
pub fn baseline_judgement_briefs() -> Vec<JudgementBrief> {
    vec![
        JudgementBrief {
            id: "reviewer-independence".to_owned(),
            title: "Reviewer independence".to_owned(),
            intent_ref: "invariants.reviewer-independence".to_owned(),
            brief: "Review whether this code change preserves reviewer independence, model provenance checks, and family separation wherever it touches launch, workflow, state, or adaptation seams."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
        JudgementBrief {
            id: "material-findings".to_owned(),
            title: "Material findings".to_owned(),
            intent_ref: "invariants.material-findings".to_owned(),
            brief: "Review the change for material correctness, safety, maintainability, isolation, error-handling, and contract issues worth another pass. Ignore purely cosmetic noise."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
        JudgementBrief {
            id: "conformance-to-purpose".to_owned(),
            title: "Conformance to stated purpose".to_owned(),
            intent_ref: "behaviours.purpose".to_owned(),
            brief: "Review whether the code change meaningfully serves the subject purpose, stays within the intended behaviour, and includes appropriate tests or proof for the touched logic."
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
        JUDGEMENT_FAIL_TOKEN, JUDGEMENT_PASS_TOKEN, JudgementBrief, judgement_dir,
        load_judgement_briefs, write_toml,
    };

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
