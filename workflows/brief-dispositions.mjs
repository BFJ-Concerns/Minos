// Node-side twin of the deterministic brief-disposition logic in
// workflows/review-briefs.js. The Ensemble sandbox cannot import modules, so
// the workflow keeps its own copy; the parity test in
// review-scope_test.mjs pins the two against each other.

export function parseFrontmatter(content) {
  const front = { title: null, extent: "diff", sweep: "per-file", occasion: [], relevance: null };
  if (typeof content !== "string") return front;
  const match = content.match(/^---\n([\s\S]*?)\n---\n?/);
  if (!match) return front;
  for (const line of match[1].split("\n")) {
    const pair = line.match(/^(\w+):\s*(.*)$/);
    if (!pair) continue;
    const key = pair[1].toLowerCase();
    let value = pair[2];
    if (key !== "title" && key !== "relevance") value = value.replace(/#.*$/, "");
    value = value.trim();
    if (key === "title" && value) front.title = value;
    if (key === "extent" && value === "full") front.extent = "full";
    if (key === "sweep" && value === "whole-tree") front.sweep = "whole-tree";
    if (key === "occasion") front.occasion = value.split(",").map((token) => token.trim()).filter(Boolean);
    if (key === "relevance" && value) front.relevance = value;
  }
  return front;
}

export function briefTitle(path, front) {
  if (front.title) return front.title;
  const stem = path.replace(/^.*\//, "").replace(/\.md$/, "");
  return stem.split("-").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
}

export function deterministicDisposition(brief, front, occasion, changedPaths) {
  const hasOccasion = front.occasion.length > 0;
  const hasRelevance = Boolean(front.relevance);
  const hasPathScope = typeof brief.scope === "string" && brief.scope !== "";
  const misconfiguration = hasPathScope && brief.scopeExists === false
    ? {
        skipKind: "misconfigured-scope",
        reason: `brief scope ${brief.scope}/ matches no repository directory`,
      }
    : null;
  if (!hasOccasion && !hasRelevance && !hasPathScope) {
    return {
      status: "skipped",
      skipKind: "no-trigger",
      reason: "brief declares no relevance, occasion, or path-scope trigger",
    };
  }
  // Occasion is a veto: a brief that names occasions runs only on a run that
  // names a matching one, whatever else it declares — it selects which runs the
  // brief applies to. A brief naming no occasion is unrestricted by occasion.
  if (hasOccasion) {
    if (!occasion)
      return {
        status: "skipped",
        skipKind: "occasion",
        reason: `brief applies on occasion ${front.occasion.join(", ")}; this run names none`,
        misconfiguration,
      };
    if (!front.occasion.includes(occasion))
      return {
        status: "skipped",
        skipKind: "occasion",
        reason: `brief applies on occasion ${front.occasion.join(", ")}, not ${occasion}`,
        misconfiguration,
      };
    // A matched occasion is itself a satisfied trigger; a path-scope or relevance
    // it also carries only refines width, it does not further gate a brief the
    // occasion has already opted in.
    return { triggered: true, misconfiguration };
  }
  // No occasion declared: run on a satisfied positive trigger — a touched
  // path-scope, or a relevance condition judged in the relevance phase.
  const scopeSatisfied =
    hasPathScope && changedPaths.some((path) => path === brief.scope || path.startsWith(brief.scope + "/"));
  if (scopeSatisfied) return { triggered: true, misconfiguration };
  if (hasRelevance) return { triggered: false, misconfiguration };
  if (misconfiguration) return { status: "skipped", ...misconfiguration, misconfiguration };
  return { status: "skipped", skipKind: "empty", reason: `nothing changed under scope ${brief.scope}/` };
}

// Settles which of a repository's briefs this change engages. A brief is
// engaged when its deterministic disposition triggers it, or when it carries a
// relevance condition the deterministic pass cannot settle — the relevance
// judgement belongs to the brief workflow's own leg, so a pending one always
// commits the run to the full pipeline.
export function briefEngagement(briefs, occasion, changedPaths) {
  const dispositions = [];
  const misconfigurations = [];
  let briefsEngage = false;
  for (const brief of briefs) {
    const front = parseFrontmatter(brief.content);
    const base = { brief: brief.path, title: briefTitle(brief.path, front) };
    const disposition = deterministicDisposition(brief, front, occasion, changedPaths);
    const { misconfiguration, ...recorded } = disposition;
    if (misconfiguration)
      misconfigurations.push({ ...base, member: "primary", kind: misconfiguration.skipKind, reason: misconfiguration.reason });
    if (disposition.status === "skipped") {
      dispositions.push({ ...base, ...recorded });
      continue;
    }
    briefsEngage = true;
    dispositions.push({
      ...base,
      engaged: true,
      via: disposition.triggered
        ? (front.occasion.length > 0 ? "occasion" : "path-scope")
        : "relevance-pending",
    });
  }
  return { briefsEngage, dispositions, misconfigurations };
}
