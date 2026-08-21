// Workflow tests spawn the adjudication wrapper, the Ensemble launcher and
// helper scripts for real, and build child environments from process.env.
// Inside a live Minos run that inherited environment points at the run's own
// record directory and the long-term archive, so a fixture would write into
// live operational state. Import this module first in every workflow test
// file: it strips the whole MINOS_*/ENSEMBLE_* namespace, and each test sets
// the values it needs explicitly.
for (const name of Object.keys(process.env)) {
  if (name.startsWith("MINOS_") || name.startsWith("ENSEMBLE_")) delete process.env[name];
}
