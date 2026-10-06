// Reads the guidance documents a run's orientation record lists, for the
// deterministic input builders. Setup wrote the record after reading each
// document once; this re-read is the builder's own fail-closed check that
// every listed document is still a regular, non-empty file within bounds,
// so a workflow never receives a guidance entry without its content.

import { readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";

export const MAXIMUM_GUIDANCE_BYTES = 262_144;

export function guidanceFromOrientation(orientation) {
  const entries = orientation && orientation.guidance;
  if (!Array.isArray(entries) || entries.length === 0)
    throw new Error("orientation lists no guidance");
  return entries.map((entry, index) => {
    const source = entry && entry.source;
    if (
      !source || typeof source.path !== "string" || source.path === "" ||
      (source.repository !== undefined && (typeof source.repository !== "string" || source.repository === "")) ||
      typeof entry.location !== "string" || entry.location === "" ||
      (entry.origin !== "configured" && entry.origin !== "checked-in")
    ) throw new Error(`orientation guidance entry ${index} is malformed`);
    const location = resolve(entry.location);
    const stat = statSync(location);
    if (!stat.isFile()) throw new Error(`guidance ${location} is not a regular file`);
    if (stat.size > MAXIMUM_GUIDANCE_BYTES)
      throw new Error(`guidance ${location} exceeds ${MAXIMUM_GUIDANCE_BYTES} bytes`);
    const content = readFileSync(location, "utf8");
    if (content.trim() === "") throw new Error(`guidance ${location} is empty`);
    return {
      repository: source.repository === undefined ? null : source.repository,
      path: source.path,
      origin: entry.origin,
      content,
    };
  });
}
