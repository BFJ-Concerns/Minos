export const meta = {
  name: "minos-repository-review-briefs",
  description: "Run applicable repository review briefs after the main pull-request review",
  phases: [
    { title: "Relevance", detail: "select the briefs this change gives work to" },
    { title: "Partition", detail: "divide broad brief scopes into coherent review units" },
    { title: "Review", detail: "run repository-concern specialists" },
    { title: "Merge", detail: "group the proposed findings one fix would resolve" },
    { title: "Verify", detail: "opposite-family verification of each merged finding" },
  ],
};


function slug(value) {
  return String(value).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "review";
}

// The input checks, prompt filling, merge and verification below are the
// same in review.js and review-briefs.js: a workflow script imports
// nothing, so each carries them.

// The finding, merge and verifier contracts arrive as `contracts`, built
// once in workflows/review-contracts.mjs by the input builder; this script
// keeps no copy, and a missing or malformed set fails the workflow before
// dispatch.
function contractsFromInput(input) {
  const contracts = input && input.contracts;
  if (!contracts || typeof contracts !== "object" || Array.isArray(contracts)) return null;
  if (!Number.isInteger(contracts.verifierBatchSize) || contracts.verifierBatchSize < 1) return null;
  if (!Number.isInteger(contracts.verificationRounds) || contracts.verificationRounds < 1) return null;
  for (const name of ["mergeFindingFields", "verifierFindingFields"])
    if (!Array.isArray(contracts[name]) || !contracts[name].includes("id")) return null;
  for (const name of [
    "explorationSchema",
    "findingShape",
    "outOfScopeObservationShape",
    "applicabilityShape",
    "specialistSchema",
    "mergeSchema",
    "verifierVerdictShape",
    "verifierSchema",
  ]) {
    const shape = contracts[name];
    if (
      !shape || typeof shape !== "object" || Array.isArray(shape) ||
      shape.type !== "object" ||
      !Array.isArray(shape.required) ||
      !shape.properties || typeof shape.properties !== "object" || Array.isArray(shape.properties)
    ) return null;
  }
  return contracts;
}

// A prompt is its shipped file with the file slots already filled by the
// input builder (workflows/review-prompts.mjs); the run slots are filled
// here, in one pass, so that no value is itself read for slots. A prompt
// carrying a slot its stage does not fill fails the workflow before
// dispatch.
const SLOT = /\{\{([a-z-]+)\}\}/g;
const REVIEWED_CHANGE_SLOTS = ["target", "head", "project-documents", "pull-request"];
const STAGE_SLOTS = {
  exploration: [...REVIEWED_CHANGE_SLOTS, "concerns"],
  specialist: [...REVIEWED_CHANGE_SLOTS, "concern-name", "other-concerns", "planned-focus"],
  repository: [...REVIEWED_CHANGE_SLOTS, "brief-path", "brief", "assignment"],
  merge: ["target", "head", "findings"],
  verifier: [...REVIEWED_CHANGE_SLOTS, "findings"],
};

function promptFits(text, stage) {
  return typeof text === "string" && text.trim() !== "" &&
    [...text.matchAll(SLOT)].every((match) => STAGE_SLOTS[stage].includes(match[1]));
}

function fillPrompt(template, values) {
  return template.replace(SLOT, (whole, name) => {
    if (!Object.hasOwn(values, name)) throw new Error(`prompt slot ${whole} has no value`);
    return values[name];
  });
}

const ROUTING_ROLES = ["exploration", "proposer", "verifier", "engagement-gate", "brief-planner"];
const ROUTING_ENGINES = ["claude", "codex"];

