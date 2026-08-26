#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [verdictPath, mode] = process.argv.slice(2);
const terminal = mode === "--terminal";

function guardedResult(minos, args) {
  try {
    return JSON.parse(execFileSync(minos, args, { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] }));
  } catch (error) {
    const stdout = error && typeof error === "object" && "stdout" in error ? error.stdout : null;
    if (!stdout) throw error;
    return JSON.parse(String(stdout));
  }
}

if (!verdictPath || (process.argv.length !== 3 && !(process.argv.length === 4 && terminal))) {
  process.stderr.write("usage: node workflows/publish-member-reviews.mjs VERDICT [--terminal]\n");
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
    let failure = null;
    for (const member of verdict.members) {
      if (!member || typeof member.owner !== "string" || typeof member.repo !== "string" || !Number.isInteger(member.number) ||
          typeof member.head !== "string" || member.head === "" || typeof member.target !== "string" || member.target === "")
        throw new Error("member coordinates are incomplete");
      const review = reviews.get(member.id);
      if (!review) throw new Error(`member ${member.id} has no review result`);
      const prefix = ["forge", "--member", member.owner, member.repo, String(member.number)];
      const bodyPath = join(scratch, `${member.id}.md`);
      const commentsPath = join(scratch, `${member.id}.json`);
      writeFileSync(bodyPath, review.body, { mode: 0o600 });
      writeFileSync(commentsPath, JSON.stringify(review.comments), { mode: 0o600 });
      const publication = guardedResult(
        minos,
        [
          ...prefix,
          "review",
          member.head,
          member.target,
          terminal && review.status === "attention" ? "request-changes" : review.verdict,
          bodyPath,
          commentsPath,
        ],
      );
      if (publication.outcome !== "applied") {
        failure = publication;
        break;
      }
      if (terminal && review.status === "attention") {
        const status = guardedResult(
          minos,
          [...prefix, "status", member.head, member.target, review.status],
        );
        if (status.outcome !== "applied") {
          failure = status;
          break;
        }
      }
    }
    process.stdout.write(`${JSON.stringify(failure || { outcome: "applied", reviews: verdict.members.length })}\n`);
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}
