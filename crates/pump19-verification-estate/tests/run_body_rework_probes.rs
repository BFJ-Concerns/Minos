#![allow(clippy::expect_used, clippy::unwrap_used)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::Duration,
};

use pump19_adaptations::write_baseline_forgejo_commands;
use pump19_engine::{
    EngineKind, EngineSessionLauncher, ExitClassification, LaunchBounds, LaunchSpec, NoRepair,
    WriteAccess,
};
use pump19_review::{DEFAULT_OCCASION, load_review_briefs_from_dir, matching_briefs};
use serde_json::{Value, json};
use tempfile::tempdir;

#[test]
fn stage1_governing_briefs_are_ready_for_frame_consumption()
-> Result<(), Box<dyn std::error::Error>> {
    let dir = tempdir()?;
    let commands = write_baseline_forgejo_commands(&dir.path().join("commands"))?;
    let remotes = dir.path().join("remotes");
    let bare = remotes.join("acme/widgets.git");
    let work = dir.path().join("work");
    fs::create_dir_all(bare.parent().expect("bare parent"))?;
    run_git(["init", "--bare", bare.to_str().expect("bare path")]);
    run_git(["init", work.to_str().expect("work path")]);
    run_git_in(&work, ["config", "user.email", "pump19@example.invalid"]);
    run_git_in(&work, ["config", "user.name", "Pump 19"]);
    run_git_in(
        &work,
        ["remote", "add", "origin", bare.to_str().expect("bare path")],
    );

    fs::create_dir_all(work.join(".review"))?;
    fs::create_dir_all(work.join("src"))?;
    fs::write(work.join("AGENTS.md"), "base guidance\n")?;
    fs::write(
        work.join(".review/security.md"),
        "+++\ntitle = \"Base security\"\nscope = [\"src/**\"]\nextent = \"deep\"\nrun-condition = [\"every-pr\"]\n+++\nBase security criterion.\n",
    )?;
    fs::write(
        work.join(".review/release.md"),
        "+++\ntitle = \"Release only\"\nrun-condition = [\"release\"]\n+++\nRelease criterion.\n",
    )?;
    fs::write(work.join("src/lib.rs"), "pub fn value() -> u8 { 1 }\n")?;
    run_git_in(&work, ["add", "."]);
    run_git_in(&work, ["commit", "-m", "base"]);
    let base_sha = git_stdout_in(&work, ["rev-parse", "HEAD"]);

    fs::write(work.join("AGENTS.md"), "head guidance must not govern\n")?;
    fs::write(
        work.join(".review/security.md"),
        "+++\ntitle = \"Head security\"\nscope = [\"docs/**\"]\n+++\nHead criterion must not govern.\n",
    )?;
    fs::write(work.join("src/lib.rs"), "pub fn value() -> u8 { 2 }\n")?;
    run_git_in(&work, ["add", "."]);
    run_git_in(&work, ["commit", "-m", "head"]);
    let head_sha = git_stdout_in(&work, ["rev-parse", "HEAD"]);
    run_git_in(&work, ["push", "origin", "HEAD:main"]);

    let preparation_root = dir.path().join("prepared");
    let input = json!({
        "step_id": "prepare-source",
        "run_id": "run-frame-fixture",
        "run_kind": "review",
        "repository": "acme/widgets",
        "pull_request": "42",
        "commit_sha": head_sha,
        "workspace_root": dir.path().join("workspace"),
        "preparation_root": preparation_root,
        "previous_tree": null,
        "event": {},
        "state": {
            "extensions": {
                "pump19.core.forge_facts": {
                    "base": { "sha": base_sha }
                }
            }
        }
    });
    let base_url = format!("file://{}", remotes.display());
    let output = run_json_command_with_args(
        &commands.prepare_source_command,
        &["--git-base-url", &base_url],
        &input,
    )?;

    assert_command_success(&output);
    let prepared: Value = serde_json::from_slice(&output.stdout)?;
    let tree = PathBuf::from(prepared["tree"].as_str().expect("tree path"));
    let governing_dir = tree.join(".pump19/review/governing");
    assert_eq!(
        fs::read_to_string(governing_dir.join("AGENTS.md"))?,
        "base guidance\n"
    );
    assert_eq!(
        fs::read_to_string(governing_dir.join(".review/security.md"))?,
        "+++\ntitle = \"Base security\"\nscope = [\"src/**\"]\nextent = \"deep\"\nrun-condition = [\"every-pr\"]\n+++\nBase security criterion.\n"
    );
    assert_eq!(
        fs::read_to_string(tree.join(".review/security.md"))?,
        "+++\ntitle = \"Head security\"\nscope = [\"docs/**\"]\n+++\nHead criterion must not govern.\n"
    );

    let load = load_review_briefs_from_dir(&governing_dir.join(".review"))?;
    assert!(load.warnings.is_empty());
    let matched = matching_briefs(&load.briefs, DEFAULT_OCCASION, &["src/lib.rs".to_owned()]);
    assert_eq!(matched.len(), 1);
    assert_eq!(matched[0].id, "security");
    assert_eq!(matched[0].title, "Base security");
    assert_eq!(matched[0].body, "Base security criterion.");
    Ok(())
}

