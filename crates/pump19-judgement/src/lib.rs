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
    collections::BTreeSet,
    ffi::OsStr,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::{Deserialize, Serialize};
use thiserror::Error;

const INTENT_FILE: &str = "pump19.intent.toml";
const VERIFICATION_DIR: &str = "verification";
const JUDGEMENT_DIR: &str = "judgement";
const REVIEWERS_FILE: &str = "reviewers.toml";
const PROMPT_TOKEN: &str = "{prompt}";
const BRIEF_ID_TOKEN: &str = "{brief_id}";
const JUDGEMENT_PASS_TOKEN: &str = "PUMP19_JUDGEMENT: PASS";
const JUDGEMENT_FAIL_TOKEN: &str = "PUMP19_JUDGEMENT: FAIL";
const LEGACY_WIDGET_PASS_TOKEN: &str = "WIDGET_JUDGEMENT: PASS";
const LEGACY_WIDGET_FAIL_TOKEN: &str = "WIDGET_JUDGEMENT: FAIL";

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
    #[error("reviewer {agent_id:?} has an empty command")]
    EmptyReviewerCommand { agent_id: String },
    #[error("reviewer {agent_id:?} is the recorded author {author_agent_id:?}")]
    ReviewerIsAuthor {
        agent_id: String,
        author_agent_id: String,
    },
    #[error("judgement reviewers must span at least two model families; got {families:?}")]
    NotEnoughModelFamilies { families: Vec<String> },
    #[error("judgement command for {label:?} failed to start: {source}")]
    CommandStart {
        label: String,
        #[source]
        source: std::io::Error,
    },
    #[error("judgement command for {label:?} produced non-UTF-8 stdout")]
    NonUtf8Stdout { label: String },
    #[error("judgement command for {label:?} produced non-UTF-8 stderr")]
    NonUtf8Stderr { label: String },
    #[error("system clock is before UNIX epoch")]
    Clock,
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

/// Reviewer launch configuration.
///
/// This remains a working default surface for U1. Later adaptation units own the
/// user-configurable prompt and command policy; the invariant enforcement here is
/// intentionally not delegated to that future surface.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ReviewerConfig {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub reviewers: Vec<Reviewer>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Reviewer {
    pub agent_id: String,
    pub model_family: String,
    pub command: Vec<String>,
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

#[must_use]
pub fn reviewer_config_path(root: &Path) -> PathBuf {
    verification_dir(root).join(REVIEWERS_FILE)
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
    write_toml(
        &reviewer_config_path(root),
        &ReviewerConfig {
            reviewers: vec![
                Reviewer {
                    agent_id: "codex-reviewer".to_owned(),
                    model_family: "codex".to_owned(),
                    command: vec![
                        "codex".to_owned(),
                        "exec".to_owned(),
                        "--sandbox".to_owned(),
                        "read-only".to_owned(),
                        "--ignore-rules".to_owned(),
                        PROMPT_TOKEN.to_owned(),
                    ],
                },
                Reviewer {
                    agent_id: "claude-reviewer".to_owned(),
                    model_family: "claude".to_owned(),
                    command: vec![
                        "claude".to_owned(),
                        "--print".to_owned(),
                        "--permission-mode".to_owned(),
                        "dontAsk".to_owned(),
                        PROMPT_TOKEN.to_owned(),
                    ],
                },
            ],
        },
    )?;
    Ok(())
}

/// Writes Pump-19's baseline judgement briefs.
///
/// # Errors
///
/// Returns an error when any brief file cannot be serialised or written.
pub fn write_baseline_briefs(root: &Path) -> Result<(), JudgementError> {
    let briefs = [
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
            brief: "Judge whether the implementation meaningfully serves the stated purpose in pump19.intent.toml."
                .to_owned(),
            evidence_paths: Vec::new(),
        },
    ];
    for brief in briefs {
        write_toml(
            &judgement_dir(root).join(format!("{}.toml", brief.id)),
            &brief,
        )?;
    }
    Ok(())
}

