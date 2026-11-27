// The stages every review workflow runs: the proposing legs, the merge of
// the findings one fix would resolve, and their verification in rounds.
// Each workflow comes by its proposing units and prompts them in its own
// way: review.js from exploration's plan, review-briefs.js from the
// repository's briefs, and a quick review (review-mode.mjs) from the
// change itself or its division into shares. The dispatch of those units
// and everything after it is shared.
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

// The proposed findings one list of a proposing leg's output gives, each
// carrying the id `idOf` gives it and the provenance `fields`.
export function proposalsOf(findings, idOf, fields) {
  return (Array.isArray(findings) ? findings : []).map((finding, index) => ({ id: idOf(index), finding, ...fields }));
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

  // The proposing legs: one for each unit, each sent the unit's own
  // `prompt` under its `label` on the unit's `route`, or on the proposer
  // route where it carries none. A unit answers on its own `schema` and
  // its leg is recorded under its own `role`; one that names neither is a
  // specialist answering on the specialist schema. A unit's `source` names
  // it on the observations it records. Each unit comes back with whether
  // its leg completed and its usable output, null where the leg failed or
  // answered nothing, which already marks the run incomplete.
  async function proposeUnits(units, phaseName) {
    const routeOf = (unit) => unit.route || routing.proposer;
    for (const unit of units) addLeg(unit.label, unit.role || "specialist", routeOf(unit).model);
    const settlements = await parallel(units.map((unit) => () => agent(unit.prompt, {
      engine: routeOf(unit).engine,
      schema: unit.schema || contracts.specialistSchema,
      model: routeOf(unit).model,
      effort: routeOf(unit).effort,
      strip: ["skills", "agents"],
      identity: true,
      label: unit.label,
      phase: phaseName,
    })));
    return units.map((unit, index) => {
      const settlement = settlements[index];
      const output = legOutput(settlement, unit.label);
      if (output) recordObservations(output.outOfScopeObservations, unit.source, unit.label);
      return { unit, completed: Boolean(settlement && settlement.failure === null), output };
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
      wholeScopeBrief: subject.wholeScopeBrief,
    }, contracts.verifierFindingFields);
  }

  // Verifies every subject, then each finding the verifiers raised, in
  // rounds: what one round raises is merged like any proposed findings and
  // verified by legs of the next, never by the leg that raised it. What the
  // last round raises has no verdict, which leaves the run incomplete; it is
  // returned unchecked for the record. Where a later round's merge fails,
  // which has already left the run incomplete, the findings it was to group
  // are returned unchecked in the same way.
  //
  // Each finding is verified on the route verifierRoute gives the engine
  // that proposed it: its `proposedOn`, or the proposer's engine where it
  // names none. A round whose findings came from more than one engine sends
  // each engine's share to verifier legs of its own, dispatched together.
  async function verifySubjects(subjects, labelPrefix, mergeLabel, phaseName) {
    const verified = [];
    let pending = subjects;
    for (let round = 1; round <= contracts.verificationRounds && pending.length > 0; round += 1) {
      const roundPrefix = round === 1 ? labelPrefix : `${labelPrefix}-round-${round}`;
      const byRoute = new Map();
      for (const subject of pending) {
        const route = verifierRoute(subject.proposedOn || routing.proposer.engine);
        if (!byRoute.has(route)) byRoute.set(route, []);
        byRoute.get(route).push(subject);
      }
      const raised = (await Promise.all([...byRoute].map(([route, share]) =>
        verificationRound(share, roundPrefix, route, phaseName)))).flat();
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
      (Array.isArray(response.newFindings) ? response.newFindings : []).forEach((raisedFinding, findingIndex) => {
        // The name of the finding being checked is provenance, never part of
        // the write-up; it lends that finding's whole scope, and the brief
        // that asked for it, where the raised finding's path lies within it
        // (raisedFindingShape).
        const { foundWhileChecking, ...finding } = raisedFinding || {};
        const checked = batch.items.find((item) => item.id === foundWhileChecking);
        const lendsScope = checked?.wholeScope !== undefined && pathWithinScope(finding.path, checked.wholeScope);
        raised.push({
          id: `${batch.label}:raised:${findingIndex + 1}`,
          finding,
          ...(lendsScope ? { wholeScope: checked.wholeScope, wholeScopeBrief: checked.wholeScopeBrief } : {}),
          source: "verification",
          reportedBy: "verification",
          outsideConcern: false,
          proposingLabel: batch.label,
          proposedOn: route.engine,
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
    proposeUnits,
    mergeProposed,
    verifySubjects,
  };
}

// Whether a repository-relative path lies under a whole scope: a directory
// with a trailing slash, or `./` for the whole repository. A path that
// climbs out with `..`, or is absolute, lies under none.
export function pathWithinScope(path, scope) {
  if (typeof path !== "string" || typeof scope !== "string") return false;
  const segments = path.split("/").filter((segment) => segment !== "" && segment !== ".");
  if (path.startsWith("/") || segments.length === 0 || segments.includes("..")) return false;
  if (scope === "./") return true;
  return `${segments.join("/")}/`.startsWith(scope);
}

// One finding to verify: a merged group's primary write-up, with what the
// group's other write-ups add, or a finding a verifier raised. A group
// holding a whole-scope brief's finding is verified as whole-scope, under
// the first such write-up's directory and brief, whichever write-up is
// primary.
export function subjectOf({ primary, members }) {
  const writeUps = [primary, ...members];
  const dependsOn = [...new Set(writeUps
    .map((item) => typeof item.finding.dependsOn === "string" ? item.finding.dependsOn.trim() : "")
    .filter((fact) => fact !== ""))].join("; ");
  const whole = writeUps.find((item) => item.wholeScope !== undefined);
  return {
    ...primary,
    ...(whole ? { wholeScope: whole.wholeScope, wholeScopeBrief: whole.wholeScopeBrief } : {}),
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
    ...(subject.wholeScope !== undefined ? { wholeScope: subject.wholeScope, wholeScopeBrief: subject.wholeScopeBrief } : {}),
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
