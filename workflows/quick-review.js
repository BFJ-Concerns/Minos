export const meta = {
  name: "minos-review",
  description: "General pull-request review with cross-family verification",
  phases: [
    { title: "Review", detail: "a general reviewer reads the whole change, or divides it into shares" },
    { title: "Shares", detail: "one general reviewer for each share of a divided change" },
    { title: "Merge", detail: "group the proposed findings one fix would resolve" },
    { title: "Verify", detail: "opposite-family verification of each merged finding" },
  ],
};

// The quick review (review-mode.mjs): general reviewers with no plan and no
// concerns. One is given the whole change and either reviews it or divides
// it into shares, and a reviewer is then sent for each share. Every
// finding they propose goes through the merge and verification every
// review shares (review-stages.mjs), loaded from beside this script as
// review.js loads them.
//
// The workflow's name is the main review's whichever its mode, so whatever
// finds the main review's status snapshot by its workflow name finds this
// one too; the envelope's `mode` says which review it was.
const { contractsFromInput } = await import("./review-contracts.mjs");
const { routingFromInput } = await import("./role-routing.mjs");
const { projectGuidanceFromInput, projectGuidanceSection } = await import("./orientation-guidance.mjs");
const { repositoryDescriptionFromInput, repositoryDescriptionSection } = await import("./repository-description.mjs");
const { pullRequestFromInput, pullRequestSection } = await import("./pull-request-description.mjs");
const { promptFits, fillPrompt } = await import("./review-prompts.mjs");
const { reviewStages, proposalsOf, subjectOf, proposedFindingOf, uncheckedFindingOf } = await import("./review-stages.mjs");
const { QUICK } = await import("./review-mode.mjs");

// The leg role every general reviewer is recorded under, and the `source`
// the whole-change reviewer's findings and observations carry. A share's
// reviewer carries its share's focus sentence, as a specialist carries its
// unit's.
const GENERAL = "general";
const WHOLE_CHANGE_SOURCE = "general review of the whole change";

function promptsFromInput(input) {
  const prompts = input && input.prompts;
  if (!prompts || typeof prompts !== "object" || Array.isArray(prompts)) return null;
  const assignment = prompts.generalAssignment;
  if (!assignment || typeof assignment !== "object" || Array.isArray(assignment)) return null;
  if (
    !promptFits(prompts.general, "general") ||
    !promptFits(assignment.wholeChange, "general-assignment") ||
    !promptFits(assignment.share, "general-assignment") ||
    !promptFits(prompts.merge, "merge") ||
    !promptFits(prompts.verifier, "verifier")
  ) return null;
  return prompts;
}

// A division the whole-change reviewer returned: null where it is fewer
// than two shares, which is no division; the shares where each has a
// distinct id, a focus and changed paths no other share lists; false
// otherwise, a division no reviewer can be sent on.
function divisionOf(shares, changedPaths) {
  if (!Array.isArray(shares) || shares.length < 2) return null;
  const ids = new Set();
  const listed = new Set();
  for (const share of shares) {
    if (
      !share || typeof share !== "object" || Array.isArray(share) ||
      typeof share.id !== "string" || share.id === "" || ids.has(share.id) ||
      typeof share.focus !== "string" || share.focus.trim() === "" ||
      !Array.isArray(share.paths) || share.paths.length === 0
    ) return false;
    ids.add(share.id);
    for (const path of share.paths) {
      if (typeof path !== "string" || !changedPaths.has(path) || listed.has(path)) return false;
      listed.add(path);
    }
  }
  return shares;
}

