// The findings Minos posted on a pull request before this run, read from
// the forge's own record of them (`minos forge snapshot` and `minos forge
// threads`). A finding sits in one of two places. Most are inline threads,
// which `threads` lists already narrowed to the ones Minos opened: the
// first comment is the finding and its `commit_id` the head it was raised
// on. One the forge could not anchor inline sits in the body of the review
// that raised it, under the folded section the service writes
// (`foldedReviewHeading` in internal/shell/review_anchoring.go), beneath a
// `#### \`PATH\` line N` heading; that review's `commit_id` is its head.
//
// Reading these is mechanical. Which of them still block, and whether a
// finding of this run restates one, are judgements made elsewhere.

export const THREAD = "thread";
export const REVIEW_BODY = "review-body";

// How a finding's text opens when it blocks (finding-presentation.mjs).
const BLOCKING_OPENING = "**Blocking";
const FOLDED_SECTION = "\n\n---\n\nFindings that could not be anchored inline:\n";
const FOLDED_ENTRY = /^#### `(.+)` lines? (\d+)(?:-\d+)?$/;

// The entries of one review body's folded section, each with the path,
// line and text the heading and the lines below it give. A body without the
// section has none.
export function foldedEntries(body) {
  const at = typeof body === "string" ? body.indexOf(FOLDED_SECTION) : -1;
  if (at === -1) return [];
  const entries = [];
  let current = null;
  for (const line of body.slice(at + FOLDED_SECTION.length).split("\n")) {
    const heading = line.match(FOLDED_ENTRY);
    if (heading) {
      current = { path: heading[1], line: Number(heading[2]), lines: [] };
      entries.push(current);
    } else if (current) {
      current.lines.push(line);
    }
  }
  return entries.map(({ path, line, lines }) => ({ path, line, finding: lines.join("\n").trim() }));
}

// Every finding Minos posted, oldest place first: threads in the order the
// forge lists them, then each Minos review's folded entries. Each carries
// `key` (unique in this list), `where`, `path`, `line`, `head`, `finding`
// and `blocking`. A thread finding carries `resolved` and its `replies`,
// the thread's later comments as the forge gave them; a review-body
// finding carries the review's `submittedAt`.
export function earlierMinosFindings(snapshot, threads) {
  if (!snapshot || typeof snapshot.authenticated_user !== "string" || snapshot.authenticated_user === "")
    throw new Error("the forge snapshot names no authenticated user, so Minos's own reviews cannot be told apart");
  if (!Array.isArray(threads)) throw new Error("the forge's threads are not a list");
  const found = [];
  threads.forEach((thread, index) => {
    const [first, ...replies] = Array.isArray(thread.comments) ? thread.comments : [];
    if (!first || typeof first.body !== "string") return;
    found.push({
      key: `thread-${index + 1}`,
      where: THREAD,
      path: thread.path,
      line: thread.line,
      head: first.commit_id,
      finding: first.body.trim(),
      blocking: first.body.trimStart().startsWith(BLOCKING_OPENING),
      resolved: thread.resolved === true,
      replies,
    });
  });
  const reviews = Array.isArray(snapshot.reviews) ? snapshot.reviews : [];
  for (const review of reviews) {
    if (review.user !== snapshot.authenticated_user) continue;
    foldedEntries(review.body).forEach((entry, index) => {
      found.push({
        key: `review-${review.id}-${index + 1}`,
        where: REVIEW_BODY,
        ...entry,
        head: review.commit_id,
        blocking: entry.finding.startsWith(BLOCKING_OPENING),
        submittedAt: review.submitted_at,
      });
    });
  }
  return found;
}
