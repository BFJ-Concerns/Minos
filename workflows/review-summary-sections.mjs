import { BRIEF, CONCERN } from "./summary-comment.mjs";

// Credit a confirmed group to every section that proposed a write-up in
// it. Several matching write-ups in one section still count only once.
export function reviewSummarySections(envelope, confirmed, { ran, skipped }) {
  const sections = new Map();
  const sectionByLabel = new Map();
  const concernBriefs = new Set();
  const dispatches = envelope.dispatches.filter(Boolean);
  const skippedByBrief = Map.groupBy(skipped, ({ brief }) => brief);

  for (const { label, specialistType, roleBrief, brief } of dispatches) {
    if (specialistType) {
      if (!sections.has(specialistType))
        sections.set(specialistType, { kind: CONCERN, name: specialistType, findings: 0 });
      sectionByLabel.set(label, specialistType);
      concernBriefs.add(roleBrief);
    } else if (brief) {
      sectionByLabel.set(label, brief);
    }
  }

  // A concern may be partitioned across specialists. It was skipped only
  // if all its dispatched units reported that their concern did not apply.
  const concernUnits = Map.groupBy(dispatches, ({ specialistType }) => specialistType);
  for (const [name, section] of sections) {
    const units = concernUnits.get(name);
    const skips = skippedByBrief.get(units[0].roleBrief) ?? [];
    if (skips.length === units.length) {
      sections.set(name, { kind: section.kind, name, skipped: [...new Set(skips.map(({ reason }) => reason))].join(" / ") });
    }
  }

  for (const { brief } of ran) sections.set(brief, { kind: BRIEF, name: brief, findings: 0 });
  for (const { brief, reason } of skipped) {
    if (!concernBriefs.has(brief))
      sections.set(brief, { kind: BRIEF, name: brief, skipped: reason });
  }

  const confirmedIds = new Set(confirmed.map(({ id }) => id));
  for (const { id, proposingLabel, mergedFrom } of envelope.proposedFindings) {
    if (!confirmedIds.has(id)) continue;
    const members = Array.isArray(mergedFrom) ? mergedFrom : [];
    const labels = [proposingLabel, ...members.map((member) => member?.proposingLabel)];
    const sources = new Set(labels.map((label) => sectionByLabel.get(label)));
    for (const name of sources) {
      const section = sections.get(name);
      if (section?.findings !== undefined) section.findings += 1;
    }
  }
  return [...sections.values()];
}
