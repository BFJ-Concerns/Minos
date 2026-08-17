import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";

const LOW_COMBINED_CONFIDENCE = 70;

async function jsonAt(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

async function manifestPaths(directory) {
  const found = [];
  const visit = async (path) => {
    const entries = await readdir(path, { withFileTypes: true });
    for (const entry of entries) {
      const child = join(path, entry.name);
      if (entry.isDirectory()) await visit(child);
      else if (entry.isFile() && entry.name === "manifest.json") found.push(child);
    }
  };
  await visit(directory);
  return found;
}

function reviewedFrom(envelope) {
  const reviewed = envelope && envelope.reviewed;
  return {
    target: reviewed && typeof reviewed.target === "string" ? reviewed.target : null,
    head: reviewed && typeof reviewed.head === "string" ? reviewed.head : null,
    occasion: reviewed && typeof reviewed.occasion === "string" ? reviewed.occasion : null,
  };
}

function emptyVerdict(envelope, reason) {
  return {
    status: "incomplete",
    complete: false,
    reviewed: reviewedFrom(envelope),
    incomplete: [reason],
    infrastructureFailure: null,
    confirmedFindings: [],
    operatorAttention: [],
    reviewBody: null,
    ran: [],
    skipped: [],
    misconfigurations: [],
    briefFixRequired: false,
    modelEvidence: [],
  };
}

function validLeg(leg) {
  const findingIds = leg && leg.findingIds;
  return Boolean(
    leg &&
    typeof leg.label === "string" && leg.label !== "" &&
    typeof leg.role === "string" && leg.role !== "" &&
    typeof leg.pinnedModel === "string" && leg.pinnedModel !== "" &&
    (findingIds === undefined || (
      Array.isArray(findingIds) &&
      findingIds.length > 0 &&
      findingIds.every((id) => typeof id === "string" && id !== "") &&
      new Set(findingIds).size === findingIds.length
    ))
  );
}

function validInapplicableUnit(unit) {
  return Boolean(
    unit && typeof unit === "object" &&
    typeof unit.label === "string" && unit.label !== "" &&
    (unit.concern === null || typeof unit.concern === "string") &&
    typeof unit.reason === "string" && unit.reason.trim() !== ""
  );
}

function inapplicableUnitsFrom(entry, incomplete) {
  if (entry.inapplicableUnits === undefined) return undefined;
  const name = entry.title || entry.brief || "unknown";
  if (!Array.isArray(entry.inapplicableUnits)) {
    incomplete.push(`brief "${name}" carries an unreadable inapplicable-unit list`);
    return undefined;
  }
  if (entry.inapplicableUnits.some((unit) => !validInapplicableUnit(unit))) {
    incomplete.push(`brief "${name}" carries a malformed inapplicable-unit record`);
    return undefined;
  }
  if (entry.inapplicableUnits.length === 0) return undefined;
  return entry.inapplicableUnits.map((unit) => ({
    label: unit.label,
    concern: unit.concern,
    reason: unit.reason,
  }));
}

function dispositionsFrom(briefs, incomplete) {
  const ran = [];
  const skipped = [];
  for (const entry of briefs) {
    if (!entry || typeof entry !== "object") {
      incomplete.push("envelope briefs contains an unreadable disposition");
      continue;
    }
    if (entry.status === "not-run") {
      incomplete.push(`brief "${entry.title || entry.brief || "unknown"}" was not run (${entry.reason || "no reason supplied"})`);
      continue;
    }
    if (entry.status === "run") {
      if (typeof entry.brief !== "string" || entry.brief === "" || typeof entry.title !== "string" || entry.title === "")
        incomplete.push("envelope contains a malformed run disposition");
      else {
        const disposition = {
          brief: entry.brief,
          title: entry.title,
        };
        const units = inapplicableUnitsFrom(entry, incomplete);
        if (units) disposition.inapplicableUnits = units;
        ran.push(disposition);
      }
      continue;
    }
    if (entry.status !== "skipped") {
      incomplete.push(`envelope brief disposition has unknown status ${String(entry.status)}`);
      continue;
    }
    if (
      typeof entry.brief !== "string" || entry.brief === "" ||
      typeof entry.title !== "string" || entry.title === "" ||
      typeof entry.skipKind !== "string" || entry.skipKind === "" ||
      typeof entry.reason !== "string" || entry.reason === ""
    ) {
      incomplete.push("envelope contains a malformed skipped disposition");
      continue;
    }
    const disposition = {
      brief: entry.brief,
      title: entry.title,
      skipKind: entry.skipKind,
      reason: entry.reason,
    };
    const units = inapplicableUnitsFrom(entry, incomplete);
    if (units) disposition.inapplicableUnits = units;
    skipped.push(disposition);
  }
  return { ran, skipped };
}

function misconfigurationsFrom(entries, incomplete) {
  const misconfigurations = [];
  for (const entry of entries) {
    if (
      !entry || typeof entry !== "object" ||
      typeof entry.brief !== "string" || entry.brief === "" ||
      typeof entry.title !== "string" || entry.title === "" ||
      typeof entry.kind !== "string" || entry.kind === "" ||
      typeof entry.reason !== "string" || entry.reason === ""
    ) {
      incomplete.push("envelope contains a malformed brief misconfiguration");
      continue;
    }
    misconfigurations.push({
      brief: entry.brief,
      title: entry.title,
      kind: entry.kind,
      reason: entry.reason,
    });
  }
  return misconfigurations;
}

function validProposedFinding(finding) {
  return Boolean(
    finding &&
    typeof finding.id === "string" && finding.id !== "" &&
    typeof finding.source === "string" && finding.source !== "" &&
    typeof finding.title === "string" && finding.title !== "" &&
    ["Critical", "High", "Medium", "Low"].includes(finding.severity) &&
    Number.isInteger(finding.confidence) && finding.confidence >= 0 && finding.confidence <= 100 &&
    typeof finding.path === "string" && finding.path !== "" &&
    Number.isInteger(finding.line) && finding.line >= 1 &&
    typeof finding.explanation === "string" && finding.explanation !== "" &&
    typeof finding.proposingLabel === "string" && finding.proposingLabel !== "" &&
    typeof finding.verifyLabel === "string" && finding.verifyLabel !== ""
  );
}

function findingComment(finding) {
  return {
    path: finding.path,
    body:
      `**${finding.source}: ${finding.title}**\n\n${finding.explanation}\n\n` +
      `Severity: ${finding.severity}. Reviewer confidence: ${finding.confidence}. ` +
      `Verifier confidence: ${finding.verifierConfidence}.`,
    line: finding.line,
  };
}

export async function adjudicate({ envelope, recordDir }) {
  if (!envelope || typeof envelope !== "object" || Array.isArray(envelope))
    return emptyVerdict(envelope, "launcher returned an unreadable envelope");
  if (typeof recordDir !== "string" || recordDir === "")
    return emptyVerdict(envelope, "run-record directory is absent");

  const incomplete = [];
  if (!envelope.reviewed || typeof envelope.reviewed !== "object")
    incomplete.push("envelope reviewed identity is absent");
  else {
    if (typeof envelope.reviewed.target !== "string" || envelope.reviewed.target === "")
      incomplete.push("envelope reviewed target is absent or unreadable");
    if (typeof envelope.reviewed.head !== "string" || envelope.reviewed.head === "")
      incomplete.push("envelope reviewed head is absent or unreadable");
    if (envelope.reviewed.occasion !== null && typeof envelope.reviewed.occasion !== "string")
      incomplete.push("envelope reviewed occasion is unreadable");
  }
  if (envelope.stage !== "present" && envelope.stage !== "absent")
    incomplete.push("envelope stage is absent or unknown");
  if (envelope.incomplete !== undefined) {
    if (!Array.isArray(envelope.incomplete) || envelope.incomplete.length === 0)
      incomplete.push("envelope incomplete reasons are absent or unreadable");
    else {
      for (const reason of envelope.incomplete) {
        if (typeof reason !== "string" || reason.trim() === "")
          incomplete.push("envelope contains a malformed incomplete reason");
        else
          incomplete.push(`workflow reported incomplete: ${reason}`);
      }
    }
  }
  for (const field of ["requiredModelEvidence", "proposedFindings", "briefs", "misconfigurations", "dispatches", "reviewers"])
    if (!Array.isArray(envelope[field])) incomplete.push(`envelope ${field} is absent or unreadable`);

  let manifests;
  try {
    manifests = await manifestPaths(recordDir);
  } catch (error) {
    return emptyVerdict(envelope, `run-record directory could not be read: ${error.message}`);
  }
  if (manifests.length !== 1)
    return emptyVerdict(envelope, `run-record directory contains ${manifests.length} manifests; expected exactly one`);

  let manifest;
  try {
    manifest = await jsonAt(manifests[0]);
  } catch (error) {
    return emptyVerdict(envelope, `run manifest is unreadable: ${error.message}`);
  }
  if (!manifest || typeof manifest !== "object" || typeof manifest.status !== "string")
    return emptyVerdict(envelope, "run manifest status is absent or unreadable");

  if (["failed", "timed-out", "interrupted"].includes(manifest.status)) {
    const detail = manifest.result && typeof manifest.result === "object"
      ? manifest.result.detail || manifest.result.error || manifest.result.status || manifest.status
      : manifest.status;
    return {
      ...emptyVerdict(envelope, `ensemble run ended ${manifest.status}`),
      status: "infrastructure-failure",
      infrastructureFailure: { kind: manifest.status, detail },
    };
  }
  if (manifest.status !== "complete")
    incomplete.push(`run manifest status is ${manifest.status}; expected complete`);

  const archiveDir = manifests[0].slice(0, -"manifest.json".length);
  const agentsDir = join(archiveDir, "agents");
  const agentsByLabel = new Map();
  const duplicateLabels = new Set();
  const legs = Array.isArray(envelope.requiredModelEvidence) ? envelope.requiredModelEvidence : [];
  try {
    const agentDirectories = await readdir(agentsDir, { withFileTypes: true });
    for (const entry of agentDirectories) {
      if (!entry.isDirectory()) continue;
      const record = await jsonAt(join(agentsDir, entry.name, "agent.json"));
      if (!record || typeof record !== "object" || typeof record.label !== "string" || record.label === "") {
        incomplete.push(`agent record ${entry.name} has no readable label`);
        continue;
      }
      if (agentsByLabel.has(record.label)) duplicateLabels.add(record.label);
      else agentsByLabel.set(record.label, record);
    }
  } catch (error) {
    // Ensemble does not create agents/ when a workflow dispatches no legs.
    if (!(error.code === "ENOENT" && legs.length === 0))
      incomplete.push(`agent records are unreadable: ${error.message}`);
  }
  for (const label of duplicateLabels)
    incomplete.push(`agent label ${label} occurs more than once in the run archive`);

  const requiredLabels = new Set();
  const modelEvidence = legs.map((leg, index) => {
    if (!validLeg(leg)) {
      incomplete.push(`required model-evidence leg ${index + 1} is malformed`);
      return {
        label: leg && typeof leg.label === "string" ? leg.label : null,
        role: leg && typeof leg.role === "string" ? leg.role : null,
        pinnedModel: leg && typeof leg.pinnedModel === "string" ? leg.pinnedModel : null,
        resolvedModel: null,
        status: "absent",
      };
    }
    if (requiredLabels.has(leg.label)) incomplete.push(`required model-evidence label ${leg.label} occurs more than once`);
    requiredLabels.add(leg.label);
    const record = duplicateLabels.has(leg.label) ? null : agentsByLabel.get(leg.label);
    const resolvedModel = record && typeof record.resolved_model === "string" && record.resolved_model !== ""
      ? record.resolved_model
      : null;
    const status = record && typeof record.status === "string" ? record.status : "absent";
    if (!record) incomplete.push(`agent record for ${leg.label} is absent or ambiguous`);
    else if (status !== "complete") incomplete.push(`agent ${leg.label} has status ${status}; expected complete`);
    const evidence = {
      label: leg.label,
      role: leg.role,
      pinnedModel: leg.pinnedModel,
      resolvedModel,
      status,
    };
    if (Array.isArray(leg.findingIds)) evidence.findingIds = leg.findingIds;
    return evidence;
  });

  const evidenceByLabel = new Map(modelEvidence.map((entry) => [entry.label, entry]));
  const proposedFindings = Array.isArray(envelope.proposedFindings) ? envelope.proposedFindings : [];
  const adjudicated = proposedFindings.map((proposed, index) => {
    if (!validProposedFinding(proposed)) {
      incomplete.push(`proposed finding ${index + 1} is absent or malformed`);
      return null;
    }
    const reviewEvidence = evidenceByLabel.get(proposed.proposingLabel);
    const verifyEvidence = evidenceByLabel.get(proposed.verifyLabel);
    const rawVerifier = proposed.rawVerifier;
    let verification = "verifier returned no result";
    let verdict = "no-verdict";
    if (!reviewEvidence || reviewEvidence.status !== "complete" || !verifyEvidence || verifyEvidence.status !== "complete") {
      const failed = [reviewEvidence, verifyEvidence].filter((entry) => !entry || entry.status !== "complete");
      verification = `leg completion did not confirm ${failed.map((entry) => entry ? entry.label : "a referenced leg").join(" and ")}`;
    } else if (
      !rawVerifier ||
      (rawVerifier.verdict !== "upheld" && rawVerifier.verdict !== "refuted") ||
      !Number.isInteger(rawVerifier.confidence) ||
      typeof rawVerifier.reason !== "string"
    ) {
      verification = "verifier returned no valid result";
    } else {
      verdict = rawVerifier.verdict === "upheld" ? "confirmed" : "refuted";
      verification = rawVerifier.reason;
    }
    if (verdict === "no-verdict")
      incomplete.push(`finding "${proposed.title || proposed.id || index + 1}" has no complete verdict (${verification})`);
    const combinedConfidence = Number.isInteger(proposed.confidence) && rawVerifier && Number.isInteger(rawVerifier.confidence)
      ? Math.round((proposed.confidence + rawVerifier.confidence) / 2)
      : null;
    const { proposingLabel, verifyLabel, rawVerifier: omittedVerifier, ...finding } = proposed;
    return {
      ...finding,
      verifierConfidence: rawVerifier && Number.isInteger(rawVerifier.confidence) ? rawVerifier.confidence : null,
      combinedConfidence,
      verdict,
      verification,
      operatorAttention: verdict === "confirmed" && combinedConfidence < LOW_COMBINED_CONFIDENCE,
    };
  }).filter(Boolean);

  const briefs = Array.isArray(envelope.briefs) ? envelope.briefs : [];
  const { ran, skipped } = dispositionsFrom(briefs, incomplete);
  const misconfigurations = misconfigurationsFrom(
    Array.isArray(envelope.misconfigurations) ? envelope.misconfigurations : [],
    incomplete,
  );
  const complete = incomplete.length === 0;
  const confirmedFindings = complete ? adjudicated.filter((finding) => finding.verdict === "confirmed") : [];
  const operatorAttention = confirmedFindings
    .filter((finding) => finding.operatorAttention)
    .map((finding) => ({
      finding: finding.id,
      title: finding.title,
      combinedConfidence: finding.combinedConfidence,
      threshold: LOW_COMBINED_CONFIDENCE,
    }));
  const comments = confirmedFindings.map(findingComment);
  const isBriefReview = Array.isArray(envelope.dispatches) &&
    envelope.dispatches.some((dispatch) => dispatch && typeof dispatch.brief === "string");

  return {
    status: complete ? "complete" : "incomplete",
    complete,
    reviewed: reviewedFrom(envelope),
    incomplete,
    infrastructureFailure: null,
    confirmedFindings,
    operatorAttention,
    reviewBody: complete && comments.length > 0
      ? {
          verdict: "comment",
          body: isBriefReview ? "Repository review brief findings." : "Confirmed findings in the reviewed code.",
          comments,
        }
      : null,
    ran,
    skipped,
    misconfigurations,
    briefFixRequired: isBriefReview && complete && confirmedFindings.length > 0,
    modelEvidence,
  };
}
