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
    fs,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};
use thiserror::Error;

const FRONTMATTER_DELIMITER: &str = "+++";
pub const DEFAULT_OCCASION: &str = "every-pr";
const WHOLE_REPO_SCOPE: &str = "**";

/// Errors raised while loading or writing review briefs.
#[derive(Debug, Error)]
pub enum ReviewBriefError {
    #[error("I/O error at {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("TOML serialise error: {0}")]
    SerialiseToml(#[from] toml::ser::Error),
}

/// Non-fatal issues found while loading review briefs.
///
/// A malformed brief must stay visible to the run rather than vanishing because
/// its metadata had a bad morning.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ReviewBriefWarning {
    pub path: PathBuf,
    pub message: String,
}

/// Loaded briefs plus non-fatal parse warnings.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct ReviewBriefLoad {
    pub briefs: Vec<ReviewBrief>,
    pub warnings: Vec<ReviewBriefWarning>,
}

/// The amount of attention a specialist pass should give a standing concern.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum ReviewBriefExtent {
    Focused,
    #[default]
    Standard,
    Deep,
}

/// A standing review criterion selected by occasion and changed-file scope.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ReviewBrief {
    pub id: String,
    pub title: String,
    pub body: String,
    pub scope: Vec<String>,
    pub extent: ReviewBriefExtent,
    pub run_condition: Vec<String>,
}

/// Namespaces applied when combining baseline, organisation, and repository briefs.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum BriefNamespace {
    Baseline,
    Organisation,
    Repository,
}

impl BriefNamespace {
    #[must_use]
    pub const fn prefix(self) -> &'static str {
        match self {
            Self::Baseline => "baseline",
            Self::Organisation => "org",
            Self::Repository => "repo",
        }
    }
}

/// Parses a markdown review brief using optional TOML `+++` frontmatter.
///
/// Malformed frontmatter produces a warning and a body-only every-PR brief.
#[must_use]
pub fn parse_review_brief(
    id: &str,
    markdown: &str,
    path: &Path,
) -> (ReviewBrief, Vec<ReviewBriefWarning>) {
    let (frontmatter, body, frontmatter_warnings) = split_frontmatter(markdown, path);
    let mut warnings = frontmatter_warnings;
    let metadata =
        frontmatter.and_then(
            |source| match toml::from_str::<ReviewBriefFrontmatter>(source) {
                Ok(parsed) => Some(parsed),
                Err(source) => {
                    warnings.push(ReviewBriefWarning {
                        path: path.to_path_buf(),
                        message: format!("TOML frontmatter parse failed: {source}"),
                    });
                    None
                }
            },
        );
    let title = metadata
        .as_ref()
        .and_then(|parsed| parsed.title.clone())
        .unwrap_or_else(|| id.to_owned());
    let scope = default_non_empty(
        metadata.as_ref().and_then(|parsed| parsed.scope.clone()),
        WHOLE_REPO_SCOPE,
    );
    let run_condition = default_non_empty(
        metadata
            .as_ref()
            .and_then(|parsed| parsed.run_condition.clone()),
        DEFAULT_OCCASION,
    );
    let extent = metadata
        .as_ref()
        .and_then(|parsed| parsed.extent)
        .unwrap_or_default();
    (
        ReviewBrief {
            id: id.to_owned(),
            title,
            body: body.trim().to_owned(),
            scope,
            extent,
            run_condition,
        },
        warnings,
    )
}

/// Loads markdown review briefs from a directory.
///
/// # Errors
///
/// Returns an error when the directory cannot be read for reasons other than
/// absence, or when a brief file cannot be read. Malformed frontmatter is
/// returned as warning data instead.
pub fn load_review_briefs_from_dir(dir: &Path) -> Result<ReviewBriefLoad, ReviewBriefError> {
    let read_dir = match fs::read_dir(dir) {
        Ok(read_dir) => read_dir,
        Err(source) if source.kind() == std::io::ErrorKind::NotFound => {
            return Ok(ReviewBriefLoad::default());
        }
        Err(source) => {
            return Err(ReviewBriefError::Io {
                path: dir.display().to_string(),
                source,
            });
        }
    };
    let mut entries =
        read_dir
            .collect::<Result<Vec<_>, _>>()
            .map_err(|source| ReviewBriefError::Io {
                path: dir.display().to_string(),
                source,
            })?;
    entries.sort_by_key(std::fs::DirEntry::path);

    let mut load = ReviewBriefLoad::default();
    for entry in entries {
        let path = entry.path();
        if path.extension().and_then(std::ffi::OsStr::to_str) != Some("md") {
            continue;
        }
        let markdown = fs::read_to_string(&path).map_err(|source| ReviewBriefError::Io {
            path: path.display().to_string(),
            source,
        })?;
        let id = path
            .file_stem()
            .and_then(std::ffi::OsStr::to_str)
            .unwrap_or_default();
        let (brief, mut warnings) = parse_review_brief(id, &markdown, &path);
        load.briefs.push(brief);
        load.warnings.append(&mut warnings);
    }
    Ok(load)
}

