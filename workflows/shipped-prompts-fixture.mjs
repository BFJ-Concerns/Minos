// The shipped prompts and contracts as the input builders attach them, for
// tests that hand a workflow its input directly. The prompts read as they
// do for a repository that leaves every prompt setting unset.
import { fileURLToPath } from "node:url";

import { reviewContracts } from "./review-contracts.mjs";
import { promptSettingsFromEnvironment } from "./prompt-settings.mjs";
import { reviewPrompts } from "./review-prompts.mjs";

const shipped = reviewPrompts(fileURLToPath(new URL("..", import.meta.url)), promptSettingsFromEnvironment({}));

export const shippedConcerns = shipped.concerns;
export const shippedPrompts = shipped.prompts;
export const shippedBriefPrompts = {
  repository: shipped.prompts.repository,
  briefRelevance: shipped.prompts.briefRelevance,
  briefPartition: shipped.prompts.briefPartition,
  assignment: shipped.prompts.assignment,
  merge: shipped.prompts.merge,
  verifier: shipped.prompts.verifier,
};

export function shippedContracts() {
  return reviewContracts(shipped.concerns.map(({ name }) => name));
}