// The resolved routing the input builder attached: every role's engine,
// model and effort, already defaulted for what the deployment provisioned.
// Nothing here is fixed in source; a missing or malformed table fails the
// workflow before any dispatch.
function routingFromInput(input) {
  const routing = input && input.routing;
  if (!routing || typeof routing !== "object" || Array.isArray(routing)) return null;
  for (const role of ROUTING_ROLES) {
    const entry = routing[role];
    if (
      !entry ||
      !ROUTING_ENGINES.includes(entry.engine) ||
      typeof entry.model !== "string" || entry.model === "" ||
      typeof entry.effort !== "string" || entry.effort === ""
    ) return null;
  }
  return routing;
}

function projectGuidanceFromInput(input) {
  const guidance = input && input.guidance;
  if (!Array.isArray(guidance) || guidance.length === 0) return null;
  for (const entry of guidance) {
    if (
      !entry ||
      typeof entry.path !== "string" ||
      entry.path === "" ||
      (entry.repository !== null && (typeof entry.repository !== "string" || entry.repository === "")) ||
      (entry.origin !== "configured" && entry.origin !== "checked-in") ||
      typeof entry.content !== "string" ||
      entry.content.trim() === ""
    ) return null;
  }
  return guidance;
}

// One block per guidance document, in the configured order: the reviewed
// project's declared intent, as the repository owner names it.
function projectGuidanceSection(guidance) {
  return guidance.map((entry) =>
    `<project-guidance origin="${entry.origin}"${entry.repository === null ? "" : ` repository="${entry.repository}"`} path="${entry.path}">\n` +
    `${entry.content}\n</project-guidance>`
  ).join("\n\n");
}

function pullRequestFromInput(input) {
  const pullRequest = input && input.pullRequest;
  if (
    !pullRequest ||
    typeof pullRequest.title !== "string" ||
    typeof pullRequest.body !== "string" ||
    (pullRequest.title.trim() === "" && pullRequest.body.trim() === "")
  ) return null;
  return { title: pullRequest.title, body: pullRequest.body };
}

function pullRequestSection(pullRequest) {
  if (!pullRequest) return "";
  return `<pull-request-description title=${JSON.stringify(pullRequest.title)}>\n${pullRequest.body}\n</pull-request-description>`;
}

function pick(record, fields) {
  return Object.fromEntries(fields.filter((field) => record[field] !== undefined).map((field) => [field, record[field]]));
}

function legOutput(result, label) {
  if (!result || result.failure !== null) {
    incomplete.push(`agent ${result?.label || label} failed: ${result?.failure?.message || "no failure account returned"}`);
    return null;
  }
  if (result.output === null)
    incomplete.push(`agent ${result.label || label} answered without usable output`);
  return result.output;
}

function addLeg(label, role, pinnedModel, findingIds = null) {
  const leg = { label, role, pinnedModel };
  if (findingIds) leg.findingIds = findingIds;
  legs.push(leg);
}

function recordObservations(observations, source, label) {
  (Array.isArray(observations) ? observations : []).forEach((observation, index) => {
    outOfScopeObservations.push({
      id: `${label}:observation:${index + 1}`,
      source,
      ...observation,
      observingLabel: label,
      verified: false,
    });
  });
}