#[test]
#[ignore = "spends live Codex/Ensemble tokens; run manually for run-body rework verification"]
fn live_engine_session_drives_ensemble_workflow_through_payload_file()
-> Result<(), Box<dyn std::error::Error>> {
    let claude = command_path("claude")?;
    let node = command_path("node")?;
    let ensemble = PathBuf::from("/home/operator/.claude/skills/ensemble-workflow/scripts/ensemble.mjs");
    if !ensemble.exists() {
        return Err(format!("missing Ensemble launcher {}", ensemble.display()).into());
    }

    let root = tempdir()?;
    let run_dir = root.path();
    fs::create_dir_all(run_dir.join("bin"))?;
    fs::create_dir_all(run_dir.join("input"))?;
    fs::create_dir_all(run_dir.join("out"))?;
    fs::create_dir_all(run_dir.join("archive"))?;

    let workflow =
        workspace_root().join("examples/deployment/adaptations/prompt/workflows/repair-output.js");
    let payload = json!({
        "repairer": {
            "engine": "codex",
            "agent_id": "repairer-live"
        },
        "schema": {
            "type": "object",
            "additionalProperties": false,
            "required": ["marker", "status"],
            "properties": {
                "marker": { "type": "string" },
                "status": { "type": "string" }
            }
        },
        "invalid_output": "{\"marker\":\"live-shim\"}",
        "errors": ["missing required property status"],
        "agent_timeout_ms": 120_000
    });
    fs::write(
        run_dir.join("input/repair.json"),
        serde_json::to_vec_pretty(&payload)?,
    )?;
    write_workflow_shim(
        &run_dir.join("bin/pump19-workflow"),
        &node,
        &ensemble,
        &workflow,
    )?;
    fs::write(
        run_dir.join("schema.json"),
        r#"{"type":"object","additionalProperties":false,"required":["slot","marker","status","boundary_log_present"],"properties":{"slot":{"type":"string"},"marker":{"type":"string"},"status":{"type":"string"},"boundary_log_present":{"type":"boolean"}}}"#,
    )?;
    fs::write(
        run_dir.join("mission.md"),
        "Run exactly:\n./bin/pump19-workflow repair-output input/repair.json > out/workflow.json\n\nRead out/workflow.json and out/boundary.jsonl. Return only this JSON shape, with marker and status copied from out/workflow.json: {\"slot\":\"repair-output\",\"marker\":\"...\",\"status\":\"...\",\"boundary_log_present\":true}.",
    )?;

    let run = EngineSessionLauncher::default().launch(
        &LaunchSpec {
            engine: EngineKind::Claude,
            executable: claude,
            working_dir: run_dir.to_path_buf(),
            extra_workspace_roots: Vec::new(),
            prompt_path: run_dir.join("mission.md"),
            schema_path: Some(run_dir.join("schema.json")),
            archive_dir: run_dir.join("archive/lead"),
            requested_model: Some("sonnet".to_owned()),
            write_access: WriteAccess::Writable,
            bounds: LaunchBounds {
                wall_clock: Duration::from_mins(4),
                max_budget_usd: Some("1.00".to_owned()),
                max_total_tokens: None,
            },
            env: BTreeMap::new(),
        },
        &mut NoRepair,
    )?;

    eprintln!("engine classification: {:?}", run.classification);
    eprintln!("engine output: {:?}", run.output);
    eprintln!("engine final text: {:?}", run.final_text);
    for transcript in &run.transcripts {
        eprintln!(
            "transcript {:?}: {}",
            transcript.kind,
            fs::read_to_string(&transcript.path).unwrap_or_default()
        );
    }

    assert_eq!(run.classification, ExitClassification::Success);
    let output = run.output.expect("lead output");
    assert_eq!(output["slot"], "repair-output");
    assert_eq!(output["marker"], "live-shim");
    assert_eq!(output["boundary_log_present"], true);
    let boundary = fs::read_to_string(run_dir.join("out/boundary.jsonl"))?;
    assert!(boundary.contains("\"slot\":\"repair-output\""));
    assert!(boundary.contains("\"input_path\":\"input/repair.json\""));
    assert!(run_dir.join("archive/workflows/repair-output").exists());
    Ok(())
}

