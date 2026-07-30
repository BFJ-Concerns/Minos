import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { prepareFixWave } from "./fix-wave-plan.mjs";

function runCommand(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, options);
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

function parseSingleJSONLine(stdout, description) {
  const lines = stdout.split(/\r?\n/).filter((line) => line !== "");
  if (lines.length !== 1)
    throw new Error(`${description} emitted ${lines.length} stdout lines; expected one JSON result`);
  const result = JSON.parse(lines[0]);
  if (!result || typeof result !== "object" || Array.isArray(result))
    throw new Error(`${description} did not emit a JSON object`);
  return result;
}

function publicationFailure(plan, reason, publication = null) {
  return {
    ...plan,
    status: "incomplete",
    reason,
    dispatches: [],
    publication,
    integration: { commits: [], pushCount: 0 },
    fixReview: null,
    rerunReview: false,
  };
}

async function defaultForge({ minosBin, head, target, bodyPath, commentsPath, cwd, env }) {
  return runCommand(
    minosBin,
    ["forge", "review", head, target, "comment", bodyPath, commentsPath],
    { cwd, env },
  );
}

async function defaultDispatch({ launcher, workflowScript, planPath, cwd, env }) {
  return runCommand(
    process.execPath,
    [launcher, "--json-args", `@${planPath}`, workflowScript],
    { cwd, env },
  );
}

export async function publishBeforeFix({
  input,
  head,
  target,
  minosBin,
  launcher,
  workflowScript,
  cwd = process.cwd(),
  env = process.env,
  runForge = defaultForge,
  runDispatch = defaultDispatch,
  onEvent = () => {},
  diagnostics = (message) => { process.stderr.write(message); },
}) {
  const plan = prepareFixWave(input);
  if (plan.fingerprint) onEvent(`fix-plan-created:${plan.fingerprint}`);
  if (plan.status !== "complete" || plan.classification === "terminal") return plan;
  if (plan.classification !== "working")
    return publicationFailure(plan, `publication-before-fix refuses ${String(plan.classification)} preparation`);
  if (input.review.reviewed.head !== head || input.review.reviewed.target !== target)
    return publicationFailure(plan, "prepared wave does not match the requested head and target");

  const operationDir = mkdtempSync(join(tmpdir(), "minos-publication-before-fix-"));
  try {
    const bodyPath = join(operationDir, "sweep-review.md");
    const commentsPath = join(operationDir, "sweep-review-comments.json");
    const planPath = join(operationDir, "fix-wave-plan.json");
    writeFileSync(bodyPath, plan.sweepReview.body, { mode: 0o600 });
    writeFileSync(commentsPath, JSON.stringify(plan.sweepReview.comments), { mode: 0o600 });
    writeFileSync(planPath, JSON.stringify(plan), { mode: 0o600 });

    let forgeResult;
    try {
      forgeResult = await runForge({ minosBin, head, target, bodyPath, commentsPath, cwd, env });
    } catch (error) {
      return publicationFailure(plan, `sweep review publication command failed: ${error.message}`);
    }
    if (forgeResult.stderr) diagnostics(forgeResult.stderr);

    let publication;
    try {
      publication = parseSingleJSONLine(forgeResult.stdout, "sweep review publication command");
    } catch (error) {
      return publicationFailure(plan, error.message);
    }
    if (forgeResult.code !== 0)
      return publicationFailure(
        plan,
        `sweep review publication command exited ${forgeResult.signal || forgeResult.code}`,
        publication,
      );
    if (publication.outcome !== "applied")
      return publicationFailure(
        plan,
        `sweep review publication returned ${String(publication.outcome || "no outcome")}`,
        publication,
      );

    onEvent(`forge-review-confirmed:${plan.fingerprint}`);
    let dispatchCwd = cwd;
    if (env.MINOS_RUN_DIR) {
      try {
        const reconciliation = JSON.parse(
          readFileSync(join(env.MINOS_RUN_DIR, "reconciliation.json"), "utf8"),
        );
        dispatchCwd = reconciliation.publication || cwd;
      } catch (error) {
        return publicationFailure(
          plan,
          `fix dispatch workspace resolution failed: ${error.message}`,
          publication,
        );
      }
    }
    onEvent(`fix-dispatch-started:${plan.fingerprint}`);
    let dispatchResult;
    try {
      dispatchResult = await runDispatch({ launcher, workflowScript, planPath, plan, cwd: dispatchCwd, env });
    } catch (error) {
      return publicationFailure(plan, `fix dispatcher failed to start: ${error.message}`, publication);
    }
    if (dispatchResult.stderr) diagnostics(dispatchResult.stderr);
    if (dispatchResult.code !== 0)
      return publicationFailure(
        plan,
        `fix dispatcher exited ${dispatchResult.signal || dispatchResult.code}`,
        publication,
      );

    let dispatched;
    try {
      dispatched = parseSingleJSONLine(dispatchResult.stdout, "fix dispatcher");
    } catch (error) {
      return publicationFailure(plan, error.message, publication);
    }
    return { ...dispatched, publication };
  } finally {
    rmSync(operationDir, { recursive: true, force: true });
  }
}
