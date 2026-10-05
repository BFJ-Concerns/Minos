// Reads repository review briefs in deterministic path order, deriving each
// brief's directory scope and whether that scope exists in the workspace.
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { relative, resolve, sep } from "node:path";

export function reviewBriefsFromWorkspace(workspace) {
  const reviewDirectory = resolve(workspace, ".review");
  const hasReviewDirectory = existsSync(reviewDirectory) && statSync(reviewDirectory).isDirectory();
  const markdownPaths = [];
  if (hasReviewDirectory) {
    const visit = (directory) => {
      for (const entry of readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
        const path = resolve(directory, entry.name);
        if (entry.isDirectory()) visit(path);
        else if (entry.isFile() && entry.name.endsWith(".md")) markdownPaths.push(path);
      }
    };
    visit(reviewDirectory);
  }
  const briefs = markdownPaths.map((absolute) => {
    const path = relative(workspace, absolute).split(sep).join("/");
    const inner = path.replace(/^\.review\//, "");
    const scope = inner.includes("/") ? inner.replace(/\/[^/]*$/, "") : null;
    const scopePath = scope ? resolve(workspace, scope) : null;
    return {
      path,
      content: readFileSync(absolute, "utf8"),
      scope,
      scopeExists: !scopePath || (existsSync(scopePath) && statSync(scopePath).isDirectory()),
    };
  });

  return { hasReviewDirectory, briefs };
}
