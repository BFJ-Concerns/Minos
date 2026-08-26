import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

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

const memberPublisher = join(dirname(fileURLToPath(import.meta.url)), "publish-member-reviews.mjs");

// Terminal publication is an effect of the fix plan, not a replay of the
// review envelope. The plan is where the threshold and the persisted
// confirmed-unfixed findings meet, so it alone decides which members hold.
function terminalMemberReview(plan, review) {
  if (review.members.some((member) => !member || typeof member.id !== "string" || member.id === ""))
    return { reason: "terminal member coordinates are incomplete" };
  const knownMembers = new Set(review.members.map((member) => member.id));
  const blocking = new Map(review.members.map((member) => [member.id, []]));
  const comments = plan.requestChangesReview ? plan.requestChangesReview.comments : [];
  if (comments.length !== plan.requestChanges.length)
    return { reason: "terminal request-changes payload is incomplete" };
  for (let index = 0; index < plan.requestChanges.length; index += 1) {
    const member = plan.requestChanges[index]?.finding?.member;
    if (typeof member !== "string" || !knownMembers.has(member))
      return { reason: "terminal blocking finding has no publishable member" };
    blocking.get(member).push(comments[index]);
  }
  return {
    review: {
      ...review,
      memberReviews: review.members.map((member) => {
        const memberComments = blocking.get(member.id);
        if (memberComments.length > 0) {
          return {
            member: member.id,
            verdict: "request-changes",
            body: plan.requestChangesReview.body,
            comments: memberComments,
            status: "attention",
          };
        }
        return {
          member: member.id,
          verdict: "comment",
          body: "No blocking findings in the reviewed code.",
          comments: [],
          status: "clean",
        };
      }),
    },
  };
}

async function defaultMemberPublication({ verdictPath, minosBin, terminal, cwd, env }) {
  return runCommand(process.execPath, [memberPublisher, verdictPath, ...(terminal ? ["--terminal"] : [])], {
    cwd,
    env: { ...env, ...(minosBin ? { MINOS_BIN: minosBin } : {}) },
  });
}

export async function publishBeforeFix({
  input,
  head,
  target,
  carriedFrom = null,
  minosBin,
  launcher,
  workflowScript,
  cwd = process.cwd(),
  env = process.env,
  runForge = defaultForge,
  runMemberPublication = defaultMemberPublication,
  runDispatch = defaultDispatch,
  onEvent = () => {},
  diagnostics = (message) => { process.stderr.write(message); },
}) {
  const plan = prepareFixWave(input);
  if (plan.fingerprint) onEvent(`fix-plan-created:${plan.fingerprint}`);
  if (plan.status !== "complete") return plan;
  const hasMemberPublication = Array.isArray(input.review.members) && Array.isArray(input.review.memberReviews);
  if (plan.classification === "terminal") {
    // A carried verdict judged an earlier head, so it may only drive repairs;
    // standing as a terminal round would end the loop on a head it never saw.
    if (carriedFrom)
      return publicationFailure(plan, "a carried verdict cannot stand as a terminal round at a moved head");
    if (!hasMemberPublication) return plan;
  }
  if (plan.classification !== "working" && plan.classification !== "terminal")
    return publicationFailure(plan, `publication-before-fix refuses ${String(plan.classification)} preparation`);
  if (carriedFrom) {
    if (input.review.reviewed.head !== carriedFrom.head || input.review.reviewed.target !== carriedFrom.target)
      return publicationFailure(plan, "carried verdict does not match the declared reviewed head and target");
  } else if (input.review.reviewed.head !== head || input.review.reviewed.target !== target) {
    return publicationFailure(plan, "prepared wave does not match the requested head and target");
  }

  const publicationReview = plan.classification === "terminal"
    ? terminalMemberReview(plan, input.review)
    : { review: input.review };
  if (publicationReview.reason) return publicationFailure(plan, publicationReview.reason);

  const operationDir = mkdtempSync(join(tmpdir(), "minos-publication-before-fix-"));
  try {
    const bodyPath = join(operationDir, "sweep-review.md");
    const commentsPath = join(operationDir, "sweep-review-comments.json");
    const planPath = join(operationDir, "fix-wave-plan.json");
    const verdictPath = join(operationDir, "review-result.json");
    if (!hasMemberPublication) {
      writeFileSync(bodyPath, plan.sweepReview.body, { mode: 0o600 });
      writeFileSync(commentsPath, JSON.stringify(plan.sweepReview.comments), { mode: 0o600 });
    }
    writeFileSync(planPath, JSON.stringify(plan), { mode: 0o600 });
    writeFileSync(verdictPath, JSON.stringify(publicationReview.review), { mode: 0o600 });

    let forgeResult;
    try {
      forgeResult = hasMemberPublication
        ? await runMemberPublication({
            verdictPath,
            review: publicationReview.review,
            terminal: plan.classification === "terminal",
            minosBin,
            cwd,
            env,
          })
        : await runForge({ minosBin, head, target, bodyPath, commentsPath, cwd, env });
    } catch (error) {
      return publicationFailure(plan, `review publication command failed: ${error.message}`);
    }
    if (forgeResult.stderr) diagnostics(forgeResult.stderr);

    let publication;
    try {
      publication = parseSingleJSONLine(forgeResult.stdout, "review publication command");
    } catch (error) {
      return publicationFailure(plan, error.message);
    }
    if (forgeResult.code !== 0)
      return publicationFailure(
        plan,
        `review publication command exited ${forgeResult.signal || forgeResult.code}`,
        publication,
      );
    if (publication.outcome !== "applied")
      return publicationFailure(
        plan,
        `review publication returned ${String(publication.outcome || "no outcome")}`,
        publication,
      );

    onEvent(`forge-review-confirmed:${plan.fingerprint}`);
    if (plan.classification === "terminal") return { ...plan, publication };
    onEvent(`fix-dispatch-started:${plan.fingerprint}`);
    let dispatchResult;
    try {
      // The launcher owns the immutable per-agent record. Keep it under the
      // run's existing retained-record root, while the status snapshot stays
      // at the run root for the lead's live watcher.
      const runRecordRoot = typeof env.MINOS_RUN_DIR === "string" && env.MINOS_RUN_DIR !== ""
        ? `${env.MINOS_RUN_DIR}/ensemble-records/fix`
        : null;
      const dispatchEnv = runRecordRoot === null ? env : {
        ...env,
        ENSEMBLE_STATUS_DIR: env.MINOS_RUN_DIR,
        ENSEMBLE_RUN_RECORD: "on",
        ENSEMBLE_RUN_RECORD_DIR: runRecordRoot,
      };
      dispatchResult = await runDispatch({ launcher, workflowScript, planPath, plan, cwd: input.workspace, env: dispatchEnv });
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
