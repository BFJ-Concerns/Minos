// The description Minos keeps of a repository, for everything in a run
// that reads one. The description is an account of the repository's
// layout, its components and how they connect, written by the
// repository-description stage (workflows/judgements) from the default
// branch at one commit. The service keeps it with that commit as one JSON
// record for each repository (internal/shell/repository_description.go):
//
//   { "kind": "minos-repository-description-v1", "commit": "", "description": "" }
//
// with whatever else the service notes beside them. A run reads its own
// copy of the record, which its orientation names, so every leg of one run
// is shown the same description whatever the service keeps by the time
// the leg starts. An input builder attaches what descriptionFromOrientation
// returns as `repositoryDescription`, and a workflow or a stage shows it
// with repositoryDescriptionSection, as the value of the
// `{{repository-description}}` slot (workflows/review-briefs/README.md).
//
// A repository has no description until one is enabled for it and the
// first has been written, so every reader here takes "none" as an
// ordinary answer.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const RECORD_KIND = "minos-repository-description-v1";

// The longest description the stage may return and a record may hold. The
// description goes into the prompt of every leg of every review of the
// repository, so its length is paid for on each of them.
export const MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS = 40_000;

const COMMIT = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;

// Whether a value is a commit's full object name, the only form a
// description's commit takes.
export function isCommit(value) {
  return typeof value === "string" && COMMIT.test(value);
}

// What is wrong with a description and the commit it was made from, or
// null. The stage's answer, a record and a workflow's input are all held
// to this.
export function descriptionProblem(commit, description) {
  if (!isCommit(commit)) return "a description needs the commit it was made from, as a full object name";
  if (typeof description !== "string" || description.trim() === "") return "the description is blank";
  if (description.length > MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS)
    return `the description is longer than ${MAXIMUM_REPOSITORY_DESCRIPTION_CHARACTERS} characters`;
  return null;
}

// The description a record file holds, as `{ commit, description }`, or
// undefined where there is no file: none has been kept. A file that is
// there and is not a whole record throws.
export function descriptionFromRecord(path) {
  let text;
  try {
    text = readFileSync(resolve(path), "utf8");
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    throw new Error(`repository description record ${path} cannot be read (${error.message})`);
  }
  let record;
  try {
    record = JSON.parse(text);
  } catch {
    throw new Error(`repository description record ${path} is not JSON`);
  }
  if (!record || typeof record !== "object" || Array.isArray(record) || record.kind !== RECORD_KIND)
    throw new Error(`repository description record ${path} is not a ${RECORD_KIND} record`);
  const problem = descriptionProblem(record.commit, record.description);
  if (problem) throw new Error(`repository description record ${path}: ${problem}`);
  return { commit: record.commit, description: record.description };
}

// The description a run was set up with, for the input builders:
// undefined where the orientation names none. `repositoryDescription` in
// an orientation is the path of the run's copy of the record, and a path
// it names that holds no whole record fails the build.
export function descriptionFromOrientation(orientation) {
  if (orientation.repositoryDescription === undefined) return undefined;
  if (typeof orientation.repositoryDescription !== "string" || orientation.repositoryDescription === "")
    throw new Error("orientation's repository-description record path is unusable");
  const description = descriptionFromRecord(orientation.repositoryDescription);
  if (description === undefined) throw new Error("orientation names a missing repository-description record");
  return description;
}

// A workflow's check of the description its input builder attached from
// descriptionFromOrientation: the commit and the text, or null when there
// is none to show.
export function repositoryDescriptionFromInput(input) {
  const attached = input && input.repositoryDescription;
  if (!attached || descriptionProblem(attached.commit, attached.description)) return null;
  return { commit: attached.commit, description: attached.description };
}

// The description as the prompts show it, or nothing when there is none.
export function repositoryDescriptionSection(repositoryDescription) {
  if (!repositoryDescription) return "";
  return `<repository-description commit="${repositoryDescription.commit}">\n${repositoryDescription.description}\n</repository-description>`;
}
