import { execFileSync } from "node:child_process";

// Count a rename at both paths so moving a file out of a brief's scope
// still engages it. The merge-base range matches the diff reviewers read.
export function changedPaths(workspace, base, head) {
  return execFileSync("git", ["-C", workspace, "diff", "--no-renames", "--name-only", "-z", `${base}...${head}`, "--"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: Infinity,
  }).split("\0").filter(Boolean);
}
