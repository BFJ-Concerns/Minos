// Reads the pull request's title and description from the record a run's
// orientation names, for the input builders. A record the orientation
// names but that cannot be read fails the build; a record with neither a
// title nor a description gives none, as does an orientation naming none.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const MAXIMUM_DESCRIPTION_CHARACTERS = 65_536;

export function pullRequestFromOrientation(orientation) {
  if (orientation.pullRequest === undefined) return undefined;
  if (typeof orientation.pullRequest !== "string" || orientation.pullRequest === "")
    throw new Error("orientation's pull-request record path is unusable");
  let record;
  try {
    record = JSON.parse(readFileSync(resolve(orientation.pullRequest), "utf8"));
  } catch {
    throw new Error("orientation names a missing or non-JSON pull-request record");
  }
  if (!record || typeof record !== "object" || Array.isArray(record) ||
      typeof record.title !== "string" || typeof record.body !== "string")
    throw new Error("pull-request record needs string title and body fields");
  const body = record.body.length > MAXIMUM_DESCRIPTION_CHARACTERS
    ? `${record.body.slice(0, MAXIMUM_DESCRIPTION_CHARACTERS)}\n[pull-request description truncated]`
    : record.body;
  if (record.title.trim() === "" && body.trim() === "") return undefined;
  return { title: record.title, body };
}

// A workflow's check of the description its input builder attached from
// pullRequestFromOrientation: the title and body, or null when there is
// none to show.
export function pullRequestFromInput(input) {
  const pullRequest = input && input.pullRequest;
  if (
    !pullRequest ||
    typeof pullRequest.title !== "string" ||
    typeof pullRequest.body !== "string" ||
    (pullRequest.title.trim() === "" && pullRequest.body.trim() === "")
  ) return null;
  return { title: pullRequest.title, body: pullRequest.body };
}

// The description as the prompts show it, or nothing when there is none.
export function pullRequestSection(pullRequest) {
  if (!pullRequest) return "";
  return `<pull-request-description title=${JSON.stringify(pullRequest.title)}>\n${pullRequest.body}\n</pull-request-description>`;
}
