import { existsSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { applied, checkHead } from "./forge-writes.mjs";

export async function continueRun(run, stage) {
  if (!existsSync(join(run.runDir, "memory-pressure"))) return false;

  const snapshot = await run.forge("snapshot");
  checkHead(run, snapshot);
  const latestReview = (snapshot.reviews || []).reduce((latest, review) =>
    review.user === snapshot.authenticated_user && review.commit_id === snapshot.head_sha
      ? Math.max(latest, review.id) : latest, 0);
  const { owner, repository, number } = run.pullRequest;
  const previous = run.env.MINOS_PREDECESSOR_PROGRESS;
  const handoff = {
    kind: "minos-run-handoff-v1",
    pullRequest: { owner, repo: repository, number },
    head: snapshot.head_sha,
    runDir: run.runDir,
    stoppedAt: stage,
    writtenAt: new Date().toISOString(),
    ...(previous ? { predecessorProgress: JSON.parse(previous) } : {}),
    progress: { stage, head: snapshot.head_sha, latestReview },
  };
  const path = run.env.MINOS_HANDOFF;
  if (!path) throw new Error("MINOS_HANDOFF is required for a continuation");
  await applied(run, "status", [snapshot.head_sha, snapshot.target_sha, "continuation"]);
  writeFileSync(`${path}.tmp`, `${JSON.stringify(handoff)}\n`);
  renameSync(`${path}.tmp`, path);
  return true;
}