// The merge leg groups the proposed findings one fix would resolve, so
// that each fault is verified and published once. Fewer than two findings
// need no merge. A grouping that does not place every finding in exactly
// one group, under a primary of its own, is no usable answer: the leg
// counts as one that did not complete, and null stops the workflow.
async function mergeProposed(proposed, mergeLabel, phaseName) {
  if (proposed.length < 2) return proposed.map((item) => ({ primary: item, members: [] }));
  addLeg(mergeLabel, "merge", routing.verifier.model);
  const shown = proposed.map((item) => pick({
    id: item.id,
    reportedBy: item.reportedBy,
    outsideConcern: item.outsideConcern,
    ...item.finding,
  }, contracts.mergeFindingFields));
  const settlement = await agent(
    fillPrompt(prompts.merge, { target, head, findings: JSON.stringify(shown, null, 2) }),
    {
      engine: routing.verifier.engine,
      schema: contracts.mergeSchema,
      model: routing.verifier.model,
      effort: routing.verifier.effort,
      strip: ["skills", "agents"],
      identity: true,
      label: mergeLabel,
      phase: phaseName,
    }
  );
  const output = legOutput(settlement, mergeLabel);
  if (!output) return null;
  const byId = new Map(proposed.map((item) => [item.id, item]));
  const placed = new Set();
  const groups = [];
  for (const group of Array.isArray(output.groups) ? output.groups : []) {
    const ids = Array.isArray(group && group.findingIds) ? group.findingIds : [];
    if (ids.length === 0 || !ids.includes(group.primary) || ids.some((id) => !byId.has(id) || placed.has(id))) {
      incomplete.push(`agent ${mergeLabel} returned a grouping that does not place every finding exactly once under a primary of its group`);
      return null;
    }
    ids.forEach((id) => placed.add(id));
    groups.push({
      primary: byId.get(group.primary),
      members: ids.filter((id) => id !== group.primary).map((id) => byId.get(id)),
    });
  }
  if (placed.size !== proposed.length) {
    incomplete.push(`agent ${mergeLabel} returned a grouping that does not place every finding exactly once under a primary of its group`);
    return null;
  }
  return groups;
}

// One finding to verify: a merged group's primary write-up, with what the
// group's other write-ups add, or a finding a verifier raised.
function subjectOf({ primary, members }) {
  const writeUps = [primary, ...members];
  const dependsOn = [...new Set(writeUps
    .map((item) => typeof item.finding.dependsOn === "string" ? item.finding.dependsOn.trim() : "")
    .filter((fact) => fact !== ""))].join("; ");
  return {
    ...primary,
    finding: { ...primary.finding, dependsOn },
    members,
  };
}

function shownToVerifier(subject) {
  return pick({
    id: subject.id,
    ...subject.finding,
    alsoReportedAs: subject.members.length > 0
      ? subject.members.map(({ finding }) => ({ title: finding.title, explanation: finding.explanation }))
      : undefined,
    wholeScope: subject.wholeScope,
  }, contracts.verifierFindingFields);
}

// Verifies every subject, then each finding the verifiers raised, in
// rounds: what one round raises is merged like any proposed findings and
// verified by legs of the next, never by the leg that raised it. What the
// last round raises has no verdict, which leaves the run incomplete; it is
// returned unchecked for the record. Null means a merge failed, which has
// already left the run incomplete.
async function verifySubjects(subjects, labelPrefix, mergeLabel, phaseName) {
  const verified = [];
  let pending = subjects;
  for (let round = 1; round <= contracts.verificationRounds && pending.length > 0; round += 1) {
    const raised = await verificationRound(pending, round === 1 ? labelPrefix : `${labelPrefix}-round-${round}`, phaseName);
    verified.push(...pending);
    if (raised.length === 0 || round === contracts.verificationRounds) {
      pending = raised;
      break;
    }
    const groups = await mergeProposed(raised, `${mergeLabel}-round-${round + 1}`, phaseName);
    if (!groups) return { verified, unchecked: raised };
    pending = groups.map(subjectOf);
  }
  if (pending.length > 0)
    incomplete.push(`verification stopped after ${contracts.verificationRounds} rounds with ${pending.length} finding(s) raised and not verified`);
  return { verified, unchecked: pending };
}