#[test]
#[ignore = "spends live Claude tokens; proves the frame run dir can read the separate prepared workspace"]
fn live_claude_lead_reads_separate_workspace_and_drives_workflow_shim()
-> Result<(), Box<dyn std::error::Error>> {
    let claude = command_path("claude")?;

    let root = tempdir()?;
    let run_dir = root.path().join("frame-runs/lead-frame-review");
    let workspace_dir = root.path().join("prepared-workspace");
    fs::create_dir_all(run_dir.join("bin"))?;
    fs::create_dir_all(run_dir.join("input"))?;
    fs::create_dir_all(run_dir.join("out"))?;
    fs::create_dir_all(run_dir.join("archive"))?;
    fs::create_dir_all(workspace_dir.join("src"))?;

    fs::write(
        workspace_dir.join("src/probe.txt"),
        "LIVE_SHAPE_WORKSPACE_MARKER_4d62\n",
    )?;
    write_read_only_tree(&workspace_dir)?;
    fs::write(
        run_dir.join("manifest.json"),
        serde_json::to_vec_pretty(&json!({
            "run_id": "lead-frame-review",
            "workspace": {
                "tree_root": workspace_dir,
                "changed_files": ["src/probe.txt"],
                "diff_path": workspace_dir.join(".pump19/review/diff.patch")
            }
        }))?,
    )?;
    fs::write(
        run_dir.join("input/review.json"),
        serde_json::to_vec_pretty(&json!({"marker": "workflow-input"}))?,
    )?;
    write_simple_workflow_shim(&run_dir.join("bin/pump19-workflow"))?;
    fs::write(
        run_dir.join("schema.json"),
        r#"{"type":"object","additionalProperties":false,"required":["workspace_marker","workflow_marker","manifest_workspace_root"],"properties":{"workspace_marker":{"type":"string"},"workflow_marker":{"type":"string"},"manifest_workspace_root":{"type":"string"}}}"#,
    )?;
    fs::write(
        run_dir.join("mission.md"),
        "Read manifest.json. Then read src/probe.txt from manifest.workspace.tree_root. Run exactly:\n./bin/pump19-workflow review input/review.json > out/workflow.json\n\nRead out/workflow.json. Return only this JSON shape: {\"workspace_marker\":\"<trimmed file contents>\",\"workflow_marker\":\"<marker from workflow json>\",\"manifest_workspace_root\":\"<manifest workspace.tree_root>\"}.",
    )?;

    let run = EngineSessionLauncher::default().launch(
        &LaunchSpec {
            engine: EngineKind::Claude,
            executable: claude,
            working_dir: run_dir.clone(),
            extra_workspace_roots: vec![workspace_dir.clone()],
            prompt_path: run_dir.join("mission.md"),
            schema_path: Some(run_dir.join("schema.json")),
            archive_dir: run_dir.join("archive/lead"),
            requested_model: Some("sonnet".to_owned()),
            write_access: WriteAccess::ReadOnly,
            bounds: LaunchBounds {
                wall_clock: Duration::from_secs(150),
                max_budget_usd: Some("0.80".to_owned()),
                max_total_tokens: None,
            },
            env: BTreeMap::new(),
        },
        &mut NoRepair,
    )?;

    eprintln!("engine classification: {:?}", run.classification);
    eprintln!("engine provenance: {:?}", run.provenance);
    eprintln!("engine output: {:?}", run.output);
    for transcript in &run.transcripts {
        eprintln!(
            "transcript {:?}: {}",
            transcript.kind,
            fs::read_to_string(&transcript.path).unwrap_or_default()
        );
    }

    assert_eq!(run.classification, ExitClassification::Success);
    assert_eq!(
        run.provenance.resolved_model.as_deref(),
        Some("claude-sonnet-5")
    );
    let output = run.output.expect("lead output");
    assert_eq!(
        output["workspace_marker"],
        "LIVE_SHAPE_WORKSPACE_MARKER_4d62"
    );
    assert_eq!(output["workflow_marker"], "workflow-shim-ok");
    assert_eq!(
        output["manifest_workspace_root"],
        workspace_dir.display().to_string()
    );
    assert_eq!(
        fs::read_to_string(run_dir.join("out/workflow.json"))?,
        "{\"marker\":\"workflow-shim-ok\"}\n"
    );
    Ok(())
}

fn write_simple_workflow_shim(path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    fs::write(
        path,
        r#"#!/bin/sh
set -eu
slot=$1
input_path=$2
test "$slot" = "review"
test -f "$input_path"
mkdir -p out
printf '{"marker":"workflow-shim-ok"}\n'
"#,
    )?;
    make_executable(path)?;
    Ok(())
}

