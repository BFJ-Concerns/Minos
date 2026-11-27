// The stages every review workflow runs after its own legs have proposed
// findings: the merge of the findings one fix would resolve, and their
// verification in rounds. review.js and review-briefs.js propose in
// different ways and share everything from there on.
//
// A workflow script cannot import statically, so it loads this module
// where it needs it, from beside itself:
//
//   const { reviewStages, subjectOf, proposedFindingOf, uncheckedFindingOf } =
//     await import("./review-stages.mjs");
//
// The Ensemble runtime resolves that specifier from the script's own path,
// and the installer copies the whole workflows directory, so the module is
// wherever the script is. A module a workflow imports is an ordinary
// module: it may import statically, and it sees none of the runtime's hook
// bindings, so `agent` and `parallel` reach it as arguments.
//
// Only code is loaded this way. What a run is given, its prompts,
// contracts, routing and guidance, arrives in the input its builder
// attached, and a workflow checks it before dispatching anything.
//
// This module holds no state of its own. One process may run several
// workflows, as the tests do, and each run's state is the value
// reviewStages returns.

import { fillPrompt } from "./review-prompts.mjs";

function pick(record, fields) {
  return Object.fromEntries(fields.filter((field) => record[field] !== undefined).map((field) => [field, record[field]]));
}

// One run's stages over the values the workflow checked in its input:
// the runtime's `agent` and `parallel`, the resolved `routing`, the
// `contracts`, `prompts` carrying at least `merge` and `verifier`, and
// `reviewedChangeValues`, the values of the reviewed-change run slots
// (`target`, `head`, `project-documents`, `pull-request`).
//
// The run's record is returned with the stages and the workflow reads it
// into its envelope: `legs` is every leg dispatched, `incomplete` each
// reason the run cannot be published, `outOfScopeObservations` what the
// legs noticed outside the change. The workflow records its own legs
// through addLeg, legOutput and recordObservations, so one run has one
// record however many of its legs are dispatched from here.
export function reviewStages({ agent, parallel, routing, contracts, prompts, reviewedChangeValues }) {
  const { target, head } = reviewedChangeValues;
  const incomplete = [];
  const legs = [];
  const outOfScopeObservations = [];

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
      if (
        ids.length === 0 || !ids.includes(group.primary) || new Set(ids).size !== ids.length ||
        ids.some((id) => !byId.has(id) || placed.has(id))
      ) {
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
  // returned unchecked for the record. Where a later round's merge fails,
  // which has already left the run incomplete, the findings it was to group
  // are returned unchecked in the same way.
  async function verifySubjects(subjects, labelPrefix, mergeLabel, phaseName) {
    const verified = [];
    let pending = subjects;
    let raisedBy = routing.proposer.engine;
    for (let round = 1; round <= contracts.verificationRounds && pending.length > 0; round += 1) {
      const route = verifierRoute(raisedBy);
      const raised = await verificationRound(pending, round === 1 ? labelPrefix : `${labelPrefix}-round-${round}`, route, phaseName);
      verified.push(...pending);
      raisedBy = route.engine;
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

  // The route that verifies findings proposed on an engine: the verifier's,
  // unless the verifier runs on that same engine and the proposer's does
  // not, as for a finding a verifier raised. A deployment with one engine
  // has no other family to offer.
  function verifierRoute(proposedOn) {
    if (routing.verifier.engine !== proposedOn) return routing.verifier;
    if (routing.proposer.engine !== proposedOn) return routing.proposer;
    return routing.verifier;
  }

  async function verificationRound(subjects, labelPrefix, route, phaseName) {
    const batches = [];
    for (let offset = 0; offset < subjects.length; offset += contracts.verifierBatchSize) {
      const items = subjects.slice(offset, offset + contracts.verifierBatchSize);
      const label = `${labelPrefix}-${batches.length + 1}-${route.engine}`;
      const findingIds = items.map((item) => item.id);
      items.forEach((item) => { item.verifyLabel = label; item.rawVerifier = null; });
      batches.push({ items, label, findingIds });
      addLeg(label, "verifier", route.model, findingIds);
    }
    const settlements = await parallel(batches.map((batch) => () =>
      agent(
        fillPrompt(prompts.verifier, {
          ...reviewedChangeValues,
          findings: JSON.stringify(batch.items.map(shownToVerifier), null, 2),
        }),
        {
          engine: route.engine,
          schema: contracts.verifierSchema,
          model: route.model,
          effort: route.effort,
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

  return {
    incomplete,
    legs,
    outOfScopeObservations,
    addLeg,
    legOutput,
    recordObservations,
    mergeProposed,
    verifySubjects,
  };
}

// One finding to verify: a merged group's primary write-up, with what the
// group's other write-ups add, or a finding a verifier raised. A group
// holding a whole-scope brief's finding is verified as whole-scope, under
// the first such write-up's directory, whichever write-up is primary.
export function subjectOf({ primary, members }) {
  const writeUps = [primary, ...members];
  const dependsOn = [...new Set(writeUps
    .map((item) => typeof item.finding.dependsOn === "string" ? item.finding.dependsOn.trim() : "")
    .filter((fact) => fact !== ""))].join("; ");
  const wholeScope = writeUps.find((item) => item.wholeScope !== undefined)?.wholeScope;
  return {
    ...primary,
    ...(wholeScope !== undefined ? { wholeScope } : {}),
    finding: { ...primary.finding, dependsOn },
    members,
  };
}

// A verified finding as the adjudicator reads it: the primary write-up,
// the group's joined outside facts, and the write-ups merged into it.
export function proposedFindingOf(subject) {
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

// A finding left without a verdict, as the run record carries it.
export function uncheckedFindingOf(subject) {
  return {
    id: subject.id,
    ...subject.finding,
    source: subject.source,
    proposingLabel: subject.proposingLabel,
  };
}
