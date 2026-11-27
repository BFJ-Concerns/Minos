import { applied, checkHead } from "./forge-writes.mjs";

export async function claim(run) {
  const snapshot = await run.forge("snapshot");
  checkHead(run, snapshot);
  await applied(run, "claim", []);
  return { snapshot };
}
