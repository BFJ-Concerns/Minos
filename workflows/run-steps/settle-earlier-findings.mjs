// Finds which blocking findings of earlier Minos reviews on this pull
// request still stand. Part of that is mechanical: a resolved thread is the
// author's answer. A finding raised on this head is read like any other,
// because an earlier run on the same head may have raised it. Settlement
// finishes before this run publishes anything.
// Whether the author rejected a finding in words, and whether the defect it
// describes is gone from the code at this head, are judgements, so this
// step gathers the rest with their replies and the author's comments, asks
// the earlier-findings stage, and carries its answer. It runs on every
// head, reviewed or not.
//
// Settles `outstanding`: the earlier blocking findings that stand, each
// { key, where, path, line, head, reason }, with a title when the original
// finding has one. An empty list means none stands.

import { REVIEW_BODY, THREAD, earlierMinosFindings } from "../earlier-minos-findings.mjs";
import { STANDS, stage as earlierFindings } from "../judgements/earlier-findings.mjs";
import { currentSnapshot } from "./forge-writes.mjs";

// The comments that can answer a review-body finding: the pull request's
// author's, written after the review that holds the finding was published.
// Times are compared as instants, so two spellings of one moment agree.
export function authorCommentsAfter(comments, author, submittedAt) {
  const published = Date.parse(submittedAt);
  if (Number.isNaN(published)) throw new Error(`a Minos review's submitted_at is not a time: ${JSON.stringify(submittedAt)}`);
  return comments
    .filter((comment) => comment.user === author && Date.parse(comment.created_at) > published)
    .map((comment) => comment.body);
}

// The blocking findings that need reading, as the stage is shown them.
export function findingsToRead({ snapshot, threads, comments }) {
  return earlierMinosFindings(snapshot, threads)
    .filter((finding) => finding.blocking && !(finding.where === THREAD && finding.resolved))
    .map(({ key, where, path, line, head: raisedOn, finding, replies, submittedAt }) => ({
      key, path, line, head: raisedOn, finding, where,
      ...(where === THREAD
        ? { replies: replies.map((reply) => ({ byAuthor: reply.user === snapshot.author, body: reply.body })) }
        : { comments: authorCommentsAfter(comments, snapshot.author, submittedAt) }),
    }));
}

export async function settleEarlierFindings(run) {
  const snapshot = await currentSnapshot(run);
  const threads = await run.forge("threads");
  const needsComments = earlierMinosFindings(snapshot, threads)
    .some((finding) => finding.blocking && finding.where === REVIEW_BODY);
  const comments = needsComments ? await run.forge("comments") : [];
  const findings = findingsToRead({ snapshot, threads, comments });
  if (findings.length === 0) return { outstanding: [] };

  const answer = await run.judge(earlierFindings, { head: run.head, findings });
  const byKey = new Map(findings.map((finding) => [finding.key, finding]));
  return {
    outstanding: answer.findings
      .filter((entry) => entry.outcome === STANDS)
      .map(({ key, reason }) => {
        const { where, path, line, head, finding } = byKey.get(key);
        const title = finding.match(/^\*\*Blocking · [^:\r\n]+: (.+)\*\*(?:\r?\n|$)/)?.[1];
        return { key, where, path, line, head, ...(title ? { title } : {}), reason };
      }),
  };
}