/// Returns whether a brief should run for an occasion and changed-file set.
#[must_use]
pub fn brief_matches_occasion(
    brief: &ReviewBrief,
    occasion: &str,
    changed_files: &[String],
) -> bool {
    brief
        .run_condition
        .iter()
        .any(|condition| condition == occasion)
        && scope_intersects_changed_files(&brief.scope, changed_files)
}

/// Filters briefs for a run's occasion and changed files.
#[must_use]
pub fn matching_briefs<'a>(
    briefs: &'a [ReviewBrief],
    occasion: &str,
    changed_files: &[String],
) -> Vec<&'a ReviewBrief> {
    briefs
        .iter()
        .filter(|brief| brief_matches_occasion(brief, occasion, changed_files))
        .collect()
}

/// Applies a source namespace to each brief id.
#[must_use]
pub fn namespace_briefs(namespace: BriefNamespace, briefs: &[ReviewBrief]) -> Vec<ReviewBrief> {
    briefs
        .iter()
        .map(|brief| {
            let mut namespaced = brief.clone();
            namespaced.id = format!("{}:{}", namespace.prefix(), brief.id);
            namespaced
        })
        .collect()
}

/// Merges baseline, organisation, and repository brief sets with source namespaces.
#[must_use]
pub fn merge_brief_sets(
    baseline: &[ReviewBrief],
    organisation: &[ReviewBrief],
    repository: &[ReviewBrief],
) -> Vec<ReviewBrief> {
    let mut merged = namespace_briefs(BriefNamespace::Baseline, baseline);
    merged.extend(namespace_briefs(BriefNamespace::Organisation, organisation));
    merged.extend(namespace_briefs(BriefNamespace::Repository, repository));
    merged
}

/// Returns Pump-19's shipped baseline review briefs.
#[must_use]
pub fn baseline_review_briefs() -> Vec<ReviewBrief> {
    vec![
        ReviewBrief {
            id: "reviewer-independence".to_owned(),
            title: "Reviewer independence".to_owned(),
            body: "Review whether this code change preserves reviewer independence, model provenance checks, and family separation wherever it touches launch, workflow, state, or adaptation seams."
                .to_owned(),
            scope: vec![WHOLE_REPO_SCOPE.to_owned()],
            extent: ReviewBriefExtent::Standard,
            run_condition: vec![DEFAULT_OCCASION.to_owned()],
        },
        ReviewBrief {
            id: "material-findings".to_owned(),
            title: "Material findings".to_owned(),
            body: "Review the change for material correctness, safety, maintainability, isolation, error-handling, and contract issues worth another pass. Ignore purely cosmetic noise."
                .to_owned(),
            scope: vec![WHOLE_REPO_SCOPE.to_owned()],
            extent: ReviewBriefExtent::Standard,
            run_condition: vec![DEFAULT_OCCASION.to_owned()],
        },
        ReviewBrief {
            id: "conformance-to-purpose".to_owned(),
            title: "Conformance to stated purpose".to_owned(),
            body: "Review whether the code change meaningfully serves the subject purpose, stays within the intended behaviour, and includes appropriate tests or proof for the touched logic."
                .to_owned(),
            scope: vec![WHOLE_REPO_SCOPE.to_owned()],
            extent: ReviewBriefExtent::Standard,
            run_condition: vec![DEFAULT_OCCASION.to_owned()],
        },
    ]
}

