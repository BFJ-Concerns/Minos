import { memberIdsFrom } from "./member-record.mjs";

function validObservation(observation, memberIds) {
  return Boolean(
    observation &&
    typeof observation.id === "string" && observation.id !== "" &&
    typeof observation.source === "string" && observation.source !== "" &&
    typeof observation.title === "string" && observation.title !== "" &&
    typeof observation.path === "string" && observation.path !== "" &&
    Number.isInteger(observation.line) && observation.line >= 1 &&
    typeof observation.explanation === "string" && observation.explanation !== "" &&
    typeof observation.observingLabel === "string" && observation.observingLabel !== "" &&
    observation.verified === false &&
    (typeof observation.member === "string"
      ? memberIds.has(observation.member)
      : memberIds.size === 1)
  );
}

export function observationsFromEnvelope(envelope) {
  const observations = envelope && Array.isArray(envelope.outOfScopeObservations)
    ? envelope.outOfScopeObservations
    : [];
  const parsedMemberIds = memberIdsFrom(envelope && envelope.members);
  const memberIds = parsedMemberIds || new Set(["primary"]);
  const soleMember = memberIds.size === 1 ? [...memberIds][0] : null;
  return observations.filter((observation) => validObservation(observation, memberIds)).map((observation) => ({
    id: observation.id,
    source: observation.source,
    member: typeof observation.member === "string" && observation.member !== "" ? observation.member : soleMember,
    title: observation.title,
    path: observation.path,
    line: observation.line,
    explanation: observation.explanation,
    observingLabel: observation.observingLabel,
    verified: false,
  }));
}

export function attachOutOfScopeObservations(verdict, envelope) {
  return {
    ...verdict,
    outOfScopeObservations: observationsFromEnvelope(envelope),
  };
}
