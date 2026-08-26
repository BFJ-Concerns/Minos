#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [verdictPath] = process.argv.slice(2);
if (!verdictPath || process.argv.length !== 3) {
  process.stderr.write("usage: node workflows/publish-member-reviews.mjs VERDICT\n");
  process.exitCode = 2;
} else {
  const verdict = JSON.parse(readFileSync(verdictPath, "utf8"));
  if (verdict.status !== "complete" || !Array.isArray(verdict.members) || !Array.isArray(verdict.memberReviews))
    throw new Error("complete verdict with members and memberReviews is required");
  const reviews = new Map(verdict.memberReviews.map((review) => [review.member, review]));
  const minos = process.env.MINOS_BIN;
  if (!minos) throw new Error("MINOS_BIN is required");
  const scratch = mkdtempSync(join(tmpdir(), "minos-member-reviews-"));
  try {
    for (const member of verdict.members) {
      if (!member || typeof member.owner !== "string" || typeof member.repo !== "string" || !Number.isInteger(member.number))
        throw new Error("member coordinates are incomplete");
      const review = reviews.get(member.id);
      if (!review) throw new Error(`member ${member.id} has no review result`);
      const prefix = ["forge", "--member", member.owner, member.repo, String(member.number)];
      const snapshot = JSON.parse(execFileSync(minos, [...prefix, "snapshot"], { encoding: "utf8" }));
      if (typeof snapshot.head_sha !== "string" || typeof snapshot.target_sha !== "string")
        throw new Error(`member ${member.id} snapshot is unusable`);
      const bodyPath = join(scratch, `${member.id}.md`);
      const commentsPath = join(scratch, `${member.id}.json`);
      writeFileSync(bodyPath, review.body, { mode: 0o600 });
      writeFileSync(commentsPath, JSON.stringify(review.comments), { mode: 0o600 });
      const publication = JSON.parse(execFileSync(
        minos,
        [...prefix, "review", snapshot.head_sha, snapshot.target_sha, review.verdict, bodyPath, commentsPath],
        { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] },
      ));
      if (publication.outcome !== "applied")
        throw new Error(`member ${member.id} review publication returned ${String(publication.outcome || "no outcome")}`);
    }
    process.stdout.write(`${JSON.stringify({ outcome: "applied", reviews: verdict.members.length })}\n`);
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}
