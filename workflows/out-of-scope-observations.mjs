function validObservation(observation) {
  return Boolean(
    observation &&
    typeof observation.id === "string" && observation.id !== "" &&
    typeof observation.source === "string" && observation.source !== "" &&
    typeof observation.title === "string" && observation.title !== "" &&
    typeof observation.path === "string" && observation.path !== "" &&
    Number.isInteger(observation.line) && observation.line >= 1 &&
    typeof observation.explanation === "string" && observation.explanation !== "" &&
    typeof observation.observingLabel === "string" && observation.observingLabel !== "" &&
    observation.verified === false
  );
}

export function observationsFromEnvelope(envelope) {
  const observations = envelope && Array.isArray(envelope.outOfScopeObservations)
    ? envelope.outOfScopeObservations
    : [];
  return observations.filter(validObservation).map((observation) => ({
    id: observation.id,
    source: observation.source,
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
