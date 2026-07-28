function validInapplicableUnit(unit) {
  return Boolean(
    unit &&
    typeof unit.label === "string" && unit.label !== "" &&
    (unit.concern === null || typeof unit.concern === "string") &&
    typeof unit.reason === "string"
  );
}

function detailsByDisposition(envelope) {
  const details = new Map();
  const briefs = envelope && Array.isArray(envelope.briefs)
    ? envelope.briefs
    : [];
  for (const entry of briefs) {
    if (
      !entry ||
      !["run", "skipped"].includes(entry.status) ||
      typeof entry.brief !== "string" ||
      !Array.isArray(entry.inapplicableUnits)
    ) continue;
    const units = entry.inapplicableUnits
      .filter(validInapplicableUnit)
      .map((unit) => ({
        label: unit.label,
        concern: unit.concern,
        reason: unit.reason,
      }));
    if (units.length > 0)
      details.set(`${entry.status}\0${entry.brief}`, units);
  }
  return details;
}

function attachDetails(dispositions, status, details) {
  if (!Array.isArray(dispositions)) return dispositions;
  return dispositions.map((disposition) => {
    const units = disposition && details.get(`${status}\0${disposition.brief}`);
    return units
      ? { ...disposition, inapplicableUnits: units }
      : disposition;
  });
}

export function attachBriefPartitionDetails(verdict, envelope) {
  const details = detailsByDisposition(envelope);
  return {
    ...verdict,
    ran: attachDetails(verdict.ran, "run", details),
    skipped: attachDetails(verdict.skipped, "skipped", details),
  };
}
