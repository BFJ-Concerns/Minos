use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::{Path, PathBuf},
    time::{Duration, SystemTime},
};

use pump19_contract::{ContractVersion, PrRunState, PullRequestRef, RunId, RunKind, RunRecord};
use pump19_core::{
    CoreError, JsonRunStateStore, OperatorLog, OperatorLogEvent, OperatorLogEventKind,
    RunStateStore,
};
use serde::{Deserialize, Serialize};

/// Daemon-owned retention policy for frame-run archives.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ArchiveRetentionConfig {
    /// Keep every terminal archive at least this many days.
    #[serde(default = "default_keep_days")]
    pub keep_days: u64,
    /// Keep at least this many terminal archives per pull request, regardless of age.
    #[serde(default = "default_keep_terminal_runs_per_pr")]
    pub keep_terminal_runs_per_pr: usize,
    /// Bound long-lived PR history after the age window has passed.
    #[serde(default = "default_max_terminal_runs_per_pr")]
    pub max_terminal_runs_per_pr: usize,
}

impl Default for ArchiveRetentionConfig {
    fn default() -> Self {
        Self {
            keep_days: default_keep_days(),
            keep_terminal_runs_per_pr: default_keep_terminal_runs_per_pr(),
            max_terminal_runs_per_pr: default_max_terminal_runs_per_pr(),
        }
    }
}

impl ArchiveRetentionConfig {
    const fn minimum_retained_per_pr(&self) -> usize {
        self.keep_terminal_runs_per_pr
    }

    fn age_cutoff(&self, now: SystemTime) -> SystemTime {
        now.checked_sub(Duration::from_secs(
            self.keep_days.saturating_mul(24 * 60 * 60),
        ))
        .unwrap_or(SystemTime::UNIX_EPOCH)
    }
}

const fn default_keep_days() -> u64 {
    14
}

const fn default_keep_terminal_runs_per_pr() -> usize {
    2
}