/// Writes Pump-19's shipped baseline review briefs as markdown files.
///
/// # Errors
///
/// Returns an error when any brief file cannot be serialised or written.
pub fn write_baseline_review_briefs_to_dir(dir: &Path) -> Result<(), ReviewBriefError> {
    fs::create_dir_all(dir).map_err(|source| ReviewBriefError::Io {
        path: dir.display().to_string(),
        source,
    })?;
    for brief in baseline_review_briefs() {
        write_review_brief(&dir.join(format!("{}.md", brief.id)), &brief)?;
    }
    Ok(())
}

fn split_frontmatter<'a>(
    markdown: &'a str,
    path: &Path,
) -> (Option<&'a str>, &'a str, Vec<ReviewBriefWarning>) {
    let trimmed_start = markdown.strip_prefix(FRONTMATTER_DELIMITER);
    let Some(after_open) = trimmed_start else {
        return (None, markdown, Vec::new());
    };
    let after_open = after_open.strip_prefix('\n').unwrap_or(after_open);
    if let Some((frontmatter, body)) = after_open.split_once("\n+++") {
        let body = body.strip_prefix('\n').unwrap_or(body);
        return (Some(frontmatter), body, Vec::new());
    }
    (
        None,
        markdown,
        vec![ReviewBriefWarning {
            path: path.to_path_buf(),
            message: "frontmatter opening delimiter has no closing delimiter".to_owned(),
        }],
    )
}

fn default_non_empty(values: Option<Vec<String>>, default: &str) -> Vec<String> {
    values
        .filter(|items| items.iter().any(|item| !item.trim().is_empty()))
        .unwrap_or_else(|| vec![default.to_owned()])
}

fn scope_intersects_changed_files(scope: &[String], changed_files: &[String]) -> bool {
    if scope.iter().any(|pattern| pattern == WHOLE_REPO_SCOPE) {
        return true;
    }
    changed_files
        .iter()
        .any(|path| scope.iter().any(|pattern| simple_glob_match(pattern, path)))
}

fn simple_glob_match(pattern: &str, path: &str) -> bool {
    if pattern == path || pattern == WHOLE_REPO_SCOPE {
        return true;
    }
    if let Some(prefix) = pattern.strip_suffix("/**") {
        return path == prefix || path.starts_with(&format!("{prefix}/"));
    }
    if let Some(suffix) = pattern.strip_prefix("**/") {
        return path.ends_with(suffix);
    }
    if let Some((prefix, suffix)) = pattern.split_once('*') {
        return path.starts_with(prefix) && path.ends_with(suffix);
    }
    false
}

fn write_review_brief(path: &Path, brief: &ReviewBrief) -> Result<(), ReviewBriefError> {
    let metadata = ReviewBriefFrontmatter {
        title: Some(brief.title.clone()),
        scope: Some(brief.scope.clone()),
        extent: Some(brief.extent),
        run_condition: Some(brief.run_condition.clone()),
    };
    let frontmatter = toml::to_string(&metadata)?;
    fs::write(
        path,
        format!(
            "{FRONTMATTER_DELIMITER}\n{frontmatter}{FRONTMATTER_DELIMITER}\n{}\n",
            brief.body
        ),
    )
    .map_err(|source| ReviewBriefError::Io {
        path: path.display().to_string(),
        source,
    })
}

#[derive(Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "kebab-case")]
struct ReviewBriefFrontmatter {
    title: Option<String>,
    scope: Option<Vec<String>>,
    extent: Option<ReviewBriefExtent>,
    run_condition: Option<Vec<String>>,
}

#[cfg(test)]
mod tests {
    use tempfile::tempdir;

    use super::{
        BriefNamespace, DEFAULT_OCCASION, ReviewBriefExtent, ReviewBriefLoad,
        baseline_review_briefs, brief_matches_occasion, load_review_briefs_from_dir,
        matching_briefs, merge_brief_sets, namespace_briefs, parse_review_brief,
        write_baseline_review_briefs_to_dir,
    };

    #[test]
    fn parses_markdown_frontmatter_and_body() {
        let markdown = "+++\ntitle = \"Migrations\"\nscope = [\"migrations/**\"]\nextent = \"deep\"\nrun-condition = [\"version-bump\"]\n+++\nCheck rollback paths.\n";

        let (brief, warnings) = parse_review_brief("migration", markdown, "migration.md".as_ref());

        assert!(warnings.is_empty());
        assert_eq!(brief.title, "Migrations");
        assert_eq!(brief.scope, ["migrations/**"]);
        assert_eq!(brief.extent, ReviewBriefExtent::Deep);
        assert_eq!(brief.run_condition, ["version-bump"]);
        assert_eq!(brief.body, "Check rollback paths.");
    }

