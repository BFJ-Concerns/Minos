import { applied, currentSnapshot } from "./forge-writes.mjs";

export async function claim(run) {
  const snapshot = await currentSnapshot(run);
  await applied(run, "claim", []);
  return { snapshot };
}