async function verificationRound(subjects, labelPrefix, phaseName) {
  const batches = [];
  for (let offset = 0; offset < subjects.length; offset += contracts.verifierBatchSize) {
    const items = subjects.slice(offset, offset + contracts.verifierBatchSize);
    const label = `${labelPrefix}-${batches.length + 1}-${routing.verifier.engine}`;
    const findingIds = items.map((item) => item.id);
    items.forEach((item) => { item.verifyLabel = label; item.rawVerifier = null; });
    batches.push({ items, label, findingIds });
    addLeg(label, "verifier", routing.verifier.model, findingIds);
  }
  const settlements = await parallel(batches.map((batch) => () =>
    agent(
      fillPrompt(prompts.verifier, {
        ...reviewedChangeValues,
        findings: JSON.stringify(batch.items.map(shownToVerifier), null, 2),
      }),
      {
        engine: routing.verifier.engine,
        schema: contracts.verifierSchema,
        model: routing.verifier.model,
        effort: routing.verifier.effort,
        strip: ["skills", "agents"],
        identity: true,
        label: batch.label,
        phase: phaseName,
      }
    )
  ));
  const raised = [];
  batches.forEach((batch, index) => {
    const response = legOutput(settlements[index], batch.label);
    if (!response) return;
    // What a verifier established while checking survives even when its
    // verdict set is discarded as malformed.
    recordObservations(response.outOfScopeObservations, "verification", batch.label);
    (Array.isArray(response.newFindings) ? response.newFindings : []).forEach((finding, findingIndex) => {
      raised.push({
        id: `${batch.label}:raised:${findingIndex + 1}`,
        finding,
        source: "verification",
        reportedBy: "verification",
        outsideConcern: false,
        proposingLabel: batch.label,
        members: [],
      });
    });
    if (!Array.isArray(response.verdicts)) return;
    const expected = new Set(batch.findingIds);
    if (response.verdicts.some((verdict) => !expected.has(verdict && verdict.findingId))) return;
    for (const item of batch.items) {
      const matches = response.verdicts.filter((verdict) => verdict.findingId === item.id);
      if (matches.length !== 1) continue;
      const { findingId: omittedFindingId, ...rawVerifier } = matches[0];
      item.rawVerifier = rawVerifier;
    }
  });
  return raised;
}

// A verified finding as the adjudicator reads it: the primary write-up,
// the group's joined outside facts, and the write-ups merged into it.
function proposedFindingOf(subject) {
  return {
    id: subject.id,
    ...subject.finding,
    source: subject.source,
    ...(subject.outsideConcern ? { outsideConcern: true } : {}),
    ...(subject.wholeScope !== undefined ? { wholeScope: subject.wholeScope } : {}),
    ...(subject.members.length > 0 ? {
      mergedFrom: subject.members.map((member) => ({
        id: member.id,
        source: member.source,
        proposingLabel: member.proposingLabel,
        title: member.finding.title,
        path: member.finding.path,
        line: member.finding.line,
      })),
    } : {}),
    proposingLabel: subject.proposingLabel,
    verifyLabel: subject.verifyLabel,
    rawVerifier: subject.rawVerifier,
  };
}

function uncheckedFindingOf(subject) {
  return {
    id: subject.id,
    ...subject.finding,
    source: subject.source,
    proposingLabel: subject.proposingLabel,
  };
}

// The deterministic brief pass arrives computed: the input builder parsed
// each brief's frontmatter, named it and settled its path-scope and
// occasion triggers through workflows/brief-dispositions.mjs, the one
// source the scope workflow's engagement reads too. This workflow adds the
// judged legs and re-derives nothing; a brief missing any of it fails the
// workflow before dispatch.
function briefsFromInput(input) {
  const briefs = input && input.briefs;
  if (!Array.isArray(briefs)) return null;
  for (const brief of briefs) {
    const front = brief && brief.front;
    const disposition = brief && brief.disposition;
    if (
      !brief ||
      typeof brief.path !== "string" || brief.path === "" ||
      typeof brief.content !== "string" ||
      (brief.scope !== null && typeof brief.scope !== "string") ||
      typeof brief.title !== "string" || brief.title === "" ||
      !front || typeof front !== "object" ||
      (front.extent !== "diff" && front.extent !== "full") ||
      (front.sweep !== "per-file" && front.sweep !== "whole-tree") ||
      !Array.isArray(front.occasion) ||
      (front.relevance !== null && typeof front.relevance !== "string") ||
      !disposition || typeof disposition !== "object" ||
      !(disposition.status === "skipped"
        ? typeof disposition.skipKind === "string" && typeof disposition.reason === "string"
        : typeof disposition.triggered === "boolean") ||
      (disposition.misconfiguration !== undefined && disposition.misconfiguration !== null &&
        (typeof disposition.misconfiguration.skipKind !== "string" || typeof disposition.misconfiguration.reason !== "string"))
    ) return null;
  }
  return briefs;
}