/// Runs all judgement briefs through the configured independent reviewers.
///
/// # Errors
///
/// Returns an error when files cannot be read, reviewer independence is invalid, or a reviewer
/// command cannot run.
pub fn run_judgement(root: &Path) -> Result<JudgementRun, JudgementError> {
    let intent = load_intent(root)?;
    let reviewers = load_reviewer_config(root)?;
    validate_reviewers(&intent, &reviewers)?;
    let briefs = load_judgement_briefs(root)?;
    let mut brief_results = Vec::new();
    for brief in briefs {
        brief_results.push(run_brief(root, &intent, &reviewers.reviewers, &brief)?);
    }
    let status = if brief_results
        .iter()
        .any(|result| result.status == JudgementStatus::Failed)
    {
        JudgementStatus::Failed
    } else {
        JudgementStatus::Passed
    };
    let families = reviewer_families(&reviewers.reviewers);
    Ok(JudgementRun {
        status,
        briefs: brief_results,
        model_families: families,
    })
}

/// Runs one brief through every configured reviewer.
///
/// # Errors
///
/// Returns an error when evidence cannot be read or a reviewer command cannot run.
pub fn run_brief(
    root: &Path,
    intent: &IntentSpec,
    reviewers: &[Reviewer],
    brief: &JudgementBrief,
) -> Result<JudgementBriefResult, JudgementError> {
    let prompt = judgement_prompt(root, intent, brief)?;
    let mut reviews = Vec::new();
    for reviewer in reviewers {
        reviews.push(run_reviewer(root, reviewer, &brief.id, &prompt)?);
    }
    let status = if reviews
        .iter()
        .any(|review| review.status == JudgementStatus::Failed)
    {
        JudgementStatus::Failed
    } else {
        JudgementStatus::Passed
    };
    Ok(JudgementBriefResult {
        brief_id: brief.id.clone(),
        status,
        reviews,
    })
}

/// Runs one reviewer command with the prompt and brief-id tokens expanded.
///
/// # Errors
///
/// Returns an error when the command is empty, cannot start, or emits non-UTF-8 output.
pub fn run_reviewer(
    root: &Path,
    reviewer: &Reviewer,
    brief_id: &str,
    prompt: &str,
) -> Result<ReviewerResult, JudgementError> {
    let (program, args) =
        reviewer
            .command
            .split_first()
            .ok_or_else(|| JudgementError::EmptyReviewerCommand {
                agent_id: reviewer.agent_id.clone(),
            })?;
    let expanded_args = args
        .iter()
        .map(|arg| {
            arg.replace(PROMPT_TOKEN, prompt)
                .replace(BRIEF_ID_TOKEN, brief_id)
        })
        .collect::<Vec<_>>();
    let output = Command::new(program)
        .args(expanded_args)
        .current_dir(root)
        .output()
        .map_err(|source| JudgementError::CommandStart {
            label: reviewer.agent_id.clone(),
            source,
        })?;
    let stdout = String::from_utf8(output.stdout).map_err(|_| JudgementError::NonUtf8Stdout {
        label: reviewer.agent_id.clone(),
    })?;
    let stderr = String::from_utf8(output.stderr).map_err(|_| JudgementError::NonUtf8Stderr {
        label: reviewer.agent_id.clone(),
    })?;
    let reviewer_passed = stdout.contains(JUDGEMENT_PASS_TOKEN)
        || stderr.contains(JUDGEMENT_PASS_TOKEN)
        || stdout.contains(LEGACY_WIDGET_PASS_TOKEN)
        || stderr.contains(LEGACY_WIDGET_PASS_TOKEN);
    let reviewer_failed = stdout.contains(JUDGEMENT_FAIL_TOKEN)
        || stderr.contains(JUDGEMENT_FAIL_TOKEN)
        // Widget-era reviewer commands may still exist in early migration fixtures.
        || stdout.contains(LEGACY_WIDGET_FAIL_TOKEN)
        || stderr.contains(LEGACY_WIDGET_FAIL_TOKEN);
    let status = if !output.status.success()
        || reviewer_failed
        // Reviewers must say what they decided. A silent zero exit is operationally
        // successful, but it is not a judgement.
        || !reviewer_passed
    {
        JudgementStatus::Failed
    } else {
        JudgementStatus::Passed
    };
    Ok(ReviewerResult {
        agent_id: reviewer.agent_id.clone(),
        model_family: reviewer.model_family.clone(),
        status,
        stdout,
        stderr,
    })
}

