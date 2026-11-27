// Verdict classification: which confirmed findings gate the pull request,
// which take a never-gating class, which are advisory. One stage answers
// for both verdicts a run can hold, the main review's and the brief
// stage's, because one defect takes one call across the two. The stage is
// given the pull request as a review is, each verdict's confirmed findings
// as the digest and the verifier give them, everything Minos posted on the
// pull request before, and what the filing destination already holds. Its
// answer becomes one `minos-verdict-decision-v1` decision per verdict,
// held to the same validation the publication composer applies.

import { projectGuidanceFromInput, projectGuidanceSection } from "../orientation-guidance.mjs";
import { pullRequestFromInput, pullRequestSection } from "../pull-request-description.mjs";
import { repositoryDescriptionFromInput, repositoryDescriptionSection } from "../repository-description.mjs";
import { REVIEWED_CHANGE_SLOTS } from "../review-prompts.mjs";
import { DECISION_KIND, DIGEST_KIND, NEVER_GATING_CLASSES, SEVERITY, validateVerdictDecision } from "../verdict-classification.mjs";

export const GROUPS = ["main", "brief"];

const FINDING_FIELDS = {
  key: "string",
  severity: "string",
  atOrAboveThreshold: "boolean",
  path: "string",
  line: "number",
  title: "string",
  explanation: "string",
  verification: "string",
  source: "string",
};

function groupsOf(input) {
  return GROUPS.filter((group) => input.findings[group] !== undefined);
}

function findingsProblem(findings) {
  if (!findings || typeof findings !== "object" || Array.isArray(findings)) return "findings must be an object of groups";
  for (const group of Object.keys(findings))
    if (!GROUPS.includes(group)) return `findings.${group} is not a group`;
  if (findings.main === undefined) return "findings.main is required";
  for (const group of GROUPS) {
    if (findings[group] === undefined) continue;
    if (!Array.isArray(findings[group])) return `findings.${group} must be a list`;
    const keys = new Set();
    for (const finding of findings[group]) {
      if (!finding || typeof finding !== "object") return `findings.${group} holds a finding that is not an object`;
      for (const [field, type] of Object.entries(FINDING_FIELDS))
        if (typeof finding[field] !== type) return `findings.${group} holds a finding whose ${field} is not a ${type}`;
      if (!Object.hasOwn(SEVERITY, finding.severity)) return `findings.${group} holds a finding of unknown severity ${finding.severity}`;
      if (keys.has(finding.key)) return `findings.${group} names ${finding.key} twice`;
      keys.add(finding.key);
    }
  }
  return null;
}

function earlierProblem(earlier) {
  if (!Array.isArray(earlier)) return "earlierFindings must be a list";
  for (const finding of earlier)
    if (
      !finding || typeof finding.path !== "string" || typeof finding.line !== "number" ||
      typeof finding.head !== "string" || typeof finding.finding !== "string"
    ) return "each earlier finding needs its path, line, head and finding";
  return null;
}

// The digest validateVerdictDecision checks a group's decision against,
// rebuilt from the input so that the answer is checked against exactly the
// facts the stage was shown.
function groupDigest(input, group) {
  return { kind: DIGEST_KIND, status: "complete", threshold: input.threshold, findings: input.findings[group] };
}

const DISPOSITION_SCHEMA = {
  type: "object",
  additionalProperties: false,
  required: ["key", "gating", "class", "defect", "undergrade"],
  properties: {
    key: { type: "string" },
    gating: { type: "boolean" },
    class: { type: "string", enum: ["", ...NEVER_GATING_CLASSES] },
    defect: { type: "string" },
    undergrade: { type: "string" },
  },
};

const DECISION_SCHEMA = {
  type: "object",
  additionalProperties: false,
  required: ["verdict", "basis", "findings"],
  properties: {
    verdict: { type: "string", enum: ["clean", "request-changes"] },
    basis: { type: "string" },
    findings: { type: "array", items: DISPOSITION_SCHEMA },
  },
};

// One group's answer as a recorded decision: the kind added, and the
// fields the answer leaves empty where they do not apply dropped, since
// the decision's contract is that an absent field does not apply.
export function decisionFrom(answered) {
  return {
    kind: DECISION_KIND,
    verdict: answered.verdict,
    basis: answered.basis,
    findings: answered.findings.map(({ key, gating, class: kind, defect, undergrade }) => ({
      key,
      gating,
      ...(kind === "" ? {} : { class: kind }),
      ...(defect === "" ? {} : { defect }),
      ...(undergrade === "" ? {} : { undergrade }),
    })),
  };
}

export const stage = {
  name: "verdict-classification",
  slots: [...REVIEWED_CHANGE_SLOTS, "threshold", "findings", "earlier-findings", "filed-entries"],

  // {
  //   target, head            the commits bounding the pull request
  //   guidance, pullRequest,  as a review's input builder attaches them
  //   repositoryDescription
  //   threshold               the repository's severity threshold
  //   findings                { main, brief? }: each verdict's confirmed
  //                           findings as { key, severity,
  //                           atOrAboveThreshold, path, line, title,
  //                           explanation, verification, source }, brief
  //                           only where the brief stage returned a verdict
  //   earlierFindings         every finding Minos posted on the pull
  //                           request before, as { path, line, head, finding }
  //   filedEntries            the filing destination's text, or a sentence
  //                           saying why the run could not read it
  // }
  inputProblem(input) {
    if (!input || typeof input !== "object" || Array.isArray(input)) return "the input must be an object";
    for (const name of ["target", "head"])
      if (typeof input[name] !== "string" || input[name] === "") return `${name} must be a commit`;
    if (!projectGuidanceFromInput(input)) return "the reviewed project's guidance is missing or malformed";
    if (!Object.hasOwn(SEVERITY, input.threshold)) return "threshold must be Critical, High, Medium or Low";
    return findingsProblem(input.findings) || earlierProblem(input.earlierFindings) ||
      (typeof input.filedEntries === "string" && input.filedEntries.trim() !== "" ? null : "filedEntries must say what the destination holds or why it could not be read");
  },

  promptValues(input) {
    const findings = Object.fromEntries(groupsOf(input).map((group) => [group, input.findings[group]]));
    return {
      target: input.target,
      head: input.head,
      "project-documents": projectGuidanceSection(projectGuidanceFromInput(input)),
      "repository-description": repositoryDescriptionSection(repositoryDescriptionFromInput(input)),
      "pull-request": pullRequestSection(pullRequestFromInput(input)),
      threshold: input.threshold,
      findings: JSON.stringify(findings, null, 2),
      "earlier-findings": JSON.stringify(input.earlierFindings.map(({ path, line, head, finding }) => ({ path, line, head, finding })), null, 2),
      "filed-entries": input.filedEntries,
    };
  },

  // A decision for each group the stage was shown, and for no other. Every
  // field is required, since the judgement role may run on an engine whose
  // schemas take no optional property; empty strings stand for absent.
  answerSchema(input) {
    const groups = groupsOf(input);
    return {
      type: "object",
      additionalProperties: false,
      required: groups,
      properties: Object.fromEntries(groups.map((group) => [group, DECISION_SCHEMA])),
    };
  },

  answerProblem(answer, input) {
    for (const group of groupsOf(input)) {
      if (!answer[group]) return `there is no decision for ${group}`;
      const validation = validateVerdictDecision(decisionFrom(answer[group]), groupDigest(input, group));
      if (!validation.ok) return `the ${group} decision does not hold: ${validation.reason}`;
    }
    for (const group of Object.keys(answer))
      if (!groupsOf(input).includes(group)) return `${group} was not shown, so it takes no decision`;
    return null;
  },
};