fn write_workflow_shim(
    path: &Path,
    node: &Path,
    ensemble: &Path,
    workflow: &Path,
) -> Result<(), Box<dyn std::error::Error>> {
    let source = format!(
        r#"#!/bin/sh
set -eu
slot=$1
input_path=$2
case "$slot" in
  repair-output) workflow="{workflow}" ;;
  *) echo "unsupported slot: $slot" >&2; exit 64 ;;
esac
mkdir -p "archive/workflows/$slot" out
input_size=$(wc -c < "$input_path" | tr -d ' ')
printf '{{"slot":"%s","input_path":"%s","input_size":%s}}\n' "$slot" "$input_path" "$input_size" >> out/boundary.jsonl
wrapper="$PWD/out/.pump19-workflow-$slot-$$.js"
trap 'rm -f "$wrapper"' EXIT INT TERM
"{node}" - "$workflow" "$input_path" "$wrapper" <<'NODE'
const fs = require("node:fs");

const [workflow, inputPath, wrapper] = process.argv.slice(2);
const source = fs.readFileSync(workflow, "utf8");
const payload = fs.readFileSync(inputPath, "utf8");
const argsLiteral = JSON.stringify(payload);
const output = [];
let inserted = false;
for (const line of source.split(/\r?\n/)) {{
  output.push(line);
  if (!inserted && /^}};\s*$/.test(line)) {{
    output.push("const args = JSON.parse(" + argsLiteral + ");");
    inserted = true;
  }}
}}
if (!inserted) {{
  console.error("workflow script did not expose a top-level meta object terminator");
  process.exit(65);
}}
fs.writeFileSync(wrapper, output.join("\n"));
NODE
ENSEMBLE_RUN_RECORD_DIR="$PWD/archive/workflows/$slot" "{node}" "{ensemble}" --budget codex=10000 --concurrency codex=1 --timeout 180000 "$wrapper"
"#,
        node = node.display(),
        ensemble = ensemble.display(),
        workflow = workflow.display(),
    );
    fs::write(path, source)?;
    make_executable(path)?;
    Ok(())
}

fn write_read_only_tree(path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;

        fn chmod_recursive(path: &Path) -> Result<(), Box<dyn std::error::Error>> {
            let metadata = fs::metadata(path)?;
            if metadata.is_dir() {
                for entry in fs::read_dir(path)? {
                    chmod_recursive(&entry?.path())?;
                }
                fs::set_permissions(path, fs::Permissions::from_mode(0o555))?;
            } else {
                fs::set_permissions(path, fs::Permissions::from_mode(0o444))?;
            }
            Ok(())
        }

        chmod_recursive(path)?;
    }
    Ok(())
}

fn run_json_command_with_args(
    command: &Path,
    args: &[&str],
    input: &Value,
) -> Result<std::process::Output, Box<dyn std::error::Error>> {
    let mut child = Command::new(command)
        .args(args)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()?;
    let stdin = child.stdin.as_mut().expect("stdin");
    serde_json::to_writer(stdin, input)?;
    Ok(child.wait_with_output()?)
}

fn run_git<const N: usize>(args: [&str; N]) {
    let output = Command::new("git").args(args).output().expect("run git");
    assert_command_success(&output);
}

fn run_git_in<const N: usize>(dir: &Path, args: [&str; N]) {
    let output = Command::new("git")
        .current_dir(dir)
        .args(args)
        .output()
        .expect("run git");
    assert_command_success(&output);
}

fn git_stdout_in<const N: usize>(dir: &Path, args: [&str; N]) -> String {
    let output = Command::new("git")
        .current_dir(dir)
        .args(args)
        .output()
        .expect("run git");
    assert_command_success(&output);
    String::from_utf8(output.stdout)
        .expect("git utf8")
        .trim()
        .to_owned()
}

fn command_path(command: &str) -> Result<PathBuf, Box<dyn std::error::Error>> {
    let output = Command::new("sh")
        .arg("-lc")
        .arg(format!("command -v {command}"))
        .output()?;
    assert_command_success(&output);
    Ok(PathBuf::from(
        String::from_utf8(output.stdout)?.trim().to_owned(),
    ))
}

fn workspace_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .expect("workspace root")
        .to_path_buf()
}

fn assert_command_success(output: &std::process::Output) {
    assert!(
        output.status.success(),
        "command failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn make_executable(path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;

        let mut permissions = fs::metadata(path)?.permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(path, permissions)?;
    }
    Ok(())
}