/// Builds the prompt given to an independent judgement reviewer.
///
/// # Errors
///
/// Returns an error when a referenced evidence path cannot be read.
pub fn judgement_prompt(
    root: &Path,
    intent: &IntentSpec,
    brief: &JudgementBrief,
) -> Result<String, JudgementError> {
    let evidence = evidence_text(root, brief)?;
    Ok(format!(
        "You are an independent Pump-19 judgement reviewer.\n\
         Subject: {} ({})\n\
         Purpose: {}\n\
         Brief {}: {}\n\
         {}\n\n\
         Evidence:\n{}\n\n\
         Reply with {JUDGEMENT_PASS_TOKEN} or {JUDGEMENT_FAIL_TOKEN} and one short reason.",
        intent.app.name,
        intent.app.slug,
        intent.app.purpose,
        brief.id,
        brief.title,
        brief.brief,
        evidence
    ))
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

/// Validates that judgement reviewers are independent of the author and span model families.
///
/// # Errors
///
/// Returns an error when a reviewer is the recorded author or fewer than two model families are
/// configured.
pub fn validate_reviewers(
    intent: &IntentSpec,
    config: &ReviewerConfig,
) -> Result<(), JudgementError> {
    if let Some(author) = &intent.app.author_agent_id {
        for reviewer in &config.reviewers {
            if &reviewer.agent_id == author {
                return Err(JudgementError::ReviewerIsAuthor {
                    agent_id: reviewer.agent_id.clone(),
                    author_agent_id: author.clone(),
                });
            }
        }
    }
    let families = reviewer_families(&config.reviewers);
    if families.len() < 2 {
        return Err(JudgementError::NotEnoughModelFamilies { families });
    }
    Ok(())
}

/// Loads judgement briefs from `verification/judgement/*.toml`.
///
/// # Errors
///
/// Returns an error when the directory cannot be read or a brief cannot be parsed.
pub fn load_judgement_briefs(root: &Path) -> Result<Vec<JudgementBrief>, JudgementError> {
    load_toml_dir(&judgement_dir(root))
}

/// Loads reviewer configuration from `verification/reviewers.toml`.
///
/// # Errors
///
/// Returns an error when the file cannot be read or parsed.
pub fn load_reviewer_config(root: &Path) -> Result<ReviewerConfig, JudgementError> {
    read_toml(&reviewer_config_path(root))
}

/// Writes a JSON run artifact under `verification/runs`.
///
/// # Errors
///
/// Returns an error when the runs directory cannot be created, the value cannot be serialised, or
/// the artifact cannot be written.
pub fn write_json_run<T>(root: &Path, prefix: &str, value: &T) -> Result<PathBuf, JudgementError>
where
    T: Serialize,
{
    let runs = verification_dir(root).join("runs");
    fs::create_dir_all(&runs).map_err(|source| JudgementError::Io {
        path: runs.display().to_string(),
        source,
    })?;
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|_| JudgementError::Clock)?
        .as_secs();
    let path = runs.join(format!("{prefix}-{now}.json"));
    let text = serde_json::to_string_pretty(value)?;
    fs::write(&path, text).map_err(|source| JudgementError::Io {
        path: path.display().to_string(),
        source,
    })?;
    Ok(path)
}