function promptsFromInput(input) {
  const prompts = input && input.prompts;
  if (!prompts || typeof prompts !== "object" || Array.isArray(prompts)) return null;
  if (
    !promptFits(prompts.repository, "repository") ||
    !promptFits(prompts.merge, "merge") ||
    !promptFits(prompts.verifier, "verifier")
  ) return null;
  return prompts;
}

function emptyEnvelope(input) {
  return {
    reviewed: {
      target: input && typeof input.target === "string" ? input.target : null,
      head: input && typeof input.head === "string" ? input.head : null,
      occasion: input && typeof input.occasion === "string" ? input.occasion : null,
    },
    stage: "absent",
    requiredModelEvidence: [],
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations: [],
    briefs: [],
    misconfigurations: [],
    dispatches: [],
    reviewers: [],
  };
}

const relevanceSchema = {
  type: "object",
  additionalProperties: false,
  required: ["decisions"],
  properties: {
    decisions: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["brief", "applicable", "reason"],
        properties: {
          brief: { type: "string" },
          applicable: { type: "boolean" },
          reason: { type: "string" },
        },
      },
    },
  },
};

const partitionSchema = {
  type: "object",
  additionalProperties: false,
  required: ["units"],
  properties: {
    units: {
      type: "array",
      minItems: 1,
      items: {
        type: "object",
        additionalProperties: false,
        required: ["id", "concern", "files"],
        properties: {
          id: { type: "string" },
          concern: { type: "string" },
          files: { type: "array", minItems: 1, items: { type: "string" } },
        },
      },
    },
  },
};

function normalisePartition(plan, inventory) {
  const known = new Set(inventory.map((entry) => entry.path));
  const assigned = new Set();
  const units = [];
  for (const unit of plan.units) {
    const files = [];
    for (const path of unit.files) {
      if (known.has(path) && !assigned.has(path)) {
        assigned.add(path);
        files.push(path);
      }
    }
    if (files.length > 0) units.push({ id: unit.id, concern: unit.concern, files });
  }
  const missing = inventory.map((entry) => entry.path).filter((path) => !assigned.has(path));
  if (missing.length > 0) {
    units.push({
      id: "unassigned-files",
      concern: "Files omitted from the proposed partition",
      files: missing,
    });
  }
  return units;
}

const input = args && typeof args === "object" && !Array.isArray(args) ? args : null;
if (!input || typeof input.target !== "string" || typeof input.head !== "string")
  throw new Error("brief workflow needs args {target, head, briefs, changedPaths, trackedFiles, guidance, prompts, contracts}");
const target = input.target;
const head = input.head;
const contracts = contractsFromInput(input);
if (!contracts) throw new Error("deterministic input omitted or malformed the review contracts");
if (input.hasReviewDirectory === false) return emptyEnvelope(input);
const briefs = briefsFromInput(input);
if (!briefs || !Array.isArray(input.changedPaths) || !Array.isArray(input.trackedFiles))
  throw new Error("deterministic brief enumeration is incomplete: every brief carries its frontmatter, title and disposition");

const prompts = promptsFromInput(input);
if (!prompts) throw new Error("deterministic input omitted or malformed the review prompts");
const guidance = projectGuidanceFromInput(input);
if (!guidance) throw new Error("deterministic input omitted reviewed-project guidance");
const routing = routingFromInput(input);
if (!routing) throw new Error("deterministic input omitted the role routing");
const reviewedChangeValues = {
  target,
  head,
  "project-documents": projectGuidanceSection(guidance),
  "pull-request": pullRequestSection(pullRequestFromInput(input)),
};