    #[test]
    fn defaults_missing_frontmatter_to_every_pr_whole_repo() {
        let (brief, warnings) =
            parse_review_brief("general", "Read the diff.", "general.md".as_ref());

        assert!(warnings.is_empty());
        assert_eq!(brief.title, "general");
        assert_eq!(brief.scope, ["**"]);
        assert_eq!(brief.extent, ReviewBriefExtent::Standard);
        assert_eq!(brief.run_condition, [DEFAULT_OCCASION]);
        assert_eq!(brief.body, "Read the diff.");
    }

    #[test]
    fn malformed_frontmatter_is_warning_data_and_body_only() {
        let markdown = "+++\ntitle = [\n+++\nStill review this file.\n";

        let (brief, warnings) = parse_review_brief("broken", markdown, "broken.md".as_ref());

        assert_eq!(warnings.len(), 1);
        assert_eq!(brief.title, "broken");
        assert_eq!(brief.scope, ["**"]);
        assert_eq!(brief.run_condition, [DEFAULT_OCCASION]);
        assert_eq!(brief.body, "Still review this file.");
    }

    #[test]
    fn occasion_and_scope_both_have_to_match() {
        let (brief, _) = parse_review_brief(
            "rust",
            "+++\nscope = [\"crates/**\"]\nrun-condition = [\"every-pr\"]\n+++\nRust checks.\n",
            "rust.md".as_ref(),
        );

        assert!(brief_matches_occasion(
            &brief,
            "every-pr",
            &["crates/pump19-review/src/lib.rs".to_owned()]
        ));
        assert!(!brief_matches_occasion(
            &brief,
            "release",
            &["crates/pump19-review/src/lib.rs".to_owned()]
        ));
        assert!(!brief_matches_occasion(
            &brief,
            "every-pr",
            &["docs/deployment.md".to_owned()]
        ));
    }

    #[test]
    fn loads_markdown_briefs_from_directory_in_stable_order()
    -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        std::fs::write(dir.path().join("b.md"), "B")?;
        std::fs::write(dir.path().join("a.md"), "A")?;
        std::fs::write(dir.path().join("ignored.toml"), "nope")?;

        let load = load_review_briefs_from_dir(dir.path())?;

        assert!(load.warnings.is_empty());
        assert_eq!(
            load.briefs
                .iter()
                .map(|brief| brief.id.as_str())
                .collect::<Vec<_>>(),
            ["a", "b"]
        );
        Ok(())
    }

    #[test]
    fn missing_review_directory_loads_as_empty() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let missing = dir.path().join(".review");

        let load = load_review_briefs_from_dir(&missing)?;

        assert_eq!(load, ReviewBriefLoad::default());
        Ok(())
    }

    #[test]
    fn namespaces_and_merges_sources_without_overrides() {
        let baseline = baseline_review_briefs();
        let org = vec![baseline[0].clone()];
        let repo = vec![baseline[1].clone()];

        let merged = merge_brief_sets(&baseline[..1], &org, &repo);

        assert_eq!(
            merged
                .iter()
                .map(|brief| brief.id.as_str())
                .collect::<Vec<_>>(),
            [
                "baseline:reviewer-independence",
                "org:reviewer-independence",
                "repo:material-findings"
            ]
        );
        assert_eq!(
            namespace_briefs(BriefNamespace::Repository, &repo)[0].id,
            "repo:material-findings"
        );
    }

    #[test]
    fn matching_briefs_returns_only_applicable_briefs() {
        let briefs = baseline_review_briefs();

        let selected = matching_briefs(&briefs, DEFAULT_OCCASION, &["anything.rs".to_owned()]);

        assert_eq!(selected.len(), briefs.len());
    }

    #[test]
    fn writes_baseline_briefs_as_parseable_markdown() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;

        write_baseline_review_briefs_to_dir(dir.path())?;
        let load = load_review_briefs_from_dir(dir.path())?;
        let mut expected = baseline_review_briefs();
        expected.sort_by(|left, right| left.id.cmp(&right.id));

        assert!(load.warnings.is_empty());
        assert_eq!(load.briefs, expected);
        Ok(())
    }
}