fn reviewer_families(reviewers: &[Reviewer]) -> Vec<String> {
    reviewers
        .iter()
        .map(|reviewer| reviewer.model_family.clone())
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect()
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
        IntentApp, IntentSpec, IntentStatement, JudgementBrief, JudgementError, JudgementStatus,
        Reviewer, ReviewerConfig, install_standalone, judgement_dir, judgement_prompt,
        load_judgement_briefs, load_reviewer_config, run_judgement, save_intent,
        validate_reviewers, write_toml,
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
    fn judgement_reviewers_must_exclude_recorded_author() {
        let intent = intent(Some("author"));
        let config = ReviewerConfig {
            reviewers: vec![
                Reviewer {
                    agent_id: "author".to_owned(),
                    model_family: "codex".to_owned(),
                    command: vec!["true".to_owned()],
                },
                Reviewer {
                    agent_id: "claude-reviewer".to_owned(),
                    model_family: "claude".to_owned(),
                    command: vec!["true".to_owned()],
                },
            ],
        };

        assert!(matches!(
            validate_reviewers(&intent, &config),
            Err(JudgementError::ReviewerIsAuthor { .. })
        ));
    }

    #[test]
    fn judgement_reviewers_must_span_model_families() {
        let config = ReviewerConfig {
            reviewers: vec![
                Reviewer {
                    agent_id: "codex-a".to_owned(),
                    model_family: "codex".to_owned(),
                    command: vec!["true".to_owned()],
                },
                Reviewer {
                    agent_id: "codex-b".to_owned(),
                    model_family: "codex".to_owned(),
                    command: vec!["true".to_owned()],
                },
            ],
        };

        assert!(matches!(
            validate_reviewers(&intent(Some("author")), &config),
            Err(JudgementError::NotEnoughModelFamilies { .. })
        ));
    }

    #[test]
    fn standalone_install_writes_intent_reviewers_and_baseline_briefs()
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
        assert!(dir.path().join("verification/reviewers.toml").exists());
        assert!(
            dir.path()
                .join("verification/judgement/reviewer-independence.toml")
                .exists()
        );
        assert_eq!(load_judgement_briefs(dir.path())?.len(), 3);
        assert_eq!(load_reviewer_config(dir.path())?.reviewers.len(), 2);
        Ok(())
    }

    #[test]
    fn prompt_includes_intent_brief_and_evidence() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        fs::write(dir.path().join("evidence.txt"), "Observed rendered output")?;
        let prompt = judgement_prompt(
            dir.path(),
            &intent(Some("author")),
            &JudgementBrief {
                id: "purpose".to_owned(),
                title: "Purpose".to_owned(),
                intent_ref: "behaviours.purpose".to_owned(),
                brief: "Judge the implementation against its purpose.".to_owned(),
                evidence_paths: vec!["evidence.txt".to_owned()],
            },
        )?;

        assert!(prompt.contains("Sample (sample)"));
        assert!(prompt.contains("Judge the implementation against its purpose."));
        assert!(prompt.contains("Observed rendered output"));
        assert!(prompt.contains("PUMP19_JUDGEMENT: PASS"));
        Ok(())
    }

    #[test]
    fn judgement_run_loads_toml_and_dispatches_reviewers() -> Result<(), Box<dyn std::error::Error>>
    {
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
        write_toml(
            &dir.path().join("verification/reviewers.toml"),
            &ReviewerConfig {
                reviewers: vec![
                    Reviewer {
                        agent_id: "codex-reviewer".to_owned(),
                        model_family: "codex".to_owned(),
                        command: vec![
                            "sh".to_owned(),
                            "-c".to_owned(),
                            "printf '%s\\n' 'PUMP19_JUDGEMENT: PASS codex'".to_owned(),
                        ],
                    },
                    Reviewer {
                        agent_id: "claude-reviewer".to_owned(),
                        model_family: "claude".to_owned(),
                        command: vec![
                            "sh".to_owned(),
                            "-c".to_owned(),
                            "printf '%s\\n' 'PUMP19_JUDGEMENT: PASS claude'".to_owned(),
                        ],
                    },
                ],
            },
        )?;

        let run = run_judgement(dir.path())?;

        assert_eq!(run.status, JudgementStatus::Passed);
        assert_eq!(run.model_families, ["claude", "codex"]);
        assert_eq!(run.briefs.len(), 1);
        assert_eq!(run.briefs[0].reviews.len(), 2);
        Ok(())
    }

    #[test]
    fn reviewer_fail_token_fails_the_brief() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let result = super::run_reviewer(
            dir.path(),
            &Reviewer {
                agent_id: "codex-reviewer".to_owned(),
                model_family: "codex".to_owned(),
                command: vec![
                    "sh".to_owned(),
                    "-c".to_owned(),
                    "printf '%s\\n' 'PUMP19_JUDGEMENT: FAIL found risk'".to_owned(),
                ],
            },
            "purpose",
            "prompt",
        )?;

        assert_eq!(result.status, JudgementStatus::Failed);
        Ok(())
    }

    #[test]
    fn legacy_widget_pass_token_still_passes_during_extraction()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let result = super::run_reviewer(
            dir.path(),
            &Reviewer {
                agent_id: "legacy-reviewer".to_owned(),
                model_family: "widget-era".to_owned(),
                command: vec![
                    "sh".to_owned(),
                    "-c".to_owned(),
                    "printf '%s\\n' 'WIDGET_JUDGEMENT: PASS no issue'".to_owned(),
                ],
            },
            "purpose",
            "prompt",
        )?;

        assert_eq!(result.status, JudgementStatus::Passed);
        Ok(())
    }

    #[test]
    fn reviewer_without_explicit_verdict_fails_closed() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let result = super::run_reviewer(
            dir.path(),
            &Reviewer {
                agent_id: "quiet-reviewer".to_owned(),
                model_family: "quiet".to_owned(),
                command: vec!["true".to_owned()],
            },
            "purpose",
            "prompt",
        )?;

        assert_eq!(result.status, JudgementStatus::Failed);
        Ok(())
    }
}