const incomplete = [];
const legs = [];
const outOfScopeObservations = [];
const reports = new Map();
const misconfigurations = [];
const candidates = [];
for (const brief of briefs) {
  const { front, disposition } = brief;
  const base = { brief: brief.path, title: brief.title };
  const { misconfiguration, ...recordedDisposition } = disposition;
  if (disposition.status === "skipped") reports.set(brief.path, { ...base, ...recordedDisposition });
  else
    candidates.push({ brief, front, base, triggered: disposition.triggered });
  if (misconfiguration)
    misconfigurations.push({
      ...base,
      kind: misconfiguration.skipKind,
      reason: misconfiguration.reason,
    });
}

// Only briefs not already settled by a deterministic trigger need the judged
// relevance evaluation; a triggered brief runs regardless of relevance.
const relevanceCandidates = candidates.filter((candidate) => candidate.front.relevance && !candidate.triggered);
const relevanceDecisions = new Map();
if (relevanceCandidates.length > 0) {
  phase("Relevance");
  addLeg("brief-relevance", "relevance", routing["brief-planner"].model);
  const relevanceResult = await agent(
    `${projectGuidanceSection(guidance)}\n\n` +
      `Judge which repository concerns ${target}...${head} gives work to. Inspect the actual diff when paths alone do not settle it. ` +
      `Mark a concern inapplicable only when the diff clearly gives it nothing to judge. Return one decision for every entry and preserve each brief path.\n` +
      `Changed paths: ${JSON.stringify(input.changedPaths)}\n` +
      `Concerns: ${JSON.stringify(relevanceCandidates.map(({ brief, front }) => ({ brief: brief.path, relevance: front.relevance })))}`,
    {
      engine: routing["brief-planner"].engine,
      schema: relevanceSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
      strip: ["skills", "agents"],
      label: "brief-relevance",
      phase: "Relevance",
    },
  );
  for (const decision of relevanceResult && Array.isArray(relevanceResult.decisions) ? relevanceResult.decisions : [])
    if (relevanceCandidates.some(({ brief }) => brief.path === decision.brief) && !relevanceDecisions.has(decision.brief))
      relevanceDecisions.set(decision.brief, decision);
}

const runnable = [];
for (const candidate of candidates) {
  const { brief, front, base, triggered } = candidate;
  if (!triggered && front.relevance) {
    const decision = relevanceDecisions.get(brief.path);
    if (!decision) {
      reports.set(brief.path, { ...base, status: "not-run", reason: "relevance judgement returned no decision" });
      continue;
    }
    if (!decision.applicable) {
      reports.set(brief.path, { ...base, status: "skipped", skipKind: "relevance", reason: decision.reason });
      continue;
    }
  }
  runnable.push(candidate);
}

function scopeInventory(scope) {
  return input.trackedFiles.filter((entry) => !scope || entry.path === scope || entry.path.startsWith(scope + "/"));
}

function makeUnit(candidate, files, suffix, concern) {
  const { brief, front, base } = candidate;
  return {
    brief: brief.path,
    briefContent: brief.content,
    title: base.title,
    extent: front.extent,
    scope: brief.scope,
    files,
    concern,
    label: `repository-${slug(brief.path)}${suffix ? `-${suffix}` : ""}-${routing.proposer.engine}`,
  };
}

const partitionRequests = runnable.map((candidate, index) => {
  const { brief, front } = candidate;
  const inventory = front.extent === "full" ? scopeInventory(brief.scope) : [];
  if (front.extent !== "full" || front.sweep !== "per-file" || inventory.length === 0) return null;
  return {
    candidate,
    inventory,
    label: `brief-partition-${index + 1}-${slug(brief.path)}-${routing["brief-planner"].engine}`,
  };
}).filter(Boolean);

