import { copyFileSync, existsSync, readFileSync, renameSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { prepareStage, runCommand } from "../run-process.mjs";

export async function dispatchReview(run, inspection, { name, builder, workflow, carry }) {
  const command = fileURLToPath(new URL(`../${builder}`, import.meta.url));
  const input = await runCommand(run, process.execPath, [command, inspection.reviewBase, run.head]);
  let attempt = prepareStage(run, name, input);
  const resultFile = () => join(run.runDir, `${attempt}-result.json`);
  const carriedFile = join(run.runDir, "carried-review-result.json");
  if (carry && existsSync(carriedFile)) {
    const verdict = JSON.parse(readFileSync(carriedFile, "utf8"));
    if (verdict.reviewed?.head === run.head && verdict.reviewed?.target === inspection.reviewBase) {
      renameSync(carriedFile, resultFile());
      return finish(verdict, { carried: true });
    }
    renameSync(carriedFile, join(run.runDir, "stale-review-result.json"));
  }

  let verdict;
  try {
    verdict = await run.dispatch(attempt, workflow);
  } catch (error) {
    if (!error.killed || existsSync(join(run.runDir, "memory-pressure"))) throw error;
    const first = error;
    attempt = prepareStage(run, name, input);
    try {
      verdict = await run.dispatch(attempt, workflow);
    } catch (error) {
      throw new Error(`${first.message}; retry: ${error.message}`);
    }
    return finish(verdict, { dispatchProblem: first.message });
  }
  return finish(verdict);

  function finish(verdict, details) {
    const file = resultFile();
    if (verdict?.status !== "complete") {
      const reason = verdict?.incomplete?.join("; ");
      throw new Error(`${attempt}: ${verdict?.status ?? "missing verdict status"}${reason ? `: ${reason}` : ""}`);
    }
    if (carry && attempt !== name) {
      // Admission consumes the canonical main verdict on continuation.
      copyFileSync(file, join(run.runDir, "review-result.json"));
    }
    return { verdict, file, ...details };
  }
}