const fn default_max_terminal_runs_per_pr() -> usize {
    50
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct ArchiveRetentionSummary {
    pub candidates: usize,
    pub pruned: usize,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct ArchiveRetentionCandidate {
    pr: PullRequestRef,
    run_id: RunId,
    run_kind: RunKind,
    pass_index: u32,
    commit_sha: String,
    path: PathBuf,
    modified: SystemTime,
}

/// Applies archive retention once, recording each deletion in the operator log.
pub fn enforce_archive_retention(
    policy: &ArchiveRetentionConfig,
    state_root: &Path,
    frame_run_root: &Path,
    operator_log: &mut dyn OperatorLog,
    now: SystemTime,
) -> Result<ArchiveRetentionSummary, CoreError> {
    let store = JsonRunStateStore::new(state_root)?;
    let states = store.completion_recovery_states()?;
    let candidates = collect_candidates(&states, frame_run_root)?;
    let candidate_count = candidates.len();
    let pruned = plan_prunes(policy, candidates, now);
    for candidate in &pruned {
        operator_log.record(OperatorLogEvent {
            contract_version: ContractVersion::current(),
            kind: OperatorLogEventKind::ArchiveRetention,
            pr: Some(candidate.pr.clone()),
            run_id: Some(candidate.run_id.clone()),
            run_kind: Some(candidate.run_kind),
            pass_index: Some(candidate.pass_index),
            commit_sha: Some(candidate.commit_sha.clone()),
            message: format!("pruning frame-run archive {}", candidate.path.display()),
            refusal_reason: None,
        })?;
        fs::remove_dir_all(&candidate.path).map_err(|source| CoreError::Io {
            path: candidate.path.display().to_string(),
            source,
        })?;
    }
    Ok(ArchiveRetentionSummary {
        candidates: candidate_count,
        pruned: pruned.len(),
    })
}

fn collect_candidates(
    states: &[PrRunState],
    frame_run_root: &Path,
) -> Result<Vec<ArchiveRetentionCandidate>, CoreError> {
    let active_run_ids = states
        .iter()
        .filter_map(|state| state.active_run.as_ref())
        .map(|record| record.run_id.clone())
        .collect::<BTreeSet<_>>();
    let mut by_path = BTreeMap::new();
    for state in states {
        for record in &state.run_history {
            if active_run_ids.contains(&record.run_id) || !is_terminal(record) {
                continue;
            }
            for path in archive_paths_for_record(frame_run_root, record)? {
                let metadata = fs::metadata(&path).map_err(|source| CoreError::Io {
                    path: path.display().to_string(),
                    source,
                })?;
                let modified = metadata.modified().unwrap_or(SystemTime::UNIX_EPOCH);
                by_path
                    .entry(path.clone())
                    .or_insert_with(|| ArchiveRetentionCandidate {
                        pr: state.pr.clone(),
                        run_id: record.run_id.clone(),
                        run_kind: record.run_kind,
                        pass_index: record.pass_index,
                        commit_sha: record.commit_sha.clone(),
                        path,
                        modified,
                    });
            }
        }
    }
    Ok(by_path.into_values().collect())
}

fn archive_paths_for_record(
    frame_run_root: &Path,
    record: &RunRecord,
) -> Result<Vec<PathBuf>, CoreError> {
    let name = pump19_runs::frame_run_archive_dir_name(&record.run_id);
    let mut paths = Vec::new();
    let base = frame_run_root.join(&name);
    if base.is_dir() {
        paths.push(base);
    }
    if !frame_run_root.exists() {
        return Ok(paths);
    }
    let superseded_prefix = format!("{name}.superseded.");
    for entry in fs::read_dir(frame_run_root).map_err(|source| CoreError::Io {
        path: frame_run_root.display().to_string(),
        source,
    })? {
        let entry = entry.map_err(|source| CoreError::Io {
            path: frame_run_root.display().to_string(),
            source,
        })?;
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }
        let Some(file_name) = path.file_name().and_then(|value| value.to_str()) else {
            continue;
        };
        if file_name.starts_with(&superseded_prefix) {
            paths.push(path);
        }
    }
    Ok(paths)
}

const fn is_terminal(record: &RunRecord) -> bool {
    !matches!(
        record.status,
        pump19_contract::RunStatus::Pending | pump19_contract::RunStatus::Running
    )
}

fn plan_prunes(
    policy: &ArchiveRetentionConfig,
    candidates: Vec<ArchiveRetentionCandidate>,
    now: SystemTime,
) -> Vec<ArchiveRetentionCandidate> {
    let cutoff = policy.age_cutoff(now);
    let mut by_pr: BTreeMap<(String, String), Vec<ArchiveRetentionCandidate>> = BTreeMap::new();
    for candidate in candidates {
        by_pr
            .entry((candidate.pr.repository.clone(), candidate.pr.id.clone()))
            .or_default()
            .push(candidate);
    }
    let mut pruned = Vec::new();
    for candidates in by_pr.values_mut() {
        candidates.sort_by(|left, right| {
            left.modified
                .cmp(&right.modified)
                .then_with(|| left.path.cmp(&right.path))
        });
        let mut retained = candidates.len();
        let minimum = policy.minimum_retained_per_pr();
        for candidate in candidates.iter() {
            if retained <= minimum || candidate.modified > cutoff {
                continue;
            }
            if retained > policy.max_terminal_runs_per_pr {
                retained = retained.saturating_sub(1);
                pruned.push(candidate.clone());
            }
        }
    }
    pruned
}

pub const fn retention_failure_event(message: String) -> OperatorLogEvent {
    OperatorLogEvent {
        contract_version: ContractVersion::current(),
        kind: OperatorLogEventKind::ArchiveRetention,
        pr: None,
        run_id: None,
        run_kind: None,
        pass_index: None,
        commit_sha: None,
        message,
        refusal_reason: None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use pump19_contract::{
        Extensions, PrRunState, PublicationState, PullRequestRef, RunCeiling, RunOutcome, RunStatus,
    };
    use std::io;
    use tempfile::tempdir;

    #[test]
    fn policy_prunes_oldest_after_age_window_and_keeps_per_pr_minimum() {
        let pr = pr("acme/widgets", "7");
        let now = SystemTime::UNIX_EPOCH + Duration::from_hours(720);
        let old = now - Duration::from_hours(480);
        let young = now - Duration::from_hours(48);
        let candidates = vec![
            candidate(&pr, "run-1", old),
            candidate(&pr, "run-2", old + Duration::from_secs(1)),
            candidate(&pr, "run-3", young),
        ];

        let pruned = plan_prunes(
            &ArchiveRetentionConfig {
                keep_days: 14,
                keep_terminal_runs_per_pr: 1,
                max_terminal_runs_per_pr: 1,
            },
            candidates,
            now,
        );

        assert_eq!(pruned.len(), 2);
        assert_eq!(pruned[0].run_id.0, "run-1");
        assert_eq!(pruned[1].run_id.0, "run-2");
    }

    #[test]
    fn policy_never_prunes_inside_age_window_even_when_count_is_high() {
        let pr = pr("acme/widgets", "7");
        let now = SystemTime::UNIX_EPOCH + Duration::from_hours(720);
        let young = now - Duration::from_hours(1);
        let candidates = vec![
            candidate(&pr, "run-1", young),
            candidate(&pr, "run-2", young + Duration::from_secs(1)),
            candidate(&pr, "run-3", young + Duration::from_secs(2)),
        ];

        let pruned = plan_prunes(
            &ArchiveRetentionConfig {
                keep_days: 1,
                keep_terminal_runs_per_pr: 1,
                max_terminal_runs_per_pr: 1,
            },
            candidates,
            now,
        );

        assert!(pruned.is_empty());
    }

    #[test]
    fn collect_candidates_ignores_active_run_and_includes_superseded_siblings() {
        let temp = tempdir().expect("temp dir");
        let frame_root = temp.path().join("archives/frame-runs");
        fs::create_dir_all(frame_root.join("terminal-run")).expect("terminal dir");
        fs::create_dir_all(frame_root.join("terminal-run.superseded.1.0")).expect("superseded dir");
        fs::create_dir_all(frame_root.join("active-run")).expect("active dir");
        let mut state = state_with_record("terminal-run", RunStatus::Completed);
        state.active_run = Some(record("active-run", RunStatus::Running));

        let candidates = collect_candidates(&[state], &frame_root).expect("candidates");

        let paths = candidates
            .iter()
            .map(|candidate| {
                candidate
                    .path
                    .file_name()
                    .and_then(|name| name.to_str())
                    .map(ToOwned::to_owned)
                    .ok_or_else(|| io_invalid_data("candidate path has no UTF-8 file name"))
            })
            .collect::<Result<Vec<_>, _>>()
            .expect("candidate names");
        assert_eq!(paths.len(), 2);
        assert!(paths.contains(&"terminal-run".to_owned()));
        assert!(paths.contains(&"terminal-run.superseded.1.0".to_owned()));
        assert!(!paths.contains(&"active-run".to_owned()));
    }

    #[test]
    fn enforce_removes_terminal_archives_and_records_operator_log() {
        let temp = tempdir().expect("temp dir");
        let state_root = temp.path().join("state");
        let frame_root = temp.path().join("archives/frame-runs");
        fs::create_dir_all(frame_root.join("old-run")).expect("old dir");
        let mut store = JsonRunStateStore::new(&state_root).expect("state store");
        let state = state_with_record("old-run", RunStatus::Completed);
        store.save(&state).expect("save state");
        let mut log = RecordingLog::default();

        let summary = enforce_archive_retention(
            &ArchiveRetentionConfig {
                keep_days: 0,
                keep_terminal_runs_per_pr: 0,
                max_terminal_runs_per_pr: 0,
            },
            &state_root,
            &frame_root,
            &mut log,
            SystemTime::now(),
        )
        .expect("retention");

        assert_eq!(summary.pruned, 1);
        assert!(!frame_root.join("old-run").exists());
        assert_eq!(log.events.len(), 1);
        assert_eq!(log.events[0].kind, OperatorLogEventKind::ArchiveRetention);
        assert_eq!(
            log.events[0].run_id.as_ref().map(|id| id.0.as_str()),
            Some("old-run")
        );
    }

    #[derive(Default)]
    struct RecordingLog {
        events: Vec<OperatorLogEvent>,
    }

    impl OperatorLog for RecordingLog {
        fn record(&mut self, event: OperatorLogEvent) -> Result<(), CoreError> {
            self.events.push(event);
            Ok(())
        }
    }

    fn candidate(
        pr: &PullRequestRef,
        run_id: &str,
        modified: SystemTime,
    ) -> ArchiveRetentionCandidate {
        ArchiveRetentionCandidate {
            pr: pr.clone(),
            run_id: RunId(run_id.to_owned()),
            run_kind: RunKind::Review,
            pass_index: 1,
            commit_sha: "abc123".to_owned(),
            path: PathBuf::from(run_id),
            modified,
        }
    }

    fn state_with_record(run_id: &str, status: RunStatus) -> PrRunState {
        PrRunState {
            contract_version: ContractVersion::current(),
            pr: pr("acme/widgets", "7"),
            commit_sha: "abc123".to_owned(),
            current_head_sha: None,
            pass_index: 1,
            status,
            active_run: None,
            run_history: vec![record(run_id, status)],
            loop_history: Vec::new(),
            superseded_by: None,
            findings: Vec::new(),
            verdict: None,
            coverage: None,
            patches: Vec::new(),
            publication: PublicationState::default(),
            ceiling: None::<RunCeiling>,
            extensions: Extensions::new(),
        }
    }

    fn record(run_id: &str, status: RunStatus) -> RunRecord {
        RunRecord {
            run_id: RunId(run_id.to_owned()),
            run_kind: RunKind::Review,
            event_id: "event-1".to_owned(),
            rule_id: "review".to_owned(),
            pass_index: 1,
            commit_sha: "abc123".to_owned(),
            status,
            outcome: Some(RunOutcome::Succeeded),
            refusal: None,
            session_archives: Vec::new(),
            independence_degradations: Vec::new(),
            provenance: Vec::new(),
        }
    }

    fn pr(repository: &str, id: &str) -> PullRequestRef {
        PullRequestRef {
            repository: repository.to_owned(),
            id: id.to_owned(),
        }
    }

    fn io_invalid_data(message: &str) -> io::Error {
        io::Error::new(io::ErrorKind::InvalidData, message)
    }
}