const partitions = new Map();
if (partitionRequests.length > 0) {
  phase("Partition");
  for (const request of partitionRequests)
    addLeg(request.label, "partition", routing["brief-planner"].model);
  const partitionResults = await parallel(partitionRequests.map((request) => () => agent(
    `${projectGuidanceSection(guidance)}\n\n` +
      `<repository-brief path="${request.candidate.brief.path}">\n${request.candidate.brief.content}\n</repository-brief>\n\n` +
      `Create a lossless partition of the assigned file inventory into coherent review units for this brief. Group paths by module, directory, or concern, whichever reflects the repository's actual structure. ` +
      `Each dispatched unit has a real cost, so the partition is the minimal one whose units each need a genuinely distinct reading for this brief's concern — merge groups one reading covers, and never split a unit to mirror directory structure for its own sake. ` +
      `Assign every listed path to exactly one unit; include no unlisted paths. Use no skills for this planning judgement.\n` +
      `Assigned file inventory: ${JSON.stringify(request.inventory.map((entry) => entry.path))}`,
    {
      engine: routing["brief-planner"].engine,
      schema: partitionSchema,
      model: routing["brief-planner"].model,
      effort: routing["brief-planner"].effort,
      strip: ["skills", "agents"],
      identity: true,
      label: request.label,
      phase: "Partition",
    },
  )));
  partitionRequests.forEach((request, index) => {
    const result = legOutput(partitionResults[index], request.label);
    if (!result || !Array.isArray(result.units)) {
      reports.set(request.candidate.brief.path, {
        ...request.candidate.base,
        status: "not-run",
        reason: "partition exploration returned no usable result",
      });
      return;
    }
    partitions.set(request.candidate.brief.path, normalisePartition(result, request.inventory));
  });
}

const dispatched = [];
for (const candidate of runnable) {
  const { brief, front } = candidate;
  if (front.extent !== "full") {
    dispatched.push(makeUnit(candidate, [], null, null));
    continue;
  }
  const inventory = scopeInventory(brief.scope);
  if (front.sweep === "per-file" && inventory.length > 0) {
    const partition = partitions.get(brief.path);
    if (!partition) continue;
    partition.forEach((unit, index) =>
      dispatched.push(makeUnit(candidate, unit.files, index + 1, unit.concern)));
  } else {
    dispatched.push(makeUnit(candidate, inventory.map((entry) => entry.path), null, null));
  }
}

// The directory a whole-scope brief audits: the brief's own directory, or
// the repository root for a brief at the top of `.review/`.
function wholeScopeOf(unit) {
  return unit.scope ? `${unit.scope}/` : "./";
}

