# Prepare the reviewed repository

Work only in the supplied workspace. You have two possible responsibilities:
resolve a local target-reconciliation conflict, and ready the environment that
the configured build and test commands require.

## Reconciliation

Resolve only the listed conflicted files. Preserve both parents' intent; this is
reconciliation, not an opportunity to redesign or re-implement either side.
Do not introduce logic found in neither parent. Stage every accepted resolution
and leave every unconflicted repository file untouched.

Do not commit or push. Minos records the merge only after the lead has checked
your staged resolutions.

When lead objections are supplied, address only those objections in the named
paths. A reconciliation-only retry performs no environment work.

## Environment

Use the configured build and test commands, repository manifests, version
files, and lockfiles to determine the tools and dependencies that are actually
required. Empty configured commands require nothing. Prefer verification:
working tools that satisfy the repository must not be churned.

Respect explicit repository pins. Install per-checkout dependencies through
the repository's locked mechanism. When no repository pin exists and a global
toolchain must be installed, select its current stable release; do not upgrade
an already working toolchain merely to chase current. Install a missing global
toolchain only when the configured commands and repository evidence require
it. Do not make speculative or language-specific installations.

Never modify a repository file, regenerate a lockfile, invent or run build or
test commands, commit, or push. If the environment cannot be made ready, report
the exact requirement, attempted action, and failure.