// A share as general-assignment/share.md shows it, in the form a planned
// unit's focus takes in specialist.md: its sentence, then its paths.
function shareText(share) {
  return `${share.focus}\n\n${share.paths.map((path) => `- \`${path}\``).join("\n")}`;
}

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
const target = input && typeof input.target === "string" ? input.target : null;
const head = input && typeof input.head === "string" ? input.head : null;
if (!target || !head || !Array.isArray(input.changedPaths))
  throw new Error("quick review workflow needs args {target, head, changedPaths, guidance, prompts, contracts}");
const changedPaths = new Set(input.changedPaths);
const contracts = contractsFromInput(input);
if (!contracts) throw new Error("deterministic input omitted or malformed the review contracts");
const prompts = promptsFromInput(input);
if (!prompts) throw new Error("deterministic input omitted or malformed the review prompts");
const projectGuidance = projectGuidanceFromInput(input);
if (!projectGuidance) throw new Error("deterministic input omitted reviewed-project guidance");
const routing = routingFromInput(input);
if (!routing) throw new Error("deterministic input omitted the role routing");
const reviewedChangeValues = {
  target,
  head,
  "project-documents": projectGuidanceSection(projectGuidance),
  "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
  "pull-request": pullRequestSection(pullRequestFromInput(input)),
};

const {
  incomplete, legs, outOfScopeObservations,
  proposeUnits, mergeProposed, verifySubjects,
} = reviewStages({ agent, parallel, routing, contracts, prompts, reviewedChangeValues });

const reviewers = [];
const proposed = [];

function envelope(fields = {}) {
  return {
    reviewed: { target, head, occasion: null },
    mode: QUICK,
    stage: "present",
    ...(incomplete.length > 0 ? { incomplete } : {}),
    requiredModelEvidence: legs,
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations,
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers,
    ...fields,
  };
}

// Every general reviewer proposes on the proposer's route: it has no
// concern for `[routing.concerns]` to route.
function generalPrompt(assignment) {
  return fillPrompt(prompts.general, { ...reviewedChangeValues, assignment });
}

// Records each settled leg and proposes its findings. A general reviewer
// has no concern, so none of its findings lies outside one.
function takeResults(results) {
  for (const { unit, completed, output } of results) {
    reviewers.push({
      label: unit.label,
      role: GENERAL,
      ...(unit.share ? { share: unit.share } : {}),
      family: routing.proposer.engine,
      pinnedModel: routing.proposer.model,
      status: completed ? "done" : "no-result",
    });
    if (!output) continue;
    proposed.push(...proposalsOf(output.findings, (index) => `${unit.label}:${index + 1}`, {
      source: unit.source,
      reportedBy: GENERAL,
      outsideConcern: false,
      proposingLabel: unit.label,
      proposedOn: routing.proposer.engine,
    }));
  }
}

phase("Review");
const wholeChange = {
  label: `general-${routing.proposer.engine}`,
  role: GENERAL,
  schema: contracts.generalWholeChangeSchema,
  source: WHOLE_CHANGE_SOURCE,
  prompt: generalPrompt(fillPrompt(prompts.generalAssignment.wholeChange, { target, head })),
};
const [wholeChangeResult] = await proposeUnits([wholeChange], "Review");
takeResults([wholeChangeResult]);
if (!wholeChangeResult.output) return envelope();

const division = divisionOf(wholeChangeResult.output.shares, changedPaths);
if (division === false) {
  incomplete.push("the general reviewer returned a division that does not give every share its own id, a focus and changed paths no other share lists");
  return envelope();
}

let dispatches = [];
if (division) {
  phase("Shares");
  const shareUnits = division.map((share, index) => ({
    label: `general-share-${index + 1}-${routing.proposer.engine}`,
    role: GENERAL,
    schema: contracts.generalSchema,
    source: share.focus,
    share: { id: share.id, focus: share.focus, paths: share.paths },
    prompt: generalPrompt(fillPrompt(prompts.generalAssignment.share, {
      target,
      head,
      share: shareText(share),
      "other-shares": division.filter((other) => other !== share).map(shareText).join("\n\n"),
    })),
  }));
  dispatches = shareUnits.map((unit) => ({ id: unit.share.id, label: unit.label, focus: unit.share.focus, paths: unit.share.paths, family: routing.proposer.engine }));
  takeResults(await proposeUnits(shareUnits, "Shares"));
}

phase("Merge");
const groups = await mergeProposed(proposed, "merge", "Merge");
if (!groups) return envelope({ dispatches });

phase("Verify");
const { verified, unchecked } = await verifySubjects(groups.map(subjectOf), "verify", "merge", "Verify");

return envelope({
  proposedFindings: verified.map(proposedFindingOf),
  uncheckedFindings: unchecked.map(uncheckedFindingOf),
  dispatches,
});