// What a brief's reviewer is given to judge: the change within the brief's
// directory, or for a whole-scope brief all of that directory as it stands,
// narrowed to a partition unit's files where the scope was partitioned.
function assignment(unit) {
  const scope = unit.scope ? `\`${unit.scope}/\`` : "the whole repository";
  const lines = [unit.extent === "full"
    ? `All of ${scope} as it stands at the head commit, \`${head}\`, whether or not the change touched it.`
    : `What \`git diff ${target}...${head}\` changed within ${scope}.`];
  if (unit.concern) lines.push(`Within that, this review unit: ${unit.concern}`);
  if (unit.files.length > 0) lines.push(`The files of this unit:\n\n${unit.files.map((path) => `- \`${path}\``).join("\n")}`);
  return lines.join("\n\n");
}

phase("Review");
for (const unit of dispatched) addLeg(unit.label, "specialist", routing.proposer.model);
const specialistResults = await parallel(dispatched.map((unit) => () => agent(
  fillPrompt(prompts.repository, {
    ...reviewedChangeValues,
    "brief-path": unit.brief,
    brief: unit.briefContent,
    assignment: assignment(unit),
  }),
  {
    engine: routing.proposer.engine,
    schema: contracts.specialistSchema,
    model: routing.proposer.model,
    effort: routing.proposer.effort,
    strip: ["skills", "agents"],
    identity: true,
    label: unit.label,
    phase: "Review",
  },
)));

const reviewerStates = [];
const proposed = [];
const inapplicableByBrief = new Map();
dispatched.forEach((unit, unitIndex) => {
  const settlement = specialistResults[unitIndex];
  const result = legOutput(settlement, unit.label);
  reviewerStates.push({
    label: unit.label,
    role: "specialist",
    brief: unit.brief,
    family: routing.proposer.engine,
    pinnedModel: routing.proposer.model,
    status: settlement && settlement.failure === null ? "done" : "no-result",
  });
  if (!result) {
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "not-run",
      reason: "specialist returned no result",
    });
    return;
  }
  recordObservations(result.outOfScopeObservations, unit.title, unit.label);
  const propose = (findings, kind, fields) =>
    (Array.isArray(findings) ? findings : []).forEach((finding, findingIndex) => {
      proposed.push({
        id: `repository-brief-unit-${unitIndex + 1}:${kind}:${findingIndex + 1}`,
        finding,
        source: unit.title,
        reportedBy: unit.title,
        proposingLabel: unit.label,
        ...fields,
      });
    });
  // A problem outside the brief's concern that the change introduced is a
  // finding about the change, whatever the brief's extent or applicability.
  propose(result.outsideConcernFindings, "outside", { outsideConcern: true });
  if (result.applicability.status === "inapplicable") {
    const aggregate = inapplicableByBrief.get(unit.brief) || {
      title: unit.title,
      units: [],
    };
    aggregate.units.push({
      label: unit.label,
      concern: unit.concern,
      reason: result.applicability.reason,
    });
    inapplicableByBrief.set(unit.brief, aggregate);
    return;
  }
  if (!reports.has(unit.brief))
    reports.set(unit.brief, {
      brief: unit.brief,
      title: unit.title,
      status: "run",
      reason: "applicable concern reviewed",
    });
  propose(result.findings, "finding", {
    outsideConcern: false,
    ...(unit.extent === "full" ? { wholeScope: wholeScopeOf(unit) } : {}),
  });
});

for (const [brief, inapplicable] of inapplicableByBrief) {
  const report = reports.get(brief);
  if (report) {
    reports.set(brief, {
      ...report,
      inapplicableUnits: inapplicable.units,
    });
    continue;
  }
  reports.set(brief, {
    brief,
    title: inapplicable.title,
    status: "skipped",
    skipKind: "inapplicable",
    reason: inapplicable.units.length === 1
      ? inapplicable.units[0].reason
      : `all ${inapplicable.units.length} partition units were inapplicable`,
    inapplicableUnits: inapplicable.units,
  });
}

function envelope(fields) {
  return {
    reviewed: { target, head, occasion: input.occasion || null },
    stage: "present",
    ...(incomplete.length > 0 ? { incomplete } : {}),
    requiredModelEvidence: legs,
    proposedFindings: [],
    uncheckedFindings: [],
    outOfScopeObservations,
    briefs: [...reports.values()],
    misconfigurations,
    dispatches: dispatched.map(({ brief, title, label, extent, scope, files }) => ({ brief, title, label, extent, scope, files })),
    reviewers: reviewerStates,
    ...fields,
  };
}

phase("Merge");
const groups = await mergeProposed(proposed, "brief-merge", "Merge");
if (!groups) return envelope({});

phase("Verify");
const { verified, unchecked } = await verifySubjects(groups.map(subjectOf), "verify-brief", "brief-merge", "Verify");

return envelope({
  proposedFindings: verified.map(proposedFindingOf),
  uncheckedFindings: unchecked.map(uncheckedFindingOf),
});
